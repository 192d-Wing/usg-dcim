// New rack — Cloudscape ContentLayout + Container with cascading
// Site → Building → Room → Row pickers and a height picker.

import { useState } from 'react';
import { useNavigate } from 'react-router';
import { useList } from '@refinedev/core';
import { useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

import Button from '@cloudscape-design/components/button';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';

import { http } from '@/lib/http';
import { RackHeightPicker } from '@/components/rack-height-picker';

type Site = { id: string; code: string; name: string };
type HierarchyItem = { id: string; code: string; name: string };

export function RackCreatePage() {
  const nav = useNavigate();
  const qc = useQueryClient();

  const [siteOpt, setSiteOpt] = useState<SelectProps.Option | null>(null);
  const [buildingOpt, setBuildingOpt] = useState<SelectProps.Option | null>(null);
  const [roomOpt, setRoomOpt] = useState<SelectProps.Option | null>(null);
  const [rowOpt, setRowOpt] = useState<SelectProps.Option | null>(null);
  const [name, setName] = useState('');
  const [code, setCode] = useState('');
  const [uHeight, setUHeight] = useState(42);
  const [maxKw, setMaxKw] = useState('');
  const [serial, setSerial] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const siteId = siteOpt?.value ?? '';
  const buildingId = buildingOpt?.value ?? '';
  const roomId = roomOpt?.value ?? '';

  const sites = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const buildings = useList<HierarchyItem>({
    resource: 'inventory/buildings',
    pagination: { pageSize: 200 },
    filters: siteId ? [{ field: 'site_id', operator: 'eq', value: siteId }] : [],
    queryOptions: { enabled: !!siteId },
  });
  const rooms = useList<HierarchyItem>({
    resource: 'inventory/rooms',
    pagination: { pageSize: 200 },
    filters: buildingId ? [{ field: 'building_id', operator: 'eq', value: buildingId }] : [],
    queryOptions: { enabled: !!buildingId },
  });
  const rows = useList<HierarchyItem>({
    resource: 'inventory/rows',
    pagination: { pageSize: 200 },
    filters: roomId ? [{ field: 'room_id', operator: 'eq', value: roomId }] : [],
    queryOptions: { enabled: !!roomId },
  });

  const toOpts = (items: HierarchyItem[]): SelectProps.Option[] =>
    items.map((i) => ({ value: i.id, label: `${i.code} · ${i.name}` }));

  async function quickCreate(kind: 'building' | 'room' | 'row', label: string) {
    if (!label.trim()) return;
    try {
      let payload: Record<string, unknown> = {};
      if (kind === 'building') payload = { site_id: siteId, name: label, code: label };
      else if (kind === 'room') payload = { building_id: buildingId, name: label, code: label };
      else payload = { room_id: roomId, name: label, code: label };
      const r = await http.post(`/inventory/${kind}s`, payload);
      const opt = { value: r.data.id, label: `${label} · ${label}` };
      toast.success(`${kind} created`);
      if (kind === 'building') {
        await qc.invalidateQueries({ queryKey: ['data', 'inventory/buildings'] });
        setBuildingOpt(opt);
        setRoomOpt(null); setRowOpt(null);
      } else if (kind === 'room') {
        await qc.invalidateQueries({ queryKey: ['data', 'inventory/rooms'] });
        setRoomOpt(opt);
        setRowOpt(null);
      } else {
        await qc.invalidateQueries({ queryKey: ['data', 'inventory/rows'] });
        setRowOpt(opt);
      }
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!siteId) errs.site = 'Site required';
    if (!buildingId) errs.building = 'Building required';
    if (!roomId) errs.room = 'Room required';
    if (!rowOpt?.value) errs.row = 'Row required';
    if (!name.trim()) errs.name = 'Name required';
    if (!code.trim()) errs.code = 'Code required';
    if (uHeight < 1 || uHeight > 60) errs.height = '1..60';
    if (maxKw.trim() && Number.isNaN(Number(maxKw))) errs.maxKw = 'Number';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      const r = await http.post('/inventory/racks', {
        site_id: siteId,
        row_id: rowOpt!.value,
        name, code, u_height: uHeight,
        // NUMERIC rides as a JSON string on the wire (the backend field
        // is *string) — a JSON number fails decoding with a 400.
        max_kw: maxKw.trim() || null,
        serial: serial || null,
      });
      toast.success('Rack created');
      nav(`/racks/${r.data.id}`);
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          actions={<Button onClick={() => nav('/racks')} iconName="angle-left">All racks</Button>}
        >
          New rack
        </Header>
      }
    >
      <Container>
        <form onSubmit={onSubmit}>
          <Form
            actions={
              <Button variant="primary" formAction="submit" loading={submitting}>
                {submitting ? 'Creating…' : 'Create rack'}
              </Button>
            }
          >
            <SpaceBetween size="m">
              <FormField label="Site" errorText={errors.site}>
                <Select
                  placeholder="Pick a site…"
                  selectedOption={siteOpt}
                  onChange={({ detail }) => {
                    setSiteOpt(detail.selectedOption);
                    setBuildingOpt(null); setRoomOpt(null); setRowOpt(null);
                  }}
                  options={toOpts(sites.result.data ?? [])}
                  expandToViewport
                />
              </FormField>

              <CascadeRow
                label="Building"
                disabled={!siteId}
                opt={buildingOpt}
                options={toOpts(buildings.result.data ?? [])}
                errorText={errors.building}
                onChange={(o) => { setBuildingOpt(o); setRoomOpt(null); setRowOpt(null); }}
                onAdd={(v) => quickCreate('building', v)}
              />
              <CascadeRow
                label="Room"
                disabled={!buildingId}
                opt={roomOpt}
                options={toOpts(rooms.result.data ?? [])}
                errorText={errors.room}
                onChange={(o) => { setRoomOpt(o); setRowOpt(null); }}
                onAdd={(v) => quickCreate('room', v)}
              />
              <CascadeRow
                label="Row"
                disabled={!roomId}
                opt={rowOpt}
                options={toOpts(rows.result.data ?? [])}
                errorText={errors.row}
                onChange={setRowOpt}
                onAdd={(v) => quickCreate('row', v)}
              />

              <ColumnLayout columns={2}>
                <FormField label="Name" errorText={errors.name}>
                  <Input value={name} onChange={({ detail }) => setName(detail.value)} />
                </FormField>
                <FormField label="Code" errorText={errors.code}>
                  <Input value={code} onChange={({ detail }) => setCode(detail.value)} />
                </FormField>
              </ColumnLayout>
              <FormField label="Rack height" errorText={errors.height}>
                <RackHeightPicker value={uHeight} onChange={(v) => setUHeight(v)} />
              </FormField>
              <ColumnLayout columns={2}>
                <FormField label="Max kW" errorText={errors.maxKw}>
                  <Input type="number" value={maxKw} onChange={({ detail }) => setMaxKw(detail.value)} />
                </FormField>
                <FormField label="Serial">
                  <Input value={serial} onChange={({ detail }) => setSerial(detail.value)} />
                </FormField>
              </ColumnLayout>
            </SpaceBetween>
          </Form>
        </form>
      </Container>
    </ContentLayout>
  );
}

function CascadeRow({
  label, disabled, opt, options, errorText, onChange, onAdd,
}: Readonly<{
  label: string;
  disabled: boolean;
  opt: SelectProps.Option | null;
  options: SelectProps.Option[];
  errorText?: string;
  onChange: (opt: SelectProps.Option) => void;
  onAdd: (label: string) => void;
}>) {
  function quickAdd() {
    const v = window.prompt(`New ${label.toLowerCase()} (used for both name and code):`);
    if (v) onAdd(v.trim());
  }
  return (
    <FormField
      label={label}
      errorText={errorText}
      description={!disabled && options.length === 0 ? `None yet — use + New to create one.` : undefined}
    >
      <SpaceBetween size="xs" direction="horizontal">
        <Select
          placeholder={disabled ? 'Pick the parent first' : `Select a ${label.toLowerCase()}…`}
          selectedOption={opt}
          onChange={({ detail }) => onChange(detail.selectedOption)}
          options={options}
          disabled={disabled}
          expandToViewport
        />
        <Button disabled={disabled} iconName="add-plus" onClick={quickAdd}>New</Button>
      </SpaceBetween>
    </FormField>
  );
}

