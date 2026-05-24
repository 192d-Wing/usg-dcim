// TCP AO (RFC 5925) — modern replacement for the BGP MD5 password.
// A key chain holds rotatable keys; peers reference the chain (not
// individual keys) so secrets can roll over without re-pointing.

import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

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

type Algorithm = 'hmac-sha1-96' | 'aes-128-cmac';
type KeyChain = { id: string; name: string; description: string | null };
type AoKey = {
  id: string;
  key_chain_id: string;
  key_id: number;
  send_id: number;
  recv_id: number;
  algorithm: Algorithm;
  secret: string;
  valid_from: string | null;
  valid_to: string | null;
  description: string | null;
};

const ALG_OPTIONS: SelectProps.Option[] = [
  { value: 'hmac-sha1-96', label: 'HMAC-SHA-1-96' },
  { value: 'aes-128-cmac', label: 'AES-128-CMAC' },
];

export function TcpAoPanel({ canWrite }: { canWrite: boolean }) {
  const [selectedChainId, setSelectedChainId] = useState<string | null>(null);

  return (
    <ColumnLayout columns={2}>
      <KeyChainsPanel
        canWrite={canWrite}
        selectedChainId={selectedChainId}
        onSelect={setSelectedChainId}
      />
      {selectedChainId
        ? <KeysPanel chainId={selectedChainId} canWrite={canWrite} />
        : (
          <Container>
            <Box padding="m" color="text-status-inactive">
              Pick a key chain to see its keys.
            </Box>
          </Container>
        )}
    </ColumnLayout>
  );
}

function KeyChainsPanel({
  canWrite, selectedChainId, onSelect,
}: Readonly<{
  canWrite: boolean;
  selectedChainId: string | null;
  onSelect: (id: string | null) => void;
}>) {
  const qc = useQueryClient();
  const chainsQ = useQuery({
    queryKey: ['tcp-ao-chains'],
    queryFn: async () => (
      await http.get<{ items: KeyChain[] }>('/bgp/tcp-ao-key-chains?page_size=200')
    ).data.items ?? [],
  });
  const chains = chainsQ.data ?? [];

  const [createOpen, setCreateOpen] = useState(false);

  async function remove(c: KeyChain) {
    if (!window.confirm(`Delete key chain ${c.name}?`)) return;
    try {
      await http.delete(`/bgp/tcp-ao-key-chains/${c.id}`);
      if (selectedChainId === c.id) onSelect(null);
      toast.success('Key chain removed');
      await qc.invalidateQueries({ queryKey: ['tcp-ao-chains'] });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  return (
    <>
      <Table<KeyChain>
        variant="container"
        loading={chainsQ.isLoading}
        loadingText="Loading key chains…"
        items={chains}
        trackBy="id"
        selectionType="single"
        selectedItems={selectedChainId ? chains.filter((c) => c.id === selectedChainId) : []}
        onSelectionChange={({ detail }) => {
          const next = detail.selectedItems[0];
          onSelect(next ? next.id : null);
        }}
        ariaLabels={{
          selectionGroupLabel: 'Key chain selection',
          itemSelectionLabel: (_d, item) => `Select ${item.name}`,
          allItemsSelectionLabel: () => 'select all',
        }}
        header={
          <Header
            counter={`(${chains.length})`}
            description="A chain groups rotatable keys so a peer can swap secrets without bringing the session down."
            actions={canWrite && (
              <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                New chain
              </Button>
            )}
          >
            TCP AO key chains
          </Header>
        }
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (c) => c.name },
          {
            id: 'description', header: 'Description',
            cell: (c) => (
              <Box variant="span" color="text-status-inactive" fontSize="body-s">
                {c.description ?? '—'}
              </Box>
            ),
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (c: KeyChain) => (
              <Button
                iconName="remove"
                variant="inline-icon"
                onClick={() => remove(c)}
                ariaLabel={`Delete ${c.name}`}
              />
            ),
            width: 60,
          }] : []),
        ]}
        empty={<Box textAlign="center" color="inherit" padding="m">No key chains yet.</Box>}
      />
      {canWrite && (
        <Modal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header="New TCP AO key chain"
          size="medium"
        >
          <KeyChainForm
            onSaved={async () => {
              setCreateOpen(false);
              await qc.invalidateQueries({ queryKey: ['tcp-ao-chains'] });
            }}
          />
        </Modal>
      )}
    </>
  );
}

