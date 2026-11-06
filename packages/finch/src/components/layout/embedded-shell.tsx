// Shell used when finch is embedded in the ops console. The console owns
// the chrome: no TopNavigation, SideNavigation, services/region/account
// utilities or fabric picker here. Title, breadcrumbs and global
// notifications go to the console through the bridge; route changes are
// reported with pathChanged and console Navigate messages drive the router.

import {
  isValidElement, useEffect, useRef, useState, useSyncExternalStore, type ReactNode,
} from 'react';
import { Outlet, useLocation, useNavigate } from 'react-router';
import AppLayout from '@cloudscape-design/components/app-layout';
import Box from '@cloudscape-design/components/box';
import Alert from '@cloudscape-design/components/alert';
import Button from '@cloudscape-design/components/button';
import { applyMode, Mode } from '@cloudscape-design/global-styles';
import { toast, useSonner } from 'sonner';
import {
  getBridge, isSessionEnded, onConsoleNavigate, onConsoleTheme, retrySession, subscribeEmbedded,
} from '@/lib/embedded';
import { stripBase } from '@/lib/base-path';
import type { Level } from '@/vendor/console-sdk/console-sdk';

/** Shown instead of the page when the session ended. It is announced once
 *  through the console (bridge.notify in embedded.ts), so no role="alert"
 *  here; focus moves to the heading so keyboard/SR users are not left on
 *  the iframe body. Retry asks the console to refresh, then re-checks. */
export function SessionEndedState({ initialFailed = false }: { initialFailed?: boolean } = {}) {
  const heading = useRef<HTMLHeadingElement>(null);
  const [busy, setBusy] = useState(false);
  const [failed, setFailed] = useState(initialFailed);
  useEffect(() => { heading.current?.focus(); }, []);
  const retry = async () => {
    setBusy(true);
    setFailed(false);
    const ok = await retrySession();
    setBusy(false);
    if (!ok) {
      setFailed(true);
      heading.current?.focus();
    }
  };
  return (
    <Box padding="xxl" textAlign="center" data-testid="session-ended">
      <h1
        ref={heading}
        tabIndex={-1}
        id="session-ended-heading"
        aria-describedby={failed ? 'session-retry-failed' : undefined}
      >Session ended</h1>
      <Box variant="p" color="text-body-secondary">
        Your console session has ended. Sign in again from the console to continue.
      </Box>
      {failed && (
        // Rendered only on failure, so role="status" announces it once.
        <p role="status" id="session-retry-failed" data-testid="session-retry-failed">
          <Box variant="span" color="text-status-error">
            The session is still not active. Sign in from the console and try again.
          </Box>
        </p>
      )}
      <Button onClick={() => { void retry(); }} loading={busy}>Retry session</Button>
    </Box>
  );
}

/** First-load fallback (auth check failed before EmbeddedShell mounts):
 *  keeps a main landmark in the frame. */
export function SessionEndedPage() {
  return <main><SessionEndedState /></main>;
}

const DARK_QUERY = '(prefers-color-scheme: dark)';
type MediaLike = Pick<MediaQueryList, 'matches' | 'addEventListener' | 'removeEventListener'>;
type ThemeDeps = {
  matchMedia?: (q: string) => MediaLike;
  apply?: (dark: boolean) => void;
};
let stopSystemTheme: (() => void) | null = null;

/** Removes the "system" prefers-color-scheme listener, if any. */
export function stopSystemThemeListener(): void {
  stopSystemTheme?.();
  stopSystemTheme = null;
}

function applyDark(dark: boolean): void {
  document.documentElement.classList.toggle('dark', dark);
  applyMode(dark ? Mode.Dark : Mode.Light);
}

/** Maps console ThemeChanged ("light" | "dark" | "system") to Cloudscape
 *  mode and the Tailwind class. "system" follows prefers-color-scheme and
 *  keeps a change listener until an explicit light/dark arrives. */
