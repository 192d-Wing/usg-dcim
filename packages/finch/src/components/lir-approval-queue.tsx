// NIC approval queue — lists pending LIR requests and offers
// approve / reject modals. The approve modal lets the operator
// override the tenant's pool preference; backend rejects 422 if the
// chosen pool family doesn't match the request family or if the
// requested prefix length is outside the pool's bounds, so the
// dropdown filters to eligible pools client-side too as a UX
// shortcut.

import { useEffect, useMemo, useState } from 'react';
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
import Textarea from '@cloudscape-design/components/textarea';

import { http } from '@/lib/http';

type LirRequest = {
  id: string;
  organization_id: string;
  requester_user_id: string;
  pool_id: string | null;
  site_id: string | null;
  ip_family: number;
  prefix_length: number;
  purpose: string | null;
  justification: string;
  status: string;
  submitted_at: string;
};

type LirPool = {
  id: string;
  name: string;
  slug: string;
  ip_family: number;
  min_prefix_length: number;
  max_prefix_length: number;
  enabled: boolean;
};

type Organization = { id: string; name: string };

export function LirApprovalQueue({ canApprove, canReject }: {
  canApprove: boolean;
  canReject: boolean;
}) {
  const reqsQ = useQuery({
    queryKey: ['lir-requests', 'pending'],
    queryFn: async () => (
      await http.get<{ items: LirRequest[] }>(
        '/lir/requests?status=pending_approval&limit=200',
      )
    ).data.items ?? [],
  });
  const orgsQ = useQuery({
    queryKey: ['lir-orgs'],
    queryFn: async () => (
      await http.get<{ items: Organization[] }>('/organizations?page_size=500')
    ).data.items ?? [],
  });
  const orgsById = useMemo(() => {
    const m = new Map<string, Organization>();
    (orgsQ.data ?? []).forEach((o) => m.set(o.id, o));
    return m;
  }, [orgsQ.data]);

  const [approveTarget, setApproveTarget] = useState<LirRequest | null>(null);
  const [rejectTarget, setRejectTarget] = useState<LirRequest | null>(null);

  const items = reqsQ.data ?? [];

  return (
    <>
      <Table<LirRequest>
        variant="container"
        loading={reqsQ.isLoading}
        loadingText="Loading pending requests…"
        items={items}
        trackBy="id"
        header={
          <Header
            counter={`(${items.length})`}
            description="LIR requests awaiting NIC decision. Approve carves a tenant Supernet; reject is terminal."
          >
            Approval queue
          </Header>
        }
        empty={<Box color="text-status-inactive">No pending requests.</Box>}
        columnDefinitions={[
          {
            id: 'submitted', header: 'Submitted',
            cell: (r) => formatDateShort(r.submitted_at), width: 160,
          },
          {
            id: 'org', header: 'Organization',
            cell: (r) => orgsById.get(r.organization_id)?.name ?? r.organization_id.slice(0, 8) + '…',
          },
          {
            id: 'family', header: 'Family',
            cell: (r) => `IPv${r.ip_family}`, width: 80,
          },
          {
            id: 'size', header: 'Size',
            cell: (r) => <Badge>{`/${r.prefix_length}`}</Badge>, width: 80,
          },
          {
            id: 'purpose', header: 'Purpose',
            cell: (r) => r.purpose ?? <Box color="text-status-inactive">—</Box>,
            width: 100,
          },
          {
            id: 'justification', header: 'Justification',
            cell: (r) => <Box fontSize="body-s">{r.justification}</Box>,
          },
          {
            id: 'actions', header: '',
            cell: (r) => (
              <SpaceBetween direction="horizontal" size="xs">
                {canApprove && (
                  <Button onClick={() => setApproveTarget(r)} variant="primary">
                    Approve
                  </Button>
                )}
                {canReject && (
                  <Button onClick={() => setRejectTarget(r)}>
                    Reject
                  </Button>
                )}
              </SpaceBetween>
            ),
            width: 180,
          },
        ]}
      />
      <ApproveModal target={approveTarget} onClose={() => setApproveTarget(null)} />
      <RejectModal target={rejectTarget} onClose={() => setRejectTarget(null)} />
    </>
  );
}

// ---------- approve ----------

