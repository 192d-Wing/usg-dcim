import { vi } from 'vitest';
import type { Bridge, Handlers } from '@/vendor/console-sdk/console-sdk';

export type FakeBridge = Bridge & { handlers: Handlers; calls: Record<string, unknown[][]> };

export function fakeBridge(scopeHeader: string | null = null): {
  bridge: () => FakeBridge | null;
  connect: (h: Handlers) => Promise<Bridge | null>;
} {
  let b: FakeBridge | null = null;
  const calls: Record<string, unknown[][]> = {};
  const rec = (name: string) => vi.fn((...a: unknown[]) => { (calls[name] ??= []).push(a); });
  return {
    bridge: () => b,
    connect: async (h: Handlers) => {
      b = {
        handlers: h,
        calls,
        consoleOrigin: 'https://console.test',
        toolId: 'dcim',
        state: () => null,
        dropped: () => 0,
        setTitle: rec('setTitle'),
        setBreadcrumbs: rec('setBreadcrumbs'),
        notify: rec('notify'),
        setBadge: rec('setBadge'),
        navigateConsole: rec('navigateConsole'),
        pathChanged: rec('pathChanged'),
        requestSessionRefresh: rec('requestSessionRefresh'),
        focusShell: rec('focusShell'),
        scopeHeader: () => scopeHeader,
        close: rec('close'),
      };
      return b;
    },
  };
}

export function meFetch(body: unknown, status = 200) {
  return vi.fn(async () => new Response(JSON.stringify(body), {
    status, headers: { 'content-type': 'application/json' },
  }));
}

export class MemoryStorage {
  store = new Map<string, string>();
  getItem = vi.fn((k: string) => this.store.get(k) ?? null);
  setItem = vi.fn((k: string, v: string) => { this.store.set(k, String(v)); });
  removeItem = vi.fn((k: string) => { this.store.delete(k); });
  clear(): void { this.store.clear(); }
}
