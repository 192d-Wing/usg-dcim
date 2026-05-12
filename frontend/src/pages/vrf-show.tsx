// VRF detail page — edit metadata (name, route target, description)
// and manage the BGP peer bindings that advertise this VRF.
//
// Reached via the Edit (pencil) icon on a VRF row in /ipam.

import { useMemo, useState } from 'react';
import { useNavigate, useParams } from 'react-router';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Spinner from '@cloudscape-design/components/spinner';
import Table from '@cloudscape-design/components/table';

import { http } from '@/lib/http';
import { hasCapability } from '@/lib/access-control-provider';

type Vrf = {
  id: string; fabric_id: string; name: string;
  route_target: string | null;
  description: string | null; is_default: boolean;
};
type Fabric = { id: string; name: string; slug: string };
type BgpPeer = {
  id: string; name: string; site_id: string;
  local_asn_id: string; peer_asn_id: string; peer_ip: string;
};
type Asn = { id: string; asn: number; name: string };
type AddressFamily = 'vpnv4' | 'vpnv6' | 'evpn';
type VrfBgpPeer = {
  id: string; vrf_id: string; bgp_peer_id: string;
  address_family: AddressFamily; rd: string | null; enabled: boolean;
};

const AF_OPTIONS: SelectProps.Option[] = [
  { value: 'vpnv4', label: 'VPNv4' },
  { value: 'vpnv6', label: 'VPNv6' },
  { value: 'evpn', label: 'EVPN' },
];

// RFC 4364 §4.2 — a Route Distinguisher (and Route Target, which uses
// the same syntax for the extended-community types we surface) has three
// forms:
//   Type 0: <2-byte ASN>:<4-byte value>   e.g. 65000:100
//   Type 1: <IPv4>:<2-byte value>         e.g. 10.1.1.1:100
//   Type 2: <4-byte ASN>:<2-byte value>   e.g. 4200000000:100
// Returns null when valid, or a human-readable error string.
export function validateRdRt(s: string): string | null {
  const v = s.trim();
  if (!v) return null;  // empty is allowed; callers gate on requiredness.

  // Type 1: dotted IPv4 on the left side.
  if (v.includes('.')) {
    const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3}):(\d+)$/.exec(v);
    if (!m) return 'Expected "<IPv4>:<value>" e.g. 10.1.1.1:100';
    const octets = [m[1], m[2], m[3], m[4]].map(Number);
    if (octets.some((o) => o > 255)) return 'IPv4 octet out of range (0–255)';
    const val = Number(m[5]);
    if (val > 65535) return 'Value too large (2-byte max for IPv4 form)';
    return null;
  }

  // Type 0 / Type 2: <ASN>:<value>.
  const m = /^(\d+):(\d+)$/.exec(v);
  if (!m) return 'Expected "<ASN>:<value>" e.g. 65000:100 or "<IPv4>:<value>"';
  const asn = Number(m[1]);
  const val = Number(m[2]);
  if (asn > 4_294_967_295) return 'ASN too large (4-byte max: 4294967295)';
  // 2-byte ASN → 4-byte value allowed. 4-byte ASN → 2-byte value only.
  if (asn <= 65_535) {
    if (val > 4_294_967_295) return 'Value too large (4-byte max for 2-byte ASN form)';
  } else if (val > 65_535) {
    return 'Value too large (2-byte max when ASN is 4-byte)';
  }
  return null;
}

