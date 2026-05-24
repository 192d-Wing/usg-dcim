// ASN catalog — labeled BGP autonomous system numbers with metadata.
// Cross-references peer rows by matching BgpPeer.local_asn / peer_asn.

import { useState } from 'react';
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

import { http } from '@/lib/http';

type AsnKind = 'public' | 'private' | 'documentation' | 'reserved';
type Asn = {
  id: string;
  asn: number;
  name: string;
  kind: AsnKind;
  organization_id: string | null;
  description: string | null;
};
type Organization = { id: string; name: string; arin_org_id: string | null };

const KIND_OPTIONS: SelectProps.Option[] = [
  { value: 'public', label: 'Public' },
  { value: 'private', label: 'Private' },
  { value: 'documentation', label: 'Documentation' },
  { value: 'reserved', label: 'Reserved' },
];

function kindColor(k: AsnKind): 'green' | 'blue' | 'grey' | 'severity-medium' {
  if (k === 'public') return 'green';
  if (k === 'private') return 'blue';
  if (k === 'reserved') return 'severity-medium';
  return 'grey';
}

// IANA-aligned ASN range buckets — mirror of schemas/bgp.py asn_kind_for.
// Single source of truth for the "kind must match number" check both
// here (live UI feedback) and on the server (Pydantic validator).
const PRIVATE_RANGES: [number, number][] = [[64512, 65534], [4_200_000_000, 4_294_967_294]];
const DOC_RANGES: [number, number][] = [[64496, 64511], [65536, 65551]];
const RESERVED_VALUES = new Set([0, 23456, 65535, 4_294_967_295]);

function inRange(n: number, ranges: [number, number][]): boolean {
  return ranges.some(([lo, hi]) => n >= lo && n <= hi);
}

export function asnKindFor(n: number): AsnKind {
  if (RESERVED_VALUES.has(n)) return 'reserved';
  if (inRange(n, PRIVATE_RANGES)) return 'private';
  if (inRange(n, DOC_RANGES)) return 'documentation';
  return 'public';
}

export function AsnsPanel({ canWrite }: { canWrite: boolean }) {
  const qc = useQueryClient();
  const asnsQ = useQuery({
    queryKey: ['bgp-asns'],
    queryFn: async () => (
      await http.get<{ items: Asn[] }>('/bgp/asns?page_size=500')
    ).data.items ?? [],
  });
  // Orgs come from /organizations — the global owner registry. The Asn
  // form needs the full list for its dropdown; the table just needs
  // an id→name lookup, so the same query feeds both.
  const orgsQ = useQuery({
    queryKey: ['organizations'],
    queryFn: async () => (
      await http.get<{ items: Organization[] }>('/organizations?page_size=500')
    ).data.items ?? [],
  });
  const asns = asnsQ.data ?? [];
  const orgs = orgsQ.data ?? [];
  const orgsById = new Map(orgs.map((o) => [o.id, o]));

  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<Asn | null>(null);

  async function remove(a: Asn) {
    if (!window.confirm(`Delete AS${a.asn} (${a.name})?`)) return;
    try {
      await http.delete(`/bgp/asns/${a.id}`);
      toast.success('ASN removed');
      await qc.invalidateQueries({ queryKey: ['bgp-asns'] });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  return (
    <>
      <Table<Asn>
        variant="container"
        loading={asnsQ.isLoading}
        loadingText="Loading ASNs…"
        items={asns}
        trackBy="id"
        header={
          <Header
            counter={`(${asns.length})`}
            description="Labeled catalog of Autonomous System numbers in use across this estate."
            actions={canWrite && (
              <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                New ASN
              </Button>
            )}
          >
            ASNs
          </Header>
        }
        columnDefinitions={[
          {
            id: 'asn', header: 'ASN',
            cell: (a) => <span style={{ fontFamily: 'ui-monospace, monospace' }}>AS{a.asn}</span>,
            width: 140,
          },
          { id: 'name', header: 'Name', cell: (a) => a.name },
          {
            id: 'kind', header: 'Kind',
            cell: (a) => <Badge color={kindColor(a.kind)}>{a.kind}</Badge>,
            width: 140,
          },
          {
            id: 'org', header: 'Organization',
            cell: (a) => {
              if (!a.organization_id) return '—';
              const o = orgsById.get(a.organization_id);
              if (!o) return <span style={{ fontFamily: 'ui-monospace, monospace' }}>{a.organization_id.slice(0, 8)}…</span>;
              return o.arin_org_id ? `${o.name} (${o.arin_org_id})` : o.name;
            },
          },
          {
            id: 'description', header: 'Description',
            cell: (a) => (
              <Box variant="span" color="text-status-inactive" fontSize="body-s">
                {a.description ?? '—'}
              </Box>
            ),
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (a: Asn) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button iconName="edit" variant="inline-icon" onClick={() => setEditing(a)} ariaLabel={`Edit ${a.name}`} />
                <Button iconName="remove" variant="inline-icon" onClick={() => remove(a)} ariaLabel={`Delete ${a.name}`} />
              </SpaceBetween>
            ),
            width: 90,
          }] : []),
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">No ASNs yet.</Box>
        }
      />
      {canWrite && (
        <>
          <Modal
            visible={createOpen}
            onDismiss={() => setCreateOpen(false)}
            header="New ASN"
            size="medium"
          >
            <AsnForm
              orgs={orgs}
              onSaved={async () => {
                setCreateOpen(false);
                await qc.invalidateQueries({ queryKey: ['bgp-asns'] });
              }}
            />
          </Modal>
          <Modal
            visible={editing !== null}
            onDismiss={() => setEditing(null)}
            header="Edit ASN"
            size="medium"
          >
            {editing && (
              <AsnForm
                asn={editing}
                orgs={orgs}
                onSaved={async () => {
                  setEditing(null);
                  await qc.invalidateQueries({ queryKey: ['bgp-asns'] });
                }}
              />
            )}
          </Modal>
        </>
      )}
    </>
  );
}

