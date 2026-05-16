// BGP — top-level IPAM tab covering the cross-cutting BGP entities
// shared by recursive DNS anycast announcements and VRF MP-BGP.
//
// The "Peers" sub-tab is the only one with backing storage today; the
// other sub-tabs are placeholders showing what each surface will hold
// when its model + endpoints land. Keeping the navigation in place now
// avoids reflowing the IA every time one of these features ships.

import { useMemo, useState } from 'react';
import { useList } from '@refinedev/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

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
import Tabs from '@cloudscape-design/components/tabs';

import { http } from '@/lib/http';
import { AsnsPanel } from './bgp-asns';
import { TcpAoPanel } from './bgp-tcp-ao';
import { PrefixListsPanel } from './bgp-prefix-lists';
import { CommunitiesPanel } from './bgp-communities';
import { RouteMapsPanel } from './bgp-route-maps';

type Site = { id: string; code: string; name: string };
type BgpPeer = {
  id: string;
  name: string;
  site_id: string;
  local_asn_id: string;
  peer_asn_id: string;
  peer_ip: string;
  tcp_ao_key_chain_id: string | null;
  enabled: boolean;
};
type Asn = { id: string; asn: number; name: string };
type KeyChain = { id: string; name: string };

const MONO = { fontFamily: 'ui-monospace, monospace' } as const;

export function BgpPeersTab({ canWrite }: { canWrite: boolean }) {
  const [activeTab, setActiveTab] = useState<string>('peers');
  return (
    <Tabs
      activeTabId={activeTab}
      onChange={({ detail }) => setActiveTab(detail.activeTabId)}
      tabs={[
        {
          id: 'peers',
          label: 'Peers',
          content: <PeersPanel canWrite={canWrite} />,
        },
        { id: 'asns', label: 'ASNs', content: <AsnsPanel canWrite={canWrite} /> },
        { id: 'ao', label: 'TCP AO keys', content: <TcpAoPanel canWrite={canWrite} /> },
        { id: 'prefix-lists', label: 'Prefix lists', content: <PrefixListsPanel canWrite={canWrite} /> },
        { id: 'communities', label: 'Communities', content: <CommunitiesPanel canWrite={canWrite} /> },
        { id: 'route-maps', label: 'Route maps', content: <RouteMapsPanel canWrite={canWrite} /> },
      ]}
    />
  );
}


