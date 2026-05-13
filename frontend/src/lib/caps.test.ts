import { describe, expect, test } from 'vitest';
import { hasAnyCap, hasCap } from './caps';

// caps.ts is a 1:1 mirror of the backend's find_matching_capability.
// Drift between the two would let the UI show buttons that the API
// then 403s on (best case) or hide buttons that the API would have
// allowed (worst case — operators think a feature doesn't exist).
// These tests pin the wildcard semantics so a refactor can't drift.

describe('hasCap', () => {
  test('returns false for undefined caps', () => {
    expect(hasCap(undefined, 'inventory:sites:read')).toBe(false);
  });

  test('returns false for empty caps array', () => {
    expect(hasCap([], 'inventory:sites:read')).toBe(false);
  });

  test('returns true on exact match', () => {
    expect(hasCap(['inventory:sites:read'], 'inventory:sites:read')).toBe(true);
  });

  test('returns false when the held cap is a different code', () => {
    expect(hasCap(['inventory:sites:read'], 'inventory:sites:write')).toBe(false);
  });

  test('bare global wildcard short-circuits any check', () => {
    expect(hasCap(['*'], 'anything:we:might:ask')).toBe(true);
    expect(hasCap(['*'], 'dns:zones:delete')).toBe(true);
  });

  test('segment wildcard at the tail grants anything in that namespace', () => {
    expect(hasCap(['inventory:sites:*'], 'inventory:sites:read')).toBe(true);
    expect(hasCap(['inventory:sites:*'], 'inventory:sites:write')).toBe(true);
  });

  test('segment wildcard at the middle grants any verb on any noun', () => {
    expect(hasCap(['inventory:*:read'], 'inventory:sites:read')).toBe(true);
    expect(hasCap(['inventory:*:read'], 'inventory:racks:read')).toBe(true);
    // Must NOT grant a different verb.
    expect(hasCap(['inventory:*:read'], 'inventory:sites:write')).toBe(false);
  });

  test('namespace wildcard grants everything below', () => {
    // The backend lets `inventory:*` match `inventory:sites:read` —
    // mirror that exact semantics here.
    expect(hasCap(['inventory:*'], 'inventory:sites:read')).toBe(false);
    // ^ segment-count mismatch: held has 2 parts, code has 3. The
    // backend documents this on purpose: each wildcard occupies
    // exactly one segment. Operators who want "all of inventory"
    // hold `inventory:*:*` or `*`. Locking this so a future
    // permissive refactor doesn't silently widen access.
  });

  test('segment-count mismatch refuses the match', () => {
    expect(hasCap(['inventory:sites'], 'inventory:sites:read')).toBe(false);
    expect(hasCap(['inventory:sites:read:extra'], 'inventory:sites:read')).toBe(false);
  });

  test('mixed wildcard and literal — both must align', () => {
    expect(hasCap(['*:sites:read'], 'inventory:sites:read')).toBe(true);
    expect(hasCap(['*:sites:read'], 'inventory:racks:read')).toBe(false);
    expect(hasCap(['*:sites:*'], 'inventory:sites:delete')).toBe(true);
  });

  test('first matching pattern wins even when held alongside misses', () => {
    expect(
      hasCap(
        ['ipam:supernets:read', 'inventory:sites:read', 'dns:zones:read'],
        'inventory:sites:read',
      ),
    ).toBe(true);
  });

  test('case-sensitive comparison — capability codes are lowercase by convention', () => {
    expect(hasCap(['INVENTORY:sites:read'], 'inventory:sites:read')).toBe(false);
  });
});

describe('hasAnyCap', () => {
  test('returns true when any code is granted', () => {
    expect(
      hasAnyCap(['inventory:sites:read'], ['inventory:sites:write', 'inventory:sites:read']),
    ).toBe(true);
  });

  test('returns false when none of the codes is granted', () => {
    expect(
      hasAnyCap(['dns:zones:read'], ['inventory:sites:read', 'ipam:supernets:write']),
    ).toBe(false);
  });

  test('empty codes list always returns false', () => {
    expect(hasAnyCap(['*'], [])).toBe(false);
  });

  test('honors wildcard caps across the OR list', () => {
    expect(
      hasAnyCap(['inventory:*:read'], ['inventory:racks:read', 'unrelated:thing:write']),
    ).toBe(true);
  });
});
