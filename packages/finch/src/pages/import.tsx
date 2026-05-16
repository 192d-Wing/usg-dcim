// Bulk import — Cloudscape page with one tab per supported entity
// (assets / subnets / IPs). Each tab owns its CSV shape + parser +
// post-URL but shares the file-drop + validation-summary + preview-
// table + result-banner UI via the <ImportRunner> component below.
//
// Assets historically lived as the whole page; subnets and IPs land
// alongside as the IPAM-side equivalents (see roadmap "Near-term
// IPAM polish"). Each tab is capability-gated independently so an
// operator with only ipam:bulk:execute sees the IPAM tabs and an
// operator with only inventory:bulk:execute sees the Assets tab.

import React, { useMemo, useRef, useState } from 'react';
import { useGetIdentity, useList } from '@refinedev/core';
import { toast } from 'sonner';

import Alert from '@cloudscape-design/components/alert';
import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import Tabs from '@cloudscape-design/components/tabs';
import {
  colorBackgroundInputDisabled, colorBorderDividerDefault,
} from '@cloudscape-design/design-tokens';

import { hasCap } from '@/lib/caps';
import { http } from '@/lib/http';
import { parseCsv } from '@/lib/csv';

// ----------------------- Types -----------------------

type Site = { id: string; code: string; name: string };
type Rack = { id: string; name: string; code: string; site_id: string };
type Fabric = { id: string; slug: string; name: string };
type Supernet = { id: string; prefix: string; name: string | null; fabric_id: string };
type Subnet = { id: string; prefix: string; name: string | null; supernet_id: string };

type Issue = { row: number; message: string };
type BulkRequest = { url: string; payload: Record<string, unknown>[] };
type BulkResult = {
  inserted: number;
  updated?: number;
  skipped?: number;
  failed: number;
  errors: { row?: number; error: string }[];
};

// ----------------------- Top-level page -----------------------

export function ImportPage() {
  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const canAssets = hasCap(identity?.capabilities, 'inventory:bulk:execute');
  const canIpam = hasCap(identity?.capabilities, 'ipam:bulk:execute');

  const tabs: { id: string; label: string; content: React.ReactNode }[] = [];
  if (canAssets) tabs.push({ id: 'assets', label: 'Assets', content: <AssetsTab /> });
  if (canIpam) tabs.push({ id: 'subnets', label: 'Subnets', content: <SubnetsTab /> });
  if (canIpam) tabs.push({ id: 'ips', label: 'IP addresses', content: <IpsTab /> });

  return (
    <ContentLayout header={<Header variant="h1">Bulk import</Header>}>
      {tabs.length === 0 ? (
        <Box color="text-status-inactive">
          You don't have any bulk-import capabilities. Ask an admin for{' '}
          <code style={{ fontFamily: 'ui-monospace, monospace' }}>inventory:bulk:execute</code>{' '}
          (assets) or <code style={{ fontFamily: 'ui-monospace, monospace' }}>ipam:bulk:execute</code>{' '}
          (subnets, IPs).
        </Box>
      ) : (
        <Tabs tabs={tabs} />
      )}
    </ContentLayout>
  );
}

// ----------------------- Shared ImportRunner -----------------------

// Per-tab config: column rules, sample CSV, mapper, parent selector
// rendered above the file drop, and the POST URL.
type ImportConfig = {
  required: readonly string[];
  supported: readonly string[];
  sampleFilename: string;
  sample: string;
  description: React.ReactNode;
  mapRow: (
    r: Record<string, string>,
    rowNum: number,
    issues: Issue[],
    ctx: ParentCtx,
  ) => Record<string, unknown> | null;
  buildRequest: (bodies: Record<string, unknown>[], ctx: ParentCtx) => BulkRequest | null;
  parentSelector: React.ReactNode;
  parentReady: boolean;
  ctx: ParentCtx;
};

// Per-tab context the mapper can read — keeps the runner generic
// while letting each tab pass its parent ID (site / supernet / subnet)
// plus any lookup tables (racksByCode for the asset tab).
type ParentCtx = {
  parentId: string;
  racksByCode?: Map<string, Rack>;
};