function PeersPanel({ canWrite }: { canWrite: boolean }) {
  const qc = useQueryClient();
  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 500 } });
  const sites = sitesRes.result.data ?? [];
  const sitesById = useMemo(() => new Map(sites.map((s) => [s.id, s])), [sites]);

  const peersQ = useQuery({
    queryKey: ['bgp-peers'],
    queryFn: async () => (
      await http.get<{ items: BgpPeer[] }>('/dns/bgp-peers?page_size=500')
    ).data.items ?? [],
  });
  const asnsQ = useQuery({
    queryKey: ['bgp-asns'],
    queryFn: async () => (
      await http.get<{ items: Asn[] }>('/bgp/asns?page_size=500')
    ).data.items ?? [],
  });
  const chainsQ = useQuery({
    queryKey: ['tcp-ao-chains'],
    queryFn: async () => (
      await http.get<{ items: KeyChain[] }>('/bgp/tcp-ao-key-chains?page_size=500')
    ).data.items ?? [],
  });
  const peers = peersQ.data ?? [];
  const asns = asnsQ.data ?? [];
  const chains = chainsQ.data ?? [];
  const asnsById = useMemo(() => new Map(asns.map((a) => [a.id, a])), [asns]);
  const chainsById = useMemo(() => new Map(chains.map((c) => [c.id, c])), [chains]);

  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<BgpPeer | null>(null);

  async function remove(p: BgpPeer) {
    if (!window.confirm(`Delete BGP peer ${p.name}?`)) return;
    try {
      await http.delete(`/dns/bgp-peers/${p.id}`);
      toast.success('BGP peer removed');
      await qc.invalidateQueries({ queryKey: ['bgp-peers'] });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  function fmtAsn(id: string): string {
    const a = asnsById.get(id);
    if (!a) return id.slice(0, 8) + '…';
    return `AS${a.asn} (${a.name})`;
  }

  return (
    <>
      <Table<BgpPeer>
        variant="container"
        loading={peersQ.isLoading}
        loadingText="Loading BGP peers…"
        items={peers}
        trackBy="id"
        header={
          <Header
            counter={`(${peers.length})`}
            description="Shared peer registry. Used by recursive DNS anycast announcements and by VRF MP-BGP bindings (VPNv4 / VPNv6 / EVPN)."
            actions={canWrite && (
              <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                New BGP peer
              </Button>
            )}
          >
            BGP peers
          </Header>
        }
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (p) => p.name },
          {
            id: 'site', header: 'Site',
            cell: (p) => (
              <Box fontSize="body-s">
                {sitesById.get(p.site_id)?.code ?? p.site_id.slice(0, 8) + '…'}
              </Box>
            ),
            width: 110,
          },
          {
            id: 'peer_ip', header: 'Peer IP',
            cell: (p) => <span style={MONO}>{p.peer_ip}</span>,
            width: 180,
          },
          {
            id: 'local_asn', header: 'Local AS',
            cell: (p) => <span style={MONO}>{fmtAsn(p.local_asn_id)}</span>,
            width: 200,
          },
          {
            id: 'peer_asn', header: 'Peer AS',
            cell: (p) => <span style={MONO}>{fmtAsn(p.peer_asn_id)}</span>,
            width: 200,
          },
          {
            id: 'auth', header: 'TCP AO chain',
            cell: (p) => p.tcp_ao_key_chain_id
              ? chainsById.get(p.tcp_ao_key_chain_id)?.name ?? p.tcp_ao_key_chain_id.slice(0, 8) + '…'
              : <Box color="text-status-inactive" fontSize="body-s">none</Box>,
            width: 160,
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (p: BgpPeer) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button iconName="edit" variant="inline-icon" onClick={() => setEditing(p)} ariaLabel={`Edit ${p.name}`} />
                <Button iconName="remove" variant="inline-icon" onClick={() => remove(p)} ariaLabel={`Delete ${p.name}`} />
              </SpaceBetween>
            ),
            width: 90,
          }] : []),
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            No BGP peers yet.
          </Box>
        }
      />
      {canWrite && (
        <>
          <Modal
            visible={createOpen}
            onDismiss={() => setCreateOpen(false)}
            header="New BGP peer"
            size="medium"
          >
            <BgpPeerForm
              sites={sites}
              asns={asns}
              chains={chains}
              onSaved={async () => {
                setCreateOpen(false);
                await qc.invalidateQueries({ queryKey: ['bgp-peers'] });
              }}
            />
          </Modal>
          <Modal
            visible={editing !== null}
            onDismiss={() => setEditing(null)}
            header="Edit BGP peer"
            size="medium"
          >
            {editing && (
              <BgpPeerForm
                sites={sites}
                asns={asns}
                chains={chains}
                peer={editing}
                onSaved={async () => {
                  setEditing(null);
                  await qc.invalidateQueries({ queryKey: ['bgp-peers'] });
                }}
              />
            )}
          </Modal>
        </>
      )}
    </>
  );
}


