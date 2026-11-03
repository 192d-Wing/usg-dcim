// IPAM hierarchy through the IPAM page's Hierarchy tab: drill
// Fabric → VRF, carve a supernet and a subnet, allocate and release an
// address. The spec creates its own fabric + VRF via the API so the
// prefixes below can't collide with anything else on the stack, and
// tears the whole tree down afterwards. Server-side guards (subnet
// outside its supernet, duplicate address) are asserted on outcome —
// no row appears — rather than on error-toast wording.
import { test, expect, type Page } from '@playwright/test';
import { Api, uniq } from './helpers';

test.describe.configure({ mode: 'serial' });

const runId = uniq();
const fabricName = `E2E IPAM Fabric ${runId}`;
const vrfName = `e2e-vrf-${runId}`;
const supernetPrefix = '10.250.0.0/16';
const subnetPrefix = '10.250.1.0/24';
const outsidePrefix = '10.251.0.0/24';
const address = '10.250.1.10';
let fabricId = '';

/** Row-name matcher for a literal prefix/address. */
const rx = (s: string) => new RegExp(s.replaceAll('.', '\\.'));

/** Supernet-tree row. The tree is a plain <table> whose rows carry
 *  dnd-kit drag attributes (role="button"), so they are not ARIA rows. */
const treeRow = (page: Page, prefix: string) => page.locator('tr', { hasText: prefix });

test.beforeAll(async ({ request, baseURL }) => {
  const api = await Api.login(request, baseURL!);
  const f = await api.post('/ipam/fabrics', {
    name: fabricName, slug: `e2e-ipam-${runId}`, description: 'E2E fixture',
    enclave: null, classification: null,
    dns_recursive_upstreams: null, dns_deny_networks: null, dns_allow_networks: null,
  });
  fabricId = f.id;
  await api.post('/ipam/vrfs', {
    fabric_id: fabricId, name: vrfName, route_target: null,
    description: 'E2E fixture', is_default: false,
  });
});

test.afterAll(async ({ request, baseURL }) => {
  // Tolerant cleanup, leaves first: addresses → subnets → supernets
  // (children before parents) → VRFs → fabric.
  // An empty fabric_id filter would list every fabric's data.
  if (!fabricId) return;
  const api = await Api.login(request, baseURL!);
  const subnets = await api.list('/ipam/subnets', { fabric_id: fabricId }).catch(() => []);
  for (const s of subnets) {
    for (const a of await api.list('/ipam/addresses', { subnet_id: s.id }).catch(() => [])) {
      await api.delete(`/ipam/addresses/${a.id}`).catch(() => {});
    }
    await api.delete(`/ipam/subnets/${s.id}`).catch(() => {});
  }
  const supernets = await api.list('/ipam/supernets', { fabric_id: fabricId }).catch(() => []);
  for (const sn of [...supernets].reverse()) {
    await api.delete(`/ipam/supernets/${sn.id}`).catch(() => {});
  }
  for (const v of await api.list('/ipam/vrfs', { fabric_id: fabricId }).catch(() => [])) {
    await api.delete(`/ipam/vrfs/${v.id}`).catch(() => {});
  }
  await api.delete(`/ipam/fabrics/${fabricId}`).catch(() => {});
});

/** Hierarchy tab → the run's fabric → the run's VRF (supernet tree). */
async function openVrf(page: Page) {
  await page.goto('/ipam');
  await page.getByRole('button', { name: fabricName, exact: true }).click();
  await page.getByRole('button', { name: vrfName, exact: true }).click();
  await expect(page.getByRole('button', { name: 'New supernet' })).toBeVisible();
}

/** Supernet tree → expand the run's supernet. */
async function expandSupernet(page: Page) {
  await openVrf(page);
  await treeRow(page, supernetPrefix).click();
  await expect(page.getByRole('button', { name: /Add subnet here/ })).toBeVisible();
}

/** Supernet tree → the run's subnet's Addresses view. */
async function openSubnet(page: Page) {
  await expandSupernet(page);
  await treeRow(page, subnetPrefix).click();
  await expect(page.getByRole('button', { name: 'Allocate IP' })).toBeVisible();
}

test('create a supernet in the VRF', async ({ page }) => {
  await openVrf(page);
  await page.getByRole('button', { name: 'New supernet' }).click();
  const modal = page.getByRole('dialog');
  await modal.getByLabel('Prefix (CIDR)', { exact: true }).fill(supernetPrefix);
  await modal.getByLabel('Name', { exact: true }).fill(`E2E supernet ${runId}`);
  await modal.getByRole('button', { name: 'Create', exact: true }).click();

  await expect(page.getByText('Supernet created')).toBeVisible();
  await expect(treeRow(page, supernetPrefix)).toBeVisible();
});

test('add a subnet inside the supernet', async ({ page }) => {
  await expandSupernet(page);
  await page.getByRole('button', { name: /Add subnet here/ }).click();
  const modal = page.getByRole('dialog');
  await modal.getByLabel('Prefix (CIDR, must be inside the supernet)').fill(subnetPrefix);
  await modal.getByRole('button', { name: 'Create', exact: true }).click();

  await expect(page.getByText('Subnet created')).toBeVisible();
  await expect(treeRow(page, subnetPrefix)).toBeVisible();
});

test('reject a subnet outside its supernet', async ({ page }) => {
  await expandSupernet(page);
  await page.getByRole('button', { name: /Add subnet here/ }).click();
  const modal = page.getByRole('dialog');
  await modal.getByLabel('Prefix (CIDR, must be inside the supernet)').fill(outsidePrefix);
  await modal.getByRole('button', { name: 'Create', exact: true }).click();

  // Outcome, not wording: the modal stays open and no row appears.
  await expect(modal).toBeVisible();
  await expect(page.getByText('Subnet created')).toHaveCount(0);
  await page.keyboard.press('Escape');
  await expect(treeRow(page, outsidePrefix)).toHaveCount(0);
});

test('allocate an address in the subnet', async ({ page }) => {
  await openSubnet(page);
  await page.getByRole('button', { name: 'Allocate IP' }).click();
  const modal = page.getByRole('dialog');
  await modal.getByLabel('Address', { exact: true }).fill(address);
  await modal.getByRole('button', { name: 'Allocate', exact: true }).click();

  await expect(page.getByText('IP allocated')).toBeVisible();
  await expect(page.getByRole('row', { name: rx(address) })).toBeVisible();
});

test('reject a duplicate address', async ({ page }) => {
  await openSubnet(page);
  await page.getByRole('button', { name: 'Allocate IP' }).click();
  const modal = page.getByRole('dialog');
  await modal.getByLabel('Address', { exact: true }).fill(address);
  await modal.getByRole('button', { name: 'Allocate', exact: true }).click();

  await expect(modal).toBeVisible();
  await page.keyboard.press('Escape');
  await expect(page.getByRole('row', { name: rx(address) })).toHaveCount(1);
});

test('release the address', async ({ page }) => {
  page.on('dialog', (d) => d.accept());
  await openSubnet(page);
  await page.getByRole('button', { name: `Release ${address}` }).click();

  await expect(page.getByText('Address released')).toBeVisible();
  await expect(page.getByRole('row', { name: rx(address) })).toHaveCount(0);
});
