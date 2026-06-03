// DoD NIC registrations — end-user (internal DoD customer) intake.
//
// One page, schema-driven: pick a registration type, then fill only that
// template's fields (NicRegistrationForm). A reviewer queue lets NIC staff
// approve/reject and record whether the registration should flow upstream to
// ARIN (push_to_arin). Capability-gated UI:
//
//   nicreg:requests:create  → New registration tab
//   nicreg:requests:read    → Registrations list
//   nicreg:requests:update  → Submit a draft
//   nicreg:requests:cancel  → Cancel
//   nicreg:requests:approve → Approve (+ push_to_arin)
//   nicreg:requests:reject  → Reject
import { useMemo, useState } from 'react';
import { useGetIdentity } from '@refinedev/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import BreadcrumbGroup from '@cloudscape-design/components/breadcrumb-group';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Modal from '@cloudscape-design/components/modal';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import Tabs from '@cloudscape-design/components/tabs';
import Textarea from '@cloudscape-design/components/textarea';

import { NicRegistrationForm } from '@/components/nic-registration-form';
import {
  ACTION_OPTIONS,
  FormValues,
  getTemplate,
  templateOptions,
} from '@/nic/templates';
import { hasCap } from '@/lib/caps';
import { http } from '@/lib/http';

type Organization = { id: string; name: string };

type NicRegistration = {
  id: string;
  template_type: string;
  action_type: string;
  organization_id: string;
  requester_user_id: string;
  status: 'draft' | 'submitted' | 'approved' | 'rejected' | 'cancelled';
  push_to_arin: boolean | null;
  submitted_at: string | null;
  decided_at: string | null;
  decision_notes: string | null;
  created_at: string;
  updated_at: string;
};

const LIST_KEY = ['nic-registrations'];

export function RegistrationsPage() {
  const { data: identity } = useGetIdentity<{ email: string | null; capabilities: string[] }>();
  const caps = identity?.capabilities ?? [];
  const canCreate = hasCap(caps, 'nicreg:requests:create');
  const canRead = hasCap(caps, 'nicreg:requests:read');

  const tabs: { id: string; label: string; content: React.ReactNode }[] = [];
  if (canCreate) tabs.push({ id: 'new', label: 'New registration', content: <NewRegistrationTab /> });
  if (canRead) tabs.push({ id: 'list', label: 'Registrations', content: <RegistrationsListTab caps={caps} /> });

  if (tabs.length === 0) {
    return (
      <ContentLayout header={<Header variant="h1">Registrations</Header>}>
        <Container>
          <Box color="text-status-inactive">
            You don't hold any registration capabilities. Ask an administrator to grant
            nicreg:requests:create or nicreg:requests:read.
          </Box>
        </Container>
      </ContentLayout>
    );
  }

  return (
    <ContentLayout
      breadcrumbs={<BreadcrumbGroup items={[{ text: 'Home', href: '/' }, { text: 'Registrations', href: '/registrations' }]} />}
      header={
        <Header variant="h1" description="Submit DoD NIC registration templates for internal customers and track approvals.">
          NIC Registrations
        </Header>
      }
    >
      <Tabs tabs={tabs} />
    </ContentLayout>
  );
}

// ---------- organizations lookup ----------

function useOrgs() {
  return useQuery({
    queryKey: ['nicreg-orgs'],
    queryFn: async () => (await http.get<{ items: Organization[] }>('/organizations?page_size=500')).data.items ?? [],
  });
}

// ---------- New registration ----------