export function VrfShowPage() {
  const { id = '' } = useParams<{ id: string }>();
  const nav = useNavigate();
  const canWrite = hasCapability('ipam:vrfs:update');

  const vrfQ = useQuery({
    queryKey: ['vrf', id],
    queryFn: async () => (await http.get<Vrf>(`/ipam/vrfs/${id}`)).data,
    enabled: !!id,
  });
  const fabricQ = useQuery({
    queryKey: ['vrf-fabric', vrfQ.data?.fabric_id],
    queryFn: async () => (await http.get<Fabric>(`/ipam/fabrics/${vrfQ.data!.fabric_id}`)).data,
    enabled: !!vrfQ.data,
  });

  if (vrfQ.isLoading) {
    return (
      <ContentLayout header={<Header variant="h1">Loading…</Header>}>
        <Box textAlign="center" padding="xl"><Spinner size="large" /></Box>
      </ContentLayout>
    );
  }
  if (vrfQ.isError || !vrfQ.data) {
    return (
      <ContentLayout header={<Header variant="h1">VRF</Header>}>
        <Box color="text-status-error">Failed to load VRF.</Box>
      </ContentLayout>
    );
  }

  const vrf = vrfQ.data;
  const fabric = fabricQ.data;

  // Compose description so the "default" affordance lives alongside the
  // fabric name instead of fighting the H1 title for attention.
  const headerDescription = [
    fabric ? `Fabric: ${fabric.name}` : null,
    vrf.is_default ? "Fabric's default VRF" : null,
  ].filter(Boolean).join(' · ');

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description={headerDescription || undefined}
          actions={
            <Button onClick={() => nav('/ipam')} iconName="angle-left">All VRFs</Button>
          }
        >
          {vrf.name}
        </Header>
      }
    >
      <SpaceBetween size="l">
        <VrfMetadataPanel vrf={vrf} canWrite={canWrite} />
        <BgpPeerBindingsPanel vrf={vrf} canWrite={canWrite} />
      </SpaceBetween>
    </ContentLayout>
  );
}

function VrfMetadataPanel({ vrf, canWrite }: Readonly<{ vrf: Vrf; canWrite: boolean }>) {
  const qc = useQueryClient();
  const [name, setName] = useState(vrf.name);
  const [routeTarget, setRouteTarget] = useState(vrf.route_target ?? '');
  const [description, setDescription] = useState(vrf.description ?? '');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const dirty =
    name !== vrf.name
    || routeTarget !== (vrf.route_target ?? '')
    || description !== (vrf.description ?? '');

  // Live-validate the Route Target on each keystroke so the operator
  // sees the error without having to hit Save first.
  const rtError = validateRdRt(routeTarget);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Name required';
    if (rtError) errs.route_target = rtError;
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      await http.patch(`/ipam/vrfs/${vrf.id}`, {
        name,
        route_target: routeTarget || null,
        description: description || null,
      });
      toast.success('VRF updated');
      await qc.invalidateQueries({ queryKey: ['vrf', vrf.id] });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Container header={<Header variant="h2">Metadata</Header>}>
      <form onSubmit={onSubmit}>
        <Form
          actions={canWrite && (
            <Button
              variant="primary"
              formAction="submit"
              loading={submitting}
              disabled={!dirty}
            >
              {submitting ? 'Saving…' : 'Save'}
            </Button>
          )}
        >
          <SpaceBetween size="m">
            <ColumnLayout columns={2}>
              <FormField label="Name" errorText={errors.name}>
                <Input
                  value={name}
                  onChange={({ detail }) => setName(detail.value)}
                  disabled={!canWrite}
                />
              </FormField>
              <FormField
                label="Route target"
                description="Imported/exported community shared across all peers advertising this VRF."
                errorText={rtError ?? errors.route_target}
              >
                <Input
                  value={routeTarget}
                  onChange={({ detail }) => setRouteTarget(detail.value)}
                  placeholder="e.g. 65000:100"
                  disabled={!canWrite}
                />
              </FormField>
            </ColumnLayout>
            <FormField label="Description">
              <Input
                value={description}
                onChange={({ detail }) => setDescription(detail.value)}
                disabled={!canWrite}
              />
            </FormField>
          </SpaceBetween>
        </Form>
      </form>
    </Container>
  );
}

