import { beforeEach, describe, expect, test } from 'vitest';

// access-control-provider.ts reads from localStorage. Install a
// minimal shim before importing the module so the global lookup
// resolves to something we can write to in tests.

class MemoryStorage {
  private store = new Map<string, string>();
  getItem(key: string): string | null { return this.store.get(key) ?? null; }
  setItem(key: string, val: string): void { this.store.set(key, String(val)); }
  removeItem(key: string): void { this.store.delete(key); }
  clear(): void { this.store.clear(); }
}

(globalThis as any).localStorage = new MemoryStorage();

const { accessControlProvider, hasCapability } = await import('./access-control-provider');

function setIdentity(capabilities: string[]) {
  localStorage.setItem('dcim.identity', JSON.stringify({ capabilities }));
}

describe('accessControlProvider.can — Refine resource/action → capability mapping', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  test('grants list on sites when inventory:sites:read is held', async () => {
    setIdentity(['inventory:sites:read']);
    const res = await accessControlProvider.can!({ resource: 'sites', action: 'list' });
    expect(res.can).toBe(true);
  });

  test('show maps to read verb', async () => {
    setIdentity(['inventory:sites:read']);
    const res = await accessControlProvider.can!({ resource: 'sites', action: 'show' });
    expect(res.can).toBe(true);
  });

  test('edit maps to update verb (NOT edit)', async () => {
    // The Refine verb is `edit` but the backend cap is `update`.
    // Holding inventory:sites:edit MUST NOT grant; holding
    // inventory:sites:update MUST.
    setIdentity(['inventory:sites:edit']);
    let res = await accessControlProvider.can!({ resource: 'sites', action: 'edit' });
    expect(res.can).toBe(false);
    setIdentity(['inventory:sites:update']);
    res = await accessControlProvider.can!({ resource: 'sites', action: 'edit' });
    expect(res.can).toBe(true);
  });

  test('refuses an unmapped resource/action pair', async () => {
    setIdentity(['inventory:sites:read']);
    const res = await accessControlProvider.can!({ resource: 'sites', action: 'delete' });
    expect(res.can).toBe(false);
    expect(res.reason).toMatch(/missing capability inventory:sites:delete/);
  });

  test('dashboards:dashboards:read is a read-only crosscut for any resource', async () => {
    // Documented behavior: anyone with dashboards:dashboards:read can
    // see read-only listings even without resource-specific read caps.
    // Lock this so the fallback isn't widened to writes by accident.
    setIdentity(['dashboards:dashboards:read']);
    let res = await accessControlProvider.can!({ resource: 'racks', action: 'list' });
    expect(res.can).toBe(true);
    res = await accessControlProvider.can!({ resource: 'racks', action: 'show' });
    expect(res.can).toBe(true);
    // But NOT writes.
    res = await accessControlProvider.can!({ resource: 'racks', action: 'create' });
    expect(res.can).toBe(false);
    res = await accessControlProvider.can!({ resource: 'racks', action: 'delete' });
    expect(res.can).toBe(false);
  });

  test('global wildcard grants every resource/action', async () => {
    setIdentity(['*']);
    for (const action of ['list', 'show', 'create', 'edit', 'delete']) {
      const res = await accessControlProvider.can!({ resource: 'roles', action });
      expect(res.can).toBe(true);
    }
  });

  test('unmapped resource falls through to <resource>:<verb> code', async () => {
    // Resources not in the table map to `<resource>:<verb>` literal.
    // The cap holder must use that exact code to grant access.
    setIdentity(['custom-thing:create']);
    const res = await accessControlProvider.can!({ resource: 'custom-thing', action: 'create' });
    expect(res.can).toBe(true);
  });

  test('returns can=false with no identity in storage', async () => {
    const res = await accessControlProvider.can!({ resource: 'sites', action: 'list' });
    expect(res.can).toBe(false);
  });

  test('returns can=false when identity JSON is malformed', async () => {
    localStorage.setItem('dcim.identity', '{not json');
    const res = await accessControlProvider.can!({ resource: 'sites', action: 'list' });
    expect(res.can).toBe(false);
  });

  test('alerts and admin resources route to their non-inventory namespaces', async () => {
    setIdentity(['admin:users:read', 'alerts:rules:update']);
    let res = await accessControlProvider.can!({ resource: 'users', action: 'list' });
    expect(res.can).toBe(true);
    res = await accessControlProvider.can!({ resource: 'alerts/rules', action: 'edit' });
    expect(res.can).toBe(true);
  });
});

describe('hasCapability — non-Refine call sites', () => {
  beforeEach(() => {
    localStorage.clear();
  });

  test('reads from the same identity blob as accessControlProvider', () => {
    setIdentity(['ipam:supernets:read']);
    expect(hasCapability('ipam:supernets:read')).toBe(true);
    expect(hasCapability('ipam:supernets:write')).toBe(false);
  });

  test('honors wildcard caps', () => {
    setIdentity(['ipam:*:read']);
    expect(hasCapability('ipam:supernets:read')).toBe(true);
    expect(hasCapability('ipam:vrfs:read')).toBe(true);
    expect(hasCapability('ipam:vrfs:write')).toBe(false);
  });

  test('returns false with no identity in storage', () => {
    expect(hasCapability('anything:at:all')).toBe(false);
  });
});