function KeyChainForm({ onSaved }: { onSaved: () => void }) {
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
      await http.post('/bgp/tcp-ao-key-chains', {
        name, description: description || null,
      });
      toast.success('Key chain created');
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
            <Input value={name} onChange={({ detail }) => setName(detail.value)} placeholder="e.g. prod-bgp-keys" />
          </FormField>
          <FormField label="Description">
            <Input value={description} onChange={({ detail }) => setDescription(detail.value)} />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}

function KeysPanel({
  chainId, canWrite,
}: Readonly<{ chainId: string; canWrite: boolean }>) {
  const qc = useQueryClient();
  const keysQ = useQuery({
    queryKey: ['tcp-ao-keys', chainId],
    queryFn: async () => (
      await http.get<{ items: AoKey[] }>(`/bgp/tcp-ao-keys?key_chain_id=${chainId}&page_size=200`)
    ).data.items ?? [],
  });
  const keys = (keysQ.data ?? []).slice().sort((a, b) => a.key_id - b.key_id);

  const [createOpen, setCreateOpen] = useState(false);
  const [rotateOpen, setRotateOpen] = useState(false);

  async function remove(k: AoKey) {
    if (!window.confirm(`Delete key id ${k.key_id}?`)) return;
    try {
      await http.delete(`/bgp/tcp-ao-keys/${k.id}`);
      toast.success('Key removed');
      await qc.invalidateQueries({ queryKey: ['tcp-ao-keys', chainId] });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  // Display the lifetime as the operator's local time. Null = "always".
  function fmt(d: string | null): string {
    if (!d) return '—';
    return new Date(d).toLocaleString();
  }

  return (
    <>
      <Table<AoKey>
        variant="container"
        loading={keysQ.isLoading}
        loadingText="Loading keys…"
        items={keys}
        trackBy="id"
        header={
          <Header
            counter={`(${keys.length})`}
            description="Each key is valid for a sliding window. Use Generate rotation to lay down a year's worth of keys in one shot."
            actions={canWrite && (
              <SpaceBetween size="xs" direction="horizontal">
                <Button iconName="refresh" onClick={() => setRotateOpen(true)}>
                  Generate rotation
                </Button>
                <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                  Add key
                </Button>
              </SpaceBetween>
            )}
          >
            Keys
          </Header>
        }
        columnDefinitions={[
          { id: 'key_id', header: 'Key ID', cell: (k) => k.key_id, width: 80 },
          { id: 'send_id', header: 'Send ID', cell: (k) => k.send_id, width: 80 },
          { id: 'recv_id', header: 'Recv ID', cell: (k) => k.recv_id, width: 80 },
          { id: 'algorithm', header: 'Algorithm', cell: (k) => k.algorithm, width: 140 },
          { id: 'valid_from', header: 'Valid from', cell: (k) => fmt(k.valid_from) },
          { id: 'valid_to', header: 'Valid to', cell: (k) => fmt(k.valid_to) },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (k: AoKey) => (
              <Button
                iconName="remove"
                variant="inline-icon"
                onClick={() => remove(k)}
                ariaLabel="Delete key"
              />
            ),
            width: 60,
          }] : []),
        ]}
        empty={<Box textAlign="center" color="inherit" padding="m">No keys yet.</Box>}
      />
      {canWrite && (
        <>
          <Modal
            visible={createOpen}
            onDismiss={() => setCreateOpen(false)}
            header="Add TCP AO key"
            size="medium"
          >
            <KeyForm
              chainId={chainId}
              onSaved={async () => {
                setCreateOpen(false);
                await qc.invalidateQueries({ queryKey: ['tcp-ao-keys', chainId] });
              }}
            />
          </Modal>
          <Modal
            visible={rotateOpen}
            onDismiss={() => setRotateOpen(false)}
            header="Generate rotating key set"
            size="medium"
          >
            <RotationForm
              chainId={chainId}
              onSaved={async (n) => {
                setRotateOpen(false);
                toast.success(`Generated ${n} keys`);
                await qc.invalidateQueries({ queryKey: ['tcp-ao-keys', chainId] });
              }}
            />
          </Modal>
        </>
      )}
    </>
  );
}

