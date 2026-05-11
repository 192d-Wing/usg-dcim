// BGP community lists — named groups of community values referenced
// by route-map match/set clauses. Standard (RFC 1997) vs extended
// (RFC 4360) differ in value syntax.

import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';

import { http } from '@/lib/http';

type Kind = 'standard' | 'extended';
type Action = 'permit' | 'deny';
type CommunityList = { id: string; name: string; kind: Kind; description: string | null };
type Entry = {
  id: string; community_list_id: string; seq: number;
  action: Action; value: string; description: string | null;
};

const KIND_OPTIONS: SelectProps.Option[] = [
  { value: 'standard', label: 'Standard' },
  { value: 'extended', label: 'Extended' },
];
const ACTION_OPTIONS: SelectProps.Option[] = [
  { value: 'permit', label: 'Permit' },
  { value: 'deny', label: 'Deny' },
];
const MONO = { fontFamily: 'ui-monospace, monospace' } as const;

export function CommunitiesPanel({ canWrite }: { canWrite: boolean }) {
  const [selectedId, setSelectedId] = useState<string | null>(null);
  return (
    <ColumnLayout columns={2}>
      <ListsPanel canWrite={canWrite} selectedId={selectedId} onSelect={setSelectedId} />
      {selectedId
        ? <EntriesPanel listId={selectedId} canWrite={canWrite} />
        : (
          <Container>
            <Box padding="m" color="text-status-inactive">
              Pick a community list to see its values.
            </Box>
          </Container>
        )}
    </ColumnLayout>
  );
}

function ListsPanel({
  canWrite, selectedId, onSelect,
}: Readonly<{ canWrite: boolean; selectedId: string | null; onSelect: (id: string | null) => void }>) {
  const qc = useQueryClient();
  const listsQ = useQuery({
    queryKey: ['community-lists'],
    queryFn: async () => (
      await http.get<{ items: CommunityList[] }>('/bgp/community-lists?page_size=200')
    ).data.items ?? [],
  });
  const lists = listsQ.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);

  async function remove(l: CommunityList) {
    if (!window.confirm(`Delete community list ${l.name}?`)) return;
    try {
      await http.delete(`/bgp/community-lists/${l.id}`);
      if (selectedId === l.id) onSelect(null);
      toast.success('Community list removed');
      await qc.invalidateQueries({ queryKey: ['community-lists'] });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  return (
    <>
      <Table<CommunityList>
        variant="container"
        loading={listsQ.isLoading}
        loadingText="Loading community lists…"
        items={lists}
        trackBy="id"
        selectionType="single"
        selectedItems={selectedId ? lists.filter((l) => l.id === selectedId) : []}
        onSelectionChange={({ detail }) => {
          const next = detail.selectedItems[0];
          onSelect(next ? next.id : null);
        }}
        ariaLabels={{
          selectionGroupLabel: 'Community list selection',
          itemSelectionLabel: (_d, item) => `Select ${item.name}`,
          allItemsSelectionLabel: () => 'select all',
        }}
        header={
          <Header
            counter={`(${lists.length})`}
            description="Named groups of standard (RFC 1997) or extended (RFC 4360) BGP communities."
            actions={canWrite && (
              <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                New list
              </Button>
            )}
          >
            Community lists
          </Header>
        }
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (l) => l.name },
          {
            id: 'kind', header: 'Kind',
            cell: (l) => <Badge>{l.kind}</Badge>,
            width: 120,
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (l: CommunityList) => (
              <Button iconName="remove" variant="inline-icon" onClick={() => remove(l)} ariaLabel={`Delete ${l.name}`} />
            ),
            width: 60,
          }] : []),
        ]}
        empty={<Box textAlign="center" color="inherit" padding="m">No community lists yet.</Box>}
      />
      {canWrite && (
        <Modal visible={createOpen} onDismiss={() => setCreateOpen(false)} header="New community list" size="medium">
          <ListForm onSaved={async () => {
            setCreateOpen(false);
            await qc.invalidateQueries({ queryKey: ['community-lists'] });
          }} />
        </Modal>
      )}
    </>
  );
}

