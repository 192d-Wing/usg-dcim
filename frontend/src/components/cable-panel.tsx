import { useMemo, useState } from 'react';
import { useList, useUpdate, useDelete } from '@refinedev/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import { colorBorderDividerDefault } from '@cloudscape-design/design-tokens';

import { http } from '@/lib/http';
import { hasCapability } from '@/lib/access-control-provider';

type Cable = {
  id: string;
  site_id: string;
  a_asset_id: string;
  a_port: string | null;
  b_asset_id: string;
  b_port: string | null;
  medium: string | null;
  color: string | null;
  length_m: number | null;
  label: string | null;
  face: string | null;
};
type Asset = {
  id: string; name: string; kind: string; site_id: string;
  rack_id: string | null; port_count?: number | null;
};
type RackAsset = { id: string; name: string; kind: string; port_count?: number | null };

const COMMON_MEDIA = ['cat6', 'cat6a', 'smf', 'mmf', 'dac', 'aoc', 'power-c13', 'power-c19'];
const COMMON_COLORS = ['blue', 'yellow', 'red', 'green', 'orange', 'white', 'black', 'gray'];

const FACE_OPTIONS: SelectProps.Option[] = [
  { value: 'all', label: 'All faces' },
  { value: 'front', label: 'Front only' },
  { value: 'rear', label: 'Rear only' },
  { value: 'unspecified', label: 'Unspecified' },
];

const FORM_FACE_OPTIONS: SelectProps.Option[] = [
  { value: 'unspecified', label: 'Unspecified' },
  { value: 'front', label: 'Front' },
  { value: 'rear', label: 'Rear' },
];

const MONO = { fontFamily: 'ui-monospace, monospace' } as const;

type Props = Readonly<{
  rackId: string;
  siteId: string;
  rackAssets: RackAsset[];
}>;