function ApproveModal({ target, onClose }: {
  target: LirRequest | null; onClose: () => void;
}) {
  const qc = useQueryClient();
  const [poolOpt, setPoolOpt] = useState<SelectProps.Option | null>(null);
  const [notes, setNotes] = useState('');
  const [submitting, setSubmitting] = useState(false);

  // Reset state when target changes — opening a different row
  // shouldn't carry the previous selection. useEffect (not
  // useMemo) because this is a side effect, not a derived value;
  // useMemo runs during render and calling setState from a memo
  // factory triggers React's 'cannot update a component while
  // rendering a different component' warning under StrictMode +
  // concurrent rendering.
  useEffect(() => {
    setPoolOpt(null);
    setNotes('');
  }, [target?.id]);

  const poolsQ = useQuery({
    queryKey: ['lir-pools'],
    enabled: target !== null,
    queryFn: async () => (
      await http.get<{ items: LirPool[] }>('/lir/pools?limit=500')
    ).data.items ?? [],
  });

  const family = target?.ip_family ?? 4;
  const prefixLen = target?.prefix_length ?? 0;
  // Client-side eligibility filter: pool must be enabled, family-match,
  // and accept the requested prefix length. Backend re-validates.
  const eligible = (poolsQ.data ?? []).filter((p) =>
    p.enabled
    && p.ip_family === family
    && prefixLen >= p.min_prefix_length
    && prefixLen <= p.max_prefix_length,
  );
  const poolOpts: SelectProps.Option[] = [
    { value: '', label: 'Use tenant preference', description: target?.pool_id ? `Pool ${target.pool_id.slice(0, 8)}…` : 'No tenant preference' },
    ...eligible.map((p) => ({
      value: p.id,
      label: p.name,
      description: `/${p.min_prefix_length}–/${p.max_prefix_length}`,
    })),
  ];

  async function doApprove() {
    if (!target) return;
    setSubmitting(true);
    try {
      const body: Record<string, unknown> = {};
      if (poolOpt?.value) body.approved_pool_id = poolOpt.value;
      if (notes.trim()) body.notes = notes.trim();
      await http.post(`/lir/requests/${target.id}/approve`, body);
      toast.success('Request approved');
      await qc.invalidateQueries({ queryKey: ['lir-requests'] });
      await qc.invalidateQueries({ queryKey: ['lir-allocations'] });
      onClose();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to approve');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal
      visible={target !== null}
      onDismiss={onClose}
      header="Approve request"
      footer={
        <Box float="right">
          <SpaceBetween direction="horizontal" size="xs">
            <Button onClick={onClose} variant="link">Cancel</Button>
            <Button variant="primary" onClick={doApprove} loading={submitting}>
              Approve
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <Form>
        <SpaceBetween size="m">
          <Box>
            Carve an IPv{target?.ip_family} <b>/{target?.prefix_length}</b> for
            this tenant. The allocation will land in the LIR landing fabric;
            the tenant moves it from there.
          </Box>
          <FormField
            label="Pool override (optional)"
            description={
              target?.pool_id
                ? 'Empty leaves the tenant’s pool preference. Picking a different pool routes the carve there.'
                : 'Tenant gave no preference; you must pick one (the engine has nothing to carve from otherwise).'
            }
          >
            <Select
              selectedOption={poolOpt}
              options={poolOpts}
              placeholder={poolsQ.isLoading ? 'Loading pools…' : 'Select pool'}
              onChange={(e) => setPoolOpt(e.detail.selectedOption)}
              filteringType="auto"
            />
          </FormField>
          <FormField label="Notes (optional)" description="Audited.">
            <Textarea value={notes} onChange={(e) => setNotes(e.detail.value)} rows={3} />
          </FormField>
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

// ---------- reject ----------

function RejectModal({ target, onClose }: {
  target: LirRequest | null; onClose: () => void;
}) {
  const qc = useQueryClient();
  const [reason, setReason] = useState('');
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => { setReason(''); }, [target?.id]);

  async function doReject() {
    if (!target) return;
    if (!reason.trim()) {
      toast.error('Reason is required.');
      return;
    }
    setSubmitting(true);
    try {
      await http.post(`/lir/requests/${target.id}/reject`, { reason: reason.trim() });
      toast.success('Request rejected');
      await qc.invalidateQueries({ queryKey: ['lir-requests'] });
      onClose();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to reject');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal
      visible={target !== null}
      onDismiss={onClose}
      header="Reject request"
      footer={
        <Box float="right">
          <SpaceBetween direction="horizontal" size="xs">
            <Button onClick={onClose} variant="link">Cancel</Button>
            <Button variant="primary" onClick={doReject} loading={submitting}>
              Reject
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <SpaceBetween size="s">
        <Box>
          Reject this request for an IPv{target?.ip_family} /{target?.prefix_length}?
          The decision is terminal — the tenant must file a new request to retry.
        </Box>
        <FormField label="Reason" description="Required. Shared with the requester.">
          <Textarea value={reason} onChange={(e) => setReason(e.detail.value)} rows={3} />
        </FormField>
      </SpaceBetween>
    </Modal>
  );
}

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
