// BGP prefix lists — ordered CIDR matchers. Each entry has a sequence
// number, permit/deny action, prefix, and optional ge/le length bounds.

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

type Family = 'v4' | 'v6';
type Action = 'permit' | 'deny';
type PrefixList = {
  id: string; name: string; family: Family; description: string | null;
};
type Entry = {
  id: string; prefix_list_id: string; seq: number; action: Action;
  prefix: string; ge: number | null; le: number | null;
  description: string | null;
};

const FAMILY_OPTIONS: SelectProps.Option[] = [
  { value: 'v4', label: 'IPv4' },
  { value: 'v6', label: 'IPv6' },
];
const ACTION_OPTIONS: SelectProps.Option[] = [
  { value: 'permit', label: 'Permit' },
  { value: 'deny', label: 'Deny' },
];

const MONO = { fontFamily: 'ui-monospace, monospace' } as const;

export function PrefixListsPanel({ canWrite }: { canWrite: boolean }) {
  const [selectedId, setSelectedId] = useState<string | null>(null);
  return (
    <ColumnLayout columns={2}>
      <ListsPanel canWrite={canWrite} selectedId={selectedId} onSelect={setSelectedId} />
      {selectedId
        ? <EntriesPanel listId={selectedId} canWrite={canWrite} />
        : (
          <Container>
            <Box padding="m" color="text-status-inactive">
              Pick a prefix list to see its entries.
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
    queryKey: ['prefix-lists'],
    queryFn: async () => (
      await http.get<{ items: PrefixList[] }>('/bgp/prefix-lists?page_size=200')
    ).data.items ?? [],
  });
  const lists = listsQ.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);

  async function remove(p: PrefixList) {
    if (!window.confirm(`Delete prefix list ${p.name}?`)) return;
    try {
      await http.delete(`/bgp/prefix-lists/${p.id}`);
      if (selectedId === p.id) onSelect(null);
      toast.success('Prefix list removed');
      await qc.invalidateQueries({ queryKey: ['prefix-lists'] });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  return (
    <>
      <Table<PrefixList>
        variant="container"
        loading={listsQ.isLoading}
        loadingText="Loading prefix lists…"
        items={lists}
        trackBy="id"
        selectionType="single"
        selectedItems={selectedId ? lists.filter((l) => l.id === selectedId) : []}
        onSelectionChange={({ detail }) => {
          const next = detail.selectedItems[0];
          onSelect(next ? next.id : null);
        }}
        ariaLabels={{
          selectionGroupLabel: 'Prefix list selection',
          itemSelectionLabel: (_d, item) => `Select ${item.name}`,
          allItemsSelectionLabel: () => 'select all',
        }}
        header={
          <Header
            counter={`(${lists.length})`}
            description="Ordered CIDR matchers consumed by route-map match clauses."
            actions={canWrite && (
              <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                New list
              </Button>
            )}
          >
            Prefix lists
          </Header>
        }
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (l) => l.name },
          {
            id: 'family', header: 'Family',
            cell: (l) => <Badge>{l.family}</Badge>,
            width: 100,
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (l: PrefixList) => (
              <Button iconName="remove" variant="inline-icon" onClick={() => remove(l)} ariaLabel={`Delete ${l.name}`} />
            ),
            width: 60,
          }] : []),
        ]}
        empty={<Box textAlign="center" color="inherit" padding="m">No prefix lists yet.</Box>}
      />
      {canWrite && (
        <Modal visible={createOpen} onDismiss={() => setCreateOpen(false)} header="New prefix list" size="medium">
          <ListForm onSaved={async () => {
            setCreateOpen(false);
            await qc.invalidateQueries({ queryKey: ['prefix-lists'] });
          }} />
        </Modal>
      )}
    </>
  );
}

