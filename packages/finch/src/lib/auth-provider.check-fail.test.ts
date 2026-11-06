import { beforeEach, describe, expect, test, vi } from 'vitest';
import { MemoryStorage, fakeBridge, meFetch } from '@/test/fake-bridge';

(globalThis as any).localStorage = new MemoryStorage();
(globalThis as any).location = { href: 'https://dcim.test/racks', origin: 'https://dcim.test' };

const { http } = await import('./http');
const { embeddedAuthProvider } = await import('./auth-provider');
const { initEmbedded, isSessionEnded, resetEmbeddedForTests, SESSION_ENDED_MESSAGE } = await import('./embedded');

let fb: ReturnType<typeof fakeBridge>;
beforeEach(async () => {
  resetEmbeddedForTests();
  fb = fakeBridge();
  expect(await initEmbedded({ connect: fb.connect, fetch: meFetch({ user: { id: 'u' }, auth_mode: 'console' }) })).toBe(true);
});

const ok = (data: unknown) => vi.fn((config: any) => Promise.resolve({ status: 200, statusText: 'OK', headers: {}, config, data }));

describe('embedded check() failure without 401 (N-2)', () => {
  test('auth_mode not console ends the session and notifies once', async () => {
    http.defaults.adapter = ok({ user: { id: 'u' }, auth_mode: 'oidc' });
    expect((await embeddedAuthProvider.check()).authenticated).toBe(false);
    await embeddedAuthProvider.check();
    expect(isSessionEnded()).toBe(true);
    expect(fb.bridge()!.calls.notify).toEqual([['warning', SESSION_ENDED_MESSAGE]]);
  });

  test('network failure ends the session and notifies once', async () => {
    http.defaults.adapter = vi.fn(() => Promise.reject(new Error('Network Error')));
    expect((await embeddedAuthProvider.check()).authenticated).toBe(false);
    await embeddedAuthProvider.check();
    expect(isSessionEnded()).toBe(true);
    expect(fb.bridge()!.calls.notify).toEqual([['warning', SESSION_ENDED_MESSAGE]]);
  });
});
