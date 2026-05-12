// Bulk import — Cloudscape page with site selector, drag-drop CSV
// zone, preview Table, and import result summary.

import { useMemo, useRef, useState } from 'react';
import { useList, useGetIdentity } from '@refinedev/core';
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
import {
  colorBackgroundInputDisabled, colorBorderDividerDefault,
} from '@cloudscape-design/design-tokens';

import { hasCap } from '@/lib/caps';
import { http } from '@/lib/http';
import { parseCsv } from '@/lib/csv';

type Site = { id: string; code: string; name: string };
type Rack = { id: string; name: string; code: string; site_id: string };

const ASSET_KINDS = [
  'server', 'switch', 'router', 'pdu', 'ups', 'crac', 'sensor',
  'storage', 'chassis', 'blade', 'patch_panel', 'other',
];

const REQUIRED = ['name', 'kind'] as const;
const SUPPORTED = [
  'name', 'kind', 'hostname', 'manufacturer', 'model', 'serial', 'firmware',
  'rack_code', 'rack_position_u', 'rack_units',
  'face', 'mount', 'pdu_side', 'psu_count', 'port_count',
  'mgmt_ip', 'mgmt_protocol', 'mgmt_port', 'lifecycle_state',
] as const;

type Issue = { row: number; message: string };
type Mapped = { body: Record<string, unknown>; issues: Issue[] };

const SAMPLE = `name,kind,manufacturer,model,serial,hostname,rack_code,rack_position_u,rack_units,psu_count
R01-srv1,server,Dell,PowerEdge R750,ABC123,r01-srv1.dc1.local,R01,1,2,2
R01-sw1,switch,Cisco,Catalyst 9300,DEF456,r01-sw1.dc1.local,R01,3,1,2
R01-pdu-A,pdu,APC,AP8941,GHI789,,R01,,,
`;

