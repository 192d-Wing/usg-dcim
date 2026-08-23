// Rack detail — Cloudscape chrome around the existing visualization +
// panels. RackVisualization keeps its inline styles since it has its
// own coordinate-driven layout that doesn't depend on Tailwind.

import { useState } from 'react';
import { useNavigate, useParams } from 'react-router';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useUpdate } from '@refinedev/core';
import { toast } from 'sonner';

import Alert from '@cloudscape-design/components/alert';
import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import SegmentedControl from '@cloudscape-design/components/segmented-control';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Spinner from '@cloudscape-design/components/spinner';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';

import { http } from '@/lib/http';
import { hasCapability } from '@/lib/access-control-provider';
import { RackVisualization } from '@/components/rack-visualization';
import { RackHeightPicker } from '@/components/rack-height-picker';
import { useStencilCatalog } from '@/components/stencil';
import { CapacityPanel, type Capacity } from '@/components/capacity-panel';
import { PowerChainPanel, type PduSummary, type PerAsset } from '@/components/power-chain-panel';
import { MoveAssetDialog } from '@/components/move-asset-dialog';
import { CablePanel } from '@/components/cable-panel';
import { ForecastPanel } from '@/components/forecast-panel';

type RackDetail = {
  rack: {
    id: string; site_id: string; row_id: string; name: string; code: string;
    u_height: number; max_kw: number | null; serial: string | null;
  };
  capacity: Capacity;
  power_chain: { per_asset: Record<string, PerAsset>; pdus: PduSummary[] };
  assets: Array<{
    id: string; name: string; hostname: string | null; kind: string;
    manufacturer: string | null; model: string | null; serial: string | null;
    rack_position_u: number | null; rack_units: number;
    face?: 'front' | 'rear';
    mount?: 'rack' | 'vertical-left' | 'vertical-right';
    pdu_side?: 'A' | 'B' | 'C' | null;
    psu_count?: number | null;
    port_count?: number | null;
    redundancy?: 'redundant' | 'single' | 'unpowered' | 'n/a';
    lifecycle_state: string; open_alerts: number;
  }>;
};

