// Wildcard-aware capability matcher. Mirrors the backend's
// find_matching_capability fallback chain: exact -> <prefix>:* (most
// specific to least) -> `*`.
//
// Pages that need to gate UI on a capability should import `hasCap`
// and call it against `identity.capabilities`. The accessControlProvider
// reads from localStorage instead because it runs outside the React tree.

export function hasCap(caps: readonly string[] | undefined, code: string): boolean {
  if (!caps || caps.length === 0) return false;
  const owned = new Set(caps);
  if (owned.has(code)) return true;
  const parts = code.split(':');
  for (let i = parts.length - 1; i > 0; i--) {
    if (owned.has(parts.slice(0, i).join(':') + ':*')) return true;
  }
  return owned.has('*');
}

/** True if any of `codes` is granted. */
export function hasAnyCap(caps: readonly string[] | undefined, codes: readonly string[]): boolean {
  return codes.some((c) => hasCap(caps, c));
}