export function ImportPage() {
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
    () => new Map(racks.map((r) => [r.code.toLowerCase(), r])), [racks],
  );

  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const canBulk = hasCap(identity?.capabilities, 'inventory:bulk:execute');

  const [filename, setFilename] = useState<string | null>(null);
  const [header, setHeader] = useState<string[]>([]);
  const [rows, setRows] = useState<Record<string, string>[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState<{ inserted: number; updated: number; failed: number; errors: { serial?: string | null; error: string }[] } | null>(null);
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

  const validation = useMemo(() => mapRows(rows, racksByCode), [rows, racksByCode]);

  async function submit() {
    if (!siteId) { toast.error('Pick a site first'); return; }
    if (validation.issues.some((i) => i.message.startsWith('FATAL'))) {
      toast.error('Fix fatal issues before importing');
      return;
    }
    setSubmitting(true);
    try {
      const payload = validation.bodies.map((b) => ({ ...b, site_id: siteId }));
      const r = await http.post('/inventory/assets/bulk', payload);
      setResult(r.data);
      toast.success(`Imported: ${r.data.inserted} inserted, ${r.data.updated} updated, ${r.data.failed} failed`);
    } catch (err: any) {
      toast.error(err?.message ?? 'import failed');
    } finally {
      setSubmitting(false);
    }
  }

  function downloadSample() {
    const blob = new Blob([SAMPLE], { type: 'text/csv' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'dcim-assets-sample.csv';
    a.click();
    URL.revokeObjectURL(url);
  }

  if (!canBulk) {
    return (
      <ContentLayout header={<Header variant="h1">Bulk import</Header>}>
        <Box color="text-status-inactive">
          You don't have <code style={{ fontFamily: 'ui-monospace, monospace' }}>inventory:bulk:execute</code>.
          Ask an admin for a role that includes it (EnterpriseAdmin).
        </Box>
      </ContentLayout>
    );
  }

  const requiredMissing = REQUIRED.filter((c) => !header.includes(c));
  const unknownColumns = header.filter((c) => !SUPPORTED.includes(c as typeof SUPPORTED[number]));
  const fatal = validation.issues.filter((i) => i.message.startsWith('FATAL'));
  const warnings = validation.issues.filter((i) => !i.message.startsWith('FATAL'));

  const siteOptions: SelectProps.Option[] = sites.map((s) => ({ value: s.id, label: `${s.code} · ${s.name}` }));

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description={<>Upsert by (manufacturer, serial). Required columns: <code style={{ fontFamily: 'ui-monospace, monospace' }}>name</code>, <code style={{ fontFamily: 'ui-monospace, monospace' }}>kind</code>.</>}
          actions={<Button iconName="download" onClick={downloadSample}>Download sample</Button>}
        >
          Bulk import — assets
        </Header>
      }
    >
      <SpaceBetween size="l">
        <Container header={<Header variant="h2">Target site</Header>}>
          <FormField
            label="Site"
            description="All rows will be assigned to this site. The optional rack_code column places them in a rack."
          >
            <Select
              placeholder="Pick a site"
              selectedOption={siteOpt}
              onChange={({ detail }) => setSiteOpt(detail.selectedOption)}
              options={siteOptions}
              expandToViewport
            />
          </FormField>
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
                      disabled={submitting || !siteId || requiredMissing.length > 0 || fatal.length > 0 || rows.length === 0}
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
                    width: 60,
                  },
                  ...header.map((h) => ({
                    id: h,
                    header: SUPPORTED.includes(h as typeof SUPPORTED[number])
                      ? h
                      : <span style={{ textDecoration: 'line-through', opacity: 0.5 }}>{h}</span>,
                    cell: (r: Record<string, string>) => (
                      <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12, whiteSpace: 'nowrap' }}>
                        {r[h]}
                      </span>
                    ),
                  })),
                ]}
              />
              {rows.length > 10 && (
                <Box color="text-status-inactive" fontSize="body-s">
                  Showing 10 of {rows.length} rows. All will be sent on import.
                </Box>
              )}
            </SpaceBetween>
          </Container>
        )}

        {result && (
          <Container header={<Header variant="h2">Import complete</Header>}>
            <SpaceBetween size="s">
              <SpaceBetween size="xs" direction="horizontal">
                <StatusIndicator type="success">{`${result.inserted} inserted`}</StatusIndicator>
                <Badge>{`${result.updated} updated`}</Badge>
                {result.failed > 0
                  ? <StatusIndicator type="error">{`${result.failed} failed`}</StatusIndicator>
                  : <StatusIndicator type="success">0 failed</StatusIndicator>}
              </SpaceBetween>
              {result.errors.length > 0 && (
                <Container header={<Header variant="h3">Errors</Header>}>
                  <ul style={{ margin: 0, paddingLeft: 20, fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>
                    {result.errors.map((e) => (
                      <li key={`${e.serial ?? ''}-${e.error}`}>
                        {e.serial ? `${e.serial}: ` : ''}
                        <Box variant="span" color="text-status-error">{e.error}</Box>
                      </li>
                    ))}
                  </ul>
                </Container>
              )}
            </SpaceBetween>
          </Container>
        )}
      </SpaceBetween>
    </ContentLayout>
  );
}

// ----------------------- CSV → API mapping -----------------------

function mapRows(
  rows: Record<string, string>[],
  racksByCode: Map<string, Rack>,
): { bodies: Record<string, unknown>[]; issues: Issue[] } {
  const bodies: Record<string, unknown>[] = [];
  const issues: Issue[] = [];
  rows.forEach((r, idx) => {
    const rowNum = idx + 2;
    const m = mapRow(r, racksByCode, rowNum, issues);
    if (m) bodies.push(m.body);
  });
  return { bodies, issues };
}

function mapRow(
  r: Record<string, string>,
  racksByCode: Map<string, Rack>,
  rowNum: number,
  issues: Issue[],
): Mapped | null {
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
    name,
    kind,
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

  for (const [col, key] of [
    ['rack_position_u', 'rack_position_u'],
    ['rack_units', 'rack_units'],
    ['psu_count', 'psu_count'],
    ['port_count', 'port_count'],
    ['mgmt_port', 'mgmt_port'],
  ] as const) {
    const v = r[col];
    if (v === undefined || v === '') continue;
    const n = Number(v);
    if (Number.isNaN(n)) {
      issues.push({ row: rowNum, message: `${col} "${v}" is not a number (skipping)` });
    } else {
      body[key] = n;
    }
  }
  if (body.rack_units === undefined && body.rack_position_u !== undefined) body.rack_units = 1;

  const rackCode = (r.rack_code ?? '').trim();
  if (rackCode) {
    const rk = racksByCode.get(rackCode.toLowerCase());
    if (rk) body.rack_id = rk.id;
    else issues.push({ row: rowNum, message: `rack_code "${rackCode}" not found at this site (asset will be unplaced)` });
  }

  return { body, issues: [] };
}