function BgpPeerBindingsPanel({
  vrf, canWrite,
}: Readonly<{ vrf: Vrf; canWrite: boolean }>) {
  const qc = useQueryClient();
  const bindingsQ = useQuery({
    queryKey: ['vrf-bgp-peers', vrf.id],
    queryFn: async () => (
      await http.get<{ items: VrfBgpPeer[] }>(`/ipam/vrf-bgp-peers?vrf_id=${vrf.id}&page_size=200`)
    ).data.items ?? [],
  });
  const peersQ = useQuery({
    queryKey: ['bgp-peers'],
    queryFn: async () => (
      await http.get<{ items: BgpPeer[] }>(`/dns/bgp-peers?page_size=500`)
    ).data.items ?? [],
  });
  // Resolve a peer's local/peer ASN ids → AS numbers via the catalog.
  const asnsQ = useQuery({
    queryKey: ['bgp-asns'],
    queryFn: async () => (
      await http.get<{ items: Asn[] }>('/bgp/asns?page_size=500')
    ).data.items ?? [],
  });

  const bindings = bindingsQ.data ?? [];
  const peers = peersQ.data ?? [];
  const peersById = useMemo(() => new Map(peers.map((p) => [p.id, p])), [peers]);
  const asnsById = useMemo(
    () => new Map((asnsQ.data ?? []).map((a) => [a.id, a])),
    [asnsQ.data],
  );

  const [createOpen, setCreateOpen] = useState(false);
  const [editBinding, setEditBinding] = useState<VrfBgpPeer | null>(null);

  async function remove(b: VrfBgpPeer) {
    if (!globalThis.confirm('Remove this BGP peer binding?')) return;
    try {
      await http.delete(`/ipam/vrf-bgp-peers/${b.id}`);
      toast.success('Binding removed');
      await qc.invalidateQueries({ queryKey: ['vrf-bgp-peers', vrf.id] });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  return (
    <>
      <Table<VrfBgpPeer>
        variant="container"
        loading={bindingsQ.isLoading}
        loadingText="Loading bindings…"
        items={bindings}
        trackBy="id"
        header={
          <Header
            counter={`(${bindings.length})`}
            description={
              'A BGP peer can advertise this VRF on multiple address families '
              + '(VPNv4, VPNv6, EVPN); each binding has its own Route Distinguisher.'
            }
            actions={canWrite && (
              <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                Add peer
              </Button>
            )}
          >
            BGP peer bindings
          </Header>
        }
        columnDefinitions={[
          {
            id: 'peer', header: 'Peer',
            cell: (b) => {
              const p = peersById.get(b.bgp_peer_id);
              if (!p) return b.bgp_peer_id.slice(0, 8) + '…';
              const peerAsn = asnsById.get(p.peer_asn_id)?.asn;
              return (
                <SpaceBetween size="xxs">
                  <span>{p.name}</span>
                  <Box color="text-status-inactive" fontSize="body-s">
                    <span style={{ fontFamily: 'ui-monospace, monospace' }}>{p.peer_ip}</span>
                    {peerAsn ? ` · AS${peerAsn}` : ''}
                  </Box>
                </SpaceBetween>
              );
            },
          },
          {
            id: 'af', header: 'Address family',
            cell: (b) => <Badge>{b.address_family}</Badge>,
            width: 140,
          },
          {
            id: 'rd', header: 'Route distinguisher',
            cell: (b) => (
              <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>
                {b.rd ?? '—'}
              </span>
            ),
            width: 200,
          },
          {
            id: 'enabled', header: 'Enabled',
            cell: (b) => b.enabled ? <Badge color="green">on</Badge> : <Badge color="grey">off</Badge>,
            width: 100,
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (b: VrfBgpPeer) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button
                  iconName="edit"
                  variant="inline-icon"
                  ariaLabel="Edit binding"
                  onClick={() => setEditBinding(b)}
                />
                <Button
                  iconName="remove"
                  variant="inline-icon"
                  ariaLabel="Remove binding"
                  onClick={() => remove(b)}
                />
              </SpaceBetween>
            ),
            width: 90,
          }] : []),
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            <SpaceBetween size="xs">
              <b>No BGP peer bindings yet</b>
              <Box variant="p" color="inherit">
                Add a peer to advertise this VRF over MP-BGP.
              </Box>
            </SpaceBetween>
          </Box>
        }
      />
      {canWrite && (
        <Modal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header="Add BGP peer binding"
          size="medium"
        >
          <BindingForm
            vrf={vrf}
            peers={peers}
            asnsById={asnsById}
            existing={bindings}
            onSaved={async () => {
              setCreateOpen(false);
              await qc.invalidateQueries({ queryKey: ['vrf-bgp-peers', vrf.id] });
            }}
          />
        </Modal>
      )}
      <Modal
        visible={editBinding !== null}
        onDismiss={() => setEditBinding(null)}
        header="Edit BGP peer binding"
        size="medium"
      >
        {editBinding && (
          <BindingForm
            vrf={vrf}
            peers={peers}
            asnsById={asnsById}
            existing={bindings}
            binding={editBinding}
            onSaved={async () => {
              setEditBinding(null);
              await qc.invalidateQueries({ queryKey: ['vrf-bgp-peers', vrf.id] });
            }}
          />
        )}
      </Modal>
    </>
  );
}