function ListForm({ onSaved }: { onSaved: () => void }) {
  const [name, setName] = useState('');
  const [kindOpt, setKindOpt] = useState<SelectProps.Option>(KIND_OPTIONS[0]);
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      await http.post('/bgp/community-lists', {
        name, kind: kindOpt.value, description: description || null,
      });
      toast.success('Community list created');
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
            {submitting ? 'Saving…' : 'Create'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField label="Name" errorText={errors.name}>
            <Input value={name} onChange={({ detail }) => setName(detail.value)} placeholder="e.g. customer-tags" />
          </FormField>
          <FormField label="Kind">
            <Select
              selectedOption={kindOpt}
              onChange={({ detail }) => {
                if (detail.selectedOption.value) setKindOpt(detail.selectedOption);
              }}
              options={KIND_OPTIONS}
              expandToViewport
            />
          </FormField>
          <FormField label="Description">
            <Input value={description} onChange={({ detail }) => setDescription(detail.value)} />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}

function EntriesPanel({
  listId, canWrite,
}: Readonly<{ listId: string; canWrite: boolean }>) {
  const qc = useQueryClient();
  const entriesQ = useQuery({
    queryKey: ['community-list-entries', listId],
    queryFn: async () => (
      await http.get<{ items: Entry[] }>(`/bgp/community-list-entries?community_list_id=${listId}&page_size=500`)
    ).data.items ?? [],
  });
  const entries = (entriesQ.data ?? []).slice().sort((a, b) => a.seq - b.seq);
  const [createOpen, setCreateOpen] = useState(false);

  async function remove(e: Entry) {
    if (!window.confirm(`Delete entry seq ${e.seq}?`)) return;
    try {
      await http.delete(`/bgp/community-list-entries/${e.id}`);
      toast.success('Entry removed');
      await qc.invalidateQueries({ queryKey: ['community-list-entries', listId] });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  return (
    <>
      <Table<Entry>
        variant="container"
        loading={entriesQ.isLoading}
        loadingText="Loading entries…"
        items={entries}
        trackBy="id"
        header={
          <Header
            counter={`(${entries.length})`}
            actions={canWrite && (
              <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                Add value
              </Button>
            )}
          >
            Values
          </Header>
        }
        columnDefinitions={[
          { id: 'seq', header: 'Seq', cell: (e) => e.seq, width: 70 },
          {
            id: 'action', header: 'Action',
            cell: (e) => <Badge color={e.action === 'permit' ? 'green' : 'red'}>{e.action}</Badge>,
            width: 110,
          },
          {
            id: 'value', header: 'Value',
            cell: (e) => <span style={MONO}>{e.value}</span>,
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (e: Entry) => (
              <Button iconName="remove" variant="inline-icon" onClick={() => remove(e)} ariaLabel="Delete entry" />
            ),
            width: 60,
          }] : []),
        ]}
        empty={<Box textAlign="center" color="inherit" padding="m">No values yet.</Box>}
      />
      {canWrite && (
        <Modal visible={createOpen} onDismiss={() => setCreateOpen(false)} header="Add community value" size="medium">
          <EntryForm listId={listId} existing={entries} onSaved={async () => {
            setCreateOpen(false);
            await qc.invalidateQueries({ queryKey: ['community-list-entries', listId] });
          }} />
        </Modal>
      )}
    </>
  );
}

function EntryForm({
  listId, existing, onSaved,
}: Readonly<{ listId: string; existing: Entry[]; onSaved: () => void }>) {
  const nextSeq = existing.length === 0
    ? 10
    : Math.max(...existing.map((e) => e.seq)) + 10;
  const [seq, setSeq] = useState(String(nextSeq));
  const [actionOpt, setActionOpt] = useState<SelectProps.Option>(ACTION_OPTIONS[0]);
  const [value, setValue] = useState('');
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!value.trim()) errs.value = 'Required';
    if (!seq.trim() || !Number.isInteger(Number(seq))) errs.seq = 'Integer required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      await http.post('/bgp/community-list-entries', {
        community_list_id: listId,
        seq: Number(seq),
        action: actionOpt.value,
        value,
        description: description || null,
      });
      toast.success('Entry added');
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
            {submitting ? 'Saving…' : 'Add'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <ColumnLayout columns={2}>
            <FormField label="Sequence" errorText={errors.seq}>
              <Input type="number" value={seq} onChange={({ detail }) => setSeq(detail.value)} />
            </FormField>
            <FormField label="Action">
              <Select
                selectedOption={actionOpt}
                onChange={({ detail }) => {
                  if (detail.selectedOption.value) setActionOpt(detail.selectedOption);
                }}
                options={ACTION_OPTIONS}
                expandToViewport
              />
            </FormField>
          </ColumnLayout>
          <FormField
            label="Value"
            errorText={errors.value}
            description='Standard: "65000:100". Extended: "target:65000:100", "origin:10.0.0.1:42".'
          >
            <Input value={value} onChange={({ detail }) => setValue(detail.value)} placeholder="65000:100" />
          </FormField>
          <FormField label="Description">
            <Input value={description} onChange={({ detail }) => setDescription(detail.value)} />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}
