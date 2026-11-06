import { afterEach, beforeAll, describe, expect, test, vi } from 'vitest';
import { MemoryStorage, fakeBridge, meFetch } from '@/test/fake-bridge';

const storage = new MemoryStorage();
(globalThis as any).localStorage = storage;

const { http, TOKEN_KEY } = await import('./http');
const { initEmbedded, isSessionEnded } = await import('./embedded');

const fb = fakeBridge('site=east');
const ok = (seen: any[]) => vi.fn((config: any) => {
  seen.push(config);
  return Promise.resolve({ status: 200, statusText: 'OK', headers: {}, data: {}, config });
});

beforeAll(async () => {
  storage.setItem(TOKEN_KEY, 'leftover.standalone.token');
  expect(await initEmbedded({
    connect: fb.connect,
    fetch: meFetch({ user: { id: 'u' }, capabilities: [], auth_mode: 'console' }),
  })).toBe(true);
  storage.getItem.mockClear();
});

afterEach(() => storage.getItem.mockClear());

describe('http client when embedded', () => {
  test('GET: no bearer (even with a stored token), no CSRF, scope header added, no localStorage read', async () => {
    const seen: any[] = [];
    await http.get('/racks', { adapter: ok(seen) });
    expect(seen[0].headers.Authorization).toBeUndefined();
    expect(seen[0].headers['X-DCIM-CSRF']).toBeUndefined();
    expect(seen[0].headers['x-console-scope']).toBe('site=east');
    expect(storage.getItem).not.toHaveBeenCalled();
  });

  test.each(['post', 'put', 'patch', 'delete'] as const)('%s adds X-DCIM-CSRF: 1 and no bearer', async (m) => {
    const seen: any[] = [];
    await http.request({ url: '/racks', method: m, data: {}, adapter: ok(seen) });
    expect(seen[0].headers['X-DCIM-CSRF']).toBe('1');
    expect(seen[0].headers.Authorization).toBeUndefined();
  });

  test('caller-supplied Authorization is stripped', async () => {
    const seen: any[] = [];
    await http.get('/x', { headers: { Authorization: 'Bearer nope' }, adapter: ok(seen) });
    expect(seen[0].headers.Authorization).toBeUndefined();
  });

  test('401: no /auth/refresh, one session refresh request, session-ended state', async () => {
    const urls: string[] = [];
    const adapter = vi.fn((config: any) => {
      urls.push(config.url);
      return Promise.reject(Object.assign(new Error('401'), {
        isAxiosError: true, config,
        response: { status: 401, statusText: 'Unauthorized', headers: {}, data: { detail: 'invalid console assertion' }, config },
      }));
    });
    await expect(http.get('/racks', { adapter })).rejects.toThrow('invalid console assertion');
    await expect(http.get('/sites', { adapter })).rejects.toThrow();
    expect(urls).toEqual(['/racks', '/sites']);
    expect(isSessionEnded()).toBe(true);
    expect(fb.bridge()!.calls.requestSessionRefresh).toHaveLength(1);
    expect(storage.setItem).not.toHaveBeenCalled();
  });
});
