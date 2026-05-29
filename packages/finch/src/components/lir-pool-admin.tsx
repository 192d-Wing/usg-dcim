// NIC pool admin — CRUD on LIR pools + manage which IPAM supernets
// feed each pool. The "Source supernets" modal is the bridge between
// IPAM (where supernets live) and LIR (which pools they back).

import { useMemo, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import Textarea from '@cloudscape-design/components/textarea';

import { http } from '@/lib/http';

type LirPool = {
  id: string;
  name: string;
  slug: string;
  description: string | null;
  ip_family: number;
  fabric_id: string | null;
  classification: string | null;
  min_prefix_length: number;
  max_prefix_length: number;
  default_supernet_purpose: string | null;
  arin_parent_net_handle: string | null;
  enabled: boolean;
  created_at: string;
};

type PoolSupernet = {
  id: string;
  fabric_id: string;
  vrf_id: string;
  prefix: string;
  name: string | null;
};

type Fabric = { id: string; name: string; slug: string };

const FAMILY_OPTS: SelectProps.Option[] = [
  { value: '4', label: 'IPv4' },
  { value: '6', label: 'IPv6' },
];

export function LirPoolAdmin({ canWrite }: { canWrite: boolean }) {
  const qc = useQueryClient();
  const poolsQ = useQuery({
    queryKey: ['lir-pools'],
    queryFn: async () => (
      await http.get<{ items: LirPool[] }>('/lir/pools?limit=500')
    ).data.items ?? [],
  });
  const pools = poolsQ.data ?? [];

  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<LirPool | null>(null);
  const [supernetsTarget, setSupernetsTarget] = useState<LirPool | null>(null);

  async function remove(p: LirPool) {
    if (!window.confirm(`Delete pool "${p.name}"? Allocations under it remain.`)) return;
    try {
      await http.delete(`/lir/pools/${p.id}`);
      toast.success('Pool deleted');
      await qc.invalidateQueries({ queryKey: ['lir-pools'] });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to delete');
    }
  }

  return (
    <>
      <Table<LirPool>
        variant="container"
        loading={poolsQ.isLoading}
        loadingText="Loading pools…"
        items={pools}
        trackBy="id"
        header={
          <Header
            counter={`(${pools.length})`}
            description="LIR pools — buckets of supernet space DoW sub-allocates from."
            actions={canWrite && (
              <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                New pool
              </Button>
            )}
          >
            Pools
          </Header>
        }
        empty={<Box color="text-status-inactive">No pools yet. Create one to start accepting requests.</Box>}
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (p) => p.name },
          { id: 'slug', header: 'Slug', cell: (p) => <Box fontSize="body-s" color="text-status-inactive">{p.slug}</Box>, width: 160 },
          {
            id: 'family', header: 'Family',
            cell: (p) => <Badge color={p.ip_family === 4 ? 'blue' : 'green'}>{`IPv${p.ip_family}`}</Badge>,
            width: 90,
          },
          {
            id: 'range', header: 'Prefix range',
            cell: (p) => `/${p.min_prefix_length}–/${p.max_prefix_length}`, width: 110,
          },
          {
            id: 'arin', header: 'ARIN parent',
            cell: (p) => p.arin_parent_net_handle
              ? <Box fontSize="body-s">{p.arin_parent_net_handle}</Box>
              : <Badge color="grey">LIR-internal</Badge>,
          },
          {
            id: 'enabled', header: 'Enabled',
            cell: (p) => p.enabled
              ? <Badge color="green">Enabled</Badge>
              : <Badge color="grey">Disabled</Badge>,
            width: 100,
          },
          {
            id: 'actions', header: '',
            cell: (p) => (
              <SpaceBetween direction="horizontal" size="xs">
                <Button onClick={() => setSupernetsTarget(p)} variant="link">
                  Source supernets…
                </Button>
                {canWrite && <Button onClick={() => setEditing(p)} variant="link">Edit</Button>}
                {canWrite && <Button onClick={() => remove(p)} variant="link">Delete</Button>}
              </SpaceBetween>
            ),
            width: 260,
          },
        ]}
      />
      <PoolFormModal
        visible={createOpen || editing !== null}
        editing={editing}
        onClose={() => { setCreateOpen(false); setEditing(null); }}
      />
      <SourceSupernetsModal
        pool={supernetsTarget}
        canWrite={canWrite}
        onClose={() => setSupernetsTarget(null)}
      />
    </>
  );
}