function ListForm({ onSaved }: { onSaved: () => void }) {
  const [name, setName] = useState('');
  const [familyOpt, setFamilyOpt] = useState<SelectProps.Option>(FAMILY_OPTIONS[0]);
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
      await http.post('/bgp/prefix-lists', {
        name, family: familyOpt.value, description: description || null,
      });
      toast.success('Prefix list created');
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
            <Input value={name} onChange={({ detail }) => setName(detail.value)} placeholder="e.g. customer-prefixes" />
          </FormField>
          <FormField label="Address family">
            <Select
              selectedOption={familyOpt}
              onChange={({ detail }) => {
                if (detail.selectedOption.value) setFamilyOpt(detail.selectedOption);
              }}
              options={FAMILY_OPTIONS}
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
    queryKey: ['prefix-list-entries', listId],
    queryFn: async () => (
      await http.get<{ items: Entry[] }>(`/bgp/prefix-list-entries?prefix_list_id=${listId}&page_size=500`)
    ).data.items ?? [],
  });
  const entries = (entriesQ.data ?? []).slice().sort((a, b) => a.seq - b.seq);

  const [createOpen, setCreateOpen] = useState(false);

  async function remove(e: Entry) {
    if (!window.confirm(`Delete entry seq ${e.seq}?`)) return;
    try {
      await http.delete(`/bgp/prefix-list-entries/${e.id}`);
      toast.success('Entry removed');
      await qc.invalidateQueries({ queryKey: ['prefix-list-entries', listId] });
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
                Add entry
              </Button>
            )}
          >
            Entries
          </Header>
        }
        columnDefinitions={[
          { id: 'seq', header: 'Seq', cell: (e) => e.seq, width: 70 },
          {
            id: 'action', header: 'Action',
            cell: (e) => <Badge color={e.action === 'permit' ? 'green' : 'red'}>{e.action}</Badge>,
            width: 110,
          },
          { id: 'prefix', header: 'Prefix', cell: (e) => <span style={MONO}>{e.prefix}</span> },
          {
            id: 'bounds', header: 'ge / le',
            cell: (e) => (
              <span style={MONO}>
                {e.ge !== null ? `ge ${e.ge}` : ''}{e.ge !== null && e.le !== null ? ' / ' : ''}{e.le !== null ? `le ${e.le}` : ''}
                {e.ge === null && e.le === null ? '—' : ''}
              </span>
            ),
            width: 130,
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (e: Entry) => (
              <Button iconName="remove" variant="inline-icon" onClick={() => remove(e)} ariaLabel="Delete entry" />
            ),
            width: 60,
          }] : []),
        ]}
        empty={<Box textAlign="center" color="inherit" padding="m">No entries yet.</Box>}
      />
      {canWrite && (
        <Modal visible={createOpen} onDismiss={() => setCreateOpen(false)} header="Add prefix list entry" size="medium">
          <EntryForm listId={listId} existing={entries} onSaved={async () => {
            setCreateOpen(false);
            await qc.invalidateQueries({ queryKey: ['prefix-list-entries', listId] });
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
  const [prefix, setPrefix] = useState('');
  const [ge, setGe] = useState('');
  const [le, setLe] = useState('');
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!prefix.trim()) errs.prefix = 'Required';
    if (!seq.trim() || !Number.isInteger(Number(seq))) errs.seq = 'Integer required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      await http.post('/bgp/prefix-list-entries', {
        prefix_list_id: listId,
        seq: Number(seq),
        action: actionOpt.value,
        prefix,
        ge: ge ? Number(ge) : null,
        le: le ? Number(le) : null,
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
          <FormField label="Prefix" errorText={errors.prefix}>
            <Input value={prefix} onChange={({ detail }) => setPrefix(detail.value)} placeholder="e.g. 10.0.0.0/8" />
          </FormField>
          <ColumnLayout columns={2}>
            <FormField label="ge (optional)">
              <Input type="number" value={ge} onChange={({ detail }) => setGe(detail.value)} />
            </FormField>
            <FormField label="le (optional)">
              <Input type="number" value={le} onChange={({ detail }) => setLe(detail.value)} />
            </FormField>
          </ColumnLayout>
          <FormField label="Description">
            <Input value={description} onChange={({ detail }) => setDescription(detail.value)} />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}