export function CablePanel({ rackId, siteId, rackAssets }: Props) {
  const canWrite = hasCapability('inventory:write');
  const qc = useQueryClient();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<Cable | null>(null);
  const [faceOpt, setFaceOpt] = useState<SelectProps.Option>(FACE_OPTIONS[0]);

  const cablesRes = useQuery({
    queryKey: ['rack-cables', rackId],
    queryFn: async () => {
      const r = await http.get<{ items: Cable[] }>('/inventory/cables', {
        params: { rack_id: rackId, page_size: 500 },
      });
      return r.data.items ?? [];
    },
    enabled: !!rackId,
  });

  const allCables = cablesRes.data ?? [];
  const faceFilter = faceOpt.value;
  const cables = useMemo(() => {
    if (faceFilter === 'all') return allCables;
    if (faceFilter === 'unspecified') return allCables.filter((c) => !c.face);
    return allCables.filter((c) => c.face === faceFilter);
  }, [allCables, faceFilter]);

  const otherEndIds = useMemo(() => {
    const local = new Set(rackAssets.map((a) => a.id));
    const ids = new Set<string>();
    for (const c of allCables) {
      if (!local.has(c.a_asset_id)) ids.add(c.a_asset_id);
      if (!local.has(c.b_asset_id)) ids.add(c.b_asset_id);
    }
    return Array.from(ids);
  }, [allCables, rackAssets]);

  const remoteRes = useQuery({
    queryKey: ['cable-remote-endpoints', otherEndIds.sort().join(',')],
    queryFn: async () => {
      if (otherEndIds.length === 0) return [] as Asset[];
      const results = await Promise.all(
        otherEndIds.map((id) => http.get<Asset>(`/inventory/assets/${id}`).then((r) => r.data)),
      );
      return results;
    },
    enabled: otherEndIds.length > 0,
  });

  const assetById = useMemo(() => {
    const m = new Map<string, RackAsset>();
    for (const a of rackAssets) m.set(a.id, a);
    for (const a of remoteRes.data ?? []) {
      m.set(a.id, { id: a.id, name: a.name, kind: a.kind, port_count: a.port_count ?? null });
    }
    return m;
  }, [rackAssets, remoteRes.data]);

  const deleteMutation = useDelete();
  function onDelete(c: Cable) {
    const labelSuffix = c.label ? ` "${c.label}"` : '';
    if (!globalThis.confirm(`Delete cable${labelSuffix}?`)) return;
    deleteMutation.mutate(
      { resource: 'inventory/cables', id: c.id, successNotification: false },
      {
        onSuccess: () => {
          toast.success('Cable removed');
          qc.invalidateQueries({ queryKey: ['rack-cables', rackId] });
        },
        onError: (err: any) => toast.error(err?.message ?? 'Delete failed'),
      },
    );
  }

  const counterText = faceFilter === 'all'
    ? `(${cables.length})`
    : `(${cables.length} of ${allCables.length})`;

  return (
    <>
      <Table<Cable>
        variant="container"
        loading={cablesRes.isLoading}
        loadingText="Loading cables…"
        items={cables}
        trackBy="id"
        header={
          <Header
            counter={counterText}
            actions={
              <SpaceBetween size="xs" direction="horizontal">
                <Select
                  selectedOption={faceOpt}
                  onChange={({ detail }) => {
                    if (detail.selectedOption.value) setFaceOpt(detail.selectedOption);
                  }}
                  options={FACE_OPTIONS}
                  expandToViewport
                />
                {canWrite && (
                  <Button
                    variant="primary"
                    iconName="add-plus"
                    onClick={() => { setEditing(null); setDialogOpen(true); }}
                  >
                    Add cable
                  </Button>
                )}
              </SpaceBetween>
            }
          >
            Cables
          </Header>
        }
        columnDefinitions={[
          {
            id: 'face', header: 'Face',
            cell: (c) => c.face
              ? <Badge>{c.face}</Badge>
              : <Box color="text-status-inactive" fontSize="body-s">—</Box>,
            width: 90,
          },
          { id: 'a', header: 'A-end', cell: (c) => assetById.get(c.a_asset_id)?.name ?? c.a_asset_id.slice(0, 8) },
          { id: 'aport', header: 'A port', cell: (c) => <span style={MONO}>{c.a_port ?? '—'}</span>, width: 90 },
          { id: 'b', header: 'B-end', cell: (c) => assetById.get(c.b_asset_id)?.name ?? c.b_asset_id.slice(0, 8) },
          { id: 'bport', header: 'B port', cell: (c) => <span style={MONO}>{c.b_port ?? '—'}</span>, width: 90 },
          {
            id: 'medium', header: 'Medium',
            cell: (c) => c.medium ? <Badge>{c.medium}</Badge> : '—',
            width: 100,
          },
          {
            id: 'color', header: 'Color',
            cell: (c) => c.color ? (
              <SpaceBetween size="xxs" direction="horizontal">
                <span style={{
                  display: 'inline-block', width: 12, height: 12, borderRadius: 2,
                  background: c.color, border: `1px solid ${colorBorderDividerDefault}`,
                }} />
                <Box fontSize="body-s">{c.color}</Box>
              </SpaceBetween>
            ) : '—',
            width: 100,
          },
          { id: 'len', header: 'Len (m)', cell: (c) => c.length_m ?? '—', width: 90 },
          {
            id: 'label', header: 'Label',
            cell: (c) => c.label
              ? <span>{c.label}</span>
              : <Box color="text-status-inactive" fontSize="body-s">—</Box>,
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (c: Cable) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button
                  iconName="edit"
                  variant="inline-icon"
                  onClick={() => { setEditing(c); setDialogOpen(true); }}
                  ariaLabel="Edit cable"
                />
                <Button
                  iconName="remove"
                  variant="inline-icon"
                  onClick={() => onDelete(c)}
                  ariaLabel="Delete cable"
                />
              </SpaceBetween>
            ),
            width: 100,
          }] : []),
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            No cables logged for this rack yet.
          </Box>
        }
      />
      <CableDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        siteId={siteId}
        rackAssets={rackAssets}
        editing={editing}
        onSaved={() => qc.invalidateQueries({ queryKey: ['rack-cables', rackId] })}
      />
    </>
  );
}