function NewRegistrationTab() {
  const qc = useQueryClient();
  const orgsQ = useOrgs();

  const [typeOpt, setTypeOpt] = useState<SelectProps.Option | null>(null);
  const [actionOpt, setActionOpt] = useState<SelectProps.Option>({ value: 'N', label: 'New' });
  const [orgOpt, setOrgOpt] = useState<SelectProps.Option | null>(null);
  const [values, setValues] = useState<FormValues>({});
  const [busy, setBusy] = useState(false);

  const templateType = typeOpt?.value ?? '';
  const tmpl = templateType ? getTemplate(templateType) : undefined;
  const action = actionOpt.value ?? 'N';

  // Action choices restricted to those the chosen template supports.
  const actionOpts = useMemo(
    () => ACTION_OPTIONS.filter((a) => !tmpl || tmpl.actions.includes(a.value)),
    [tmpl],
  );

  const setField = (key: string, value: unknown) => setValues((prev) => ({ ...prev, [key]: value }));

  async function save(status: 'draft' | 'submitted') {
    if (!templateType) { toast.error('Pick a registration type'); return; }
    if (!orgOpt?.value) { toast.error('Pick an organization'); return; }
    setBusy(true);
    try {
      await http.post('/nic-registrations', {
        template_type: templateType,
        action_type: action,
        organization_id: orgOpt.value,
        status,
        payload: values,
      });
      toast.success(status === 'draft' ? 'Draft saved' : 'Registration submitted');
      await qc.invalidateQueries({ queryKey: LIST_KEY });
      setValues({});
    } catch (err: any) {
      toast.error(err?.response?.data?.error ?? err?.message ?? 'Failed to save');
    } finally {
      setBusy(false);
    }
  }

  return (
    <SpaceBetween size="l">
      <Container header={<Header variant="h3">Registration</Header>}>
        <SpaceBetween size="m">
          <FormField label="Registration type *">
            <Select
              selectedOption={typeOpt}
              options={templateOptions().map((o) => ({ value: o.value, label: o.label }))}
              placeholder="Select a template…"
              onChange={({ detail }) => {
                setTypeOpt(detail.selectedOption);
                setValues({});
                // Reset action to the first one the new template allows.
                const t = getTemplate(detail.selectedOption.value ?? '');
                const first = ACTION_OPTIONS.find((a) => t?.actions.includes(a.value));
                if (first) setActionOpt({ value: first.value, label: first.label });
              }}
            />
          </FormField>
          <FormField label="Action *">
            <Select
              selectedOption={actionOpt}
              options={actionOpts.map((a) => ({ value: a.value, label: a.label }))}
              onChange={({ detail }) => setActionOpt(detail.selectedOption)}
            />
          </FormField>
          <FormField label="Organization (internal DoD customer) *">
            <Select
              selectedOption={orgOpt}
              statusType={orgsQ.isLoading ? 'loading' : 'finished'}
              options={(orgsQ.data ?? []).map((o) => ({ value: o.id, label: o.name }))}
              placeholder="Select an organization…"
              filteringType="auto"
              onChange={({ detail }) => setOrgOpt(detail.selectedOption)}
            />
          </FormField>
        </SpaceBetween>
      </Container>

      {tmpl && (
        <NicRegistrationForm templateType={templateType} action={action} values={values} onChange={setField} />
      )}

      {tmpl && (
        <Box float="right">
          <SpaceBetween size="xs" direction="horizontal">
            <Button disabled={busy} onClick={() => save('draft')}>Save draft</Button>
            <Button variant="primary" loading={busy} onClick={() => save('submitted')}>Submit</Button>
          </SpaceBetween>
        </Box>
      )}
    </SpaceBetween>
  );
}

// ---------- Registrations list + review ----------

function RegistrationsListTab({ caps }: { caps: string[] }) {
  const orgsQ = useOrgs();
  const orgName = useMemo(() => {
    const m = new Map<string, string>();
    (orgsQ.data ?? []).forEach((o) => m.set(o.id, o.name));
    return (id: string) => m.get(id) ?? id.slice(0, 8);
  }, [orgsQ.data]);

  const listQ = useQuery({
    queryKey: LIST_KEY,
    queryFn: async () => (await http.get<{ items: NicRegistration[] }>('/nic-registrations?limit=200')).data.items ?? [],
  });

  const [active, setActive] = useState<NicRegistration | null>(null);

  return (
    <SpaceBetween size="m">
      <Table
        loading={listQ.isLoading}
        items={listQ.data ?? []}
        variant="container"
        empty={<Box color="text-status-inactive">No registrations yet.</Box>}
        columnDefinitions={[
          {
            id: 'type', header: 'Type',
            cell: (r) => getTemplate(r.template_type)?.label ?? r.template_type,
          },
          { id: 'action', header: 'Action', cell: (r) => actionLabel(r.action_type) },
          { id: 'org', header: 'Organization', cell: (r) => orgName(r.organization_id) },
          { id: 'status', header: 'Status', cell: (r) => <StatusBadge status={r.status} /> },
          { id: 'arin', header: 'ARIN', cell: (r) => arinLabel(r.push_to_arin) },
          { id: 'created', header: 'Created', cell: (r) => new Date(r.created_at).toLocaleString() },
          {
            id: 'open', header: '',
            cell: (r) => <Button variant="inline-link" onClick={() => setActive(r)}>Open</Button>,
          },
        ]}
      />
      {active && (
        <ReviewModal
          caps={caps}
          registration={active}
          orgLabel={orgName(active.organization_id)}
          onClose={() => setActive(null)}
        />
      )}
    </SpaceBetween>
  );
}

