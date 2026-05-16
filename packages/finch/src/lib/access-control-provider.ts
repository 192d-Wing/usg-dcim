/**
 * Map Refine `can(action, resource)` checks to the granular capability
 * format used by the backend (`<domain>:<resource>:<action>`), with
 * wildcard fallback (`*`, `<domain>:*`, `<domain>:<resource>:*`).
 *
 * Resources map to a (domain, resource) pair; actions map to one of
 * the catalog verbs. Anything not in either map falls back to the
 * resource/action name verbatim and will simply fail closed.
 */
import type { AccessControlProvider } from '@refinedev/core';

import { hasCap } from './caps';

type Identity = { capabilities: string[] };

const RESOURCE_TO_CODE: Record<string, [string, string]> = {
  // Inventory plane
  sites: ['inventory', 'sites'],
  regions: ['inventory', 'regions'],
  buildings: ['inventory', 'buildings'],
  rooms: ['inventory', 'rooms'],
  rows: ['inventory', 'rows'],
  racks: ['inventory', 'racks'],
  assets: ['inventory', 'assets'],
  cables: ['inventory', 'cables'],
  // Alerts plane
  alerts: ['alerts', 'alerts'],
  'alerts/rules': ['alerts', 'rules'],
  // Collectors plane
  collectors: ['collectors', 'collectors'],
  // Admin plane
  'api-tokens': ['admin', 'api-tokens'],
  users: ['admin', 'users'],
  roles: ['admin', 'roles'],
};

const ACTION_TO_VERB: Record<string, string> = {
  list: 'read',
  show: 'read',
  create: 'create',
  edit: 'update',
  delete: 'delete',
};

function getCaps(): string[] {
  try {
    const raw = localStorage.getItem('dcim.identity');
    if (!raw) return [];
    const id = JSON.parse(raw) as Identity;
    return id.capabilities ?? [];
  } catch {
    return [];
  }
}

export const accessControlProvider: AccessControlProvider = {
  async can({ resource, action }) {
    const caps = getCaps();
    const verb = ACTION_TO_VERB[action] ?? action;
    const mapped = RESOURCE_TO_CODE[resource ?? ''];
    const code = mapped
      ? `${mapped[0]}:${mapped[1]}:${verb}`
      : `${resource}:${verb}`;
    if (hasCap(caps, code)) return { can: true };
    // Cross-cutting read fallback: anyone with dashboards:dashboards:read
    // can see read-only listings even if their specific resource:read
    // code isn't granted. Kept from the legacy provider.
    if (verb === 'read' && hasCap(caps, 'dashboards:dashboards:read')) {
      return { can: true };
    }
    return { can: false, reason: `missing capability ${code}` };
  },
  options: { buttons: { enableAccessControl: true, hideIfUnauthorized: true } },
};

/** Wildcard-aware capability check against the cached identity in
 *  localStorage. Use this from components that aren't inside the
 *  Refine identity hook (e.g. cable-panel, power-chain-panel). */
export function hasCapability(cap: string): boolean {
  return hasCap(getCaps(), cap);
}