function CableDialog({
  open, onOpenChange, siteId, rackAssets, editing, onSaved,
}: Readonly<{
  open: boolean;
  onOpenChange: (o: boolean) => void;
  siteId: string;
  rackAssets: RackAsset[];
  editing: Cable | null;
  onSaved: () => void;
}>) {
  const initial = useMemo(() => ({
    a_asset_id: editing?.a_asset_id ?? rackAssets[0]?.id ?? '',
    a_port: editing?.a_port ?? '',
    b_asset_id: editing?.b_asset_id ?? rackAssets[1]?.id ?? '',
    b_port: editing?.b_port ?? '',
    medium: editing?.medium ?? '',
    color: editing?.color ?? '',
    length_m: editing?.length_m != null ? String(editing.length_m) : '',
    label: editing?.label ?? '',
    face: editing?.face ?? '',
  }), [editing, rackAssets]);

  const [aAssetId, setAAssetId] = useState(initial.a_asset_id);
  const [aPort, setAPort] = useState(initial.a_port);
  const [bAssetId, setBAssetId] = useState(initial.b_asset_id);
  const [bPort, setBPort] = useState(initial.b_port);
  const [medium, setMedium] = useState(initial.medium);
  const [color, setColor] = useState(initial.color);
  const [lengthM, setLengthM] = useState(initial.length_m);
  const [label, setLabel] = useState(initial.label);
  const [face, setFace] = useState(initial.face);
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  // Reset state when the dialog re-opens for a different editing target.
  useMemo(() => {
    setAAssetId(initial.a_asset_id);
    setAPort(initial.a_port);
    setBAssetId(initial.b_asset_id);
    setBPort(initial.b_port);
    setMedium(initial.medium);
    setColor(initial.color);
    setLengthM(initial.length_m);
    setLabel(initial.label);
    setFace(initial.face);
    setErrors({});
  }, [initial]);

  const siteAssetsRes = useList<Asset>({
    resource: 'inventory/assets',
    pagination: { pageSize: 500 },
    filters: [{ field: 'site_id', operator: 'eq', value: siteId }],
    queryOptions: { enabled: open && !!siteId },
  });
  const siteAssets = siteAssetsRes.result.data ?? [];

  const portCountById = useMemo(() => {
    const m = new Map<string, number>();
    for (const a of rackAssets) {
      if (a.port_count && a.port_count > 0) m.set(a.id, a.port_count);
    }
    for (const a of siteAssets) {
      if (a.port_count && a.port_count > 0) m.set(a.id, a.port_count);
    }
    return m;
  }, [rackAssets, siteAssets]);

  const aPortCount = portCountById.get(aAssetId);
  const bPortCount = portCountById.get(bAssetId);

  function assetOptions(): SelectProps.Options {
    const localIds = new Set(rackAssets.map((a) => a.id));
    const remote = siteAssets.filter((a) => !localIds.has(a.id));
    const groups: SelectProps.OptionGroup[] = [];
    if (rackAssets.length > 0) {
      groups.push({
        label: 'In this rack',
        options: rackAssets.map((a) => ({ value: a.id, label: a.name, description: a.kind })),
      });
    }
    if (remote.length > 0) {
      groups.push({
        label: 'Other in site',
        options: remote.map((a) => ({ value: a.id, label: a.name, description: a.kind })),
      });
    }
    return groups;
  }
  const aOptions = assetOptions();
  const bOptions = assetOptions();

  const aSelectedOpt: SelectProps.Option | null = aAssetId
    ? { value: aAssetId, label: rackAssets.concat(siteAssets as any).find((a: any) => a.id === aAssetId)?.name ?? aAssetId }
    : null;
  const bSelectedOpt: SelectProps.Option | null = bAssetId
    ? { value: bAssetId, label: rackAssets.concat(siteAssets as any).find((a: any) => a.id === bAssetId)?.name ?? bAssetId }
    : null;

  const updateMutation = useUpdate();

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!aAssetId) errs.a_asset_id = 'Required';
    if (!bAssetId) errs.b_asset_id = 'Required';
    if (aAssetId && bAssetId && aAssetId === bAssetId) errs.b_asset_id = 'A-end and B-end must differ';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;

    const payload = {
      a_asset_id: aAssetId,
      a_port: aPort || null,
      b_asset_id: bAssetId,
      b_port: bPort || null,
      medium: medium || null,
      color: color || null,
      length_m: lengthM ? Number(lengthM) : null,
      label: label || null,
      face: face || null,
    };
    setSubmitting(true);
    try {
      if (editing) {
        await new Promise<void>((resolve, reject) => {
          updateMutation.mutate(
            { resource: 'inventory/cables', id: editing.id, values: payload, successNotification: false },
            { onSuccess: () => resolve(), onError: (err) => reject(err) },
          );
        });
        toast.success('Cable updated');
      } else {
        await http.post('/inventory/cables', { ...payload, site_id: siteId });
        toast.success('Cable added');
      }
      onOpenChange(false);
      onSaved();
    } catch (err: any) {
      toast.error(err?.message ?? 'Save failed');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal
      visible={open}
      onDismiss={() => onOpenChange(false)}
      header={editing ? 'Edit cable' : 'Add cable'}
      size="large"
    >
      <form onSubmit={onSubmit}>
        <Form
          actions={
            <SpaceBetween size="xs" direction="horizontal">
              <Button onClick={() => onOpenChange(false)} variant="link">Cancel</Button>
              <Button variant="primary" formAction="submit" loading={submitting}>
                {submitting ? 'Saving…' : editing ? 'Save' : 'Add cable'}
              </Button>
            </SpaceBetween>
          }
        >
          <SpaceBetween size="m">
            <ColumnLayout columns={2}>
              <FormField label="A-end asset" errorText={errors.a_asset_id}>
                <Select
                  placeholder="Pick A-end"
                  selectedOption={aSelectedOpt}
                  onChange={({ detail }) => {
                    if (detail.selectedOption.value) setAAssetId(detail.selectedOption.value);
                  }}
                  options={aOptions}
                  expandToViewport
                />
              </FormField>
              <FormField label="A port">
                <PortPicker portCount={aPortCount} value={aPort} onChange={setAPort} />
              </FormField>
              <FormField label="B-end asset" errorText={errors.b_asset_id}>
                <Select
                  placeholder="Pick B-end"
                  selectedOption={bSelectedOpt}
                  onChange={({ detail }) => {
                    if (detail.selectedOption.value) setBAssetId(detail.selectedOption.value);
                  }}
                  options={bOptions}
                  expandToViewport
                />
              </FormField>
              <FormField label="B port">
                <PortPicker portCount={bPortCount} value={bPort} onChange={setBPort} />
              </FormField>
            </ColumnLayout>
            <ColumnLayout columns={3}>
              <FormField label="Medium">
                <Input
                  value={medium}
                  onChange={({ detail }) => setMedium(detail.value)}
                  placeholder="cat6, smf…"
                  // Native datalist for autocomplete suggestions; Cloudscape's
                  // Autosuggest would also work but Input is simpler here.
                />
                <datalist id="cable-media">
                  {COMMON_MEDIA.map((m) => <option key={m} value={m} />)}
                </datalist>
              </FormField>
              <FormField label="Color">
                <Input
                  value={color}
                  onChange={({ detail }) => setColor(detail.value)}
                  placeholder="blue, yellow…"
                />
                <datalist id="cable-colors">
                  {COMMON_COLORS.map((c) => <option key={c} value={c} />)}
                </datalist>
              </FormField>
              <FormField label="Length (m)">
                <Input
                  type="number"
                  value={lengthM}
                  onChange={({ detail }) => setLengthM(detail.value)}
                  step={0.1}
                />
              </FormField>
            </ColumnLayout>
            <ColumnLayout columns={2}>
              <FormField label="Routing face">
                <Select
                  selectedOption={
                    FORM_FACE_OPTIONS.find((o) => o.value === (face || 'unspecified')) ?? FORM_FACE_OPTIONS[0]
                  }
                  onChange={({ detail }) => {
                    const v = detail.selectedOption.value ?? 'unspecified';
                    setFace(v === 'unspecified' ? '' : v);
                  }}
                  options={FORM_FACE_OPTIONS}
                  expandToViewport
                />
              </FormField>
              <FormField label="Label (optional)">
                <Input
                  value={label}
                  onChange={({ detail }) => setLabel(detail.value)}
                  placeholder="e.g. CAB-001"
                />
              </FormField>
            </ColumnLayout>
          </SpaceBetween>
        </Form>
      </form>
    </Modal>
  );
}

function PortPicker({
  portCount, value, onChange,
}: Readonly<{
  portCount: number | undefined;
  value: string;
  onChange: (v: string) => void;
}>) {
  if (!portCount || portCount <= 0) {
    return (
      <Input
        value={value}
        onChange={({ detail }) => onChange(detail.value)}
        placeholder="e.g. eth0, Gi0/24"
      />
    );
  }
  const options: SelectProps.Option[] = Array.from({ length: portCount }, (_, i) => String(i + 1)).map((p) => ({
    value: p, label: p,
  }));
  return (
    <Select
      placeholder={`Pick port (1-${portCount})`}
      selectedOption={value ? { value, label: value } : null}
      onChange={({ detail }) => onChange(detail.selectedOption.value ?? '')}
      options={options}
      expandToViewport
    />
  );
}