function RotationForm({
  chainId, onSaved,
}: Readonly<{ chainId: string; onSaved: (count: number) => void }>) {
  const [start, setStart] = useState(toLocalInput(new Date()));
  const [count, setCount] = useState('12');
  const [daysPerKey, setDaysPerKey] = useState('30');
  const [algorithmOpt, setAlgorithmOpt] = useState<SelectProps.Option>(ALG_OPTIONS[0]);
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const n = Number(count);
  const d = Number(daysPerKey);
  const totalDays = Number.isInteger(n) && Number.isInteger(d) ? n * d : 0;

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!Number.isInteger(n) || n < 1 || n > 365) errs.count = '1..365';
    if (!Number.isInteger(d) || d < 1 || d > 366) errs.days = '1..366';
    if (!start) errs.start = 'Required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      const startIso = new Date(start).toISOString();
      const r = await http.post(`/bgp/tcp-ao-key-chains/${chainId}/rotate-batch`, {
        start: startIso,
        count: n,
        days_per_key: d,
        algorithm: algorithmOpt.value,
      });
      onSaved(r.data.length);
    } catch (err: any) {
      const detail = err?.response?.data?.error?.message ?? err?.message;
      toast.error(detail ?? 'failed');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form onSubmit={onSubmit}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={submitting}>
            {submitting ? 'Generating…' : `Generate ${n || 0} keys`}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <Box>
            Lays down a sequence of pre-shared keys with consecutive
            lifetime windows. Each key gets a fresh 256-bit secret
            generated server-side. Key IDs auto-increment from the
            highest existing key in this chain.
          </Box>
          <FormField label="Start" description="First key's valid-from. Subsequent keys chain after it.">
            <input
              type="datetime-local"
              value={start}
              onChange={(e) => setStart(e.target.value)}
              style={{
                width: '100%',
                padding: '4px 8px',
                fontSize: 14,
                fontFamily: 'inherit',
                borderRadius: 8,
                border: '1px solid var(--color-border-input-default-317xk5)',
                background: 'var(--color-background-input-default-vchpem, transparent)',
                color: 'inherit',
              }}
            />
          </FormField>
          <ColumnLayout columns={2}>
            <FormField
              label="Number of keys"
              description="Default 12 covers a year at 30 days each."
              errorText={errors.count}
            >
              <Input type="number" value={count} onChange={({ detail }) => setCount(detail.value)} />
            </FormField>
            <FormField
              label="Days per key"
              description="Rotation cadence — typically 30 days."
              errorText={errors.days}
            >
              <Input type="number" value={daysPerKey} onChange={({ detail }) => setDaysPerKey(detail.value)} />
            </FormField>
          </ColumnLayout>
          <FormField label="Algorithm">
            <Select
              selectedOption={algorithmOpt}
              onChange={({ detail }) => {
                if (detail.selectedOption.value) setAlgorithmOpt(detail.selectedOption);
              }}
              options={ALG_OPTIONS}
              expandToViewport
            />
          </FormField>
          <Box color="text-status-inactive" fontSize="body-s">
            Total coverage: {totalDays} day{totalDays === 1 ? '' : 's'}{' '}
            ({(totalDays / 30).toFixed(1)} months).
          </Box>
        </SpaceBetween>
      </Form>
    </form>
  );
}

// 32 random bytes = 256 bits of entropy = 64 hex characters. User asked
// for at least 128 bits; doubling that leaves plenty of margin while
// staying well under the 512-char column ceiling.
function randomSecretHex(bytes = 32): string {
  const buf = new Uint8Array(bytes);
  globalThis.crypto.getRandomValues(buf);
  return Array.from(buf).map((b) => b.toString(16).padStart(2, '0')).join('');
}

