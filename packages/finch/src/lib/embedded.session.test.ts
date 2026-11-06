import { beforeEach, describe, expect, test, vi } from 'vitest';
import { fakeBridge, meFetch } from '@/test/fake-bridge';
import {
  initEmbedded, isSessionEnded, markSessionEnded, resetEmbeddedForTests, retrySession,
  SESSION_ENDED_MESSAGE,
} from './embedded';

const ME = { user: { id: 'u' }, capabilities: ['x'], auth_mode: 'console' };
let fb: ReturnType<typeof fakeBridge>;

beforeEach(async () => {
  resetEmbeddedForTests();
  fb = fakeBridge();
  expect(await initEmbedded({ connect: fb.connect, fetch: meFetch(ME) })).toBe(true);
});

describe('session ended (A11Y-002)', () => {
  test('announced once via bridge.notify, however many 401s', () => {
    markSessionEnded();
    markSessionEnded();
    fb.bridge()!.handlers.onSessionRefreshed?.(false, null);
    expect(isSessionEnded()).toBe(true);
    expect(fb.bridge()!.calls.notify).toEqual([['warning', SESSION_ENDED_MESSAGE]]);
  });

  test('retry requests a session refresh, waits for it, re-checks /auth/me and clears the state', async () => {
    markSessionEnded();
    const before = fb.bridge()!.calls.requestSessionRefresh.length;
    const f = meFetch(ME);
    const p = retrySession(f, 10_000);
    expect(fb.bridge()!.calls.requestSessionRefresh.length).toBe(before + 1);
    expect(f).not.toHaveBeenCalled();
    fb.bridge()!.handlers.onSessionRefreshed?.(true, null);
    expect(await p).toBe(true);
    expect(f).toHaveBeenCalledWith('/api/v1/auth/me', expect.objectContaining({ method: 'GET' }));
    expect(isSessionEnded()).toBe(false);
  });

  test('retry that still gets 401 keeps session ended (after refresh timeout)', async () => {
    markSessionEnded();
    expect(await retrySession(meFetch({}, 401), 1)).toBe(false);
    expect(isSessionEnded()).toBe(true);
  });

  test('a second session end after recovery is announced again', async () => {
    markSessionEnded();
    const p = retrySession(meFetch(ME), 10_000);
    fb.bridge()!.handlers.onSessionRefreshed?.(true, null);
    await p;
    markSessionEnded();
    expect(fb.bridge()!.calls.notify.length).toBe(2);
  });

  test('no reload is involved', () => {
    const reload = vi.fn();
    (globalThis as any).location = { reload };
    markSessionEnded();
    return retrySession(meFetch(ME), 1).then(() => expect(reload).not.toHaveBeenCalled());
  });
});