export function RackShowPage() {
  const { id = '' } = useParams<{ id: string }>();
  const nav = useNavigate();
  const qc = useQueryClient();
  const [mode, setMode] = useState<'stencil' | 'block'>('stencil');
  const [editOpen, setEditOpen] = useState(false);
  const [addOpen, setAddOpen] = useState(false);
  const [moving, setMoving] = useState<RackDetail['assets'][number] | null>(null);
  const canWrite = hasCapability('inventory:racks:update');

  const detail = useQuery({
    queryKey: ['rack-detail', id],
    queryFn: async () => (await http.get<RackDetail>(`/dashboards/racks/${id}`)).data,
    refetchInterval: 30_000,
    enabled: !!id,
  });

  if (detail.isLoading) {
    return (
      <ContentLayout header={<Header variant="h1">Loading…</Header>}>
        <Box textAlign="center" padding="xl"><Spinner size="large" /></Box>
      </ContentLayout>
    );
  }
  if (detail.isError || !detail.data?.rack) {
    return (
      <ContentLayout header={<Header variant="h1">Rack</Header>}>
        <Box color="text-status-error">Failed to load rack.</Box>
      </ContentLayout>
    );
  }

  const r = detail.data.rack;
  const assets = detail.data.assets ?? [];
  const occupied = new Set<number>();
  for (const a of assets) {
    if (a.rack_position_u && a.rack_position_u >= 1) {
      const span = Math.max(1, a.rack_units || 1);
      for (let u = a.rack_position_u; u < a.rack_position_u + span; u++) occupied.add(u);
    }
  }

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description={[
            `${r.u_height}U`,
            r.max_kw ? `${r.max_kw} kW max` : 'unrated',
            `${assets.length} devices`,
            r.serial && `SN ${r.serial}`,
          ].filter(Boolean).join(' · ')}
          actions={
            <SpaceBetween size="xs" direction="horizontal">
              <Button onClick={() => nav('/racks')} iconName="angle-left">All racks</Button>
              <SegmentedControl
                selectedId={mode}
                onChange={({ detail }) => setMode(detail.selectedId as 'stencil' | 'block')}
                options={[{ id: 'stencil', text: 'Stencil' }, { id: 'block', text: 'Block' }]}
              />
              {canWrite && (
                <>
                  <Button variant="primary" iconName="add-plus" onClick={() => setAddOpen(true)}>
                    Add device
                  </Button>
                  <Button iconName="edit" onClick={() => setEditOpen(true)}>Edit rack</Button>
                </>
              )}
            </SpaceBetween>
          }
        >
          {r.code} · {r.name}
        </Header>
      }
    >
      <SpaceBetween size="l">
        {detail.data.capacity && <CapacityPanel capacity={detail.data.capacity} />}

        <ForecastPanel rackId={id} />

        <Container header={<Header variant="h2">Layout</Header>}>
          <RackVisualization rackId={id} uHeight={r.u_height} assets={assets as any} mode={mode} />
        </Container>

        {detail.data.power_chain && (
          <PowerChainPanel
            rackId={id}
            pdus={detail.data.power_chain.pdus}
            perAsset={detail.data.power_chain.per_asset}
            assets={assets.map((a) => ({
              id: a.id, name: a.name, kind: a.kind,
              pdu_side: a.pdu_side, psu_count: a.psu_count,
              redundancy: a.redundancy,
            }))}
          />
        )}

        <CablePanel
          rackId={id}
          siteId={r.site_id}
          rackAssets={assets.map((a) => ({
            id: a.id, name: a.name, kind: a.kind,
            port_count: a.port_count ?? null,
          }))}
        />

        <Table
          variant="container"
          header={<Header variant="h2" counter={`(${assets.length})`}>Devices</Header>}
          items={assets}
          trackBy="id"
          onRowClick={({ detail }) => nav(`/assets/${detail.item.id}`)}
          columnDefinitions={[
            {
              id: 'u', header: 'U',
              cell: (a) => <span style={{ fontVariantNumeric: 'tabular-nums' }}>{a.rack_position_u ?? '—'}</span>,
              width: 60,
            },
            { id: 'name', header: 'Name', cell: (a) => <span style={{ fontWeight: 500 }}>{a.name}</span> },
            {
              id: 'host', header: 'Hostname',
              cell: (a) => <Box variant="span" color="text-status-inactive">{a.hostname ?? '—'}</Box>,
            },
            { id: 'kind', header: 'Kind', cell: (a) => <Badge>{a.kind}</Badge>, width: 100 },
            { id: 'mfr', header: 'Manufacturer', cell: (a) => a.manufacturer ?? '—' },
            { id: 'model', header: 'Model', cell: (a) => a.model ?? '—' },
            {
              id: 'serial', header: 'Serial',
              cell: (a) => <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{a.serial ?? '—'}</span>,
            },
            {
              id: 'alerts', header: 'Alerts',
              cell: (a) => a.open_alerts > 0
                ? <Badge color="red">{String(a.open_alerts)}</Badge>
                : <Box variant="span" color="text-status-inactive">0</Box>,
              width: 90,
            },
            {
              id: 'state', header: 'State',
              cell: (a) => a.lifecycle_state === 'active'
                ? <StatusIndicator type="success">{a.lifecycle_state}</StatusIndicator>
                : <StatusIndicator type="warning">{a.lifecycle_state}</StatusIndicator>,
              width: 120,
            },
            ...(canWrite ? [{
              id: 'actions', header: '',
              cell: (a: RackDetail['assets'][number]) => (
                <Button
                  iconName="copy"
                  variant="inline-icon"
                  onClick={(e: any) => { e?.stopPropagation?.(); setMoving(a); }}
                  ariaLabel={`Move ${a.name}`}
                />
              ),
              width: 80,
            }] : []),
          ]}
          empty={<Box textAlign="center" color="inherit" padding="m">No devices in this rack.</Box>}
        />

        <MoveAssetDialog
          asset={moving ? {
            id: moving.id,
            name: moving.name,
            site_id: r.site_id,
            rack_id: r.id,
            rack_position_u: moving.rack_position_u,
            rack_units: moving.rack_units,
            face: (moving.face ?? 'front') as 'front' | 'rear',
          } : null}
          open={moving !== null}
          onOpenChange={(o) => { if (!o) setMoving(null); }}
          onMoved={() => qc.invalidateQueries({ queryKey: ['rack-detail', id] })}
        />

        {canWrite && (
          <Modal visible={addOpen} onDismiss={() => setAddOpen(false)} header="Add device to rack" size="medium">
            <NewAssetForm
              siteId={r.site_id}
              rackId={r.id}
              uHeight={r.u_height}
              occupiedSlots={occupied}
              onCreated={async () => {
                setAddOpen(false);
                toast.success('Device added');
                await qc.invalidateQueries({ queryKey: ['rack-detail', id] });
              }}
            />
          </Modal>
        )}
        {canWrite && (
          <Modal visible={editOpen} onDismiss={() => setEditOpen(false)} header="Edit rack" size="medium">
            <EditRackForm
              rack={r}
              assets={assets}
              onSaved={async () => {
                setEditOpen(false);
                await qc.invalidateQueries({ queryKey: ['rack-detail', id] });
              }}
            />
          </Modal>
        )}
      </SpaceBetween>
    </ContentLayout>
  );
}

