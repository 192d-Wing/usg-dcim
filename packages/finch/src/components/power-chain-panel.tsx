import { useMemo, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

import Alert from '@cloudscape-design/components/alert';
import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Modal from '@cloudscape-design/components/modal';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';

import {
  colorBackgroundInputDisabled, colorTextStatusInfo,
} from '@cloudscape-design/design-tokens';

import { http } from '@/lib/http';
import { hasCapability } from '@/lib/access-control-provider';

export type PowerChainAsset = {
  id: string;
  name: string;
  kind: string;
  pdu_side?: 'A' | 'B' | 'C' | null;
  psu_count?: number | null;
  redundancy?: 'redundant' | 'single' | 'unpowered' | 'n/a';
};

export type PduSummary = {
  id: string;
  name: string;
  side: 'A' | 'B' | 'C' | null;
  mount: string;
  face: string;
  total_outlets: number;
  used_outlets: number;
};

export type PerAsset = {
  sides_covered: string[];
  connections: {
    pdu_id: string; pdu_name: string; pdu_side: string | null;
    outlet_id: string; outlet_position: number; outlet_label: string | null;
    psu_index: number;
  }[];
  redundancy: 'redundant' | 'single' | 'unpowered' | 'n/a';
};

type Props = Readonly<{
  rackId: string;
  pdus: PduSummary[];
  perAsset: Record<string, PerAsset>;
  assets: PowerChainAsset[];
}>;

const MONO = { fontFamily: 'ui-monospace, monospace' } as const;

function mountLabel(mount: string): string {
  if (mount === 'rack') return '1U mount';
  if (mount === 'vertical-left') return '0U vertical (left)';
  if (mount === 'vertical-right') return '0U vertical (right)';
  return mount;
}

function sideBadgeColor(side: string | null | undefined): 'blue' | 'red' | 'grey' {
  if (side === 'A') return 'blue';
  if (side === 'B') return 'red';
  return 'grey';
}

function redundancyStatus(r: PerAsset['redundancy']) {
  if (r === 'redundant') return { type: 'success' as const, label: 'redundant' };
  if (r === 'single') return { type: 'warning' as const, label: 'single' };
  if (r === 'unpowered') return { type: 'error' as const, label: 'unpowered' };
  return { type: 'info' as const, label: 'n/a' };
}

export function PowerChainPanel({ rackId, pdus, perAsset, assets }: Props) {
  const canWrite = hasCapability('power:outlets:create');
  const nonPdus = assets.filter((a) => a.kind !== 'pdu');

  const counts = useMemo(() => {
    const c = { redundant: 0, single: 0, unpowered: 0, total: nonPdus.length };
    for (const a of nonPdus) {
      const r = perAsset[a.id]?.redundancy;
      if (r === 'redundant') c.redundant++;
      else if (r === 'single') c.single++;
      else c.unpowered++;
    }
    return c;
  }, [nonPdus, perAsset]);

  return (
    <Container
      header={
        <Header
          variant="h2"
          actions={
            <SpaceBetween size="xs" direction="horizontal">
              <StatusIndicator type="success">{counts.redundant} Redundant</StatusIndicator>
              <StatusIndicator type="warning">{counts.single} Single</StatusIndicator>
              <StatusIndicator type="error">{counts.unpowered} Unpowered</StatusIndicator>
            </SpaceBetween>
          }
        >
          Power chain
        </Header>
      }
    >
      <SpaceBetween size="m">
        <PduStrip pdus={pdus} />

        {nonPdus.length === 0 ? (
          <Box color="text-status-inactive">No powered devices in this rack yet.</Box>
        ) : (
          <Table<PowerChainAsset>
            variant="embedded"
            items={nonPdus}
            trackBy="id"
            columnDefinitions={[
              {
                id: 'device', header: 'Device',
                cell: (a) => (
                  <>
                    <Box fontWeight="bold">{a.name}</Box>
                    <Box color="text-status-inactive" fontSize="body-s">
                      {a.kind}{a.psu_count ? ` · ${a.psu_count} PSU` : ''}
                    </Box>
                  </>
                ),
              },
              {
                id: 'redundancy', header: 'Redundancy',
                cell: (a) => {
                  const chain = perAsset[a.id];
                  const r = chain?.redundancy ?? 'unpowered';
                  const s = redundancyStatus(r);
                  return <StatusIndicator type={s.type}>{s.label}</StatusIndicator>;
                },
                width: 140,
              },
              {
                id: 'feeds', header: 'Power feeds',
                cell: (a) => {
                  const chain = perAsset[a.id] ?? { sides_covered: [], connections: [], redundancy: 'unpowered' as const };
                  return <FeedsCell rackId={rackId} chain={chain} canWrite={canWrite} />;
                },
              },
              ...(canWrite ? [{
                id: 'connect', header: '',
                cell: (a: PowerChainAsset) => (
                  <ConnectButton rackId={rackId} asset={a} pdus={pdus} chain={perAsset[a.id]} />
                ),
                width: 110,
              }] : []),
            ]}
            empty={<Box color="text-status-inactive">No powered devices.</Box>}
          />
        )}
      </SpaceBetween>
    </Container>
  );
}

function PduStrip({ pdus }: Readonly<{ pdus: PduSummary[] }>) {
  if (pdus.length === 0) {
    return <Box color="text-status-inactive" fontSize="body-s">No PDUs in this rack.</Box>;
  }
  return (
    <ColumnLayout columns={Math.min(4, pdus.length) as 1 | 2 | 3 | 4}>
      {pdus.map((p) => {
        const pct = p.total_outlets ? (p.used_outlets / p.total_outlets) * 100 : 0;
        return (
          <Container key={p.id} disableContentPaddings={false}>
            <SpaceBetween size="xxs">
              <SpaceBetween size="xs" direction="horizontal">
                <span style={{ ...MONO, fontWeight: 600 }}>{p.name}</span>
                {p.side && <Badge color={sideBadgeColor(p.side)}>Side {p.side}</Badge>}
              </SpaceBetween>
              <Box color="text-status-inactive" fontSize="body-s">
                {mountLabel(p.mount)} · {p.face}
              </Box>
              <Box fontSize="body-s">
                {p.used_outlets} / {p.total_outlets} outlets used
              </Box>
              <div style={{
                height: 4, overflow: 'hidden', borderRadius: 999,
                background: colorBackgroundInputDisabled,
              }}>
                <div style={{
                  height: '100%',
                  width: `${pct}%`,
                  background: colorTextStatusInfo,
                }} />
              </div>
            </SpaceBetween>
          </Container>
        );
      })}
    </ColumnLayout>
  );
}

function FeedsCell({
  rackId, chain, canWrite,
}: Readonly<{
  rackId: string;
  chain: PerAsset;
  canWrite: boolean;
}>) {
  const qc = useQueryClient();
  async function disconnect(outletId: string) {
    try {
      await http.delete(`/power/outlets/${outletId}/connect`);
      toast.success('Disconnected');
      await qc.invalidateQueries({ queryKey: ['rack-detail', rackId] });
    } catch (err: any) {
      toast.error(err?.message ?? 'Failed to disconnect');
    }
  }
  if (chain.connections.length === 0) {
    return <Box color="text-status-inactive" fontSize="body-s">No connections</Box>;
  }
  return (
    <SpaceBetween size="xxs" direction="horizontal">
      {[...chain.connections]
        .sort((a, b) => a.psu_index - b.psu_index)
        .map((c) => (
          <SpaceBetween key={c.outlet_id} size="xxs" direction="horizontal">
            <Badge color={sideBadgeColor(c.pdu_side)}>
              PSU{c.psu_index} → {c.pdu_name} · U{String(c.outlet_label ?? c.outlet_position).padStart(2, '0')}
            </Badge>
            {canWrite && (
              <Button
                iconName="remove"
                variant="inline-icon"
                onClick={() => disconnect(c.outlet_id)}
                ariaLabel="Disconnect"
              />
            )}
          </SpaceBetween>
        ))}
    </SpaceBetween>
  );
}

function ConnectButton({
  rackId, asset, pdus, chain,
}: Readonly<{
  rackId: string;
  asset: PowerChainAsset;
  pdus: PduSummary[];
  chain: PerAsset | undefined;
}>) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const existing = chain?.connections.map((c) => c.psu_index) ?? [];
  return (
    <>
      <Button iconName="add-plus" onClick={() => setOpen(true)}>Connect</Button>
      <Modal
        visible={open}
        onDismiss={() => setOpen(false)}
        header={`Connect ${asset.name} to a PDU outlet`}
        size="medium"
      >
        <ConnectForm
          asset={asset}
          pdus={pdus}
          existingPsuIndices={existing}
          onDone={() => {
            setOpen(false);
            qc.invalidateQueries({ queryKey: ['rack-detail', rackId] });
          }}
        />
      </Modal>
    </>
  );
}

