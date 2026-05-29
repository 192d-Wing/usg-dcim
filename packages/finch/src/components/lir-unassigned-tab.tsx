// LIR "Unassigned" tab for the IPAM page — surfaces the user's
// active LIR allocations and offers the Move CTA that calls
// POST /api/v1/ipam/supernets/{id}/move to relocate the tenant
// Supernet from the LIR landing fabric to its operational
// fabric/VRF. The backend enforces the landing-fabric guard so a
// stale click on an already-moved row gets a clean 409 — the
// tab refetches on success and the row drops out.

import { useEffect, useState } from 'react';
import { useGetIdentity } from '@refinedev/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Modal from '@cloudscape-design/components/modal';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';

import { hasCap } from '@/lib/caps';
import { http } from '@/lib/http';

type LirAllocation = {
  id: string;
  prefix: string;
  tenant_supernet_id: string;
  organization_id: string;
  allocated_at: string;
  status: 'active' | 'return_requested' | 'returned';
  arin_status: string;
};

type Fabric = {
  id: string;
  name: string;
  slug: string;
  is_system: boolean;
};

type Vrf = {
  id: string;
  fabric_id: string;
  name: string;
  is_default: boolean;
};

export function LirUnassignedTab() {
  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const caps = identity?.capabilities ?? [];
  const canMove = hasCap(caps, 'ipam:supernets:update');
  const canReadAlloc = hasCap(caps, 'lir:allocations:read');

  if (!canReadAlloc) {
    return (
      <Box color="text-status-inactive" padding="m">
        You need lir:allocations:read to see LIR allocations.
      </Box>
    );
  }

  return <UnassignedBody canMove={canMove} />;
}

function UnassignedBody({ canMove }: { canMove: boolean }) {
  const allocsQ = useQuery({
    queryKey: ['lir-allocations', 'active'],
    queryFn: async () => (
      await http.get<{ items: LirAllocation[] }>(
        '/lir/allocations?status=active&limit=200',
      )
    ).data.items ?? [],
  });
  const items = allocsQ.data ?? [];

  const [moveTarget, setMoveTarget] = useState<LirAllocation | null>(null);

  return (
    <>
      <Table<LirAllocation>
        variant="container"
        loading={allocsQ.isLoading}
        loadingText="Loading allocations…"
        items={items}
        trackBy="id"
        header={
          <Header
            counter={`(${items.length})`}
            description="Active LIR allocations. Move each to its operational fabric/VRF when you're ready to start using it."
          >
            Unassigned (LIR allocations awaiting placement)
          </Header>
        }
        empty={<Box color="text-status-inactive">No active LIR allocations.</Box>}
        columnDefinitions={[
          {
            id: 'prefix', header: 'Prefix',
            cell: (a) => <Box fontWeight="bold">{a.prefix}</Box>,
            width: 220,
          },
          {
            id: 'allocated', header: 'Allocated',
            cell: (a) => formatDateShort(a.allocated_at),
            width: 160,
          },
          {
            id: 'arin', header: 'ARIN',
            cell: (a) => <ArinBadge status={a.arin_status} />,
            width: 140,
          },
          {
            id: 'actions', header: '',
            cell: (a) => canMove
              ? <Button onClick={() => setMoveTarget(a)} variant="link">
                  Move…
                </Button>
              : null,
            width: 100,
          },
        ]}
      />
      <MoveModal
        target={moveTarget}
        onClose={() => setMoveTarget(null)}
      />
    </>
  );
}

function ArinBadge({ status }: { status: string }) {
  switch (status) {
    case 'registered':
      return <Badge color="green">Registered</Badge>;
    case 'pending':
      return <Badge color="blue">Pending</Badge>;
    case 'failed':
      return <Badge color="red">Failed</Badge>;
    case 'none':
      return <Badge color="grey">None</Badge>;
    default:
      return <Badge color="grey">{status}</Badge>;
  }
}

// ---------- Move modal ----------

