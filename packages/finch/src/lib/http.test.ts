import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest';

// http.ts reads localStorage at request time. In the node test
// environment localStorage doesn't exist, so we install a minimal
// in-memory shim before importing the module. The shim mirrors the
// subset of the Web Storage API the interceptors actually use.

class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null { return this.store.get(key) ?? null; }
  setItem(key: string, val: string): void { this.store.set(key, String(val)); }
  removeItem(key: string): void { this.store.delete(key); }
  clear(): void { this.store.clear(); }
}

(globalThis as any).localStorage = new MemoryStorage();

// Import AFTER the shim is in place — the module captures the
// `localStorage` reference at import time via the global lookup.
const { http, TOKEN_KEY } = await import('./http');
const axiosModule = await import('axios');
const axios = axiosModule.default;

describe('http request interceptor — token injection', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  test('injects Authorization: Bearer <token> when a token is in storage', async () => {
    localStorage.setItem(TOKEN_KEY, 'abc.def.ghi');
    // Mock the underlying adapter so the request never leaves the
    // process. Capture the final config to assert on the header.
    const seen: any[] = [];
    const adapter = vi.fn((config: any) => {
      seen.push(config);
      return Promise.resolve({
        status: 200, statusText: 'OK', headers: {}, data: { ok: true }, config,
      });
    });
    await http.get('/things', { adapter });
    expect(seen).toHaveLength(1);
    expect(seen[0].headers.Authorization).toBe('Bearer abc.def.ghi');
  });

  test('does NOT inject Authorization when no token is in storage', async () => {
    const seen: any[] = [];
    const adapter = vi.fn((config: any) => {
      seen.push(config);
      return Promise.resolve({
        status: 200, statusText: 'OK', headers: {}, data: {}, config,
      });
    });
    await http.get('/healthz', { adapter });
    // Authorization should not be set when token is missing —
    // preserves the "anonymous request" semantics for endpoints
    // that allow it (e.g. /healthz, /readyz).
    expect(seen[0].headers.Authorization).toBeUndefined();
  });

  test('refreshes the token after a 401 and retries with the new bearer', async () => {
    localStorage.setItem(TOKEN_KEY, 'stale.token');

    // First call returns 401. Second call (the retry) returns 200.
    let call = 0;
    const adapter = vi.fn((config: any) => {
      call += 1;
      if (call === 1) {
        return Promise.reject({
          response: {
            status: 401,
            data: { error: { code: 'unauthenticated', message: 'expired' } },
            config,
          },
          config,
          isAxiosError: true,
        });
      }
      return Promise.resolve({
        status: 200, statusText: 'OK', headers: {}, data: { ok: true }, config,
      });
    });

    // Spy on the bare-axios POST that attempts the refresh.
    const refreshSpy = vi.spyOn(axios, 'post').mockResolvedValue({
      data: { access_token: 'fresh.token', id_token: 'fresh.id' },
    } as any);

    const res = await http.get('/protected', { adapter });

    expect(refreshSpy).toHaveBeenCalledOnce();
    // Retry must have run, and used the NEW token.
    expect(adapter).toHaveBeenCalledTimes(2);
    const retryCfg = adapter.mock.calls[1][0];
    expect(retryCfg.headers.Authorization).toBe('Bearer fresh.token');
    expect(res.data).toEqual({ ok: true });
    expect(localStorage.getItem(TOKEN_KEY)).toBe('fresh.token');

    refreshSpy.mockRestore();
  });

  test('rejects with normalized Error when the API returns its { error } envelope', async () => {
    localStorage.setItem(TOKEN_KEY, 'valid');
    const adapter = vi.fn((config: any) => Promise.reject({
      response: {
        status: 403,
        data: { error: { code: 'missing_capability', message: 'dns:zones:write', details: { code: 'dns:zones:write' } } },
        config,
      },
      config,
      isAxiosError: true,
    }));
    let captured: any;
    try {
      await http.get('/dns/zones/x', { adapter });
    } catch (err) {
      captured = err;
    }
    expect(captured).toBeInstanceOf(Error);
    expect(captured.message).toBe('dns:zones:write');
    expect(captured.code).toBe('missing_capability');
    expect(captured.statusCode).toBe(403);
    expect(captured.details).toEqual({ code: 'dns:zones:write' });
  });

  test('rejects with raw axios error when no envelope is present', async () => {
    const adapter = vi.fn((config: any) => Promise.reject({
      response: {
        status: 500,
        data: 'internal server error',
        config,
      },
      config,
      message: 'request failed',
      isAxiosError: true,
    }));
    let captured: any;
    try {
      await http.get('/boom', { adapter });
    } catch (err) {
      captured = err;
    }
    // No envelope → raw axios error passes through. Crucially, the
    // interceptor must NOT crash trying to read err.response.data.error.
    expect(captured).toBeDefined();
    expect(captured.response.status).toBe(500);
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });
});
