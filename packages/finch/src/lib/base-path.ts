// Build-time base path. Standalone builds use '/' (default). The console
// build (VITE_BASE=/t/dcim/) is served by the gateway under /t/dcim/ on
// the tool origin with the path passed through verbatim, so assets, the
// router and the API all live under that prefix.

/** Normalises a base to '/' or '/x/y/' (leading and trailing slash). */
export function normalizeBase(raw: string | undefined): string {
  if (!raw || raw === '/' || raw === './') return '/';
  const trimmed = raw.replace(/^\/+|\/+$/g, '');
  return trimmed ? `/${trimmed}/` : '/';
}

export const BASE = normalizeBase(import.meta.env?.BASE_URL);

/** react-router basename: undefined for '/', else '/t/dcim'. */
export function routerBasename(base: string = BASE): string | undefined {
  return base === '/' ? undefined : base.slice(0, -1);
}

/** API root: '/api/v1' standalone, '/t/dcim/api/v1' under the console. */
export function apiBase(base: string = BASE): string {
  return `${base}api/v1`;
}

/** Strips the base prefix from a browser path, giving the in-app path
 *  ('/t/dcim/racks' -> '/racks', '/t/dcim' -> '/'). Paths outside the
 *  base are returned unchanged. */
export function stripBase(path: string, base: string = BASE): string {
  if (base === '/') return path;
  const prefix = base.slice(0, -1);
  if (path === prefix) return '/';
  if (path.startsWith(base)) return path.slice(prefix.length);
  if (path.startsWith(`${prefix}?`) || path.startsWith(`${prefix}#`)) return `/${path.slice(prefix.length)}`;
  return path;
}
