// BGP route maps — ordered policy entries with match + action + set
// clauses. Match clauses can reference Prefix List + Community List by
// id (FK) and an AS-path regex inline.

import { useMemo, useState } from 'react';
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

type Action = 'permit' | 'deny';
type RouteMap = { id: string; name: string; description: string | null };
type PrefixList = { id: string; name: string; family: 'v4' | 'v6' };
type CommunityList = { id: string; name: string };
type Entry = {
  id: string;
  route_map_id: string;
  seq: number;
  action: Action;
  match_prefix_list_id: string | null;
  match_community_list_id: string | null;
  match_as_path_regex: string | null;
  set_local_pref: number | null;
  set_med: number | null;
  set_community: string | null;
  description: string | null;
};

const ACTION_OPTIONS: SelectProps.Option[] = [
  { value: 'permit', label: 'Permit' },
  { value: 'deny', label: 'Deny' },
];
const MONO = { fontFamily: 'ui-monospace, monospace' } as const;
const NONE = '__none__';

export function RouteMapsPanel({ canWrite }: { canWrite: boolean }) {
  const [selectedId, setSelectedId] = useState<string | null>(null);
  return (
    <ColumnLayout columns={2}>
      <MapsPanel canWrite={canWrite} selectedId={selectedId} onSelect={setSelectedId} />
      {selectedId
        ? <EntriesPanel mapId={selectedId} canWrite={canWrite} />
        : (
          <Container>
            <Box padding="m" color="text-status-inactive">
              Pick a route map to see its entries.
            </Box>
          </Container>
        )}
    </ColumnLayout>
  );
}

function MapsPanel({
  canWrite, selectedId, onSelect,
}: Readonly<{ canWrite: boolean; selectedId: string | null; onSelect: (id: string | null) => void }>) {
  const qc = useQueryClient();
  const mapsQ = useQuery({
    queryKey: ['route-maps'],
    queryFn: async () => (
      await http.get<{ items: RouteMap[] }>('/bgp/route-maps?page_size=200')
    ).data.items ?? [],
  });
  const maps = mapsQ.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);

  async function remove(m: RouteMap) {
    if (!window.confirm(`Delete route map ${m.name}?`)) return;
    try {
      await http.delete(`/bgp/route-maps/${m.id}`);
      if (selectedId === m.id) onSelect(null);
      toast.success('Route map removed');
      await qc.invalidateQueries({ queryKey: ['route-maps'] });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  return (
    <>
      <Table<RouteMap>
        variant="container"
        loading={mapsQ.isLoading}
        loadingText="Loading route maps…"
        items={maps}
        trackBy="id"
        selectionType="single"
        selectedItems={selectedId ? maps.filter((m) => m.id === selectedId) : []}
        onSelectionChange={({ detail }) => {
          const next = detail.selectedItems[0];
          onSelect(next ? next.id : null);
        }}
        ariaLabels={{
          selectionGroupLabel: 'Route map selection',
          itemSelectionLabel: (_d, item) => `Select ${item.name}`,
          allItemsSelectionLabel: () => 'select all',
        }}
        header={
          <Header
            counter={`(${maps.length})`}
            description="Ordered policy rules attached to a peer's import or export direction."
            actions={canWrite && (
              <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                New map
              </Button>
            )}
          >
            Route maps
          </Header>
        }
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (m) => m.name },
          {
            id: 'description', header: 'Description',
            cell: (m) => (
              <Box variant="span" color="text-status-inactive" fontSize="body-s">
                {m.description ?? '—'}
              </Box>
            ),
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (m: RouteMap) => (
              <Button iconName="remove" variant="inline-icon" onClick={() => remove(m)} ariaLabel={`Delete ${m.name}`} />
            ),
            width: 60,
          }] : []),
        ]}
        empty={<Box textAlign="center" color="inherit" padding="m">No route maps yet.</Box>}
      />
      {canWrite && (
        <Modal visible={createOpen} onDismiss={() => setCreateOpen(false)} header="New route map" size="medium">
          <MapForm onSaved={async () => {
            setCreateOpen(false);
            await qc.invalidateQueries({ queryKey: ['route-maps'] });
          }} />
        </Modal>
      )}
    </>
  );
}

