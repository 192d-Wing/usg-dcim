// LIR — Local Internet Registry tenant view.
//
// Three tabs:
//   * Request — form to submit a new IP-space request.
//   * My requests — tenant's outstanding + decided requests, with
//                   cancel button on pending rows.
//   * My allocations — issued allocations with ARIN status badge +
//                      return-request CTA.
//
// The NIC-side approval queue + pool admin live on a future
// /lir/admin page (phase 9). Capability-gated UI:
//
//   lir:requests:create          → Request tab + form
//   lir:requests:read            → My requests tab + list
//   lir:requests:cancel          → Cancel button on pending rows
//   lir:allocations:read         → My allocations tab + list
//   lir:allocations:return-request → Return CTA on active rows

import { useMemo, useState } from 'react';
import { useGetIdentity } from '@refinedev/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import BreadcrumbGroup from '@cloudscape-design/components/breadcrumb-group';
import Button from '@cloudscape-design/components/button';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import Tabs from '@cloudscape-design/components/tabs';
import Textarea from '@cloudscape-design/components/textarea';

import { hasCap } from '@/lib/caps';
import { http } from '@/lib/http';

// ---------- types (match db/generated row shapes via JSON tags) ----------

type LirPool = {
  id: string;
  name: string;
  slug: string;
  description: string | null;
  ip_family: number;
  classification: string | null;
  min_prefix_length: number;
  max_prefix_length: number;
  default_supernet_purpose: string | null;
  arin_parent_net_handle: string | null;
  enabled: boolean;
};

type LirRequestStatus =
  | 'pending_approval' | 'approved' | 'rejected' | 'cancelled' | 'failed';

type LirRequest = {
  id: string;
  organization_id: string;
  requester_user_id: string;
  pool_id: string | null;
  site_id: string | null;
  ip_family: number;
  prefix_length: number;
  purpose: string | null;
  classification: string | null;
  justification: string;
  status: LirRequestStatus;
  submitted_at: string;
  decided_at: string | null;
  decided_by_user_id: string | null;
  decision_notes: string | null;
  approved_pool_id: string | null;
};

type LirAllocationStatus = 'active' | 'return_requested' | 'returned';
type LirArinStatus =
  | 'none' | 'pending' | 'registered' | 'failed' | 'removing' | 'removed';

type LirAllocation = {
  id: string;
  request_id: string;
  organization_id: string;
  pool_id: string;
  pool_supernet_id: string;
  tenant_supernet_id: string;
  prefix: string;
  allocated_at: string;
  status: LirAllocationStatus;
  return_requested_at: string | null;
  returned_at: string | null;
  arin_status: LirArinStatus;
  arin_net_handle: string | null;
  arin_last_error: string | null;
  arin_attempts: number;
};

type Organization = { id: string; name: string };

// ---------- main page ----------

export function LirPage() {
  const { data: identity } = useGetIdentity<{
    email: string | null; capabilities: string[];
  }>();
  const caps = identity?.capabilities ?? [];
  const canCreate = hasCap(caps, 'lir:requests:create');
  const canReadReq = hasCap(caps, 'lir:requests:read');
  const canCancel = hasCap(caps, 'lir:requests:cancel');
  const canReadAlloc = hasCap(caps, 'lir:allocations:read');

  const tabs: { id: string; label: string; content: React.ReactNode }[] = [];
  if (canCreate) tabs.push({ id: 'request', label: 'Request', content: <RequestForm /> });
  if (canReadReq) tabs.push({ id: 'my-requests', label: 'My requests', content: <MyRequestsTab canCancel={canCancel} /> });
  if (canReadAlloc) tabs.push({ id: 'my-allocations', label: 'My allocations', content: <MyAllocationsTab caps={caps} /> });

  if (tabs.length === 0) {
    return (
      <ContentLayout header={<Header variant="h1">LIR</Header>}>
        <Container><Box color="text-status-inactive">
          You don't hold any LIR capabilities. Ask an administrator to grant
          lir:requests:create or lir:allocations:read.
        </Box></Container>
      </ContentLayout>
    );
  }

  return (
    <ContentLayout
      breadcrumbs={
        <BreadcrumbGroup items={[
          { text: 'Home', href: '/' },
          { text: 'LIR', href: '/lir' },
        ]} />
      }
      header={
        <Header
          variant="h1"
          description="Request IP space from DoW pools, track approvals, and manage active allocations."
        >
          LIR — Local Internet Registry
        </Header>
      }
    >
      <Tabs tabs={tabs} />
    </ContentLayout>
  );
}

// ---------- Request form ----------

const FAMILY_OPTS: SelectProps.Option[] = [
  { value: '4', label: 'IPv4' },
  { value: '6', label: 'IPv6' },
];

