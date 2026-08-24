// Shared E2E helpers: credentials, the UI login flow, and a thin
// bearer-token API client used for fixture setup/cleanup where the UI
// deliberately has no surface (e.g. row deletion).
import { expect, type Page, type APIRequestContext } from '@playwright/test';

export const CREDS = {
  email: process.env.E2E_EMAIL ?? 'admin@dcim.local',
  password: process.env.E2E_PASSWORD ?? 'changeme',
};

// Stable fixture identities, reused across runs. The sites/regions API
// has no DELETE, so the suite parks its data under one E2E-labeled
// site instead of accumulating a new one per run.
export const FIXTURES = {
  regionCode: 'E2E',
  siteCode: 'E2E-001',
  siteName: 'E2E Test Site',
  rackHomeBuilding: 'E2E-RACKS',
  rackHomeFloor: 'E2E-DH',
  rackHomeRow: 'E2E-A',
};

/** Unique-ish suffix for per-run entities. */
export function uniq(): string {
  return Date.now().toString(36).slice(-6);
}

/**
 * Drive the real login UI. Since /auth/methods went backend-driven,
 * the local form is immediately visible when SSO is off (this
 * stack); the "use local credentials" disclosure only exists on
 * SSO-enabled stacks — click it if it's there.
 */
export async function loginViaUi(page: Page): Promise<void> {
  await page.goto('/login');
  const disclosure = page.getByRole('button', { name: /use local credentials/i });
  const emailField = page.getByLabel('Email');
  await expect(emailField.or(disclosure).first()).toBeVisible();
  if (await disclosure.isVisible()) {
    await disclosure.click();
  }
  await emailField.fill(CREDS.email);
  await page.getByLabel('Password').fill(CREDS.password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.locator('a[href="/sites"]').first()).toBeVisible();
}

/** Minimal API client for fixture setup/cleanup. */
export class Api {
  private constructor(
    private readonly rq: APIRequestContext,
    private readonly base: string,
    private readonly token: string,
  ) {}

  static async login(rq: APIRequestContext, baseURL: string): Promise<Api> {
    const r = await rq.post(`${baseURL}/api/v1/auth/login`, {
      data: { email: CREDS.email, password: CREDS.password },
    });
    expect(r.ok()).toBeTruthy();
    return new Api(rq, baseURL, (await r.json()).access_token);
  }

  private headers() {
    return { Authorization: `Bearer ${this.token}` };
  }

  async get(path: string): Promise<any> {
    const r = await this.rq.get(`${this.base}/api/v1${path}`, { headers: this.headers() });
    expect(r.ok()).toBeTruthy();
    return r.json();
  }

  async list(path: string, params: Record<string, string> = {}): Promise<any[]> {
    const qs = new URLSearchParams({ limit: '200', ...params }).toString();
    const r = await this.rq.get(`${this.base}/api/v1${path}?${qs}`, { headers: this.headers() });
    expect(r.ok()).toBeTruthy();
    return (await r.json()).items ?? [];
  }

  async post(path: string, data: unknown): Promise<any> {
    const r = await this.rq.post(`${this.base}/api/v1${path}`, { headers: this.headers(), data });
    expect(r.ok(), `POST ${path}: ${r.status()} ${await r.text()}`).toBeTruthy();
    return r.json();
  }

  async patch(path: string, data: unknown): Promise<any> {
    const r = await this.rq.patch(`${this.base}/api/v1${path}`, { headers: this.headers(), data });
    expect(r.ok(), `PATCH ${path}: ${r.status()} ${await r.text()}`).toBeTruthy();
    return r.json();
  }

  async delete(path: string): Promise<void> {
    const r = await this.rq.delete(`${this.base}/api/v1${path}`, { headers: this.headers() });
    expect(r.ok(), `DELETE ${path}: ${r.status()}`).toBeTruthy();
  }

  /** Best-effort teardown for a per-run rack: hard-DELETEs its assets
   *  (child-asset/cable guards permitting), then the rack itself.
   *  Returns true when the rack is gone; false when a 409 guard (or
   *  anything else) kept it — callers treat that as non-fatal, the
   *  per-run rack just lingers like it always used to. */
  async deleteRunRack(rackId: string): Promise<boolean> {
    const assets = await this.list('/inventory/assets', { rack_id: rackId });
    for (const a of assets) {
      await this.rq.delete(`${this.base}/api/v1/inventory/assets/${a.id}`, { headers: this.headers() });
    }
    const r = await this.rq.delete(`${this.base}/api/v1/inventory/racks/${rackId}`, { headers: this.headers() });
    return r.ok();
  }

  /** A fresh rack for this run in the fixture row (per-run racks
   *  isolate specs from each other; deleteRunRack tears one down). */
  async createRunRack(code: string): Promise<{ rackId: string; siteId: string }> {
    const { siteId } = await this.ensureFixtures();
    const bldg = (await this.list('/inventory/buildings', { site_id: siteId }))
      .find((b: any) => b.code === FIXTURES.rackHomeBuilding);
    const room = (await this.list('/inventory/rooms', { building_id: bldg.id }))
      .find((r: any) => r.code === FIXTURES.rackHomeFloor);
    const row = (await this.list('/inventory/rows', { room_id: room.id }))
      .find((r: any) => r.code === FIXTURES.rackHomeRow);
    const rack = await this.post('/inventory/racks', {
      site_id: siteId, row_id: row.id, name: `E2E Rack ${code}`, code, u_height: 42,
    });
    return { rackId: rack.id, siteId };
  }

  /** Region + site + rack-home building/floor/row, created if missing. */
  async ensureFixtures(): Promise<{ siteId: string; rackHomeBuildingId: string }> {
    let region = (await this.list('/inventory/regions')).find((r) => r.code === FIXTURES.regionCode);
    region ??= await this.post('/inventory/regions', {
      name: FIXTURES.regionCode, code: FIXTURES.regionCode, description: 'E2E fixture',
    });

    let site = (await this.list('/inventory/sites')).find((s) => s.code === FIXTURES.siteCode);
    site ??= await this.post('/inventory/sites', {
      region_id: region.id, name: FIXTURES.siteName, code: FIXTURES.siteCode,
      lifecycle_state: 'active', metadata_json: {},
    });

    let bldg = (await this.list('/inventory/buildings', { site_id: site.id }))
      .find((b) => b.code === FIXTURES.rackHomeBuilding);
    bldg ??= await this.post('/inventory/buildings', {
      site_id: site.id, name: FIXTURES.rackHomeBuilding, code: FIXTURES.rackHomeBuilding,
    });

    let room = (await this.list('/inventory/rooms', { building_id: bldg.id }))
      .find((r) => r.code === FIXTURES.rackHomeFloor);
    room ??= await this.post('/inventory/rooms', {
      building_id: bldg.id, name: FIXTURES.rackHomeFloor, code: FIXTURES.rackHomeFloor,
      grid_cols: 10, grid_rows: 6,
    });

    const rows = await this.list('/inventory/rows', { room_id: room.id });
    if (!rows.some((r) => r.code === FIXTURES.rackHomeRow)) {
      await this.post('/inventory/rows', {
        room_id: room.id, name: FIXTURES.rackHomeRow, code: FIXTURES.rackHomeRow,
      });
    }
    return { siteId: site.id, rackHomeBuildingId: bldg.id };
  }
}