function AsnForm({
  asn, orgs, onSaved,
}: Readonly<{ asn?: Asn; orgs: Organization[]; onSaved: () => void }>) {
  const editing = !!asn;
  const [asnNumber, setAsnNumber] = useState(asn ? String(asn.asn) : '');
  const [name, setName] = useState(asn?.name ?? '');
  const [kindOpt, setKindOpt] = useState<SelectProps.Option>(
    KIND_OPTIONS.find((o) => o.value === (asn?.kind ?? 'private')) ?? KIND_OPTIONS[1],
  );
  const NONE = '__none__';
  const orgOptions: SelectProps.Option[] = [
    { value: NONE, label: '(none)' },
    ...orgs.map((o) => ({
      value: o.id,
      label: o.arin_org_id ? `${o.name} (${o.arin_org_id})` : o.name,
    })),
  ];
  const [orgOpt, setOrgOpt] = useState<SelectProps.Option>(() => {
    if (!asn?.organization_id) return orgOptions[0];
    return orgOptions.find((o) => o.value === asn.organization_id) ?? orgOptions[0];
  });
  const [description, setDescription] = useState(asn?.description ?? '');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  // Live: derive the kind the entered number actually belongs to, so
  // the operator sees mismatch immediately. Empty/non-numeric falls
  // through silently — that's already covered by the required-field
  // error on submit.
  const parsedN = Number(asnNumber);
  const numericValid = asnNumber.trim() !== ''
    && Number.isInteger(parsedN)
    && parsedN >= 1
    && parsedN <= 4_294_967_295;
  const expectedKind = numericValid ? asnKindFor(parsedN) : null;
  const kindMismatch = !!expectedKind && expectedKind !== kindOpt.value;

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!asnNumber.trim() || !Number.isInteger(parsedN)) {
      errs.asn = 'ASN required (1 to 4294967295)';
    } else if (parsedN < 1 || parsedN > 4_294_967_295) {
      errs.asn = 'ASN must be 1..4294967295';
    } else if (kindMismatch) {
      errs.kind = `AS${parsedN} is in the ${expectedKind} range; pick ${expectedKind} or change the number`;
    }
    if (!name.trim()) errs.name = 'Name required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      const orgId = orgOpt.value === NONE ? null : orgOpt.value;
      if (editing && asn) {
        await http.patch(`/bgp/asns/${asn.id}`, {
          name,
          kind: kindOpt.value,
          organization_id: orgId,
          description: description || null,
        });
        toast.success('ASN updated');
      } else {
        await http.post('/bgp/asns', {
          asn: Number(asnNumber),
          name,
          kind: kindOpt.value,
          organization_id: orgId,
          description: description || null,
        });
        toast.success('ASN created');
      }
      onSaved();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form onSubmit={onSubmit}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={submitting}>
            {submitting ? 'Saving…' : editing ? 'Save' : 'Create'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <ColumnLayout columns={2}>
            <FormField
              label="ASN"
              description={
                editing
                  ? 'ASN is immutable after creation.'
                  : 'Decimal: 1..4294967295. Private: 64512–65534 or 4200000000–4294967294. Documentation: 64496–64511 or 65536–65551.'
              }
              errorText={errors.asn}
            >
              <Input
                type="number"
                value={asnNumber}
                onChange={({ detail }) => setAsnNumber(detail.value)}
                placeholder="e.g. 65000"
                disabled={editing}
              />
            </FormField>
            <FormField
              label="Kind"
              errorText={
                errors.kind
                ?? (kindMismatch ? `AS${parsedN} is in the ${expectedKind} range — pick ${expectedKind}` : undefined)
              }
            >
              <Select
                selectedOption={kindOpt}
                onChange={({ detail }) => {
                  if (detail.selectedOption.value) setKindOpt(detail.selectedOption);
                }}
                options={KIND_OPTIONS}
                expandToViewport
              />
            </FormField>
          </ColumnLayout>
          <FormField label="Name" errorText={errors.name}>
            <Input
              value={name}
              onChange={({ detail }) => setName(detail.value)}
              placeholder="e.g. prod-edge"
            />
          </FormField>
          <FormField
            label="Organization"
            description="Owning entity. Manage the list under IPAM → Organizations."
          >
            <Select
              selectedOption={orgOpt}
              onChange={({ detail }) => {
                if (detail.selectedOption.value) setOrgOpt(detail.selectedOption);
              }}
              options={orgOptions}
              expandToViewport
              filteringType="auto"
            />
          </FormField>
          <FormField label="Description">
            <Input
              value={description}
              onChange={({ detail }) => setDescription(detail.value)}
            />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}