function MoveModal({
  target, onClose,
}: { target: LirAllocation | null; onClose: () => void }) {
  const qc = useQueryClient();
  const [fabricOpt, setFabricOpt] = useState<SelectProps.Option | null>(null);
  const [vrfOpt, setVrfOpt] = useState<SelectProps.Option | null>(null);
  const [moving, setMoving] = useState(false);

  // Reset selections when target changes — opening the modal twice
  // shouldn't carry stale picks. useEffect (not useMemo) because
  // this is a side effect, not a derived value; useMemo runs during
  // render and calling setState from a memo factory triggers
  // React's "cannot update a component while rendering a different
  // component" warning under StrictMode + concurrent rendering.
  useEffect(() => {
    setFabricOpt(null);
    setVrfOpt(null);
  }, [target?.id]);

  const fabricsQ = useQuery({
    queryKey: ['ipam-fabrics-for-move'],
    enabled: target !== null,
    queryFn: async () => (
      await http.get<{ items: Fabric[] }>('/ipam/fabrics?page_size=200')
    ).data.items ?? [],
  });
  // Exclude the system landing fabric — the user can't move into it,
  // and listing it would be confusing.
  const targetFabrics = (fabricsQ.data ?? []).filter((f) => !f.is_system);
  const fabricOpts: SelectProps.Option[] = targetFabrics.map((f) => ({
    value: f.id, label: f.name,
    description: f.slug,
  }));

  const fabricId = fabricOpt?.value ?? '';
  const vrfsQ = useQuery({
    queryKey: ['ipam-vrfs-for-move', fabricId],
    enabled: target !== null && !!fabricId,
    queryFn: async () => (
      await http.get<{ items: Vrf[] }>(
        `/ipam/vrfs?fabric_id=${encodeURIComponent(fabricId)}&page_size=200`,
      )
    ).data.items ?? [],
  });
  const vrfs = vrfsQ.data ?? [];
  // Default VRF first; sort the rest alphabetically.
  const sortedVrfs = [...vrfs].sort((a, b) => {
    if (a.is_default !== b.is_default) return a.is_default ? -1 : 1;
    return a.name.localeCompare(b.name);
  });
  const vrfOpts: SelectProps.Option[] = sortedVrfs.map((v) => ({
    value: v.id,
    label: v.name + (v.is_default ? ' (default)' : ''),
  }));

  async function doMove() {
    if (!target) return;
    if (!fabricOpt?.value || !vrfOpt?.value) {
      toast.error('Pick a target fabric and VRF.');
      return;
    }
    setMoving(true);
    try {
      await http.post(
        `/ipam/supernets/${target.tenant_supernet_id}/move`,
        { fabric_id: fabricOpt.value, vrf_id: vrfOpt.value },
      );
      toast.success(`Moved ${target.prefix} to ${fabricOpt.label}`);
      await qc.invalidateQueries({ queryKey: ['lir-allocations'] });
      onClose();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to move');
    } finally {
      setMoving(false);
    }
  }

  return (
    <Modal
      visible={target !== null}
      onDismiss={onClose}
      header="Move allocation to operational fabric"
      footer={
        <Box float="right">
          <SpaceBetween direction="horizontal" size="xs">
            <Button onClick={onClose} variant="link">Cancel</Button>
            <Button variant="primary" onClick={doMove} loading={moving}>
              Move
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <Form>
        <SpaceBetween size="m">
          <Box>
            Relocate <b>{target?.prefix}</b> from the LIR landing fabric to a
            fabric you'll operate it in.
          </Box>
          <FormField
            label="Target fabric"
            description="System-managed fabrics (like the landing fabric) are excluded."
          >
            <Select
              selectedOption={fabricOpt}
              options={fabricOpts}
              placeholder={fabricsQ.isLoading ? 'Loading fabrics…' : 'Select fabric'}
              onChange={(e) => {
                setFabricOpt(e.detail.selectedOption);
                setVrfOpt(null);
              }}
              filteringType="auto"
            />
          </FormField>
          <FormField
            label="Target VRF"
            description="VRFs inside the selected fabric. The fabric's default VRF is listed first."
          >
            <Select
              selectedOption={vrfOpt}
              options={vrfOpts}
              disabled={!fabricId}
              placeholder={
                !fabricId ? 'Pick a fabric first' :
                vrfsQ.isLoading ? 'Loading VRFs…' : 'Select VRF'
              }
              onChange={(e) => setVrfOpt(e.detail.selectedOption)}
            />
          </FormField>
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

// ---------- helper ----------

function formatDateShort(iso: string): string {
  try {
    const d = new Date(iso);
    return d.toLocaleString(undefined, {
      year: 'numeric', month: 'short', day: '2-digit',
      hour: '2-digit', minute: '2-digit',
    });
  } catch {
    return iso;
  }
}
