import axios, { AxiosError, AxiosRequestConfig } from 'axios';
import { getBridge, isEmbedded, markSessionEnded } from './embedded';
import { apiBase } from './base-path';
import { SCOPE_HEADER } from '@/vendor/console-sdk/console-sdk';

export const TOKEN_KEY = 'dcim.token';
const ID_TOKEN_KEY = 'dcim.id_token';

export const http = axios.create({
  baseURL: apiBase(),
});

const SAFE_METHODS = new Set(['get', 'head', 'options']);

http.interceptors.request.use((cfg) => {
  if (isEmbedded()) {
    // Embedded in the console: the gateway proxy authenticates us with
    // its signed assertion. Never send (or read) a bearer token; mark
    // unsafe methods with the CSRF header otter-go requires in console
    // mode; pass the console scope through.
    cfg.headers.delete('Authorization');
    if (!SAFE_METHODS.has((cfg.method ?? 'get').toLowerCase())) {
      cfg.headers.set('X-DCIM-CSRF', '1');
    }
    const scope = getBridge()?.scopeHeader();
    if (scope) cfg.headers.set(SCOPE_HEADER, scope);
    return cfg;
  }
  const token = localStorage.getItem(TOKEN_KEY);
  if (token) cfg.headers.Authorization = `Bearer ${token}`;
  return cfg;
});

// Single-flight refresh: many concurrent 401s should not fire N refresh
// calls. The first failure starts a refresh; siblings await the same
// promise and pick up the new token (or fail together).
let refreshInFlight: Promise<string | null> | null = null;
type Retryable = AxiosRequestConfig & { _retried?: boolean };

async function attemptRefresh(): Promise<string | null> {
  // Bare axios call so we bypass our own interceptor chain — otherwise a
  // failing refresh recurses into the 401 handler.
  try {
    const resp = await axios.post(`${apiBase()}/auth/refresh`, null, {
      headers: { Authorization: `Bearer ${localStorage.getItem(TOKEN_KEY) ?? ''}` },
    });
    const newToken = resp.data?.access_token as string | undefined;
    if (!newToken) return null;
    localStorage.setItem(TOKEN_KEY, newToken);
    if (resp.data?.id_token) localStorage.setItem(ID_TOKEN_KEY, resp.data.id_token);
    return newToken;
  } catch {
    return null;
  }
}

http.interceptors.response.use(
  (r) => r,
  async (err: AxiosError<any>) => {
    const status = err.response?.status;
    const cfg = err.config as Retryable | undefined;

    // Embedded: no token refresh (the console owns the session). Ask the
    // console once and show the "session ended" state instead.
    if (status === 401 && isEmbedded()) markSessionEnded();

    // 401 + we haven't already retried this request + the failing call
    // wasn't /auth/refresh itself: try one refresh, then retry the request.
    if (
      status === 401
      && !isEmbedded()
      && cfg
      && !cfg._retried
      && !cfg.url?.endsWith('/auth/refresh')
      && !cfg.url?.endsWith('/auth/login')
    ) {
      cfg._retried = true;
      refreshInFlight = refreshInFlight ?? attemptRefresh().finally(() => {
        refreshInFlight = null;
      });
      const newToken = await refreshInFlight;
      if (newToken) {
        cfg.headers = cfg.headers ?? {};
        (cfg.headers as Record<string, string>).Authorization = `Bearer ${newToken}`;
        return http.request(cfg);
      }
    }

    // Normalise our { error: { code, message, details } } envelope into a plain Error.
    const env = err.response?.data?.error;
    if (env) {
      const e = new Error(env.message || 'request failed');
      (e as any).code = env.code;
      (e as any).statusCode = err.response?.status;
      (e as any).details = env.details;
      return Promise.reject(e);
    }
    // otter-go's httpx.Error shape is {"detail": "..."} — surface it so
    // toasts show the API's message instead of axios's generic
    // "Request failed with status code NNN". Keep .response attached:
    // callers branch on err.response.status (e.g. the 409 delete guards).
    const detail = err.response?.data?.detail;
    if (typeof detail === 'string' && detail) {
      const e = new Error(detail);
      (e as any).statusCode = err.response?.status;
      (e as any).response = err.response;
      return Promise.reject(e);
    }
    return Promise.reject(err);
  },
);