function ConnectForm({
  asset, pdus, existingPsuIndices, onDone,
}: Readonly<{
  asset: PowerChainAsset;
  pdus: PduSummary[];
  existingPsuIndices: number[];
  onDone: () => void;
}>) {
  const psuCount = asset.psu_count ?? 2;
  const nextPsu = Array.from({ length: psuCount }, (_, i) => i + 1)
    .find((i) => !existingPsuIndices.includes(i)) ?? 1;

  const [psuIndex, setPsuIndex] = useState<number>(nextPsu);
  const [pduOpt, setPduOpt] = useState<SelectProps.Option | null>(
    pdus[0] ? { value: pdus[0].id, label: `${pdus[0].name}${pdus[0].side ? ` (side ${pdus[0].side})` : ''}` } : null,
  );
  const [outletOpt, setOutletOpt] = useState<SelectProps.Option | null>(null);
  const [busy, setBusy] = useState(false);

  const pduId = pduOpt?.value ?? '';
  const outletId = outletOpt?.value ?? '';

  const outletsRes = useQuery({
    queryKey: ['outlets', pduId],
    queryFn: async () => {
      if (!pduId) return [];
      const r = await http.get<any[]>(`/power/pdus/${pduId}/outlets`);
      return r.data;
    },
    enabled: !!pduId,
  });

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!outletId) {
      toast.error('Pick an outlet');
      return;
    }
    setBusy(true);
    try {
      await http.post(`/power/outlets/${outletId}/connect`, {
        asset_id: asset.id,
        psu_index: psuIndex,
      });
      toast.success(`Connected PSU${psuIndex}`);
      onDone();
    } catch (err: any) {
      toast.error(err?.message ?? 'Failed to connect');
    } finally {
      setBusy(false);
    }
  }

  const psuOptions: SelectProps.Option[] = Array.from({ length: psuCount }, (_, i) => i + 1).map((i) => ({
    value: String(i),
    label: `PSU${i}${existingPsuIndices.includes(i) ? ' (already connected)' : ''}`,
    disabled: existingPsuIndices.includes(i),
  }));
  const pduOptions: SelectProps.Option[] = pdus.map((p) => ({
    value: p.id,
    label: `${p.name}${p.side ? ` (side ${p.side})` : ''}`,
  }));
  const availableOutlets = (outletsRes.data ?? []).filter((o: any) => !o.connected);
  const outletOptions: SelectProps.Option[] = availableOutlets.map((o: any) => ({
    value: o.id,
    label: `Outlet ${String(o.label ?? o.position).padStart(2, '0')}${o.phase ? ` · phase ${o.phase}` : ''}${o.receptacle ? ` · ${o.receptacle}` : ''}`,
  }));

  return (
    <form onSubmit={submit}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={busy} disabled={!outletId}>
            {busy ? 'Connecting…' : 'Connect'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField label="PSU">
            <Select
              selectedOption={psuOptions.find((o) => o.value === String(psuIndex)) ?? null}
              onChange={({ detail }) => {
                if (detail.selectedOption.value) setPsuIndex(Number(detail.selectedOption.value));
              }}
              options={psuOptions}
              expandToViewport
            />
          </FormField>
          <FormField label="PDU">
            <Select
              placeholder="Pick a PDU"
              selectedOption={pduOpt}
              onChange={({ detail }) => {
                setPduOpt(detail.selectedOption);
                setOutletOpt(null);
              }}
              options={pduOptions}
              expandToViewport
            />
          </FormField>
          <FormField label="Outlet">
            <Select
              placeholder={outletsRes.isLoading ? 'Loading…' : 'Pick an outlet'}
              selectedOption={outletOpt}
              onChange={({ detail }) => setOutletOpt(detail.selectedOption)}
              options={outletOptions}
              disabled={!pduId || outletsRes.isLoading}
              empty={availableOutlets.length === 0 ? 'All outlets in use on this PDU.' : undefined}
              expandToViewport
            />
          </FormField>
          {asset.psu_count && asset.psu_count > 1 && pdus.length >= 2 && (
            <Alert type="warning">
              For redundancy, connect each PSU to a PDU on a different side (A vs B).
            </Alert>
          )}
        </SpaceBetween>
      </Form>
    </form>
  );
}
