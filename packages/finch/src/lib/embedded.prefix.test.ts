// Console build: BASE_URL=/t/dcim/. Module state is per test file.
import { afterEach, describe, expect, test, vi } from 'vitest';

vi.stubEnv('BASE_URL', '/t/dcim/');
const { BASE, apiBase } = await import('./base-path');
const { initEmbedded, onConsoleNavigate, toToolPath, resetEmbeddedForTests } = await import('./embedded');
const { fakeBridge, meFetch } = await import('@/test/fake-bridge');

afterEach(() => resetEmbeddedForTests());

describe('embedded under /t/dcim/', () => {
  test('base and API', () => {
    expect(BASE).toBe('/t/dcim/');
    expect(apiBase()).toBe('/t/dcim/api/v1');
  });

  test('toToolPath strips the prefix then validates', () => {
    expect(toToolPath('/t/dcim/racks')).toBe('/racks');
    expect(toToolPath('/t/dcim')).toBe('/');
    expect(toToolPath('/racks')).toBe('/racks');
    expect(toToolPath('/t/dcim//evil.example')).toBeNull();
  });

  test('detection calls /t/dcim/api/v1/auth/me; Navigate is prefix-stripped', async () => {
    const fb = fakeBridge();
    const fetch = meFetch({ user: { id: 'u' }, capabilities: [], auth_mode: 'console' });
    expect(await initEmbedded({ connect: fb.connect, fetch })).toBe(true);
    expect((fetch.mock.calls[0] as unknown as [string])[0]).toBe('/t/dcim/api/v1/auth/me');
    const seen: string[] = [];
    onConsoleNavigate((p) => seen.push(p));
    fb.bridge()!.handlers.onNavigate?.('/t/dcim/ipam');
    fb.bridge()!.handlers.onNavigate?.('/sites');
    expect(seen).toEqual(['/ipam', '/sites']);
  });
});