function EditRackForm({
  rack, assets, onSaved,
}: Readonly<{
  rack: RackDetail['rack'];
  assets: RackDetail['assets'];
  onSaved: () => void;
}>) {
  const updateMutation = useUpdate();
  const isPending = (updateMutation as any).isPending ?? (updateMutation as any).isLoading ?? false;
  const update = updateMutation.mutate;
  const [name, setName] = useState(rack.name);
  const [uHeight, setUHeight] = useState(rack.u_height);
  const [maxKw, setMaxKw] = useState(rack.max_kw?.toString() ?? '');
  const [serial, setSerial] = useState(rack.serial ?? '');

  const orphans = assets.filter(
    (a) => a.rack_position_u && (a.rack_position_u + Math.max(1, a.rack_units || 1) - 1) > uHeight,
  );

  function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (orphans.length > 0) {
      toast.error(`${orphans.length} device(s) would be orphaned at U${uHeight}`);
      return;
    }
    if (maxKw.trim() && Number.isNaN(Number(maxKw))) {
      toast.error('Max kW must be a number');
      return;
    }
    update(
      {
        resource: 'inventory/racks',
        id: rack.id,
        values: {
          name, u_height: uHeight,
          // NUMERIC rides as a JSON string on the wire (the backend
          // field is *string) — a JSON number fails decoding with a 400.
          max_kw: maxKw.trim() || null,
          serial: serial || null,
        },
        successNotification: false,
      },
      {
        onSuccess: () => { toast.success('Rack updated'); onSaved(); },
        onError: (err: any) => toast.error(err?.message ?? 'Update failed'),
      },
    );
  }

  return (
    <form onSubmit={onSubmit}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={isPending} disabled={orphans.length > 0}>
            {isPending ? 'Saving…' : 'Save'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField label="Name">
            <Input value={name} onChange={({ detail }) => setName(detail.value)} />
          </FormField>
          <FormField label="Rack height">
            <RackHeightPicker value={uHeight} onChange={(v) => setUHeight(v)} />
          </FormField>
          {orphans.length > 0 && (
            <Alert type="error" header={`${orphans.length} device(s) would be orphaned at ${uHeight}U`}>
              <ul style={{ marginTop: 4, paddingLeft: 20 }}>
                {orphans.slice(0, 5).map((a) => (
                  <li key={a.id}>
                    {a.name} (U{a.rack_position_u}{(a.rack_units || 1) > 1 ? `–U${(a.rack_position_u || 0) + (a.rack_units || 1) - 1}` : ''})
                  </li>
                ))}
                {orphans.length > 5 && <li>…and {orphans.length - 5} more</li>}
              </ul>
              Move them to lower U positions before shrinking the rack.
            </Alert>
          )}
          <ColumnLayout columns={2}>
            <FormField label="Max kW">
              <Input type="number" value={maxKw} onChange={({ detail }) => setMaxKw(detail.value)} />
            </FormField>
            <FormField label="Serial">
              <Input value={serial} onChange={({ detail }) => setSerial(detail.value)} />
            </FormField>
          </ColumnLayout>
        </SpaceBetween>
      </Form>
    </form>
  );
}

const KINDS = ['server', 'switch', 'router', 'pdu', 'ups', 'crac', 'sensor', 'storage', 'chassis', 'blade', 'patch_panel', 'other'] as const;
const KIND_OPTS: SelectProps.Option[] = KINDS.map((k) => ({ value: k, label: k }));