function ReviewModal({
  caps,
  registration,
  orgLabel,
  onClose,
}: {
  caps: string[];
  registration: NicRegistration;
  orgLabel: string;
  onClose: () => void;
}) {
  const qc = useQueryClient();
  const tmpl = getTemplate(registration.template_type);
  const [busy, setBusy] = useState(false);
  const [notes, setNotes] = useState('');
  const [pushToArin, setPushToArin] = useState(false);

  const detailQ = useQuery({
    queryKey: ['nic-registration', registration.id],
    queryFn: async () =>
      (await http.get<{ registration: NicRegistration; detail: Record<string, unknown> }>(
        `/nic-registrations/${registration.id}`,
      )).data,
  });

  const isDraft = registration.status === 'draft';
  const isSubmitted = registration.status === 'submitted';
  const canUpdate = hasCap(caps, 'nicreg:requests:update');
  const canCancel = hasCap(caps, 'nicreg:requests:cancel');
  const canApprove = hasCap(caps, 'nicreg:requests:approve');
  const canReject = hasCap(caps, 'nicreg:requests:reject');

  async function act(path: string, body?: Record<string, unknown>) {
    setBusy(true);
    try {
      await http.post(`/nic-registrations/${registration.id}/${path}`, body ?? null);
      toast.success(`Registration ${path}d`);
      await qc.invalidateQueries({ queryKey: LIST_KEY });
      onClose();
    } catch (err: any) {
      toast.error(err?.response?.data?.error ?? err?.message ?? `Failed to ${path}`);
    } finally {
      setBusy(false);
    }
  }

  // Detail rows come back keyed exactly like the form field keys, so feed
  // them straight into the read-only form.
  const values: FormValues = (detailQ.data?.detail as FormValues) ?? {};

  return (
    <Modal
      visible
      onDismiss={onClose}
      size="large"
      header={`${tmpl?.label ?? registration.template_type} — ${actionLabel(registration.action_type)} — ${orgLabel}`}
      footer={
        <Box float="right">
          <SpaceBetween size="xs" direction="horizontal">
            <Button onClick={onClose}>Close</Button>
            {isDraft && canUpdate && (
              <Button disabled={busy} onClick={() => act('submit')}>Submit</Button>
            )}
            {(isDraft || isSubmitted) && canCancel && (
              <Button disabled={busy} onClick={() => act('cancel', { notes })}>Cancel registration</Button>
            )}
            {isSubmitted && canReject && (
              <Button disabled={busy} onClick={() => act('reject', { notes })}>Reject</Button>
            )}
            {isSubmitted && canApprove && (
              <Button variant="primary" loading={busy} onClick={() => act('approve', { notes, push_to_arin: pushToArin })}>
                Approve
              </Button>
            )}
          </SpaceBetween>
        </Box>
      }
    >
      <SpaceBetween size="l">
        <Box>
          <StatusBadge status={registration.status} />
        </Box>
        {detailQ.isLoading ? (
          <Box>Loading…</Box>
        ) : (
          <NicRegistrationForm
            templateType={registration.template_type}
            action={registration.action_type}
            values={values}
            onChange={() => undefined}
            readOnly
          />
        )}
        {isSubmitted && (canApprove || canReject) && (
          <Container header={<Header variant="h3">Decision</Header>}>
            <SpaceBetween size="m">
              {canApprove && tmpl?.arinEligible && (
                <Checkbox checked={pushToArin} onChange={({ detail }) => setPushToArin(detail.checked)}>
                  Flow this registration up to ARIN on approval
                </Checkbox>
              )}
              <FormField label="Decision notes">
                <Textarea value={notes} onChange={({ detail }) => setNotes(detail.value)} />
              </FormField>
            </SpaceBetween>
          </Container>
        )}
      </SpaceBetween>
    </Modal>
  );
}

// ---------- helpers ----------

function actionLabel(code: string): string {
  return ACTION_OPTIONS.find((a) => a.value === code)?.label ?? code;
}

function arinLabel(v: boolean | null): React.ReactNode {
  if (v === null) return <Box color="text-status-inactive">—</Box>;
  return v ? <Badge color="green">Push</Badge> : <Badge color="grey">No</Badge>;
}

function StatusBadge({ status }: { status: NicRegistration['status'] }) {
  switch (status) {
    case 'draft':
      return <Badge color="grey">Draft</Badge>;
    case 'submitted':
      return <Badge color="blue">Submitted</Badge>;
    case 'approved':
      return <Badge color="green">Approved</Badge>;
    case 'rejected':
      return <Badge color="red">Rejected</Badge>;
    case 'cancelled':
      return <Badge color="grey">Cancelled</Badge>;
    default:
      return <Badge>{status}</Badge>;
  }
}
