// Embedded-in-console detection and bridge state.
//
// finch runs "embedded" only when BOTH hold:
//   1. the vendored console SDK's connect() returns a bridge (the console
//      served /_console/context on this origin and we are framed by it), and
//   2. otter-go's /auth/me reports auth_mode "console" (the request was
//      authenticated by the gateway's signed assertion, which the proxy adds;
//      finch never sees or sends it).
// Being framed alone never counts. Everything else is standalone, and the
// standalone code paths stay exactly as they were.

import { apiBase, stripBase } from './base-path';
import { connect } from '@/vendor/console-sdk/console-sdk';
import type { Bridge, ConsoleState, Handlers, Level, Scope } from '@/vendor/console-sdk/console-sdk';

export type EmbeddedIdentity = {
  id: string | null;
  email: string | null;
  capabilities: string[];
};

type Listener = () => void;

type EmbeddedState = {
  bridge: Bridge | null;
  identity: EmbeddedIdentity | null;
  scope: Scope | null;
  theme: string | null;
  sessionEnded: boolean;
};

const initial = (): EmbeddedState => ({
  bridge: null, identity: null, scope: null, theme: null, sessionEnded: false,
});

let state: EmbeddedState = initial();
const listeners = new Set<Listener>();
const navigateListeners = new Set<(path: string) => void>();
const themeListeners = new Set<(theme: string) => void>();
let refreshRequested = false;

function update(patch: Partial<EmbeddedState>): void {
  state = { ...state, ...patch };
  listeners.forEach((l) => l());
}

export function isEmbedded(): boolean {
  return state.bridge !== null;
}

export function getBridge(): Bridge | null {
  return state.bridge;
}

export function getEmbeddedIdentity(): EmbeddedIdentity | null {
  return state.identity;
}

export function setEmbeddedIdentity(identity: EmbeddedIdentity | null): void {
  update({ identity });
}

export function getConsoleScope(): Scope | null {
  return state.scope;
}

export function isSessionEnded(): boolean {
  return state.sessionEnded;
}

/** Subscribe to scope / identity / session-ended changes (useSyncExternalStore). */
export function subscribeEmbedded(l: Listener): () => void {
  listeners.add(l);
  return () => { listeners.delete(l); };
}

// A Navigate/Init path that arrives before the router subscribes is kept
// and replayed to the first subscriber.
let pendingPath: string | null = null;
function emitNavigate(path: string): void {
  if (navigateListeners.size === 0) { pendingPath = path; return; }
  navigateListeners.forEach((l) => l(path));
}

export function onConsoleNavigate(l: (path: string) => void): () => void {
  navigateListeners.add(l);
  if (pendingPath) { const p = pendingPath; pendingPath = null; l(p); }
  return () => { navigateListeners.delete(l); };
}

export function onConsoleTheme(l: (theme: string) => void): () => void {
  themeListeners.add(l);
  if (state.theme) l(state.theme);
  return () => { themeListeners.delete(l); };
}

/** Called on a 401 while embedded: ask the console once to refresh the
 *  session, and flip the app into its "session ended" state. Never
 *  redirects to /login (the console owns sign-in). */
export function markSessionEnded(): void {
  if (!state.bridge) return;
  if (!refreshRequested) {
    refreshRequested = true;
    state.bridge.requestSessionRefresh();
  }
  endSession();
}

export const SESSION_ENDED_MESSAGE =
  'DCIM session ended. Sign in again from the console, then choose Retry session.';

/** Flip into session-ended and announce it once through the console live
 *  region (the in-frame view moves focus to its heading but has no
 *  role="alert", so it is not announced twice). */
export function endSession(): void {
  if (state.sessionEnded) return;
  update({ sessionEnded: true });
  state.bridge?.notify('warning', SESSION_ENDED_MESSAGE);
}

let retryInFlight: Promise<boolean> | null = null;
const refreshWaiters = new Set<() => void>();

/** Resolves on the console's next SessionRefreshed, or after timeoutMs. */
function waitForRefresh(timeoutMs: number): Promise<void> {
  return new Promise((resolve) => {
    const done = () => { clearTimeout(t); refreshWaiters.delete(done); resolve(); };
    const t = setTimeout(done, timeoutMs);
    refreshWaiters.add(done);
  });
}

/** "Retry session": ask the console to refresh the session, then re-check
 *  /auth/me. Clears session-ended on success; no page reload. */
