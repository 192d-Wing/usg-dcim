// Verifies the header of a vendored console-sdk.ts. Pure ESM, no Node APIs
// (Web Crypto only), so it runs under Node 20+, vitest and browsers.
// Copied next to the vendored SDK by scripts/vendor.mjs.

export const HEADER_RE =
  /^\/\/ @usg-ops-console\/sdk-js (\d+\.\d+\.\d+) protocol=(\d+) source=([0-9a-f]{7,40}(?:\+dirty)?|unknown) sha256=([0-9a-f]{64})$/;

/** Lowercase hex SHA-256 of a UTF-8 string. */
export async function sha256Hex(text) {
  const buf = await globalThis.crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
  return Array.from(new Uint8Array(buf), (b) => b.toString(16).padStart(2, "0")).join("");
}

/** Split a vendored file into its header line and the hashed body. */
export function splitHeader(source) {
  const nl = source.indexOf("\n");
  return nl < 0 ? { header: source, body: "" } : { header: source.slice(0, nl), body: source.slice(nl + 1) };
}

/**
 * Check a vendored SDK file. `expect` may pin `protocol` and/or `version`.
 * A `source` of `unknown` or ending in `+dirty` is not reproducible from a
 * commit and is rejected unless `expect.allowDirty === true` (local
 * development only; never in a tool's committed compat test).
 * Resolves to `{ ok: true, version, protocol, source, sha256 }` or
 * `{ ok: false, error }`.
 */
export async function verifyVendoredSdk(source, expect = {}) {
  const { header, body } = splitHeader(source);
  const m = HEADER_RE.exec(header);
  if (m === null) return { ok: false, error: "missing or malformed @usg-ops-console/sdk-js header" };
  const [, version, protocolStr, src, digest] = m;
  const protocol = Number(protocolStr);
  const actual = await sha256Hex(body);
  if (actual !== digest) return { ok: false, error: `sha256 mismatch: header ${digest}, content ${actual}` };
  if (expect.protocol !== undefined && expect.protocol !== protocol)
    return { ok: false, error: `protocol ${protocol}, expected ${expect.protocol}` };
  if (expect.version !== undefined && expect.version !== version)
    return { ok: false, error: `version ${version}, expected ${expect.version}` };
  if (expect.allowDirty !== true && (src === "unknown" || src.endsWith("+dirty")))
    return { ok: false, error: `source ${src} is not a clean commit; re-vendor from a committed tree` };
  return { ok: true, version, protocol, source: src, sha256: digest };
}