function BindingForm({
  vrf, peers, asnsById, existing, binding, onSaved,
}: Readonly<{
  vrf: Vrf;
  peers: BgpPeer[];
  asnsById: Map<string, Asn>;
  existing: VrfBgpPeer[];
  binding?: VrfBgpPeer;
  onSaved: () => void;
}>) {
  const editing = !!binding;

  const peerOptions: SelectProps.Option[] = peers.map((p) => {
    const peerAsn = asnsById.get(p.peer_asn_id)?.asn;
    const asnSuffix = peerAsn ? ` · AS${peerAsn}` : '';
    return {
      value: p.id,
      label: p.name,
      description: `${p.peer_ip}${asnSuffix}`,
    };
  });

  const [peerOpt, setPeerOpt] = useState<SelectProps.Option | null>(() => {
    if (!binding) return null;
    return peerOptions.find((o) => o.value === binding.bgp_peer_id) ?? null;
  });
  const [afOpt, setAfOpt] = useState<SelectProps.Option>(
    binding
      ? AF_OPTIONS.find((o) => o.value === binding.address_family) ?? AF_OPTIONS[0]
      : AF_OPTIONS[0],
  );
  const [rd, setRd] = useState(binding?.rd ?? '');
  const [enabled, setEnabled] = useState(binding?.enabled ?? true);
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  // For create-mode, warn if the (peer, AF) tuple is already bound to
  // this VRF — the backend will 409 but we can save the round-trip.
  function collides(peerId: string, af: string): boolean {
    if (editing) return false;
    return existing.some(
      (e) => e.bgp_peer_id === peerId && e.address_family === af,
    );
  }

  const rdError = validateRdRt(rd);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!editing && !peerOpt) errs.peer = 'Pick a peer';
    if (!editing && peerOpt && collides(peerOpt.value!, String(afOpt.value))) {
      errs.peer = 'This peer is already bound on that address family';
    }
    if (rdError) errs.rd = rdError;
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      if (editing && binding) {
        await http.patch(`/ipam/vrf-bgp-peers/${binding.id}`, {
          rd: rd || null,
          enabled,
        });
        toast.success('Binding updated');
      } else {
        await http.post('/ipam/vrf-bgp-peers', {
          vrf_id: vrf.id,
          bgp_peer_id: peerOpt!.value,
          address_family: afOpt.value,
          rd: rd || null,
          enabled,
        });
        toast.success('Binding created');
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
            {submitting ? 'Saving…' : editing ? 'Save' : 'Add'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField
            label="BGP peer"
            errorText={errors.peer}
            description={editing ? 'Peer is immutable after creation.' : undefined}
          >
            <Select
              placeholder="Pick a peer"
              selectedOption={peerOpt}
              onChange={({ detail }) => setPeerOpt(detail.selectedOption)}
              options={peerOptions}
              disabled={editing}
              expandToViewport
            />
          </FormField>
          <FormField
            label="Address family"
            description={
              editing
                ? 'Address family is immutable after creation.'
                : 'VPNv4 / VPNv6 for L3VPN unicast; EVPN for overlay L2/L3.'
            }
          >
            <Select
              selectedOption={afOpt}
              onChange={({ detail }) => {
                if (detail.selectedOption.value) setAfOpt(detail.selectedOption);
              }}
              options={AF_OPTIONS}
              disabled={editing}
              expandToViewport
            />
          </FormField>
          <FormField
            label="Route distinguisher"
            description="Per (VRF, peer, AF) — disambiguates routes in the global BGP table."
            errorText={rdError ?? errors.rd}
          >
            <Input
              value={rd}
              onChange={({ detail }) => setRd(detail.value)}
              placeholder="e.g. 65000:100"
            />
          </FormField>
          <FormField label="Enabled">
            <Select
              selectedOption={enabled ? { value: 'on', label: 'Enabled' } : { value: 'off', label: 'Disabled' }}
              onChange={({ detail }) => setEnabled(detail.selectedOption.value === 'on')}
              options={[
                { value: 'on', label: 'Enabled' },
                { value: 'off', label: 'Disabled' },
              ]}
            />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}