export function applyConsoleTheme(theme: string, deps: ThemeDeps = {}): void {
  const apply = deps.apply ?? applyDark;
  stopSystemThemeListener();
  if (theme === 'system') {
    const mm = deps.matchMedia ?? (typeof matchMedia === 'function' ? matchMedia : undefined);
    if (!mm) { apply(false); return; }
    const mq = mm(DARK_QUERY);
    const onChange = (e: { matches: boolean }) => apply(e.matches);
    mq.addEventListener('change', onChange);
    stopSystemTheme = () => mq.removeEventListener('change', onChange);
    apply(mq.matches);
    return;
  }
  apply(theme === 'dark');
}

/** Plain text of a toast title/description (strings, numbers, JSX text). */
export function toastText(node: unknown): string {
  if (node == null || typeof node === 'boolean') return '';
  if (typeof node === 'string' || typeof node === 'number') return String(node);
  if (typeof node === 'function') return toastText((node as () => ReactNode)());
  if (Array.isArray(node)) return node.map(toastText).join('');
  if (isValidElement(node)) {
    return toastText((node.props as { children?: ReactNode }).children);
  }
  return '';
}

const FALLBACK: Record<Level, string> = {
  error: 'DCIM: an error occurred.',
  warning: 'DCIM: warning.',
  success: 'DCIM: done.',
  info: 'DCIM: notice.',
};

/** Console message for a toast: plain text, never dropped. */
export function toastMessage(level: Level, title: unknown, description: unknown): string {
  const msg = [toastText(title).trim(), toastText(description).trim()].filter(Boolean).join(': ');
  if (msg) return msg;
  if (import.meta.env?.DEV) console.warn('finch: toast without text forwarded with fallback');
  return FALLBACK[level];
}

/** Console title for a route; also used for document.title. */
export function consoleTitle(crumbs: { text: string }[]): string {
  const leaf = crumbs[crumbs.length - 1]?.text;
  return !leaf || leaf === 'Home' ? 'DCIM' : leaf;
}

/** Forwards sonner toasts to the console instead of rendering them in-frame. */
function ToastForwarder() {
  const { toasts } = useSonner();
  const seen = useRef(new Set<string | number>());
  useEffect(() => {
    const bridge = getBridge();
    for (const t of toasts) {
      if (seen.current.has(t.id)) continue;
      seen.current.add(t.id);
      if (t.type === 'loading') continue;
      const level: Level =
        t.type === 'error' ? 'error'
          : t.type === 'success' ? 'success'
            : t.type === 'warning' ? 'warning' : 'info';
      bridge?.notify(level, toastMessage(level, t.title, t.description));
      toast.dismiss(t.id);
    }
  }, [toasts]);
  return null;
}

export function EmbeddedShell({ crumbs, unmatchedSite = null }: {
  crumbs: { text: string; href: string }[];
  unmatchedSite?: string | null;
}) {
  const location = useLocation();
  const navigate = useNavigate();
  const ended = useSyncExternalStore(subscribeEmbedded, isSessionEnded, () => false);

  useEffect(() => onConsoleNavigate((path) => navigate(path)), [navigate]);
  useEffect(() => {
    const off = onConsoleTheme(applyConsoleTheme);
    return () => { off(); stopSystemThemeListener(); };
  }, []);

  useEffect(() => {
    const bridge = getBridge();
    if (!bridge) return;
    // useLocation is already basename-relative; stripBase is a guard.
    bridge.pathChanged(stripBase(location.pathname) + location.search);
    bridge.setBreadcrumbs(crumbs.map((c) => [c.text, c.href] as const));
    const title = consoleTitle(crumbs);
    bridge.setTitle(title);
    document.title = title === 'DCIM' ? 'DCIM' : `${title} | DCIM`;
  }, [location.pathname, location.search, crumbs]);

  return (
    <>
      <ToastForwarder />
      <AppLayout
        navigationHide
        toolsHide
        content={ended ? <SessionEndedState /> : (
          <>
            {unmatchedSite && (
              <Box margin={{ bottom: 'm' }} data-testid="scope-unmatched">
                <Alert type="info" header="No matching DCIM fabric">
                  No DCIM fabric for site {unmatchedSite}. Change site in the console.
                  Fabric-scoped views below are empty.
                </Alert>
              </Box>
            )}
            <Outlet />
          </>
        )}
      />
    </>
  );
}
