// Wildcard-aware capability matcher. Mirrors the backend's
// find_matching_capability: a held pattern grants `code` when the
// segment counts match and every pattern segment is either `*` or
// equal to the corresponding code segment. The bare global `*`
// short-circuits any check.
//
// Examples (held -> request -> result):
//   "inventory:sites:read"     -> "inventory:sites:read"   -> true
//   "inventory:*"              -> "inventory:sites:read"   -> true
//   "inventory:*:read"         -> "inventory:sites:read"   -> true
//   "inventory:sites:*"        -> "inventory:sites:read"   -> true
//   "*"                        -> "anything"               -> true
//   "dns:*:read"               -> "dns:zones:create"       -> false
//
// Pages that need to gate UI on a capability should import `hasCap`
// and call it against `identity.capabilities`. The accessControlProvider
// reads from localStorage instead because it runs outside the React tree.

export function hasCap(caps: readonly string[] | undefined, code: string): boolean {
  if (!caps || caps.length === 0) return false;
  if (caps.includes(code)) return true;
  if (caps.includes('*')) return true;
  const target = code.split(':');
  for (const pattern of caps) {
    const parts = pattern.split(':');
    if (parts.length !== target.length) continue;
    let ok = true;
    for (let i = 0; i < parts.length; i++) {
      if (parts[i] !== '*' && parts[i] !== target[i]) { ok = false; break; }
    }
    if (ok) return true;
  }
  return false;
}

/** True if any of `codes` is granted. */
export function hasAnyCap(caps: readonly string[] | undefined, codes: readonly string[]): boolean {
  return codes.some((c) => hasCap(caps, c));
}