function NewAssetForm({
  siteId, rackId, uHeight, occupiedSlots, onCreated,
}: Readonly<{
  siteId: string;
  rackId: string;
  uHeight: number;
  occupiedSlots: Set<number>;
  onCreated: () => void;
}>) {
  const catalog = useStencilCatalog();
  const [name, setName] = useState('');
  const [hostname, setHostname] = useState('');
  const [kindOpt, setKindOpt] = useState<SelectProps.Option>(KIND_OPTS[0]);
  const [manufacturer, setManufacturer] = useState('');
  const [model, setModel] = useState('');
  const [serial, setSerial] = useState('');
  const [positionUStr, setPositionUStr] = useState('');
  const [units, setUnits] = useState('1');
  const [portCount, setPortCount] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const positionU = positionUStr ? Number(positionUStr) : null;
  const unitsN = Number(units) || 1;
  const collisions: number[] = [];
  if (positionU) {
    for (let u = positionU; u < positionU + unitsN; u++) {
      if (occupiedSlots.has(u)) collisions.push(u);
    }
  }
  const vendorMatches = (catalog.data?.stencils ?? []).filter(
    (s) => manufacturer && s.manufacturer.toLowerCase().includes(manufacturer.toLowerCase()),
  );

  function applyStencil(s: { manufacturer: string; model: string; u: number; kind_hint?: string }) {
    setManufacturer(s.manufacturer);
    setModel(s.model);
    if (s.kind_hint) {
      const found = KIND_OPTS.find((k) => k.value === s.kind_hint);
      if (found) setKindOpt(found);
    }
    if (s.u > 0) setUnits(String(s.u));
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Name required';
    if (collisions.length > 0) errs.position = `Slots already occupied: U${collisions.join(', U')}`;
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      await http.post('/inventory/assets', {
        site_id: siteId,
        rack_id: rackId,
        name,
        hostname: hostname || null,
        kind: kindOpt.value,
        manufacturer: manufacturer || null,
        model: model || null,
        serial: serial || null,
        rack_position_u: positionU,
        rack_units: unitsN,
        port_count: portCount ? Number(portCount) : null,
        lifecycle_state: 'active',
        metadata_json: {},
      });
      onCreated();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to create asset');
    } finally {
      setSubmitting(false);
    }
  }

  const vendors = Array.from(new Set((catalog.data?.stencils ?? []).map((s) => s.manufacturer))).sort();

  return (
    <form onSubmit={onSubmit}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={submitting}>
            {submitting ? 'Adding…' : 'Add device'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField label="Name" errorText={errors.name}>
            <Input value={name} onChange={({ detail }) => setName(detail.value)} placeholder="e.g. R01-server9" />
          </FormField>
          <FormField label="Hostname (optional)">
            <Input value={hostname} onChange={({ detail }) => setHostname(detail.value)} />
          </FormField>
          <ColumnLayout columns={2}>
            <FormField label="Manufacturer">
              <Input value={manufacturer} onChange={({ detail }) => setManufacturer(detail.value)} placeholder="Dell, HPE, Cisco…" />
              <datalist id="vendor-list">
                {vendors.map((v) => <option key={v} value={v} />)}
              </datalist>
            </FormField>
            <FormField label="Model">
              <Input value={model} onChange={({ detail }) => setModel(detail.value)} placeholder="PowerEdge R750…" />
            </FormField>
          </ColumnLayout>
          {vendorMatches.length > 0 && model.length === 0 && (
            <Box>
              <Box variant="awsui-key-label">Stencils for {manufacturer}</Box>
              <SpaceBetween size="xxs" direction="horizontal">
                {vendorMatches.slice(0, 6).map((s) => (
                  <Button
                    key={`${s.manufacturer}-${s.model}`}
                    onClick={() => applyStencil(s)}
                  >
                    {s.model} ({s.u}U)
                  </Button>
                ))}
              </SpaceBetween>
            </Box>
          )}
          <ColumnLayout columns={3}>
            <FormField label="Kind">
              <Select selectedOption={kindOpt} onChange={({ detail }) => setKindOpt(detail.selectedOption)}
                options={KIND_OPTS} expandToViewport />
            </FormField>
            <FormField label={`Position U (1–${uHeight})`} errorText={errors.position}>
              <Input
                type="number" value={positionUStr}
                onChange={({ detail }) => setPositionUStr(detail.value)}
                placeholder="leave blank if unplaced"
              />
            </FormField>
            <FormField label="Size (U)">
              <Input type="number" value={units} onChange={({ detail }) => setUnits(detail.value)} />
            </FormField>
          </ColumnLayout>
          {kindOpt.value === 'patch_panel' && (
            <FormField label="Port count">
              <Input
                type="number" value={portCount}
                onChange={({ detail }) => setPortCount(detail.value)}
                placeholder="e.g. 24, 48"
              />
            </FormField>
          )}
          <FormField label="Serial (optional)">
            <Input value={serial} onChange={({ detail }) => setSerial(detail.value)} />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}