export function retrySession(
  f: (input: string, init: RequestInit) => Promise<Response> = (i, init) => fetch(i, init),
  refreshTimeoutMs = 5000,
): Promise<boolean> {
  if (!state.bridge) return Promise.resolve(false);
  if (retryInFlight) return retryInFlight;
  const waited = waitForRefresh(refreshTimeoutMs);
  state.bridge.requestSessionRefresh();
  refreshRequested = true;
  retryInFlight = (async () => {
    await waited;
    try {
      const r = await f(`${apiBase()}/auth/me`, {
        method: 'GET', credentials: 'same-origin', headers: { Accept: 'application/json' },
      });
      const me = r.ok ? ((await r.json()) as MeResponse) : null;
      if (me?.auth_mode === 'console') {
        refreshRequested = false;
        update({
          sessionEnded: false,
          identity: {
            id: me.user?.id ?? null,
            email: me.user?.email ?? null,
            capabilities: me.capabilities ?? [],
          },
        });
        return true;
      }
    } catch { /* stays ended */ }
    return false;
  })().finally(() => { retryInFlight = null; });
  return retryInFlight;
}

/** Global notice to the console (local inline alerts stay in place). */
export function notifyConsole(level: Level, message: string): void {
  state.bridge?.notify(level, message);
}

/** Same checks as the console's valid_path: an absolute in-app path, no
 *  scheme/authority, no backslashes or control characters. */
/** Paths may arrive with the /t/<id> prefix; it is stripped first so the
 *  router (which has its own basename) gets an in-app path. */
export function toToolPath(path: string): string | null {
  if (typeof path !== 'string') return null;
  const p = stripBase(path);
  return isSafeToolPath(p) ? p : null;
}

export function isSafeToolPath(path: string): boolean {
  if (typeof path !== 'string' || path.length === 0 || path.length > 2048) return false;
  if (!path.startsWith('/') || path.startsWith('//')) return false;
  // eslint-disable-next-line no-control-regex -- reject control characters on purpose
  if (/[\\\u0000-\u001f\u007f]/.test(path)) return false;
  return true;
}

type MeResponse = {
  user?: { id?: string | null; email?: string | null };
  capabilities?: string[];
  auth_mode?: string;
};

export type DetectDeps = {
  /** Defaults to the vendored SDK's connect(). */
  connect?: (h: Handlers) => Promise<Bridge | null>;
  /** Defaults to globalThis.fetch. */
  fetch?: (input: string, init: RequestInit) => Promise<Response>;
  /** Defaults to globalThis.window (skips connect when not framed). */
  window?: { parent: unknown } | undefined;
};

/** Detects embedded mode. Resolves true only for bridge + auth_mode console.
 *  On any other outcome the bridge (if any) is closed and finch is standalone. */
export async function initEmbedded(deps: DetectDeps = {}): Promise<boolean> {
  const win = 'window' in deps ? deps.window : (typeof window === 'undefined' ? undefined : window);
  // Not framed (or no DOM): standalone without even asking for /_console/context.
  if (!deps.connect && (win === undefined || win.parent === win)) return false;

  const handlers: Handlers = {
    onInit: (s: ConsoleState) => {
      update({ scope: s.scope, theme: s.theme });
      themeListeners.forEach((l) => l(s.theme));
      const p = s.path ? toToolPath(s.path) : null;
      if (p) emitNavigate(p);
    },
    onScope: (scope) => update({ scope }),
    onTheme: (theme) => {
      update({ theme });
      themeListeners.forEach((l) => l(theme));
    },
    onNavigate: (path) => {
      const p = toToolPath(path);
      if (p) emitNavigate(p);
    },
    onSessionRefreshed: (ok) => {
      [...refreshWaiters].forEach((w) => w());
      if (ok) {
        refreshRequested = false;
        update({ sessionEnded: false });
      } else {
        endSession();
      }
    },
  };

  let bridge: Bridge | null = null;
  try {
    bridge = await (deps.connect ?? ((h) => connect(h)))(handlers);
  } catch {
    bridge = null;
  }
  if (!bridge) return false;

  const f = deps.fetch ?? ((i: string, init: RequestInit) => fetch(i, init));
  let me: MeResponse | null = null;
  try {
    // No Authorization header: in console mode the proxy authenticates us.
    const r = await f(`${apiBase()}/auth/me`, {
      method: 'GET',
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    });
    if (r.ok) me = (await r.json()) as MeResponse;
  } catch {
    me = null;
  }
  if (!me || me.auth_mode !== 'console') {
    bridge.close();
    return false;
  }

  update({
    bridge,
    identity: {
      id: me.user?.id ?? null,
      email: me.user?.email ?? null,
      capabilities: me.capabilities ?? [],
    },
  });
  return true;
}

/** Test helper: back to standalone. */
export function resetEmbeddedForTests(): void {
  state.bridge?.close();
  state = initial();
  refreshRequested = false;
  retryInFlight = null;
  refreshWaiters.clear();
  pendingPath = null;
  listeners.clear();
  navigateListeners.clear();
  themeListeners.clear();
}