// ---------- create / edit form ----------

type PoolFormState = {
  name: string; slug: string; description: string;
  familyOpt: SelectProps.Option;
  fabricOpt: SelectProps.Option | null;
  classification: string;
  minPrefix: string; maxPrefix: string;
  defaultPurpose: string;
  arinHandle: string;
  enabled: boolean;
};

function emptyForm(): PoolFormState {
  return {
    name: '', slug: '', description: '',
    familyOpt: FAMILY_OPTS[0],
    fabricOpt: null,
    classification: '',
    minPrefix: '24', maxPrefix: '29',
    defaultPurpose: '',
    arinHandle: '',
    enabled: true,
  };
}

function fromPool(p: LirPool): PoolFormState {
  return {
    name: p.name,
    slug: p.slug,
    description: p.description ?? '',
    familyOpt: FAMILY_OPTS.find((o) => o.value === String(p.ip_family)) ?? FAMILY_OPTS[0],
    fabricOpt: p.fabric_id ? { value: p.fabric_id, label: 'current fabric' } : null,
    classification: p.classification ?? '',
    minPrefix: String(p.min_prefix_length),
    maxPrefix: String(p.max_prefix_length),
    defaultPurpose: p.default_supernet_purpose ?? '',
    arinHandle: p.arin_parent_net_handle ?? '',
    enabled: p.enabled,
  };
}