function RequestForm() {
  const qc = useQueryClient();
  const [orgOpt, setOrgOpt] = useState<SelectProps.Option | null>(null);
  const [familyOpt, setFamilyOpt] = useState<SelectProps.Option>(FAMILY_OPTS[0]);
  const [prefixLength, setPrefixLength] = useState<string>('28');
  const [poolOpt, setPoolOpt] = useState<SelectProps.Option | null>(null);
  const [purpose, setPurpose] = useState<string>('');
  const [justification, setJustification] = useState<string>('');
  const [submitting, setSubmitting] = useState(false);

  const orgsQ = useQuery({
    queryKey: ['lir-orgs'],
    queryFn: async () => (
      await http.get<{ items: Organization[] }>('/organizations?page_size=500')
    ).data.items ?? [],
  });

  const poolsQ = useQuery({
    queryKey: ['lir-pools'],
    queryFn: async () => (
      await http.get<{ items: LirPool[] }>('/lir/pools?limit=500')
    ).data.items ?? [],
  });

  const family = Number(familyOpt.value);
  // Filter pools to the chosen family + enabled, so the dropdown can't
  // surface a pool the backend would reject. Tenant preference only —
  // NIC may approve into a different pool anyway.
  const eligiblePools = useMemo(
    () => (poolsQ.data ?? []).filter((p) => p.enabled && p.ip_family === family),
    [poolsQ.data, family],
  );
  const poolOpts: SelectProps.Option[] = [
    { value: '', label: 'No preference (NIC will pick)' },
    ...eligiblePools.map((p) => ({
      value: p.id,
      label: `${p.name} (/${p.min_prefix_length}–/${p.max_prefix_length})`,
      description: p.description ?? undefined,
    })),
  ];

  async function submit() {
    const errs = validateRequest({
      orgId: orgOpt?.value, family, prefixLength, justification,
    });
    if (errs.length) {
      toast.error(errs.join(' '));
      return;
    }
    setSubmitting(true);
    try {
      const body: Record<string, unknown> = {
        organization_id: orgOpt!.value!,
        ip_family: family,
        prefix_length: Number(prefixLength),
        justification: justification.trim(),
      };
      if (poolOpt?.value) body.pool_id = poolOpt.value;
      if (purpose.trim()) body.purpose = purpose.trim();
      await http.post('/lir/requests', body);
      toast.success('Request submitted');
      await qc.invalidateQueries({ queryKey: ['lir-requests'] });
      setPurpose('');
      setJustification('');
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to submit');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Container header={<Header variant="h2">New IP-space request</Header>}>
      <Form
        actions={
          <Button variant="primary" onClick={submit} loading={submitting}>
            Submit request
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField label="Organization" description="The tenant the allocation will belong to.">
            <Select
              selectedOption={orgOpt}
              placeholder={orgsQ.isLoading ? 'Loading…' : 'Select organization'}
              options={(orgsQ.data ?? []).map((o) => ({ value: o.id, label: o.name }))}
              onChange={(e) => setOrgOpt(e.detail.selectedOption)}
              filteringType="auto"
            />
          </FormField>
          <FormField label="IP family">
            <Select
              selectedOption={familyOpt}
              options={FAMILY_OPTS}
              onChange={(e) => setFamilyOpt(e.detail.selectedOption)}
            />
          </FormField>
          <FormField
            label="Prefix length"
            description={family === 4 ? 'e.g. 28 for a /28 (14 hosts)' : 'e.g. 64 for a tenant subnet'}
          >
            <Input
              value={prefixLength}
              type="number"
              inputMode="numeric"
              onChange={(e) => setPrefixLength(e.detail.value)}
            />
          </FormField>
          <FormField label="Pool preference (optional)" description="NIC may approve into a different pool.">
            <Select
              selectedOption={poolOpt}
              options={poolOpts}
              placeholder={poolsQ.isLoading ? 'Loading pools…' : 'No preference'}
              onChange={(e) => setPoolOpt(e.detail.selectedOption)}
            />
          </FormField>
          <FormField label="Purpose (optional)" description="e.g. mgmt, data, storage.">
            <Input value={purpose} onChange={(e) => setPurpose(e.detail.value)} />
          </FormField>
          <FormField
            label="Justification"
            description="Required. Explain why you need this space."
          >
            <Textarea
              value={justification}
              onChange={(e) => setJustification(e.detail.value)}
              rows={4}
            />
          </FormField>
        </SpaceBetween>
      </Form>
    </Container>
  );
}

function validateRequest(args: {
  orgId: string | undefined;
  family: number;
  prefixLength: string;
  justification: string;
}): string[] {
  const out: string[] = [];
  if (!args.orgId) out.push('Organization is required.');
  const len = Number(args.prefixLength);
  if (!Number.isFinite(len) || len < 0) out.push('Prefix length must be a non-negative integer.');
  const cap = args.family === 4 ? 32 : 128;
  if (len > cap) out.push(`Prefix length exceeds /${cap} for IPv${args.family}.`);
  if (!args.justification.trim()) out.push('Justification is required.');
  return out;
}

// ---------- My requests ----------

function MyRequestsTab({ canCancel }: { canCancel: boolean }) {
  const qc = useQueryClient();
  const requestsQ = useQuery({
    queryKey: ['lir-requests'],
    queryFn: async () => (
      await http.get<{ items: LirRequest[] }>('/lir/requests?limit=200')
    ).data.items ?? [],
  });
  const requests = requestsQ.data ?? [];
  const [cancelTarget, setCancelTarget] = useState<LirRequest | null>(null);
  const [cancelNotes, setCancelNotes] = useState('');
  const [cancelling, setCancelling] = useState(false);

  async function doCancel() {
    if (!cancelTarget) return;
    setCancelling(true);
    try {
      const body = cancelNotes.trim() ? { notes: cancelNotes.trim() } : null;
      await http.post(`/lir/requests/${cancelTarget.id}/cancel`, body);
      toast.success('Request cancelled');
      await qc.invalidateQueries({ queryKey: ['lir-requests'] });
      setCancelTarget(null);
      setCancelNotes('');
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to cancel');
    } finally {
      setCancelling(false);
    }
  }

  return (
    <>
      <Table<LirRequest>
        variant="container"
        loading={requestsQ.isLoading}
        loadingText="Loading requests…"
        items={requests}
        trackBy="id"
        header={<Header counter={`(${requests.length})`}>My requests</Header>}
        empty={<Box color="text-status-inactive">No requests yet.</Box>}
        columnDefinitions={[
          {
            id: 'submitted', header: 'Submitted',
            cell: (r) => formatDateShort(r.submitted_at),
            width: 160,
          },
          {
            id: 'family', header: 'Family',
            cell: (r) => `IPv${r.ip_family}`, width: 80,
          },
          {
            id: 'size', header: 'Size',
            cell: (r) => `/${r.prefix_length}`, width: 80,
          },
          {
            id: 'status', header: 'Status',
            cell: (r) => <RequestStatusBadge status={r.status} />,
            width: 160,
          },
          {
            id: 'justification', header: 'Justification',
            cell: (r) => (
              <Box fontSize="body-s">{truncate(r.justification, 80)}</Box>
            ),
          },
          {
            id: 'notes', header: 'Decision notes',
            cell: (r) => r.decision_notes
              ? <Box fontSize="body-s" color="text-status-inactive">{r.decision_notes}</Box>
              : <Box color="text-status-inactive">—</Box>,
          },
          {
            id: 'actions', header: '',
            cell: (r) => canCancel && r.status === 'pending_approval'
              ? <Button onClick={() => setCancelTarget(r)} variant="link">Cancel</Button>
              : null,
            width: 100,
          },
        ]}
      />
      <Modal
        visible={cancelTarget !== null}
        onDismiss={() => setCancelTarget(null)}
        header="Cancel request"
        footer={
          <Box float="right">
            <SpaceBetween direction="horizontal" size="xs">
              <Button onClick={() => setCancelTarget(null)} variant="link">
                Keep request
              </Button>
              <Button variant="primary" onClick={doCancel} loading={cancelling}>
                Cancel request
              </Button>
            </SpaceBetween>
          </Box>
        }
      >
        <SpaceBetween size="s">
          <Box>Cancel the request for an IPv{cancelTarget?.ip_family} /{cancelTarget?.prefix_length}?</Box>
          <FormField label="Notes (optional)" description="Shared with NIC for audit.">
            <Textarea
              value={cancelNotes}
              onChange={(e) => setCancelNotes(e.detail.value)}
              rows={2}
            />
          </FormField>
        </SpaceBetween>
      </Modal>
    </>
  );
}

function RequestStatusBadge({ status }: { status: LirRequestStatus }) {
  switch (status) {
    case 'pending_approval':
      return <Badge color="blue">Pending</Badge>;
    case 'approved':
      return <Badge color="green">Approved</Badge>;
    case 'rejected':
      return <Badge color="red">Rejected</Badge>;
    case 'cancelled':
      return <Badge color="grey">Cancelled</Badge>;
    case 'failed':
      return <Badge color="red">Failed</Badge>;
  }
}

// ---------- My allocations ----------

function MyAllocationsTab({ caps }: { caps: readonly string[] }) {
  const qc = useQueryClient();
  const canReturn = hasCap(caps, 'lir:allocations:return-request');
  const allocsQ = useQuery({
    queryKey: ['lir-allocations'],
    queryFn: async () => (
      await http.get<{ items: LirAllocation[] }>('/lir/allocations?limit=200')
    ).data.items ?? [],
  });
  const allocs = allocsQ.data ?? [];

  const [returnTarget, setReturnTarget] = useState<LirAllocation | null>(null);
  const [returnReason, setReturnReason] = useState('');
  const [returning, setReturning] = useState(false);

  async function doReturn() {
    if (!returnTarget) return;
    if (!returnReason.trim()) {
      toast.error('Reason is required.');
      return;
    }
    setReturning(true);
    try {
      await http.post(
        `/lir/allocations/${returnTarget.id}/return-request`,
        { reason: returnReason.trim() },
      );
      toast.success('Return requested. NIC will confirm.');
      await qc.invalidateQueries({ queryKey: ['lir-allocations'] });
      setReturnTarget(null);
      setReturnReason('');
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to request return');
    } finally {
      setReturning(false);
    }
  }

  return (
    <>
      <Table<LirAllocation>
        variant="container"
        loading={allocsQ.isLoading}
        loadingText="Loading allocations…"
        items={allocs}
        trackBy="id"
        header={<Header counter={`(${allocs.length})`}>My allocations</Header>}
        empty={<Box color="text-status-inactive">No allocations yet.</Box>}
        columnDefinitions={[
          {
            id: 'prefix', header: 'Prefix',
            cell: (a) => <Box fontWeight="bold">{a.prefix}</Box>, width: 200,
          },
          {
            id: 'allocated', header: 'Allocated',
            cell: (a) => formatDateShort(a.allocated_at), width: 160,
          },
          {
            id: 'status', header: 'Status',
            cell: (a) => <AllocationStatusBadge status={a.status} />,
            width: 160,
          },
          {
            id: 'arin', header: 'ARIN',
            cell: (a) => <ArinStatusIndicator alloc={a} />,
            width: 180,
          },
          {
            id: 'handle', header: 'ARIN handle',
            cell: (a) => a.arin_net_handle
              ? <Box fontSize="body-s">{a.arin_net_handle}</Box>
              : <Box color="text-status-inactive">—</Box>,
          },
          {
            id: 'actions', header: '',
            cell: (a) => canReturn && a.status === 'active'
              ? <Button onClick={() => setReturnTarget(a)} variant="link">
                  Request return
                </Button>
              : null,
            width: 140,
          },
        ]}
      />
      <Modal
        visible={returnTarget !== null}
        onDismiss={() => setReturnTarget(null)}
        header="Request return"
        footer={
          <Box float="right">
            <SpaceBetween direction="horizontal" size="xs">
              <Button onClick={() => setReturnTarget(null)} variant="link">
                Cancel
              </Button>
              <Button variant="primary" onClick={doReturn} loading={returning}>
                Request return
              </Button>
            </SpaceBetween>
          </Box>
        }
      >
        <SpaceBetween size="s">
          <Box>
            Return allocation <b>{returnTarget?.prefix}</b>? NIC must
            confirm before the space goes back into the pool.
          </Box>
          <FormField label="Reason" description="Required. Audited.">
            <Textarea
              value={returnReason}
              onChange={(e) => setReturnReason(e.detail.value)}
              rows={3}
            />
          </FormField>
        </SpaceBetween>
      </Modal>
    </>
  );
}

function AllocationStatusBadge({ status }: { status: LirAllocationStatus }) {
  switch (status) {
    case 'active':
      return <Badge color="green">Active</Badge>;
    case 'return_requested':
      return <Badge color="blue">Return requested</Badge>;
    case 'returned':
      return <Badge color="grey">Returned</Badge>;
  }
}

function ArinStatusIndicator({ alloc }: { alloc: LirAllocation }) {
  switch (alloc.arin_status) {
    case 'none':
      return <StatusIndicator type="stopped">Not registered</StatusIndicator>;
    case 'pending':
      return <StatusIndicator type="in-progress">Submitting…</StatusIndicator>;
    case 'registered':
      return <StatusIndicator type="success">Registered</StatusIndicator>;
    case 'failed':
      return <StatusIndicator type="error">Failed ({alloc.arin_attempts})</StatusIndicator>;
    case 'removing':
      return <StatusIndicator type="in-progress">Removing…</StatusIndicator>;
    case 'removed':
      return <StatusIndicator type="stopped">Removed</StatusIndicator>;
  }
}

// ---------- helpers ----------

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

function truncate(s: string, n: number): string {
  if (s.length <= n) return s;
  return s.slice(0, n - 1) + '…';
}