export function BgpPeerForm({
  sites, asns, chains, peer, onSaved,
}: Readonly<{
  sites: Site[];
  asns: Asn[];
  chains: KeyChain[];
  peer?: BgpPeer;
  onSaved: () => void;
}>) {
  const editing = !!peer;
  const NONE = '__none__';

  const siteOptions: SelectProps.Option[] = sites.map((s) => ({
    value: s.id, label: `${s.code} · ${s.name}`,
  }));
  const asnOptions: SelectProps.Option[] = asns.map((a) => ({
    value: a.id,
    label: `AS${a.asn}`,
    description: a.name,
  }));
  const chainOptions: SelectProps.Option[] = [
    { value: NONE, label: '(none — unauthenticated)' },
    ...chains.map((c) => ({ value: c.id, label: c.name })),
  ];

  const [name, setName] = useState(peer?.name ?? '');
  const [siteOpt, setSiteOpt] = useState<SelectProps.Option | null>(
    peer ? siteOptions.find((o) => o.value === peer.site_id) ?? null : null,
  );
  const [localAsnOpt, setLocalAsnOpt] = useState<SelectProps.Option | null>(
    peer ? asnOptions.find((o) => o.value === peer.local_asn_id) ?? null : null,
  );
  const [peerAsnOpt, setPeerAsnOpt] = useState<SelectProps.Option | null>(
    peer ? asnOptions.find((o) => o.value === peer.peer_asn_id) ?? null : null,
  );
  const [peerIp, setPeerIp] = useState(peer?.peer_ip ?? '');
  const [chainOpt, setChainOpt] = useState<SelectProps.Option>(() => {
    if (!peer?.tcp_ao_key_chain_id) return chainOptions[0];
    return chainOptions.find((o) => o.value === peer.tcp_ao_key_chain_id) ?? chainOptions[0];
  });
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Required';
    if (!siteOpt) errs.site = 'Pick a site';
    if (!localAsnOpt) errs.local_asn = 'Pick a local ASN';
    if (!peerAsnOpt) errs.peer_asn = 'Pick a peer ASN';
    if (!peerIp.trim()) errs.peer_ip = 'Required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      const payload = {
        name,
        site_id: siteOpt!.value,
        local_asn_id: localAsnOpt!.value,
        peer_asn_id: peerAsnOpt!.value,
        peer_ip: peerIp,
        tcp_ao_key_chain_id: chainOpt.value === NONE ? null : chainOpt.value,
      };
      if (editing && peer) {
        await http.patch(`/dns/bgp-peers/${peer.id}`, payload);
        toast.success('BGP peer updated');
      } else {
        await http.post('/dns/bgp-peers', { ...payload, enabled: true });
        toast.success('BGP peer created');
      }
      onSaved();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    } finally {
      setSubmitting(false);
    }
  }

  const asnEmptyHint = asns.length === 0
    ? 'No ASNs in the catalog yet. Add one under BGP peers → ASNs.'
    : undefined;

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
          <FormField label="Name" errorText={errors.name}>
            <Input
              value={name}
              onChange={({ detail }) => setName(detail.value)}
              placeholder="e.g. site42-leaf-01"
            />
          </FormField>
          <FormField label="Site" errorText={errors.site}>
            <Select
              placeholder="Pick a site"
              selectedOption={siteOpt}
              onChange={({ detail }) => setSiteOpt(detail.selectedOption)}
              options={siteOptions}
              expandToViewport
            />
          </FormField>
          <ColumnLayout columns={2}>
            <FormField label="Local AS" errorText={errors.local_asn} description={asnEmptyHint}>
              <Select
                placeholder="Pick an ASN"
                selectedOption={localAsnOpt}
                onChange={({ detail }) => setLocalAsnOpt(detail.selectedOption)}
                options={asnOptions}
                filteringType="auto"
                expandToViewport
                empty="No ASNs in the catalog yet"
              />
            </FormField>
            <FormField label="Peer AS" errorText={errors.peer_asn} description={asnEmptyHint}>
              <Select
                placeholder="Pick an ASN"
                selectedOption={peerAsnOpt}
                onChange={({ detail }) => setPeerAsnOpt(detail.selectedOption)}
                options={asnOptions}
                filteringType="auto"
                expandToViewport
                empty="No ASNs in the catalog yet"
              />
            </FormField>
          </ColumnLayout>
          <FormField label="Peer IP" errorText={errors.peer_ip}>
            <Input
              value={peerIp}
              onChange={({ detail }) => setPeerIp(detail.value)}
              placeholder="10.42.255.1"
            />
          </FormField>
          <FormField
            label="Authentication"
            description="TCP AO key chain (RFC 5925). MD5 password is deprecated and no longer surfaced."
          >
            <Select
              selectedOption={chainOpt}
              onChange={({ detail }) => {
                if (detail.selectedOption.value) setChainOpt(detail.selectedOption);
              }}
              options={chainOptions}
              expandToViewport
            />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}