function ImportRunner({ config }: Readonly<{ config: ImportConfig }>) {
  const [filename, setFilename] = useState<string | null>(null);
  const [header, setHeader] = useState<string[]>([]);
  const [rows, setRows] = useState<Record<string, string>[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState<BulkResult | null>(null);
  const [dragOver, setDragOver] = useState(false);
  const fileInput = useRef<HTMLInputElement | null>(null);

  function clearFile() {
    setFilename(null);
    setHeader([]);
    setRows([]);
    setResult(null);
  }

  async function loadFile(file: File) {
    const text = await file.text();
    const parsed = parseCsv(text);
    if (parsed.header.length === 0) {
      toast.error('CSV is empty');
      return;
    }
    setFilename(file.name);
    setHeader(parsed.header);
    setRows(parsed.rows);
    setResult(null);
  }

  const validation = useMemo(() => {
    const bodies: Record<string, unknown>[] = [];
    const issues: Issue[] = [];
    rows.forEach((r, idx) => {
      const rowNum = idx + 2;  // +2: 1-indexed + skip header
      const body = config.mapRow(r, rowNum, issues, config.ctx);
      if (body) bodies.push(body);
    });
    return { bodies, issues };
  }, [rows, config]);

  async function submit() {
    if (!config.parentReady) { toast.error('Pick a parent first'); return; }
    if (validation.issues.some((i) => i.message.startsWith('FATAL'))) {
      toast.error('Fix fatal issues before importing');
      return;
    }
    const req = config.buildRequest(validation.bodies, config.ctx);
    if (req === null) { toast.error('Could not build request'); return; }
    setSubmitting(true);
    try {
      const r = await http.post<BulkResult>(req.url, req.payload);
      setResult(r.data);
      const inserted = r.data.inserted;
      const updated = r.data.updated ?? 0;
      const skipped = r.data.skipped ?? 0;
      const failed = r.data.failed;
      toast.success(`Imported: ${inserted} inserted, ${updated} updated, ${skipped} skipped, ${failed} failed`);
    } catch (err: any) {
      toast.error(err?.message ?? 'import failed');
    } finally {
      setSubmitting(false);
    }
  }

  function downloadSample() {
    const blob = new Blob([config.sample], { type: 'text/csv' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = config.sampleFilename;
    a.click();
    URL.revokeObjectURL(url);
  }

  const requiredMissing = config.required.filter((c) => !header.includes(c));
  const unknownColumns = header.filter((c) => !config.supported.includes(c));
  const fatal = validation.issues.filter((i) => i.message.startsWith('FATAL'));
  const warnings = validation.issues.filter((i) => !i.message.startsWith('FATAL'));

  return (
    <SpaceBetween size="l">
      <Container
        header={
          <Header
            actions={<Button iconName="download" onClick={downloadSample}>Download sample</Button>}
          >
            Target + format
          </Header>
        }
      >
        <SpaceBetween size="m">
          {config.parentSelector}
          <Box color="text-body-secondary" fontSize="body-s">{config.description}</Box>
        </SpaceBetween>
      </Container>

      {!filename ? (
        <button
          type="button"
          onClick={() => fileInput.current?.click()}
          onDragEnter={(e) => { e.preventDefault(); setDragOver(true); }}
          onDragLeave={() => setDragOver(false)}
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => {
            e.preventDefault();
            setDragOver(false);
            const file = e.dataTransfer.files?.[0];
            if (file) loadFile(file);
          }}
          style={{
            all: 'unset',
            cursor: 'pointer',
            display: 'flex', flexDirection: 'column', alignItems: 'center', justifyContent: 'center',
            gap: 8, padding: 48, borderRadius: 12,
            border: `2px dashed ${colorBorderDividerDefault}`,
            background: dragOver ? `${colorBackgroundInputDisabled}` : 'transparent',
            textAlign: 'center',
          }}
        >
          <Box fontSize="heading-m">Drop a CSV here, or click to choose</Box>
          <Box color="text-status-inactive" fontSize="body-s">UTF-8 · header row required</Box>
          <input
            ref={fileInput}
            type="file"
            accept=".csv,text/csv"
            style={{ display: 'none' }}
            onChange={(e) => {
              const file = e.target.files?.[0];
              if (file) loadFile(file);
              e.target.value = '';
            }}
          />
        </button>
      ) : (
        <Container
          header={
            <Header
              actions={
                <SpaceBetween size="xs" direction="horizontal">
                  <Button onClick={clearFile} iconName="remove">Clear</Button>
                  <Button
                    variant="primary"
                    loading={submitting}
                    disabled={submitting || !config.parentReady || requiredMissing.length > 0 || fatal.length > 0 || rows.length === 0}
                    onClick={submit}
                  >
                    {submitting ? 'Importing…' : `Import ${rows.length} row${rows.length === 1 ? '' : 's'}`}
                  </Button>
                </SpaceBetween>
              }
              counter={`(${rows.length} rows)`}
            >
              {filename}
            </Header>
          }
        >
          <SpaceBetween size="m">
            {requiredMissing.length > 0 && (
              <Alert type="error" header={`Missing required column${requiredMissing.length === 1 ? '' : 's'}`}>
                {requiredMissing.map((c) => `"${c}"`).join(', ')}
              </Alert>
            )}
            {unknownColumns.length > 0 && (
              <Alert type="warning" header={`Unrecognized column${unknownColumns.length === 1 ? '' : 's'} (will be ignored)`}>
                {unknownColumns.map((c) => `"${c}"`).join(', ')}
              </Alert>
            )}
            {fatal.length > 0 && (
              <Alert type="error" header={`${fatal.length} fatal row issue${fatal.length === 1 ? '' : 's'}`}>
                <ul style={{ marginTop: 4, paddingLeft: 20 }}>
                  {fatal.slice(0, 8).map((i) => (
                    <li key={`iss-${i.row}-${i.message}`}>row {i.row}: {i.message.replace(/^FATAL /, '')}</li>
                  ))}
                  {fatal.length > 8 && <li>…and {fatal.length - 8} more</li>}
                </ul>
              </Alert>
            )}
            {warnings.length > 0 && (
              <Alert type="warning" header={`${warnings.length} warning${warnings.length === 1 ? '' : 's'}`}>
                <ul style={{ marginTop: 4, paddingLeft: 20 }}>
                  {warnings.slice(0, 8).map((i) => (
                    <li key={`iss-${i.row}-${i.message}`}>row {i.row}: {i.message}</li>
                  ))}
                  {warnings.length > 8 && <li>…and {warnings.length - 8} more</li>}
                </ul>
              </Alert>
            )}

            <Table
              variant="embedded"
              items={rows.slice(0, 10).map((r, i) => ({ ...r, _idx: String(i) })) as Array<Record<string, string>>}
              trackBy="_idx"
              columnDefinitions={[
                {
                  id: 'rowIdx', header: '#',
                  cell: (r: Record<string, string>) => (
                    <Box variant="span" color="text-status-inactive">{Number(r._idx) + 1}</Box>
                  ),
                },
                ...header.map((h) => ({
                  id: h,
                  header: config.supported.includes(h)
                    ? <Badge>{h}</Badge>
                    : <Badge color="grey">{h}</Badge>,
                  cell: (r: Record<string, string>) => r[h] ?? '',
                })),
              ]}
              empty={<Box color="text-status-inactive">No data rows.</Box>}
            />
            {rows.length > 10 && (
              <Box color="text-status-inactive" fontSize="body-s">
                Showing first 10 of {rows.length} rows.
              </Box>
            )}

            {result && (
              <Container
                header={
                  <Header
                    variant="h3"
                    description={
                      <StatusIndicator type={result.failed > 0 ? 'warning' : 'success'}>
                        {result.inserted} inserted · {result.updated ?? 0} updated · {result.skipped ?? 0} skipped · {result.failed} failed
                      </StatusIndicator>
                    }
                  >
                    Result
                  </Header>
                }
              >
                {result.errors.length > 0 && (
                  <Alert type="warning" header="Errors">
                    <ul style={{ marginTop: 4, paddingLeft: 20 }}>
                      {result.errors.slice(0, 12).map((e, idx) => (
                        <li key={`err-${idx}`}>
                          {e.row !== undefined ? `row ${e.row}: ` : ''}{e.error}
                        </li>
                      ))}
                      {result.errors.length > 12 && (
                        <li>…and {result.errors.length - 12} more</li>
                      )}
                    </ul>
                  </Alert>
                )}
              </Container>
            )}
          </SpaceBetween>
        </Container>
      )}
    </SpaceBetween>
  );
}

// ----------------------- Assets tab -----------------------

const ASSET_KINDS = [
  'server', 'switch', 'router', 'pdu', 'ups', 'crac', 'sensor',
  'storage', 'chassis', 'blade', 'patch_panel', 'other',
];

const ASSET_REQUIRED = ['name', 'kind'] as const;
const ASSET_SUPPORTED = [
  'name', 'kind', 'hostname', 'manufacturer', 'model', 'serial', 'firmware',
  'rack_code', 'rack_position_u', 'rack_units',
  'face', 'mount', 'pdu_side', 'psu_count', 'port_count',
  'mgmt_ip', 'mgmt_protocol', 'mgmt_port', 'lifecycle_state',
] as const;

const ASSET_SAMPLE = `name,kind,manufacturer,model,serial,hostname,rack_code,rack_position_u,rack_units,psu_count
R01-srv1,server,Dell,PowerEdge R750,ABC123,r01-srv1.dc1.local,R01,1,2,2
R01-sw1,switch,Cisco,Catalyst 9300,DEF456,r01-sw1.dc1.local,R01,3,1,2
R01-pdu-A,pdu,APC,AP8941,GHI789,,R01,,,
`;

function mapAssetRow(
  r: Record<string, string>,
  rowNum: number,
  issues: Issue[],
  ctx: ParentCtx,
): Record<string, unknown> | null {
  const name = (r.name ?? '').trim();
  const kind = (r.kind ?? '').trim().toLowerCase();
  if (!name) {
    issues.push({ row: rowNum, message: 'FATAL name is empty' });
    return null;
  }
  if (!ASSET_KINDS.includes(kind)) {
    issues.push({ row: rowNum, message: `FATAL unknown kind "${kind}"` });
    return null;
  }
  const body: Record<string, unknown> = {
    name, kind,
    hostname: r.hostname || null,
    manufacturer: r.manufacturer || null,
    model: r.model || null,
    serial: r.serial || null,
    firmware: r.firmware || null,
    face: r.face || 'front',
    mount: r.mount || 'rack',
    pdu_side: r.pdu_side || null,
    mgmt_ip: r.mgmt_ip || null,
    mgmt_protocol: r.mgmt_protocol || null,
    lifecycle_state: r.lifecycle_state || 'active',
    metadata_json: {},
  };
  for (const col of ['rack_position_u', 'rack_units', 'psu_count', 'port_count', 'mgmt_port'] as const) {
    const v = r[col];
    if (v === undefined || v === '') continue;
    const n = Number(v);
    if (Number.isNaN(n)) {
      issues.push({ row: rowNum, message: `${col} "${v}" is not a number (skipping)` });
    } else {
      body[col] = n;
    }
  }
  if (body.rack_units === undefined && body.rack_position_u !== undefined) body.rack_units = 1;
  const rackCode = (r.rack_code ?? '').trim();
  if (rackCode && ctx.racksByCode) {
    const rk = ctx.racksByCode.get(rackCode.toLowerCase());
    if (rk) body.rack_id = rk.id;
    else issues.push({ row: rowNum, message: `rack_code "${rackCode}" not found at this site (asset will be unplaced)` });
  }
  return body;
}

function AssetsTab() {
  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const sites = sitesRes.result.data ?? [];
  const [siteOpt, setSiteOpt] = useState<SelectProps.Option | null>(null);
  const siteId = siteOpt?.value ?? '';
  const racksRes = useList<Rack>({
    resource: 'inventory/racks',
    pagination: { pageSize: 500 },
    filters: siteId ? [{ field: 'site_id', operator: 'eq', value: siteId }] : [],
    queryOptions: { enabled: !!siteId },
  });
  const racks = racksRes.result.data ?? [];
  const racksByCode = useMemo(
    () => new Map(racks.map((r) => [r.code.toLowerCase(), r])),
    [racks],
  );
  const siteOptions: SelectProps.Option[] = sites.map((s) => ({ value: s.id, label: `${s.code} · ${s.name}` }));

  const config: ImportConfig = {
    required: ASSET_REQUIRED,
    supported: ASSET_SUPPORTED,
    sampleFilename: 'dcim-assets-sample.csv',
    sample: ASSET_SAMPLE,
    description: <>Upsert by (manufacturer, serial). Required: <code style={MONO}>name</code>, <code style={MONO}>kind</code>. Optional <code style={MONO}>rack_code</code> places rows in a rack.</>,
    mapRow: mapAssetRow,
    buildRequest: (bodies, ctx) => ({
      url: '/inventory/assets/bulk',
      payload: bodies.map((b) => ({ ...b, site_id: ctx.parentId })),
    }),
    parentSelector: (
      <FormField
        label="Site"
        description="All rows will be assigned to this site."
      >
        <Select
          placeholder="Pick a site"
          selectedOption={siteOpt}
          onChange={({ detail }) => setSiteOpt(detail.selectedOption)}
          options={siteOptions}
          expandToViewport
        />
      </FormField>
    ),
    parentReady: !!siteId,
    ctx: { parentId: siteId, racksByCode },
  };

  return <ImportRunner config={config} />;
}

// ----------------------- Subnets tab -----------------------

const SUBNET_REQUIRED = ['prefix'] as const;
const SUBNET_SUPPORTED = [
  'prefix', 'name', 'description', 'purpose', 'vlan_id', 'gateway',
] as const;
const SUBNET_SAMPLE = `prefix,name,description,purpose,vlan_id,gateway
10.0.10.0/24,mgmt-r01,Management for rack R01,management,10,10.0.10.1
10.0.20.0/24,prod-r01,Production for rack R01,production,20,10.0.20.1
`;

function mapSubnetRow(
  r: Record<string, string>,
  rowNum: number,
  issues: Issue[],
  ctx: ParentCtx,
): Record<string, unknown> | null {
  const prefix = (r.prefix ?? '').trim();
  if (!prefix) {
    issues.push({ row: rowNum, message: 'FATAL prefix is empty' });
    return null;
  }
  if (!/^[0-9a-fA-F:.]+\/\d{1,3}$/.test(prefix)) {
    issues.push({ row: rowNum, message: `FATAL prefix "${prefix}" doesn't look like CIDR` });
    return null;
  }
  const body: Record<string, unknown> = {
    supernet_id: ctx.parentId,
    prefix,
    name: r.name || null,
    description: r.description || null,
    purpose: r.purpose || null,
    gateway: r.gateway || null,
  };
  if (r.vlan_id && r.vlan_id.trim() !== '') {
    const n = Number(r.vlan_id);
    if (Number.isNaN(n)) {
      issues.push({ row: rowNum, message: `vlan_id "${r.vlan_id}" is not a number (skipping)` });
    } else {
      body.vlan_id = n;
    }
  }
  return body;
}

function SubnetsTab() {
  const fabricsRes = useList<Fabric>({ resource: 'ipam/fabrics', pagination: { pageSize: 200 } });
  const fabrics = fabricsRes.result.data ?? [];
  const [fabricOpt, setFabricOpt] = useState<SelectProps.Option | null>(null);
  const fabricId = fabricOpt?.value ?? '';
  const supersRes = useList<Supernet>({
    resource: 'ipam/supernets',
    pagination: { pageSize: 500 },
    filters: fabricId ? [{ field: 'fabric_id', operator: 'eq', value: fabricId }] : [],
    queryOptions: { enabled: !!fabricId },
  });
  const supernets = supersRes.result.data ?? [];
  const [superOpt, setSuperOpt] = useState<SelectProps.Option | null>(null);
  const supernetId = superOpt?.value ?? '';
  const fabricOptions: SelectProps.Option[] = fabrics.map((f) => ({ value: f.id, label: `${f.slug} · ${f.name}` }));
  const supernetOptions: SelectProps.Option[] = supernets.map((s) => ({
    value: s.id, label: `${s.prefix}${s.name ? ' · ' + s.name : ''}`,
  }));

  const config: ImportConfig = {
    required: SUBNET_REQUIRED,
    supported: SUBNET_SUPPORTED,
    sampleFilename: 'dcim-subnets-sample.csv',
    sample: SUBNET_SAMPLE,
    description: <>Each row creates a subnet under the chosen supernet. Required: <code style={MONO}>prefix</code>. Server enforces CIDR containment + per-VRF uniqueness; duplicates are reported as skipped so re-runs are idempotent.</>,
    mapRow: mapSubnetRow,
    buildRequest: (bodies) => ({ url: '/ipam/subnets/bulk', payload: bodies }),
    parentSelector: (
      <SpaceBetween size="s">
        <FormField label="Fabric">
          <Select
            placeholder="Pick a fabric"
            selectedOption={fabricOpt}
            onChange={({ detail }) => { setFabricOpt(detail.selectedOption); setSuperOpt(null); }}
            options={fabricOptions}
            expandToViewport
          />
        </FormField>
        <FormField
          label="Parent supernet"
          description="All rows will be created as children of this supernet."
        >
          <Select
            placeholder={fabricId ? 'Pick a supernet' : 'Pick a fabric first'}
            selectedOption={superOpt}
            onChange={({ detail }) => setSuperOpt(detail.selectedOption)}
            options={supernetOptions}
            disabled={!fabricId}
            expandToViewport
          />
        </FormField>
      </SpaceBetween>
    ),
    parentReady: !!supernetId,
    ctx: { parentId: supernetId },
  };

  return <ImportRunner config={config} />;
}

// ----------------------- IPs tab -----------------------

const IP_REQUIRED = ['address'] as const;
const IP_SUPPORTED = [
  'address', 'role', 'status', 'source', 'dns_name', 'description',
] as const;
const IP_SAMPLE = `address,role,status,source,dns_name,description
10.0.10.10,data,active,static,r01-srv1.dc1.local,
10.0.10.11,data,active,static,r01-sw1.dc1.local,
10.0.10.1,gateway,active,static,r01-gw.dc1.local,Default gateway
`;

function mapIpRow(
  r: Record<string, string>,
  rowNum: number,
  issues: Issue[],
  ctx: ParentCtx,
): Record<string, unknown> | null {
  const address = (r.address ?? '').trim();
  if (!address) {
    issues.push({ row: rowNum, message: 'FATAL address is empty' });
    return null;
  }
  // Accept bare IP or IP/prefix. Backend does the real validation;
  // this is just an early-fail on obviously bad rows so the operator
  // sees the issue in the preview instead of in a 422 from the server.
  if (!/^[0-9a-fA-F:.]+(\/\d{1,3})?$/.test(address)) {
    issues.push({ row: rowNum, message: `FATAL address "${address}" doesn't look like an IP` });
    return null;
  }
  return {
    subnet_id: ctx.parentId,
    address,
    role: r.role || 'data',
    status: r.status || 'active',
    source: r.source || 'static',
    dns_name: r.dns_name || null,
    description: r.description || null,
  };
}

function IpsTab() {
  const fabricsRes = useList<Fabric>({ resource: 'ipam/fabrics', pagination: { pageSize: 200 } });
  const fabrics = fabricsRes.result.data ?? [];
  const [fabricOpt, setFabricOpt] = useState<SelectProps.Option | null>(null);
  const fabricId = fabricOpt?.value ?? '';
  const subnetsRes = useList<Subnet>({
    resource: 'ipam/subnets',
    pagination: { pageSize: 500 },
    filters: fabricId ? [{ field: 'fabric_id', operator: 'eq', value: fabricId }] : [],
    queryOptions: { enabled: !!fabricId },
  });
  const subnets = subnetsRes.result.data ?? [];
  const [subnetOpt, setSubnetOpt] = useState<SelectProps.Option | null>(null);
  const subnetId = subnetOpt?.value ?? '';
  const fabricOptions: SelectProps.Option[] = fabrics.map((f) => ({ value: f.id, label: `${f.slug} · ${f.name}` }));
  const subnetOptions: SelectProps.Option[] = subnets.map((s) => ({
    value: s.id, label: `${s.prefix}${s.name ? ' · ' + s.name : ''}`,
  }));

  const config: ImportConfig = {
    required: IP_REQUIRED,
    supported: IP_SUPPORTED,
    sampleFilename: 'dcim-ip-addresses-sample.csv',
    sample: IP_SAMPLE,
    description: <>Each row creates an IP allocation in the chosen subnet. Required: <code style={MONO}>address</code>. Duplicates within the subnet are reported as skipped so re-runs are idempotent.</>,
    mapRow: mapIpRow,
    buildRequest: (bodies) => ({ url: '/ipam/addresses/bulk', payload: bodies }),
    parentSelector: (
      <SpaceBetween size="s">
        <FormField label="Fabric">
          <Select
            placeholder="Pick a fabric"
            selectedOption={fabricOpt}
            onChange={({ detail }) => { setFabricOpt(detail.selectedOption); setSubnetOpt(null); }}
            options={fabricOptions}
            expandToViewport
          />
        </FormField>
        <FormField
          label="Parent subnet"
          description="All rows will be created as IP allocations in this subnet."
        >
          <Select
            placeholder={fabricId ? 'Pick a subnet' : 'Pick a fabric first'}
            selectedOption={subnetOpt}
            onChange={({ detail }) => setSubnetOpt(detail.selectedOption)}
            options={subnetOptions}
            disabled={!fabricId}
            expandToViewport
          />
        </FormField>
      </SpaceBetween>
    ),
    parentReady: !!subnetId,
    ctx: { parentId: subnetId },
  };

  return <ImportRunner config={config} />;
}

const MONO = { fontFamily: 'ui-monospace, monospace' } as const;
