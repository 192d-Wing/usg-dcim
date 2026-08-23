import axios, { AxiosError, AxiosRequestConfig } from 'axios';

export const TOKEN_KEY = 'dcim.token';
const ID_TOKEN_KEY = 'dcim.id_token';

export const http = axios.create({
  baseURL: '/api/v1',
});

http.interceptors.request.use((cfg) => {
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
    const resp = await axios.post('/api/v1/auth/refresh', null, {
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

    // 401 + we haven't already retried this request + the failing call
    // wasn't /auth/refresh itself: try one refresh, then retry the request.
    if (
      status === 401
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