// Format a Date as the `YYYY-MM-DDTHH:MM` string that <input type=
// "datetime-local"> expects. Strips seconds/milliseconds/timezone —
// the browser treats the value as local time.
function toLocalInput(d: Date): string {
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
    + `T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function KeyForm({ chainId, onSaved }: { chainId: string; onSaved: () => void }) {
  const [keyId, setKeyId] = useState('1');
  const [sendId, setSendId] = useState('1');
  const [recvId, setRecvId] = useState('1');
  const [algorithmOpt, setAlgorithmOpt] = useState<SelectProps.Option>(ALG_OPTIONS[0]);
  const [secret, setSecret] = useState('');
  // Default the lifetime to now → now+30d so a hand-rolled key gets
  // the same rotation cadence as the 12-key batch generator. Operators
  // who want a different window just edit the fields before submit.
  const now = new Date();
  const in30 = new Date(now.getTime() + 30 * 24 * 60 * 60 * 1000);
  const [validFrom, setValidFrom] = useState(toLocalInput(now));
  const [validTo, setValidTo] = useState(toLocalInput(in30));
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  function generate() {
    setSecret(randomSecretHex(32));
    toast.success('Generated 256-bit secret');
  }

  function localToIso(local: string): string | null {
    if (!local) return null;
    // datetime-local omits seconds + timezone; treat as local time
    // and let the Date constructor attach the browser's offset.
    const d = new Date(local);
    return Number.isNaN(d.getTime()) ? null : d.toISOString();
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!secret.trim()) errs.secret = 'Secret required';
    for (const [field, v] of [['key_id', keyId], ['send_id', sendId], ['recv_id', recvId]] as const) {
      const n = Number(v);
      if (!Number.isInteger(n) || n < 0 || n > 65535) errs[field] = 'Integer 0..65535';
    }
    if (validFrom && validTo) {
      const from = new Date(validFrom).getTime();
      const to = new Date(validTo).getTime();
      if (!(to > from)) errs.valid_to = 'valid_to must be after valid_from';
    }
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      await http.post('/bgp/tcp-ao-keys', {
        key_chain_id: chainId,
        key_id: Number(keyId),
        send_id: Number(sendId),
        recv_id: Number(recvId),
        algorithm: algorithmOpt.value,
        secret,
        valid_from: localToIso(validFrom),
        valid_to: localToIso(validTo),
        description: description || null,
      });
      toast.success('Key added');
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
          <ColumnLayout columns={3}>
            <FormField label="Key ID" errorText={errors.key_id}>
              <Input type="number" value={keyId} onChange={({ detail }) => setKeyId(detail.value)} />
            </FormField>
            <FormField label="Send ID" errorText={errors.send_id}>
              <Input type="number" value={sendId} onChange={({ detail }) => setSendId(detail.value)} />
            </FormField>
            <FormField label="Recv ID" errorText={errors.recv_id}>
              <Input type="number" value={recvId} onChange={({ detail }) => setRecvId(detail.value)} />
            </FormField>
          </ColumnLayout>
          <FormField label="Algorithm">
            <Select
              selectedOption={algorithmOpt}
              onChange={({ detail }) => {
                if (detail.selectedOption.value) setAlgorithmOpt(detail.selectedOption);
              }}
              options={ALG_OPTIONS}
              expandToViewport
            />
          </FormField>
          <FormField
            label="Secret"
            description="256-bit cryptographic secret. Use Generate for a fresh value."
            errorText={errors.secret}
          >
            <SpaceBetween size="xs" direction="horizontal">
              <div style={{ flex: 1, minWidth: 280 }}>
                <Input
                  type="password"
                  value={secret}
                  onChange={({ detail }) => setSecret(detail.value)}
                  placeholder="Click Generate or paste an existing key"
                />
              </div>
              <Button iconName="refresh" onClick={generate}>
                Generate
              </Button>
            </SpaceBetween>
          </FormField>
          <ColumnLayout columns={2}>
            <FormField
              label="Valid from (optional)"
              description="Earliest moment this key is in effect. Leave blank for no lower bound."
            >
              {/* Native datetime-local picker — Cloudscape's DatePicker
                is date-only, so we use the browser control for now. */}
              <input
                type="datetime-local"
                value={validFrom}
                onChange={(e) => setValidFrom(e.target.value)}
                style={{
                  width: '100%',
                  padding: '4px 8px',
                  fontSize: 14,
                  fontFamily: 'inherit',
                  borderRadius: 8,
                  border: '1px solid var(--color-border-input-default-317xk5)',
                  background: 'var(--color-background-input-default-vchpem, transparent)',
                  color: 'inherit',
                }}
              />
            </FormField>
            <FormField
              label="Valid to (optional)"
              description="Latest moment this key is in effect."
              errorText={errors.valid_to}
            >
              <input
                type="datetime-local"
                value={validTo}
                onChange={(e) => setValidTo(e.target.value)}
                style={{
                  width: '100%',
                  padding: '4px 8px',
                  fontSize: 14,
                  fontFamily: 'inherit',
                  borderRadius: 8,
                  border: '1px solid var(--color-border-input-default-317xk5)',
                  background: 'var(--color-background-input-default-vchpem, transparent)',
                  color: 'inherit',
                }}
              />
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
