import { afterEach, describe, expect, test, vi } from 'vitest';
import {
  initEmbedded, isEmbedded, isSafeToolPath, isSessionEnded, markSessionEnded,
  getConsoleScope, onConsoleNavigate, resetEmbeddedForTests,
} from './embedded';
import { fakeBridge, meFetch } from '@/test/fake-bridge';

const consoleMe = { user: { id: 'u1', email: 'a@b' }, capabilities: ['x:y:z'], auth_mode: 'console' };

afterEach(() => resetEmbeddedForTests());

describe('embedded detection', () => {
  test('not framed: standalone, connect never called', async () => {
    const win = {} as { parent: unknown };
    win.parent = win;
    expect(await initEmbedded({ window: win })).toBe(false);
    expect(isEmbedded()).toBe(false);
  });

  test('no window (node): standalone', async () => {
    expect(await initEmbedded({ window: undefined })).toBe(false);
  });

  test('framed but connect() returns null (no console): standalone', async () => {
    const fetch = meFetch(consoleMe);
    expect(await initEmbedded({ connect: async () => null, fetch })).toBe(false);
    expect(fetch).not.toHaveBeenCalled();
    expect(isEmbedded()).toBe(false);
  });

  test('bridge but auth_mode is not console (iframe without console mode): standalone, bridge closed', async () => {
    for (const mode of ['oidc', 'local', 'api_token', undefined]) {
      const fb = fakeBridge();
      const ok = await initEmbedded({ connect: fb.connect, fetch: meFetch({ ...consoleMe, auth_mode: mode }) });
      expect(ok).toBe(false);
      expect(isEmbedded()).toBe(false);
      expect(fb.bridge()?.calls.close).toHaveLength(1);
      resetEmbeddedForTests();
    }
  });

  test('bridge but /auth/me fails: standalone', async () => {
    const fb = fakeBridge();
    expect(await initEmbedded({ connect: fb.connect, fetch: meFetch({}, 401) })).toBe(false);
    const fb2 = fakeBridge();
    expect(await initEmbedded({ connect: fb2.connect, fetch: vi.fn(async () => { throw new Error('net'); }) })).toBe(false);
    expect(isEmbedded()).toBe(false);
  });

  test('bridge + auth_mode console: embedded; /auth/me sent without Authorization', async () => {
    const fb = fakeBridge();
    const fetch = meFetch(consoleMe);
    expect(await initEmbedded({ connect: fb.connect, fetch })).toBe(true);
    expect(isEmbedded()).toBe(true);
    const init = (fetch.mock.calls[0] as unknown as [string, RequestInit])[1];
    expect(JSON.stringify(init.headers ?? {})).not.toMatch(/authorization/i);
  });
});

describe('bridge events', () => {
  test('scope from Init and ScopeChanged', async () => {
    const fb = fakeBridge();
    await initEmbedded({ connect: fb.connect, fetch: meFetch(consoleMe) });
    const h = fb.bridge()!.handlers;
    h.onInit?.({
      user: { id: 'u', display_name: 'U', roles: [] },
      scope: { site: 'east', tenant: null, environment: null },
      theme: 'dark', locale: 'en', path: null, toolId: 'dcim', protocol: 1,
    });
    expect(getConsoleScope()?.site).toBe('east');
    h.onScope?.({ site: 'west', tenant: null, environment: null });
    expect(getConsoleScope()?.site).toBe('west');
  });

  test('Navigate is path-checked and replayed to the first subscriber', async () => {
    const fb = fakeBridge();
    await initEmbedded({ connect: fb.connect, fetch: meFetch(consoleMe) });
    fb.bridge()!.handlers.onNavigate?.('/racks');
    const seen: string[] = [];
    onConsoleNavigate((p) => seen.push(p));
    fb.bridge()!.handlers.onNavigate?.('//evil.example');
    fb.bridge()!.handlers.onNavigate?.('/ipam');
    expect(seen).toEqual(['/racks', '/ipam']);
  });

  test('isSafeToolPath', () => {
    expect(isSafeToolPath('/sites/1?x=1')).toBe(true);
    for (const p of ['', 'sites', '//x', 'https://x', '/a\\b', '/a\nb']) expect(isSafeToolPath(p)).toBe(false);
  });

  test('markSessionEnded requests one refresh; ok refresh clears it', async () => {
    const fb = fakeBridge();
    await initEmbedded({ connect: fb.connect, fetch: meFetch(consoleMe) });
    markSessionEnded();
    markSessionEnded();
    expect(isSessionEnded()).toBe(true);
    expect(fb.bridge()!.calls.requestSessionRefresh).toHaveLength(1);
    fb.bridge()!.handlers.onSessionRefreshed?.(true, null);
    expect(isSessionEnded()).toBe(false);
  });

  test('markSessionEnded is a no-op standalone', () => {
    markSessionEnded();
    expect(isSessionEnded()).toBe(false);
  });
});

describe('toToolPath (standalone base)', async () => {
  const { toToolPath } = await import('./embedded');
  test('safe paths pass, unsafe rejected', () => {
    expect(toToolPath('/racks?x=1')).toBe('/racks?x=1');
    expect(toToolPath('//evil')).toBeNull();
    expect(toToolPath('https://x')).toBeNull();
  });
});
