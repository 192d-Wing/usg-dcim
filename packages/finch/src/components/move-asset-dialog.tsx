import { useEffect, useMemo, useState } from 'react';
import { useList, useUpdate } from '@refinedev/core';
import { toast } from 'sonner';

import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import SegmentedControl from '@cloudscape-design/components/segmented-control';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';

type MoveAsset = {
  id: string;
  name: string;
  site_id: string;
  rack_id: string | null;
  rack_position_u: number | null;
  rack_units: number;
  face: 'front' | 'rear';
};
type Site = { id: string; code: string; name: string };
type Rack = { id: string; name: string; code: string; u_height: number; site_id: string };
type RackAsset = {
  id: string;
  rack_id: string | null;
  rack_position_u: number | null;
  rack_units: number | null;
  face: 'front' | 'rear';
  mount: 'rack' | 'vertical-left' | 'vertical-right';
};

type Props = Readonly<{
  asset: MoveAsset | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onMoved?: () => void;
}>;

export function MoveAssetDialog({ asset, open, onOpenChange, onMoved }: Props) {
  const [siteOpt, setSiteOpt] = useState<SelectProps.Option | null>(null);
  const [rackOpt, setRackOpt] = useState<SelectProps.Option | null>(null);
  const [face, setFace] = useState<'front' | 'rear'>('front');
  const [positionU, setPositionU] = useState<string>('');

  useEffect(() => {
    if (!asset || !open) return;
    setSiteOpt(null);
    setRackOpt(null);
    setFace(asset.face);
    setPositionU(asset.rack_position_u != null ? String(asset.rack_position_u) : '');
  }, [asset, open]);

  const sitesRes = useList<Site>({
    resource: 'inventory/sites',
    pagination: { pageSize: 200 },
    queryOptions: { enabled: open },
  });
  const sites = sitesRes.result.data ?? [];

  // Seed site selection once the list arrives, if the asset has a site.
  useEffect(() => {
    if (!siteOpt && asset && sites.length > 0) {
      const s = sites.find((x) => x.id === asset.site_id);
      if (s) setSiteOpt({ value: s.id, label: `${s.code} · ${s.name}` });
    }
  }, [sites, asset, siteOpt]);

  const siteId = siteOpt?.value ?? '';
  const racksRes = useList<Rack>({
    resource: 'inventory/racks',
    pagination: { pageSize: 200 },
    filters: siteId ? [{ field: 'site_id', operator: 'eq', value: siteId }] : [],
    queryOptions: { enabled: open && !!siteId },
  });
  const racks = racksRes.result.data ?? [];

  useEffect(() => {
    if (!rackOpt && asset?.rack_id && racks.length > 0) {
      const r = racks.find((x) => x.id === asset.rack_id);
      if (r) setRackOpt({ value: r.id, label: `${r.code} · ${r.name} (${r.u_height}U)` });
    }
  }, [racks, asset, rackOpt]);

  const rackId = rackOpt?.value ?? '';
  const targetAssetsRes = useList<RackAsset>({
    resource: 'inventory/assets',
    pagination: { pageSize: 500 },
    filters: rackId ? [{ field: 'rack_id', operator: 'eq', value: rackId }] : [],
    queryOptions: { enabled: open && !!rackId },
  });

  const targetAssets = targetAssetsRes.result.data ?? [];
  const targetRack = racks.find((r) => r.id === rackId);
  const units = Math.max(1, asset?.rack_units ?? 1);
  const u = positionU ? Number(positionU) : null;
  const top = u != null ? u + units - 1 : null;

  const occupied = useMemo(() => {
    const occ = new Set<number>();
    for (const a of targetAssets) {
      if (asset && a.id === asset.id) continue;
      if (a.mount !== 'rack') continue;
      if (a.face !== face) continue;
      if (a.rack_position_u == null) continue;
      const span = Math.max(1, a.rack_units || 1);
      for (let i = a.rack_position_u; i < a.rack_position_u + span; i++) occ.add(i);
    }
    return occ;
  }, [targetAssets, asset, face]);

  type ValidationKind = 'ok' | 'overflow' | 'collision' | 'unplaced';
  let validation: { kind: ValidationKind; msg: string } = {
    kind: 'unplaced', msg: 'Will be moved without a U position.',
  };
  if (u != null && top != null && targetRack) {
    if (u < 1 || top > targetRack.u_height) {
      validation = {
        kind: 'overflow',
        msg: `U${u}–U${top} overflows ${targetRack.u_height}U rack.`,
      };
    } else {
      const hits: number[] = [];
      for (let i = u; i <= top; i++) if (occupied.has(i)) hits.push(i);
      if (hits.length) {
        validation = {
          kind: 'collision',
          msg: `U${hits.join(', U')} already occupied on the ${face} face.`,
        };
      } else {
        const range = units > 1 ? `–U${top}` : '';
        validation = { kind: 'ok', msg: `Will occupy U${u}${range} on ${face}.` };
      }
    }
  }

  const updateMutation = useUpdate();
  const isPending = (updateMutation as any).isPending ?? (updateMutation as any).isLoading ?? false;
  const noChange =
    asset != null
    && rackId === (asset.rack_id ?? '')
    && face === asset.face
    && u === asset.rack_position_u;
  const canSubmit =
    !!asset && !!rackId && !isPending && !noChange
    && validation.kind !== 'overflow' && validation.kind !== 'collision';

  function submit() {
    if (!asset || !rackId) return;
    updateMutation.mutate(
      {
        resource: 'inventory/assets',
        id: asset.id,
        values: {
          rack_id: rackId,
          rack_position_u: u,
          face,
        },
        successNotification: false,
      },
      {
        onSuccess: () => {
          const rackSuffix = targetRack ? ` to ${targetRack.code}` : '';
          const uSuffix = u != null ? ` · U${u}` : '';
          toast.success(`Moved ${asset.name}${rackSuffix}${uSuffix}`);
          onOpenChange(false);
          onMoved?.();
        },
        onError: (err: any) => toast.error(err?.message ?? 'Move failed'),
      },
    );
  }

  const siteOptions: SelectProps.Option[] = sites.map((s) => ({
    value: s.id, label: `${s.code} · ${s.name}`,
  }));
  const rackOptions: SelectProps.Option[] = racks.map((r) => ({
    value: r.id, label: `${r.code} · ${r.name} (${r.u_height}U)`,
  }));

  let validationStatusType: 'success' | 'info' | 'error';
  if (validation.kind === 'ok') validationStatusType = 'success';
  else if (validation.kind === 'unplaced') validationStatusType = 'info';
  else validationStatusType = 'error';

  return (
    <Modal
      visible={open}
      onDismiss={() => onOpenChange(false)}
      header={`Move ${asset?.name ?? 'asset'}`}
      size="medium"
    >
      <Form
        actions={
          <SpaceBetween size="xs" direction="horizontal">
            <Button variant="link" onClick={() => onOpenChange(false)}>Cancel</Button>
            <Button variant="primary" disabled={!canSubmit} loading={isPending} onClick={submit}>
              {isPending ? 'Moving…' : 'Move'}
            </Button>
          </SpaceBetween>
        }
      >
        <SpaceBetween size="m">
          <ColumnLayout columns={2}>
            <FormField label="Target site">
              <Select
                placeholder="Pick a site"
                selectedOption={siteOpt}
                onChange={({ detail }) => {
                  setSiteOpt(detail.selectedOption);
                  setRackOpt(null);
                }}
                options={siteOptions}
                expandToViewport
              />
            </FormField>
            <FormField label="Target rack">
              <Select
                placeholder="Pick a rack"
                selectedOption={rackOpt}
                onChange={({ detail }) => setRackOpt(detail.selectedOption)}
                options={rackOptions}
                disabled={!siteId}
                expandToViewport
              />
            </FormField>
          </ColumnLayout>
          <ColumnLayout columns={2}>
            <FormField label="Face">
              <SegmentedControl
                selectedId={face}
                onChange={({ detail }) => setFace(detail.selectedId as 'front' | 'rear')}
                options={[
                  { id: 'front', text: 'Front' },
                  { id: 'rear', text: 'Rear' },
                ]}
              />
            </FormField>
            <FormField
              label="Position U"
              description={
                [
                  targetRack ? `1–${targetRack.u_height}` : null,
                  units > 1 ? `${units}U device` : null,
                ].filter(Boolean).join(' · ') || undefined
              }
            >
              <Input
                type="number"
                value={positionU}
                placeholder="leave blank to unplace"
                onChange={({ detail }) => setPositionU(detail.value)}
              />
            </FormField>
          </ColumnLayout>
          {asset && (
            <Box>
              <StatusIndicator type={validationStatusType}>{validation.msg}</StatusIndicator>
            </Box>
          )}
        </SpaceBetween>
      </Form>
    </Modal>
  );
}
