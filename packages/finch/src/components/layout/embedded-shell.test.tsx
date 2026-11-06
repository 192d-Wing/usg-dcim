import { describe, expect, test, vi } from 'vitest';
import { applyConsoleTheme, consoleTitle, toastMessage, toastText } from './embedded-shell';

function fakeMq(matches: boolean) {
  const ls = new Set<(e: { matches: boolean }) => void>();
  return {
    matches,
    addEventListener: vi.fn((_: string, l: any) => { ls.add(l); }),
    removeEventListener: vi.fn((_: string, l: any) => { ls.delete(l); }),
    fire(m: boolean) { ls.forEach((l) => l({ matches: m })); },
    size: () => ls.size,
  };
}

describe('applyConsoleTheme (A11Y-001)', () => {
  test.each([['light', false], ['dark', true]] as const)('%s is explicit', (t, dark) => {
    const apply = vi.fn();
    const mm = vi.fn();
    applyConsoleTheme(t, { apply, matchMedia: mm });
    expect(apply).toHaveBeenCalledWith(dark);
    expect(mm).not.toHaveBeenCalled();
  });

  test.each([true, false])('system follows prefers-color-scheme (dark=%s)', (m) => {
    const apply = vi.fn();
    const mq = fakeMq(m);
    applyConsoleTheme('system', { apply, matchMedia: () => mq as any });
    expect(apply).toHaveBeenLastCalledWith(m);
    applyConsoleTheme('light', { apply });
  });

  test('system tracks OS changes; explicit theme removes the listener', () => {
    const apply = vi.fn();
    const mq = fakeMq(false);
    applyConsoleTheme('system', { apply, matchMedia: () => mq as any });
    mq.fire(true);
    expect(apply).toHaveBeenLastCalledWith(true);
    applyConsoleTheme('dark', { apply });
    expect(mq.removeEventListener).toHaveBeenCalled();
    expect(mq.size()).toBe(0);
    apply.mockClear();
    mq.fire(false);
    expect(apply).not.toHaveBeenCalled();
  });

  test('switching system -> system does not stack listeners', () => {
    const apply = vi.fn();
    const mq = fakeMq(false);
    applyConsoleTheme('system', { apply, matchMedia: () => mq as any });
    applyConsoleTheme('system', { apply, matchMedia: () => mq as any });
    expect(mq.size()).toBe(1);
    applyConsoleTheme('light', { apply });
  });
});

describe('toast forwarding (A11Y-004)', () => {
  test('JSX title/description become plain text', () => {
    expect(toastText(<>Failed: <code>rack-1</code></>)).toBe('Failed: rack-1');
    expect(toastMessage('error', <b>Save failed</b>, () => 'try again')).toBe('Save failed: try again');
    expect(toastMessage('info', 'Saved', 3)).toBe('Saved: 3');
  });
  test('empty content falls back instead of being dropped', () => {
    const w = vi.spyOn(console, 'warn').mockImplementation(() => {});
    expect(toastMessage('error', undefined, null)).toMatch(/error/);
    w.mockRestore();
  });
});

describe('consoleTitle (A11Y-006)', () => {
  test('Home and empty map to DCIM; leaf otherwise', () => {
    expect(consoleTitle([{ text: 'Home' }])).toBe('DCIM');
    expect(consoleTitle([])).toBe('DCIM');
    expect(consoleTitle([{ text: 'Home' }, { text: 'Racks' }])).toBe('Racks');
  });
});

describe('session ended markup (N-1, N-2)', async () => {
  const { renderToStaticMarkup } = await import('react-dom/server');
  const { SessionEndedPage, SessionEndedState } = await import('./embedded-shell');

  test('retry-failed note is a status region linked from the heading', () => {
    const html = renderToStaticMarkup(<SessionEndedState initialFailed />);
    expect(html).toMatch(/<p role="status" id="session-retry-failed"/);
    expect(html).toMatch(/<h1[^>]*aria-describedby="session-retry-failed"/);
  });

  test('no status region or describedby before a failure', () => {
    const html = renderToStaticMarkup(<SessionEndedState />);
    expect(html).not.toContain('role="status"');
    expect(html).not.toContain('aria-describedby');
  });

  test('first-load page keeps a main landmark', () => {
    expect(renderToStaticMarkup(<SessionEndedPage />)).toMatch(/^<main>.*Session ended.*<\/main>$/s);
  });
});

describe('stopSystemThemeListener (nit)', async () => {
  const { stopSystemThemeListener } = await import('./embedded-shell');
  test('removes the system listener', () => {
    const mq = fakeMq(false);
    applyConsoleTheme('system', { apply: vi.fn(), matchMedia: () => mq as any });
    expect(mq.size()).toBe(1);
    stopSystemThemeListener();
    expect(mq.size()).toBe(0);
  });
});