function MapForm({ onSaved }: { onSaved: () => void }) {
  const [name, setName] = useState('');
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
      await http.post('/bgp/route-maps', {
        name, description: description || null,
      });
      toast.success('Route map created');
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
            <Input value={name} onChange={({ detail }) => setName(detail.value)} placeholder="e.g. customer-in" />
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
  mapId, canWrite,
}: Readonly<{ mapId: string; canWrite: boolean }>) {
  const qc = useQueryClient();
  const entriesQ = useQuery({
    queryKey: ['route-map-entries', mapId],
    queryFn: async () => (
      await http.get<{ items: Entry[] }>(`/bgp/route-map-entries?route_map_id=${mapId}&page_size=500`)
    ).data.items ?? [],
  });
  const entries = (entriesQ.data ?? []).slice().sort((a, b) => a.seq - b.seq);
  const prefixListsQ = useQuery({
    queryKey: ['prefix-lists'],
    queryFn: async () => (
      await http.get<{ items: PrefixList[] }>('/bgp/prefix-lists?page_size=500')
    ).data.items ?? [],
  });
  const communityListsQ = useQuery({
    queryKey: ['community-lists'],
    queryFn: async () => (
      await http.get<{ items: CommunityList[] }>('/bgp/community-lists?page_size=500')
    ).data.items ?? [],
  });
  const prefixLists = prefixListsQ.data ?? [];
  const communityLists = communityListsQ.data ?? [];
  const prefixById = useMemo(() => new Map(prefixLists.map((p) => [p.id, p])), [prefixLists]);
  const communityById = useMemo(() => new Map(communityLists.map((c) => [c.id, c])), [communityLists]);

  const [createOpen, setCreateOpen] = useState(false);

  async function remove(e: Entry) {
    if (!window.confirm(`Delete entry seq ${e.seq}?`)) return;
    try {
      await http.delete(`/bgp/route-map-entries/${e.id}`);
      toast.success('Entry removed');
      await qc.invalidateQueries({ queryKey: ['route-map-entries', mapId] });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  function formatMatch(e: Entry): string {
    const parts: string[] = [];
    if (e.match_prefix_list_id) {
      parts.push(`prefix ${prefixById.get(e.match_prefix_list_id)?.name ?? '?'}`);
    }
    if (e.match_community_list_id) {
      parts.push(`community ${communityById.get(e.match_community_list_id)?.name ?? '?'}`);
    }
    if (e.match_as_path_regex) parts.push(`as-path ${e.match_as_path_regex}`);
    return parts.length ? parts.join(' · ') : '—';
  }
  function formatSet(e: Entry): string {
    const parts: string[] = [];
    if (e.set_local_pref !== null) parts.push(`local-pref ${e.set_local_pref}`);
    if (e.set_med !== null) parts.push(`med ${e.set_med}`);
    if (e.set_community) parts.push(`community ${e.set_community}`);
    return parts.length ? parts.join(' · ') : '—';
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
          {
            id: 'match', header: 'Match',
            cell: (e) => <span style={MONO}>{formatMatch(e)}</span>,
          },
          {
            id: 'set', header: 'Set',
            cell: (e) => <span style={MONO}>{formatSet(e)}</span>,
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
        <Modal visible={createOpen} onDismiss={() => setCreateOpen(false)} header="Add route map entry" size="large">
          <EntryForm
            mapId={mapId}
            existing={entries}
            prefixLists={prefixLists}
            communityLists={communityLists}
            onSaved={async () => {
              setCreateOpen(false);
              await qc.invalidateQueries({ queryKey: ['route-map-entries', mapId] });
            }}
          />
        </Modal>
      )}
    </>
  );
}

function EntryForm({
  mapId, existing, prefixLists, communityLists, onSaved,
}: Readonly<{
  mapId: string;
  existing: Entry[];
  prefixLists: PrefixList[];
  communityLists: CommunityList[];
  onSaved: () => void;
}>) {
  const nextSeq = existing.length === 0
    ? 10
    : Math.max(...existing.map((e) => e.seq)) + 10;
  const [seq, setSeq] = useState(String(nextSeq));
  const [actionOpt, setActionOpt] = useState<SelectProps.Option>(ACTION_OPTIONS[0]);
  const [matchPrefixOpt, setMatchPrefixOpt] = useState<SelectProps.Option>({ value: NONE, label: '(none)' });
  const [matchCommunityOpt, setMatchCommunityOpt] = useState<SelectProps.Option>({ value: NONE, label: '(none)' });
  const [matchAsPath, setMatchAsPath] = useState('');
  const [setLocalPref, setSetLocalPref] = useState('');
  const [setMed, setSetMed] = useState('');
  const [setCommunity, setSetCommunity] = useState('');
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const prefixOptions: SelectProps.Option[] = [
    { value: NONE, label: '(none)' },
    ...prefixLists.map((p) => ({ value: p.id, label: `${p.name} (${p.family})` })),
  ];
  const communityOptions: SelectProps.Option[] = [
    { value: NONE, label: '(none)' },
    ...communityLists.map((c) => ({ value: c.id, label: c.name })),
  ];

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!seq.trim() || !Number.isInteger(Number(seq))) errs.seq = 'Integer required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      await http.post('/bgp/route-map-entries', {
        route_map_id: mapId,
        seq: Number(seq),
        action: actionOpt.value,
        match_prefix_list_id: matchPrefixOpt.value === NONE ? null : matchPrefixOpt.value,
        match_community_list_id: matchCommunityOpt.value === NONE ? null : matchCommunityOpt.value,
        match_as_path_regex: matchAsPath || null,
        set_local_pref: setLocalPref ? Number(setLocalPref) : null,
        set_med: setMed ? Number(setMed) : null,
        set_community: setCommunity || null,
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

          <Box variant="awsui-key-label">Match clauses (all optional)</Box>
          <ColumnLayout columns={2}>
            <FormField label="Prefix list">
              <Select
                selectedOption={matchPrefixOpt}
                onChange={({ detail }) => setMatchPrefixOpt(detail.selectedOption)}
                options={prefixOptions}
                expandToViewport
              />
            </FormField>
            <FormField label="Community list">
              <Select
                selectedOption={matchCommunityOpt}
                onChange={({ detail }) => setMatchCommunityOpt(detail.selectedOption)}
                options={communityOptions}
                expandToViewport
              />
            </FormField>
          </ColumnLayout>
          <FormField label="AS-path regex">
            <Input
              value={matchAsPath}
              onChange={({ detail }) => setMatchAsPath(detail.value)}
              placeholder="e.g. _65000_"
            />
          </FormField>

          <Box variant="awsui-key-label">Set clauses (all optional)</Box>
          <ColumnLayout columns={2}>
            <FormField label="Local pref">
              <Input type="number" value={setLocalPref} onChange={({ detail }) => setSetLocalPref(detail.value)} />
            </FormField>
            <FormField label="MED">
              <Input type="number" value={setMed} onChange={({ detail }) => setSetMed(detail.value)} />
            </FormField>
          </ColumnLayout>
          <FormField
            label="Community"
            description="Free-form: single literal community, or space-separated list to set."
          >
            <Input
              value={setCommunity}
              onChange={({ detail }) => setSetCommunity(detail.value)}
              placeholder="65000:100 65000:200"
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