function PoolFormModal({ visible, editing, onClose }: {
  visible: boolean; editing: LirPool | null; onClose: () => void;
}) {
  const qc = useQueryClient();
  const [form, setForm] = useState<PoolFormState>(emptyForm());
  const [submitting, setSubmitting] = useState(false);

  // Reset form when target changes — opening create after editing
  // shouldn't carry the old values, and re-editing the same pool
  // shouldn't either if the user dismissed without saving.
  useMemo(() => {
    setForm(editing ? fromPool(editing) : emptyForm());
  }, [editing?.id, visible]);

  const fabricsQ = useQuery({
    queryKey: ['ipam-fabrics-for-pool'],
    enabled: visible,
    queryFn: async () => (
      await http.get<{ items: Fabric[] }>('/ipam/fabrics?page_size=200')
    ).data.items ?? [],
  });
  const fabricOpts: SelectProps.Option[] = [
    { value: '', label: '(none)' },
    ...(fabricsQ.data ?? []).map((f) => ({ value: f.id, label: f.name, description: f.slug })),
  ];
  // If the editing pool had a fabric_id, surface its current name in
  // the placeholder so the operator sees what they'd be changing.
  const currentFabricLabel = editing?.fabric_id
    ? (fabricsQ.data ?? []).find((f) => f.id === editing.fabric_id)?.name ?? 'current fabric'
    : '(none)';

  async function submit() {
    const errs = validate(form);
    if (errs.length) {
      toast.error(errs.join(' '));
      return;
    }
    setSubmitting(true);
    try {
      if (editing) {
        // PATCH only the fields that differ — backend treats absent
        // fields as no-op, present fields as overrides.
        const patch: Record<string, unknown> = {};
        if (form.name !== editing.name) patch.name = form.name;
        if (form.slug !== editing.slug) patch.slug = form.slug;
        if ((form.description || null) !== editing.description) patch.description = form.description || null;
        if ((form.fabricOpt?.value || '') !== (editing.fabric_id ?? '')) {
          patch.fabric_id = form.fabricOpt?.value || null;
        }
        if ((form.classification || null) !== editing.classification) patch.classification = form.classification || null;
        if (Number(form.minPrefix) !== editing.min_prefix_length) patch.min_prefix_length = Number(form.minPrefix);
        if (Number(form.maxPrefix) !== editing.max_prefix_length) patch.max_prefix_length = Number(form.maxPrefix);
        if ((form.defaultPurpose || null) !== editing.default_supernet_purpose) patch.default_supernet_purpose = form.defaultPurpose || null;
        if ((form.arinHandle || null) !== editing.arin_parent_net_handle) patch.arin_parent_net_handle = form.arinHandle || null;
        if (form.enabled !== editing.enabled) patch.enabled = form.enabled;
        await http.patch(`/lir/pools/${editing.id}`, patch);
        toast.success('Pool updated');
      } else {
        const body: Record<string, unknown> = {
          name: form.name,
          slug: form.slug,
          description: form.description || null,
          ip_family: Number(form.familyOpt.value),
          fabric_id: form.fabricOpt?.value || null,
          classification: form.classification || null,
          min_prefix_length: Number(form.minPrefix),
          max_prefix_length: Number(form.maxPrefix),
          default_supernet_purpose: form.defaultPurpose || null,
          arin_parent_net_handle: form.arinHandle || null,
          enabled: form.enabled,
        };
        await http.post('/lir/pools', body);
        toast.success('Pool created');
      }
      await qc.invalidateQueries({ queryKey: ['lir-pools'] });
      onClose();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to save');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal
      visible={visible}
      onDismiss={onClose}
      header={editing ? `Edit pool: ${editing.name}` : 'New pool'}
      size="medium"
      footer={
        <Box float="right">
          <SpaceBetween direction="horizontal" size="xs">
            <Button onClick={onClose} variant="link">Cancel</Button>
            <Button variant="primary" onClick={submit} loading={submitting}>
              {editing ? 'Save changes' : 'Create pool'}
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <Form>
        <SpaceBetween size="m">
          <FormField label="Name">
            <Input value={form.name} onChange={(e) => setForm({ ...form, name: e.detail.value })} />
          </FormField>
          <FormField label="Slug" description="URL-safe identifier. Lowercase, hyphens.">
            <Input value={form.slug} onChange={(e) => setForm({ ...form, slug: e.detail.value })} />
          </FormField>
          <FormField label="Description (optional)">
            <Textarea value={form.description} onChange={(e) => setForm({ ...form, description: e.detail.value })} rows={2} />
          </FormField>
          <FormField label="IP family" description={editing ? 'Immutable on existing pools.' : undefined}>
            <Select
              selectedOption={form.familyOpt}
              options={FAMILY_OPTS}
              disabled={editing !== null}
              onChange={(e) => setForm({ ...form, familyOpt: e.detail.selectedOption })}
            />
          </FormField>
          <FormField label="Min prefix length" description="Widest prefix the pool will issue.">
            <Input
              type="number"
              value={form.minPrefix}
              onChange={(e) => setForm({ ...form, minPrefix: e.detail.value })}
            />
          </FormField>
          <FormField label="Max prefix length" description="Narrowest prefix the pool will issue.">
            <Input
              type="number"
              value={form.maxPrefix}
              onChange={(e) => setForm({ ...form, maxPrefix: e.detail.value })}
            />
          </FormField>
          <FormField label="Operational fabric (optional)" description="Where the pool source supernets live. Informational.">
            <Select
              selectedOption={form.fabricOpt}
              options={fabricOpts}
              placeholder={fabricsQ.isLoading ? 'Loading fabrics…' : currentFabricLabel}
              onChange={(e) => setForm({ ...form, fabricOpt: e.detail.selectedOption.value ? e.detail.selectedOption : null })}
            />
          </FormField>
          <FormField label="Classification (optional)" description="e.g. NIPR, SIPR.">
            <Input value={form.classification} onChange={(e) => setForm({ ...form, classification: e.detail.value })} />
          </FormField>
          <FormField label="Default supernet purpose (optional)" description="Stamped on carved tenant supernets when the request doesn’t set one.">
            <Input value={form.defaultPurpose} onChange={(e) => setForm({ ...form, defaultPurpose: e.detail.value })} />
          </FormField>
          <FormField
            label="ARIN parent net handle (optional)"
            description="Set to enable Reg-RWS feed-up. Leave blank for LIR-internal pools."
          >
            <Input
              value={form.arinHandle}
              onChange={(e) => setForm({ ...form, arinHandle: e.detail.value })}
              placeholder="NET-198-51-100-0-1"
            />
          </FormField>
          <FormField label="">
            <Box>
              <input
                type="checkbox"
                id="lir-pool-enabled"
                checked={form.enabled}
                onChange={(e) => setForm({ ...form, enabled: e.target.checked })}
              />{' '}
              <label htmlFor="lir-pool-enabled">Enabled</label>
            </Box>
          </FormField>
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

function validate(f: PoolFormState): string[] {
  const out: string[] = [];
  if (!f.name.trim()) out.push('Name is required.');
  if (!f.slug.trim()) out.push('Slug is required.');
  const lo = Number(f.minPrefix);
  const hi = Number(f.maxPrefix);
  if (!Number.isFinite(lo) || lo < 0) out.push('Min prefix length must be a non-negative integer.');
  if (!Number.isFinite(hi) || hi < 0) out.push('Max prefix length must be a non-negative integer.');
  if (lo > hi) out.push('Min prefix length must be ≤ max prefix length.');
  const cap = f.familyOpt.value === '4' ? 32 : 128;
  if (hi > cap) out.push(`Max prefix length exceeds /${cap} for IPv${f.familyOpt.value}.`);
  return out;
}

// ---------- source supernets modal ----------

function SourceSupernetsModal({ pool, canWrite, onClose }: {
  pool: LirPool | null; canWrite: boolean; onClose: () => void;
}) {
  const qc = useQueryClient();
  const [attachInput, setAttachInput] = useState('');
  const [attaching, setAttaching] = useState(false);

  useMemo(() => { setAttachInput(''); }, [pool?.id]);

  const supernetsQ = useQuery({
    queryKey: ['lir-pool-supernets', pool?.id],
    enabled: pool !== null,
    queryFn: async () => (
      await http.get<{ items: PoolSupernet[] }>(
        `/lir/pools/${pool!.id}/supernets?limit=200`,
      )
    ).data.items ?? [],
  });
  const items = supernetsQ.data ?? [];

  async function attach() {
    if (!pool) return;
    const sid = attachInput.trim();
    if (!sid) {
      toast.error('Paste a supernet UUID.');
      return;
    }
    setAttaching(true);
    try {
      await http.post(`/lir/pools/${pool.id}/supernets`, { supernet_id: sid });
      toast.success('Supernet attached');
      setAttachInput('');
      await qc.invalidateQueries({ queryKey: ['lir-pool-supernets', pool.id] });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to attach');
    } finally {
      setAttaching(false);
    }
  }

  async function detach(s: PoolSupernet) {
    if (!pool) return;
    if (!window.confirm(`Detach ${s.prefix} from pool "${pool.name}"?`)) return;
    try {
      await http.delete(`/lir/pools/${pool.id}/supernets/${s.id}`);
      toast.success('Supernet detached');
      await qc.invalidateQueries({ queryKey: ['lir-pool-supernets', pool.id] });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to detach');
    }
  }

  return (
    <Modal
      visible={pool !== null}
      onDismiss={onClose}
      header={`Source supernets: ${pool?.name ?? ''}`}
      size="large"
      footer={
        <Box float="right">
          <Button onClick={onClose}>Close</Button>
        </Box>
      }
    >
      <SpaceBetween size="m">
        <Table<PoolSupernet>
          variant="embedded"
          loading={supernetsQ.isLoading}
          loadingText="Loading source supernets…"
          items={items}
          trackBy="id"
          empty={<Box color="text-status-inactive">No source supernets attached. The carver has nothing to work with.</Box>}
          columnDefinitions={[
            { id: 'prefix', header: 'Prefix', cell: (s) => <Box fontWeight="bold">{s.prefix}</Box>, width: 200 },
            { id: 'name', header: 'Name', cell: (s) => s.name ?? <Box color="text-status-inactive">—</Box> },
            {
              id: 'actions', header: '',
              cell: (s) => canWrite
                ? <Button onClick={() => detach(s)} variant="link">Detach</Button>
                : null,
              width: 100,
            },
          ]}
        />
        {canWrite && (
          <FormField
            label="Attach supernet by UUID"
            description="Pick a supernet from IPAM that matches this pool’s family. Already-pooled, tenant-owned, and family-mismatch supernets are rejected."
          >
            <SpaceBetween direction="horizontal" size="xs">
              <Input
                value={attachInput}
                onChange={(e) => setAttachInput(e.detail.value)}
                placeholder="00000000-0000-0000-0000-000000000000"
              />
              <Button onClick={attach} loading={attaching} variant="primary">Attach</Button>
            </SpaceBetween>
          </FormField>
        )}
      </SpaceBetween>
    </Modal>
  );
}
