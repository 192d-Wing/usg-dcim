import { beforeAll, describe, expect, test, vi } from 'vitest';
import { MemoryStorage, fakeBridge, meFetch } from '@/test/fake-bridge';

const storage = new MemoryStorage();
(globalThis as any).localStorage = storage;
const loc = { href: 'https://dcim.test/racks', origin: 'https://dcim.test' };
(globalThis as any).location = loc;

const { http } = await import('./http');
const { authProvider } = await import('./auth-provider');
const { initEmbedded } = await import('./embedded');

beforeAll(async () => {
  const fb = fakeBridge();
  expect(await initEmbedded({
    connect: fb.connect,
    fetch: meFetch({ user: { id: 'u1', email: 'e@x' }, capabilities: ['inventory:racks:read'], auth_mode: 'console' }),
  })).toBe(true);
});

const touched = () => storage.getItem.mock.calls.length + storage.setItem.mock.calls.length
  + storage.removeItem.mock.calls.length;

describe('auth provider when embedded', () => {
  test('identity/permissions from memory, no localStorage', async () => {
    expect(await authProvider.getIdentity!()).toMatchObject({ id: 'u1', email: 'e@x' });
    expect(await authProvider.getPermissions!()).toEqual(['inventory:racks:read']);
    expect(touched()).toBe(0);
  });

  test('check() hits /auth/me without touching storage', async () => {
    const adapter = vi.fn((config: any) => Promise.resolve({
      status: 200, statusText: 'OK', headers: {}, config,
      data: { user: { id: 'u2', email: 'f@x' }, capabilities: ['c'], auth_mode: 'console' },
    }));
    http.defaults.adapter = adapter;
    expect(await authProvider.check()).toEqual({ authenticated: true });
    expect(await authProvider.getIdentity!()).toMatchObject({ id: 'u2' });
    expect(touched()).toBe(0);
  });

  test('check() failure never redirects to /login', async () => {
    http.defaults.adapter = vi.fn((config: any) => Promise.reject(Object.assign(new Error('x'), {
      isAxiosError: true, config, response: { status: 401, headers: {}, data: {}, config },
    })));
    const r = await authProvider.check();
    expect(r.authenticated).toBe(false);
    expect(r.redirectTo).toBeUndefined();
    expect(await authProvider.onError({ statusCode: 401 })).toEqual({});
  });

  test('logout and login do not touch storage or navigate', async () => {
    expect(await authProvider.logout({})).toEqual({ success: true });
    expect((await authProvider.login({})).success).toBe(false);
    expect(loc.href).toBe('https://dcim.test/racks');
    expect(touched()).toBe(0);
  });
});
