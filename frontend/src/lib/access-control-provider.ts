/**
 * Map Refine `can(action, resource)` checks to our capability strings.
 *
 * - resource: maps a Refine resource name to a capability *prefix* (e.g. 'sites' -> 'inventory')
 * - action:   maps Refine actions ('list', 'show', 'create', 'edit', 'delete') to our verbs
 *
 * Capability format: <prefix>:<verb> — e.g. inventory:read, inventory:write, alerts:ack.
 */
import type { AccessControlProvider } from '@refinedev/core';

type Identity = { capabilities: string[] };

const RESOURCE_TO_CAP_PREFIX: Record<string, string> = {
  sites: 'inventory',
  buildings: 'inventory',
  rooms: 'inventory',
  rows: 'inventory',
  racks: 'inventory',
  assets: 'inventory',
  regions: 'inventory',
  alerts: 'alerts',
  'alerts/rules': 'alerts',
  collectors: 'collector',
  'api-tokens': 'tokens',
  users: 'users',
  roles: 'roles',
};

const ACTION_TO_VERB: Record<string, string> = {
  list: 'read',
  show: 'read',
  create: 'write',
  edit: 'write',
  delete: 'write',
};

function getCaps(): Set<string> {
  try {
    const raw = localStorage.getItem('dcim.identity');
    if (!raw) return new Set();
    const id = JSON.parse(raw) as Identity;
    return new Set(id.capabilities ?? []);
  } catch {
    return new Set();
  }
}

export const accessControlProvider: AccessControlProvider = {
  async can({ resource, action }) {
    const caps = getCaps();
    const prefix = RESOURCE_TO_CAP_PREFIX[resource ?? ''] ?? resource;
    const verb = ACTION_TO_VERB[action] ?? action;
    const cap = `${prefix}:${verb}`;
    if (caps.has(cap)) return { can: true };
    // Also allow reads via the dashboard cap (used for read-only views).
    if (verb === 'read' && caps.has('dashboard:read')) return { can: true };
    return { can: false, reason: `missing capability ${cap}` };
  },
  options: { buttons: { enableAccessControl: true, hideIfUnauthorized: true } },
};

export function hasCapability(cap: string): boolean {
  return getCaps().has(cap);
}
