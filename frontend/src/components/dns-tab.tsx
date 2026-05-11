// DNS tab for the IPAM page. Three sub-panels under a fabric selector:
//   1. Zones      — apex + per-site zones, with a render preview.
//   2. Records    — drill from a zone, type-specific forms, ipam-projected
//                   rows are read-only with a badge.
//   3. Servers + anycast — register CoreDNS deployments, bind recursive
//                   servers to BGP peers + an anycast group.

import { useEffect, useMemo, useState } from 'react';
import { useList } from '@refinedev/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { http } from '@/lib/http';
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
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Spinner from '@cloudscape-design/components/spinner';
import Table from '@cloudscape-design/components/table';
import {
  colorBackgroundContainerContent, colorBorderDividerDefault,
} from '@cloudscape-design/design-tokens';

type Fabric = { id: string; name: string };
type Site = { id: string; code: string; name: string };

type DnsZone = {
  id: string;
  name: string;
  kind: 'apex' | 'site';
  fabric_id: string;
  site_id: string | null;
  description: string | null;
  default_ttl: number;
};

type DnsRecord = {
  id: string;
  zone_id: string;
  name: string;
  type: 'A' | 'AAAA' | 'CNAME' | 'MX' | 'TXT' | 'SRV' | 'NS' | 'CAA' | 'PTR';
  ttl: number | null;
  data: Record<string, any>;
  source: 'ipam' | 'manual';
  ipam_address_id: string | null;
};

type DnsServer = {
  id: string;
  name: string;
  site_id: string;
  fabric_id: string;
  role: 'auth' | 'recursive';
  unicast_ip: string;
  enabled: boolean;
  last_render_at: string | null;
  last_render_status: string | null;
  last_render_error: string | null;
  last_render_etag: string | null;
  coredns_version: string | null;
  anycast_group_id: string | null;
};

type AnycastGroup = {
  id: string;
  name: string;
  fabric_id: string;
  service: 'dns_recursive' | 'ntp' | 'log';
  anycast_ipv4: string | null;
  anycast_ipv6: string | null;
};

type BgpPeer = {
  id: string;
  name: string;
  site_id: string;
  local_asn: number;
  peer_asn: number;
  peer_ip: string;
};

const RECORD_TYPES = ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'SRV', 'NS', 'CAA', 'PTR'] as const;
type RecordType = (typeof RECORD_TYPES)[number];
const RECORD_TYPE_OPTS: SelectProps.Option[] = RECORD_TYPES.map((t) => ({ value: t, label: t }));

const MONO = { fontFamily: 'ui-monospace, monospace' } as const;

export function DnsTab({ canWrite }: { canWrite: boolean }) {
  const fabricsRes = useList<Fabric>({ resource: 'ipam/fabrics', pagination: { pageSize: 200 } });
  const fabrics = fabricsRes.result.data ?? [];
  const [fabricId, setFabricId] = useState<string>('');
  const [zoneId, setZoneId] = useState<string | null>(null);

  useEffect(() => {
    if (!fabricId && fabrics.length > 0) setFabricId(fabrics[0].id);
  }, [fabricId, fabrics]);

  const fabricOptions: SelectProps.Option[] =
    fabrics.map((f) => ({ value: f.id, label: f.name }));
  const fabricOpt = fabricOptions.find((o) => o.value === fabricId) ?? null;

  return (
    <SpaceBetween size="l">
      <Container header={<Header variant="h2">Fabric</Header>}>
        <FormField label="Fabric">
          <Select
            placeholder="Pick a fabric"
            selectedOption={fabricOpt}
            onChange={({ detail }) => {
              if (detail.selectedOption.value) {
                setFabricId(detail.selectedOption.value);
                setZoneId(null);
              }
            }}
            options={fabricOptions}
            expandToViewport
          />
        </FormField>
      </Container>

      {fabricId && (
        <ColumnLayout columns={2}>
          <ZonesPanel
            fabricId={fabricId}
            selectedZoneId={zoneId}
            onSelectZone={setZoneId}
            canWrite={canWrite}
          />
          {zoneId
            ? <RecordsPanel zoneId={zoneId} canWrite={canWrite} />
            : (
              <Container>
                <Box padding="m" color="text-status-inactive">
                  Pick a zone to see its records.
                </Box>
              </Container>
            )}
        </ColumnLayout>
      )}

      {fabricId && (
        <AnycastGroupsPanel fabricId={fabricId} canWrite={canWrite} />
      )}

      {fabricId && (
        <ServersPanel fabricId={fabricId} canWrite={canWrite} />
      )}
    </SpaceBetween>
  );
}

// ----------------------- Zones -----------------------

function ZonesPanel({
  fabricId, selectedZoneId, onSelectZone, canWrite,
}: {
  fabricId: string;
  selectedZoneId: string | null;
  onSelectZone: (id: string | null) => void;
  canWrite: boolean;
}) {
  const qc = useQueryClient();
  const zonesQ = useQuery({
    queryKey: ['dns-zones', fabricId],
    queryFn: async () => (
      await http.get<{ items: DnsZone[] }>(`/dns/zones?fabric_id=${fabricId}&page_size=200`)
    ).data.items ?? [],
  });
  const zones = zonesQ.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);
  const [previewZone, setPreviewZone] = useState<DnsZone | null>(null);

  async function refresh() {
    await qc.invalidateQueries({ queryKey: ['dns-zones', fabricId] });
  }

  async function syncFromIpam(z: DnsZone) {
    try {
      const r = await http.post<{ added: number; removed: number }>(
        `/dns/zones/${z.id}/sync-from-ipam`, {},
      );
      toast.success(`Synced ${z.name}: +${r.data.added}, -${r.data.removed}`);
      await qc.invalidateQueries({ queryKey: ['dns-records', z.id] });
    } catch (err: any) { toast.error(err?.message ?? 'sync failed'); }
  }

  async function remove(z: DnsZone) {
    if (!window.confirm(`Delete zone ${z.name}?`)) return;
    try {
      await http.delete(`/dns/zones/${z.id}`);
      if (selectedZoneId === z.id) onSelectZone(null);
      await refresh();
      toast.success('Zone removed');
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <>
      <Table<DnsZone>
        variant="container"
        loading={zonesQ.isLoading}
        loadingText="Loading zones…"
        items={zones}
        trackBy="id"
        selectionType="single"
        selectedItems={selectedZoneId
          ? zones.filter((z) => z.id === selectedZoneId)
          : []}
        onSelectionChange={({ detail }) => {
          const next = detail.selectedItems[0];
          onSelectZone(next ? next.id : null);
        }}
        ariaLabels={{
          selectionGroupLabel: 'Zone selection',
          itemSelectionLabel: (_d, item) => `Select zone ${item.name}`,
          allItemsSelectionLabel: () => 'select all',
        }}
        header={
          <Header
            counter={`(${zones.length})`}
            actions={canWrite && (
              <Button iconName="add-plus" onClick={() => setCreateOpen(true)}>
                Add zone
              </Button>
            )}
            description="Select a zone to see its records."
          >
            Zones
          </Header>
        }
        columnDefinitions={[
          {
            id: 'name', header: 'Name',
            cell: (z) => <span style={MONO}>{z.name}</span>,
          },
          {
            id: 'kind', header: 'Kind',
            cell: (z) => z.kind === 'apex'
              ? <StatusIndicator type="info">apex</StatusIndicator>
              : <Badge>site</Badge>,
            width: 100,
          },
          {
            id: 'ttl', header: 'TTL',
            cell: (z) => <span style={MONO}>{z.default_ttl}</span>,
            width: 100,
          },
          {
            id: 'actions', header: '',
            cell: (z) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button
                  iconName="file"
                  variant="inline-icon"
                  onClick={() => setPreviewZone(z)}
                  ariaLabel={`Preview ${z.name}`}
                />
                {canWrite && z.kind === 'site' && (
                  <Button
                    iconName="refresh"
                    variant="inline-icon"
                    onClick={() => syncFromIpam(z)}
                    ariaLabel={`Sync ${z.name} from IPAM`}
                  />
                )}
                {canWrite && (
                  <Button
                    iconName="remove"
                    variant="inline-icon"
                    onClick={() => remove(z)}
                    ariaLabel={`Delete ${z.name}`}
                  />
                )}
              </SpaceBetween>
            ),
            width: 140,
          },
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            No zones in this fabric yet.
          </Box>
        }
      />
      {canWrite && (
        <Modal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header="New DNS zone"
          size="medium"
        >
          <ZoneForm fabricId={fabricId} onSaved={async () => { setCreateOpen(false); await refresh(); }} />
        </Modal>
      )}
      <Modal
        visible={previewZone !== null}
        onDismiss={() => setPreviewZone(null)}
        header={
          <span>
            Zone preview:{' '}
            <span style={MONO}>{previewZone?.name}</span>
          </span>
        }
        size="large"
      >
        {previewZone && <ZonePreview zoneId={previewZone.id} />}
      </Modal>
    </>
  );
}

function ZonePreview({ zoneId }: { zoneId: string }) {
  const { data, isLoading } = useQuery({
    queryKey: ['dns-zone-preview', zoneId],
    queryFn: async () => (await http.get<{ text: string; record_count: number }>(`/dns/zones/${zoneId}/preview`)).data,
  });
  if (isLoading) return <Box color="text-status-inactive"><Spinner /> Rendering…</Box>;
  return (
    <SpaceBetween size="xs">
      <Box color="text-status-inactive" fontSize="body-s">{data?.record_count} records</Box>
      <pre style={{
        maxHeight: '60vh', overflow: 'auto', padding: '12px',
        fontSize: '12px', fontFamily: 'ui-monospace, monospace',
        background: colorBackgroundContainerContent,
        border: `1px solid ${colorBorderDividerDefault}`,
        borderRadius: '8px',
      }}>
        {data?.text}
      </pre>
    </SpaceBetween>
  );
}

function ZoneForm({ fabricId, onSaved }: { fabricId: string; onSaved: () => void }) {
  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 500 } });
  const sites = sitesRes.result.data ?? [];

  const [name, setName] = useState('');
  const [kindOpt, setKindOpt] = useState<SelectProps.Option>({ value: 'site', label: 'Site (per-site)' });
  const [siteOpt, setSiteOpt] = useState<SelectProps.Option | null>(null);
  const [ttl, setTtl] = useState('300');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const kindOptions: SelectProps.Option[] = [
    { value: 'apex', label: 'Apex (per-fabric)' },
    { value: 'site', label: 'Site (per-site)' },
  ];
  const siteOptions: SelectProps.Option[] = sites.map((s) => ({
    value: s.id, label: `${s.code} · ${s.name}`,
  }));

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Required';
    if (!ttl.trim() || Number.isNaN(Number(ttl))) errs.ttl = 'Required (seconds)';
    if (kindOpt.value === 'site' && !siteOpt) errs.site = 'Pick a site';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      await http.post('/dns/zones', {
        name,
        kind: kindOpt.value,
        fabric_id: fabricId,
        site_id: kindOpt.value === 'site' ? siteOpt?.value : null,
        default_ttl: Number(ttl),
      });
      toast.success('Zone created');
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
          <FormField label="Zone FQDN" errorText={errors.name}>
            <Input
              value={name}
              onChange={({ detail }) => setName(detail.value)}
              placeholder="e.g. site42.prod.dcim.mil"
            />
          </FormField>
          <ColumnLayout columns={2}>
            <FormField label="Kind">
              <Select
                selectedOption={kindOpt}
                onChange={({ detail }) => {
                  if (detail.selectedOption.value) setKindOpt(detail.selectedOption);
                }}
                options={kindOptions}
                expandToViewport
              />
            </FormField>
            <FormField label="Default TTL (s)" errorText={errors.ttl}>
              <Input type="number" value={ttl} onChange={({ detail }) => setTtl(detail.value)} />
            </FormField>
          </ColumnLayout>
          {kindOpt.value === 'site' && (
            <FormField label="Site" errorText={errors.site}>
              <Select
                placeholder="Pick a site"
                selectedOption={siteOpt}
                onChange={({ detail }) => setSiteOpt(detail.selectedOption)}
                options={siteOptions}
                expandToViewport
              />
            </FormField>
          )}
        </SpaceBetween>
      </Form>
    </form>
  );
}

// ----------------------- Records -----------------------

function RecordsPanel({ zoneId, canWrite }: { zoneId: string; canWrite: boolean }) {
  const qc = useQueryClient();
  const recordsQ = useQuery({
    queryKey: ['dns-records', zoneId],
    queryFn: async () => (
      await http.get<{ items: DnsRecord[] }>(`/dns/records?zone_id=${zoneId}&page_size=500`)
    ).data.items ?? [],
  });
  const records = recordsQ.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);

  async function refresh() {
    await qc.invalidateQueries({ queryKey: ['dns-records', zoneId] });
    await qc.invalidateQueries({ queryKey: ['dns-zone-preview', zoneId] });
  }

  async function remove(r: DnsRecord) {
    if (r.source === 'ipam') {
      toast.error('Clear the dns_name on the IPAddress and re-sync to remove this row');
      return;
    }
    if (!window.confirm(`Delete ${r.name} ${r.type}?`)) return;
    try {
      await http.delete(`/dns/records/${r.id}`);
      await refresh();
      toast.success('Record removed');
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <>
      <Table<DnsRecord>
        variant="container"
        loading={recordsQ.isLoading}
        loadingText="Loading records…"
        items={records}
        trackBy="id"
        header={
          <Header
            counter={`(${records.length})`}
            actions={canWrite && (
              <Button iconName="add-plus" onClick={() => setCreateOpen(true)}>
                Add record
              </Button>
            )}
          >
            Records
          </Header>
        }
        columnDefinitions={[
          {
            id: 'name', header: 'Name',
            cell: (r) => <span style={MONO}>{r.name || '@'}</span>,
          },
          {
            id: 'type', header: 'Type',
            cell: (r) => <Badge>{r.type}</Badge>,
            width: 90,
          },
          {
            id: 'data', header: 'Data',
            cell: (r) => <span style={MONO}>{formatRdata(r)}</span>,
          },
          {
            id: 'source', header: 'Source',
            cell: (r) => r.source === 'ipam'
              ? <Badge color="blue">from IPAM</Badge>
              : <Box color="text-status-inactive" fontSize="body-s">manual</Box>,
            width: 110,
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (r: DnsRecord) => (
              <Button
                iconName="remove"
                variant="inline-icon"
                disabled={r.source === 'ipam'}
                onClick={() => remove(r)}
                ariaLabel={`Delete ${r.name || '@'} ${r.type}`}
              />
            ),
            width: 60,
          }] : []),
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            No records yet.
          </Box>
        }
      />
      {canWrite && (
        <Modal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header="New DNS record"
          size="medium"
        >
          <RecordForm zoneId={zoneId} onSaved={async () => { setCreateOpen(false); await refresh(); }} />
        </Modal>
      )}
    </>
  );
}

function formatRdata(r: DnsRecord): string {
  const d = r.data ?? {};
  switch (r.type) {
    case 'A':
    case 'AAAA':
    case 'CNAME':
    case 'NS':
    case 'PTR':
      return d.target ?? '';
    case 'MX': return `${d.priority ?? 0} ${d.target ?? ''}`;
    case 'TXT': return `"${d.text ?? ''}"`;
    case 'SRV': return `${d.priority ?? 0} ${d.weight ?? 0} ${d.port ?? 0} ${d.target ?? ''}`;
    case 'CAA': return `${d.flags ?? 0} ${d.tag ?? ''} "${d.value ?? ''}"`;
    default: return JSON.stringify(d);
  }
}

function RecordForm({ zoneId, onSaved }: { zoneId: string; onSaved: () => void }) {
  const [name, setName] = useState('@');
  const [typeOpt, setTypeOpt] = useState<SelectProps.Option>({ value: 'A', label: 'A' });
  const [ttl, setTtl] = useState('');
  const [target, setTarget] = useState('');
  const [priority, setPriority] = useState('');
  const [weight, setWeight] = useState('');
  const [port, setPort] = useState('');
  const [text, setText] = useState('');
  const [flags, setFlags] = useState('');
  const [tag, setTag] = useState('issue');
  const [value, setValue] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const type = typeOpt.value as RecordType;

  function buildData(): Record<string, any> {
    switch (type) {
      case 'A':
      case 'AAAA':
      case 'CNAME':
      case 'NS':
      case 'PTR':
        return { target };
      case 'MX':
        return { priority: Number(priority || 10), target };
      case 'TXT':
        return { text };
      case 'SRV':
        return {
          priority: Number(priority || 0),
          weight: Number(weight || 0),
          port: Number(port || 0),
          target,
        };
      case 'CAA':
        return {
          flags: Number(flags || 0),
          tag: tag || 'issue',
          value,
        };
    }
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Required';
    if ((['A', 'AAAA', 'CNAME', 'NS', 'PTR', 'MX', 'SRV'] as RecordType[]).includes(type) && !target.trim()) {
      errs.target = 'Required';
    }
    if (type === 'TXT' && !text.trim()) errs.text = 'Required';
    if (type === 'CAA' && !value.trim()) errs.value = 'Required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      await http.post('/dns/records', {
        zone_id: zoneId,
        name,
        type,
        ttl: ttl ? Number(ttl) : null,
        data: buildData(),
      });
      toast.success('Record created');
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
          <ColumnLayout columns={3}>
            <FormField label="Name" errorText={errors.name}>
              <Input
                value={name}
                onChange={({ detail }) => setName(detail.value)}
                placeholder="@ or leaf-01"
              />
            </FormField>
            <FormField label="Type">
              <Select
                selectedOption={typeOpt}
                onChange={({ detail }) => {
                  if (detail.selectedOption.value) setTypeOpt(detail.selectedOption);
                }}
                options={RECORD_TYPE_OPTS}
                expandToViewport
              />
            </FormField>
            <FormField label="TTL (s, optional)">
              <Input type="number" value={ttl} onChange={({ detail }) => setTtl(detail.value)} />
            </FormField>
          </ColumnLayout>

          {(['A', 'AAAA', 'CNAME', 'NS', 'PTR'] as RecordType[]).includes(type) && (
            <FormField
              label={type === 'A' || type === 'AAAA' ? 'IP address' : 'Target FQDN'}
              errorText={errors.target}
            >
              <Input value={target} onChange={({ detail }) => setTarget(detail.value)} />
            </FormField>
          )}

          {type === 'MX' && (
            <ColumnLayout columns={2}>
              <FormField label="Priority">
                <Input
                  type="number" value={priority}
                  onChange={({ detail }) => setPriority(detail.value)}
                  placeholder="10"
                />
              </FormField>
              <FormField label="Mail server FQDN" errorText={errors.target}>
                <Input value={target} onChange={({ detail }) => setTarget(detail.value)} />
              </FormField>
            </ColumnLayout>
          )}

          {type === 'TXT' && (
            <FormField label="Text (no surrounding quotes)" errorText={errors.text}>
              <Input value={text} onChange={({ detail }) => setText(detail.value)} />
            </FormField>
          )}

          {type === 'SRV' && (
            <ColumnLayout columns={4}>
              <FormField label="Priority">
                <Input type="number" value={priority} onChange={({ detail }) => setPriority(detail.value)} />
              </FormField>
              <FormField label="Weight">
                <Input type="number" value={weight} onChange={({ detail }) => setWeight(detail.value)} />
              </FormField>
              <FormField label="Port">
                <Input type="number" value={port} onChange={({ detail }) => setPort(detail.value)} />
              </FormField>
              <FormField label="Target" errorText={errors.target}>
                <Input value={target} onChange={({ detail }) => setTarget(detail.value)} />
              </FormField>
            </ColumnLayout>
          )}

          {type === 'CAA' && (
            <ColumnLayout columns={3}>
              <FormField label="Flags">
                <Input
                  type="number" value={flags}
                  onChange={({ detail }) => setFlags(detail.value)} placeholder="0"
                />
              </FormField>
              <FormField label="Tag">
                <Input value={tag} onChange={({ detail }) => setTag(detail.value)} placeholder="issue" />
              </FormField>
              <FormField label="Value" errorText={errors.value}>
                <Input value={value} onChange={({ detail }) => setValue(detail.value)} />
              </FormField>
            </ColumnLayout>
          )}
        </SpaceBetween>
      </Form>
    </form>
  );
}

// ----------------------- Anycast groups -----------------------

function AnycastGroupsPanel({ fabricId, canWrite }: { fabricId: string; canWrite: boolean }) {
  const qc = useQueryClient();
  const groupsQ = useQuery({
    queryKey: ['anycast-groups', fabricId],
    queryFn: async () => (
      await http.get<{ items: AnycastGroup[] }>(`/dns/anycast-groups?fabric_id=${fabricId}&page_size=200`)
    ).data.items ?? [],
  });
  const serversQ = useQuery({
    queryKey: ['dns-servers', fabricId],
    queryFn: async () => (
      await http.get<{ items: DnsServer[] }>(`/dns/servers?fabric_id=${fabricId}&page_size=200`)
    ).data.items ?? [],
  });
  const groups = groupsQ.data ?? [];
  const servers = serversQ.data ?? [];

  const [createOpen, setCreateOpen] = useState(false);
  const [editGroup, setEditGroup] = useState<AnycastGroup | null>(null);

  async function refresh() {
    await qc.invalidateQueries({ queryKey: ['anycast-groups', fabricId] });
  }

  async function remove(g: AnycastGroup) {
    const bound = servers.filter((s) => s.anycast_group_id === g.id).length;
    if (bound > 0) {
      toast.error(`${bound} DNS server(s) still bound; unbind before deleting`);
      return;
    }
    if (!window.confirm(`Delete anycast group ${g.name}?`)) return;
    try {
      await http.delete(`/dns/anycast-groups/${g.id}`);
      await refresh();
      toast.success('Anycast group removed');
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <>
      <Table<AnycastGroup>
        variant="container"
        loading={groupsQ.isLoading}
        loadingText="Loading anycast groups…"
        items={groups}
        trackBy="id"
        header={
          <Header
            counter={`(${groups.length})`}
            actions={canWrite && (
              <Button iconName="add-plus" onClick={() => setCreateOpen(true)}>
                Add anycast group
              </Button>
            )}
          >
            Anycast groups
          </Header>
        }
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (g) => g.name },
          {
            id: 'service', header: 'Service',
            cell: (g) => <Badge>{g.service}</Badge>,
            width: 130,
          },
          {
            id: 'v4', header: 'Anycast v4',
            cell: (g) => <span style={MONO}>{g.anycast_ipv4 ?? '—'}</span>,
          },
          {
            id: 'v6', header: 'Anycast v6',
            cell: (g) => <span style={MONO}>{g.anycast_ipv6 ?? '—'}</span>,
          },
          {
            id: 'bound', header: 'Bound servers',
            cell: (g) => {
              const c = servers.filter((s) => s.anycast_group_id === g.id).length;
              return (
                <Box color="text-status-inactive" fontSize="body-s">
                  {c === 0 ? '—' : `${c} server${c === 1 ? '' : 's'}`}
                </Box>
              );
            },
            width: 140,
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (g: AnycastGroup) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button iconName="edit" variant="inline-icon" onClick={() => setEditGroup(g)} ariaLabel={`Edit ${g.name}`} />
                <Button iconName="remove" variant="inline-icon" onClick={() => remove(g)} ariaLabel={`Delete ${g.name}`} />
              </SpaceBetween>
            ),
            width: 110,
          }] : []),
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            No anycast groups in this fabric yet.
          </Box>
        }
      />
      {canWrite && (
        <Modal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header="New anycast group"
          size="medium"
        >
          <AnycastForm
            fabricId={fabricId}
            onSaved={async () => { setCreateOpen(false); await refresh(); }}
          />
        </Modal>
      )}
      <Modal
        visible={editGroup !== null}
        onDismiss={() => setEditGroup(null)}
        header="Edit anycast group"
        size="medium"
      >
        {editGroup && (
          <AnycastForm
            fabricId={fabricId}
            group={editGroup}
            onSaved={async () => { setEditGroup(null); await refresh(); }}
          />
        )}
      </Modal>
    </>
  );
}

// ----------------------- Servers -----------------------

function ServersPanel({ fabricId, canWrite }: { fabricId: string; canWrite: boolean }) {
  const qc = useQueryClient();
  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 500 } });
  const sites = sitesRes.result.data ?? [];
  const sitesById = useMemo(() => new Map(sites.map((s) => [s.id, s])), [sites]);

  const serversQ = useQuery({
    queryKey: ['dns-servers', fabricId],
    queryFn: async () => (
      await http.get<{ items: DnsServer[] }>(`/dns/servers?fabric_id=${fabricId}&page_size=200`)
    ).data.items ?? [],
  });
  const anycastQ = useQuery({
    queryKey: ['anycast-groups', fabricId],
    queryFn: async () => (
      await http.get<{ items: AnycastGroup[] }>(`/dns/anycast-groups?fabric_id=${fabricId}&page_size=200`)
    ).data.items ?? [],
  });
  const peersQ = useQuery({
    queryKey: ['bgp-peers'],
    queryFn: async () => (
      await http.get<{ items: BgpPeer[] }>(`/dns/bgp-peers?page_size=500`)
    ).data.items ?? [],
  });
  const servers = serversQ.data ?? [];
  const anycast = anycastQ.data ?? [];
  const peers = peersQ.data ?? [];

  const [serverOpen, setServerOpen] = useState(false);
  const [editServer, setEditServer] = useState<DnsServer | null>(null);
  const [bgpOpen, setBgpOpen] = useState(false);

  async function refreshServers() {
    await qc.invalidateQueries({ queryKey: ['dns-servers', fabricId] });
  }
  async function removeServer(s: DnsServer) {
    if (!window.confirm(`Delete DNS server ${s.name}?`)) return;
    try {
      await http.delete(`/dns/servers/${s.id}`);
      await refreshServers();
      toast.success('DNS server removed');
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <>
      <Table<DnsServer>
        variant="container"
        loading={serversQ.isLoading}
        loadingText="Loading DNS servers…"
        items={servers}
        trackBy="id"
        header={
          <Header
            counter={`(${servers.length})`}
            actions={canWrite && (
              <SpaceBetween size="xs" direction="horizontal">
                <Button iconName="add-plus" onClick={() => setBgpOpen(true)}>BGP peer</Button>
                <Button variant="primary" iconName="add-plus" onClick={() => setServerOpen(true)}>DNS server</Button>
              </SpaceBetween>
            )}
          >
            DNS servers + anycast
          </Header>
        }
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (s) => s.name },
          {
            id: 'site', header: 'Site',
            cell: (s) => (
              <Box fontSize="body-s">
                {sitesById.get(s.site_id)?.code ?? s.site_id.slice(0, 8) + '…'}
              </Box>
            ),
            width: 110,
          },
          {
            id: 'role', header: 'Role',
            cell: (s) => <Badge color={s.role === 'recursive' ? 'blue' : 'grey'}>{s.role}</Badge>,
            width: 110,
          },
          {
            id: 'unicast', header: 'Unicast IP',
            cell: (s) => <span style={MONO}>{s.unicast_ip}</span>,
          },
          {
            id: 'v4', header: 'Anycast v4',
            cell: (s) => {
              const ag = anycast.find((a) => a.id === s.anycast_group_id);
              return <span style={MONO}>{ag?.anycast_ipv4 ?? '—'}</span>;
            },
          },
          {
            id: 'v6', header: 'Anycast v6',
            cell: (s) => {
              const ag = anycast.find((a) => a.id === s.anycast_group_id);
              return <span style={MONO}>{ag?.anycast_ipv6 ?? '—'}</span>;
            },
          },
          {
            id: 'render', header: 'Last render',
            cell: (s) => <RenderStatusBadge server={s} />,
          },
          {
            id: 'bgp', header: 'BGP peers',
            cell: (s) => s.role === 'recursive'
              ? <BindingsCell server={s} peers={peers} canWrite={canWrite} />
              : <Box color="text-status-inactive" fontSize="body-s">—</Box>,
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (s: DnsServer) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button iconName="edit" variant="inline-icon" onClick={() => setEditServer(s)} ariaLabel={`Edit ${s.name}`} />
                <Button iconName="remove" variant="inline-icon" onClick={() => removeServer(s)} ariaLabel={`Delete ${s.name}`} />
              </SpaceBetween>
            ),
            width: 110,
          }] : []),
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            No DNS servers yet.
          </Box>
        }
      />
      {canWrite && (
        <>
          <Modal
            visible={bgpOpen}
            onDismiss={() => setBgpOpen(false)}
            header="New BGP peer"
            size="medium"
          >
            <BgpPeerForm
              sites={sites}
              onSaved={async () => {
                setBgpOpen(false);
                await qc.invalidateQueries({ queryKey: ['bgp-peers'] });
              }}
            />
          </Modal>
          <Modal
            visible={serverOpen}
            onDismiss={() => setServerOpen(false)}
            header="New DNS server"
            size="medium"
          >
            <ServerForm
              fabricId={fabricId}
              sites={sites}
              anycast={anycast}
              onSaved={async () => {
                setServerOpen(false);
                await qc.invalidateQueries({ queryKey: ['dns-servers', fabricId] });
              }}
            />
          </Modal>
          <Modal
            visible={editServer !== null}
            onDismiss={() => setEditServer(null)}
            header="Edit DNS server"
            size="medium"
          >
            {editServer && (
              <ServerForm
                fabricId={fabricId}
                sites={sites}
                anycast={anycast}
                server={editServer}
                onSaved={async () => { setEditServer(null); await refreshServers(); }}
              />
            )}
          </Modal>
        </>
      )}
    </>
  );
}

function RenderStatusBadge({ server }: { server: DnsServer }) {
  if (!server.last_render_at) {
    return <Box color="text-status-inactive" fontSize="body-s">never</Box>;
  }
  const ok = server.last_render_status === 'ok';
  const when = new Date(server.last_render_at).toLocaleString();
  return (
    <SpaceBetween size="xxs" direction="horizontal">
      <StatusIndicator type={ok ? 'success' : 'error'}>
        {server.last_render_status}
      </StatusIndicator>
      <Box
        color="text-status-inactive"
        fontSize="body-s"
        // The title gives a hover-over for the error text, which is the
        // operator's quickest path when last_render_status='error'.
      >
        <span title={server.last_render_error ?? ''}>{when}</span>
      </Box>
    </SpaceBetween>
  );
}

function BindingsCell({
  server, peers, canWrite,
}: { server: DnsServer; peers: BgpPeer[]; canWrite: boolean }) {
  const qc = useQueryClient();
  const bindingsQ = useQuery({
    queryKey: ['anycast-bindings', server.id],
    queryFn: async () => (
      await http.get<{ items: { id: string; bgp_peer_id: string }[] }>(`/dns/anycast-bindings?dns_server_id=${server.id}&page_size=50`)
    ).data.items ?? [],
  });
  const bindings = bindingsQ.data ?? [];
  const sitePeers = peers.filter((p) => p.site_id === server.site_id);

  async function add(peerId: string) {
    try {
      await http.post('/dns/anycast-bindings', { dns_server_id: server.id, bgp_peer_id: peerId });
      await qc.invalidateQueries({ queryKey: ['anycast-bindings', server.id] });
      toast.success('Bound');
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }
  async function remove(bindingId: string) {
    try {
      await http.delete(`/dns/anycast-bindings/${bindingId}`);
      await qc.invalidateQueries({ queryKey: ['anycast-bindings', server.id] });
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  const peerOptions: SelectProps.Option[] = sitePeers
    .filter((p) => !bindings.some((b) => b.bgp_peer_id === p.id))
    .map((p) => ({ value: p.id, label: `${p.peer_ip} (AS${p.peer_asn})` }));

  return (
    <SpaceBetween size="xxs">
      {bindings.map((b) => {
        const p = peers.find((x) => x.id === b.bgp_peer_id);
        return (
          <SpaceBetween key={b.id} size="xxs" direction="horizontal">
            <span style={{ ...MONO, fontSize: '12px' }}>
              {p?.peer_ip ?? b.bgp_peer_id.slice(0, 8) + '…'}
            </span>
            {p && (
              <Box color="text-status-inactive" fontSize="body-s">AS{p.peer_asn}</Box>
            )}
            {canWrite && (
              <Button
                iconName="remove"
                variant="inline-icon"
                onClick={() => remove(b.id)}
                ariaLabel="Remove binding"
              />
            )}
          </SpaceBetween>
        );
      })}
      {canWrite && peerOptions.length > 0 && (
        <Select
          placeholder="+ add peer"
          selectedOption={null}
          onChange={({ detail }) => {
            if (detail.selectedOption.value) add(detail.selectedOption.value);
          }}
          options={peerOptions}
          expandToViewport
        />
      )}
    </SpaceBetween>
  );
}

function ServerForm({
  fabricId, sites, anycast, server, onSaved,
}: {
  fabricId: string;
  sites: Site[];
  anycast: AnycastGroup[];
  server?: DnsServer;
  onSaved: () => void;
}) {
  const editing = !!server;
  const [name, setName] = useState(server?.name ?? '');
  const [siteOpt, setSiteOpt] = useState<SelectProps.Option | null>(() => {
    if (!server) return null;
    const s = sites.find((x) => x.id === server.site_id);
    return s ? { value: s.id, label: `${s.code} · ${s.name}` } : null;
  });
  const [roleOpt, setRoleOpt] = useState<SelectProps.Option>(
    server?.role === 'recursive'
      ? { value: 'recursive', label: 'Recursive' }
      : { value: 'auth', label: 'Authoritative' },
  );
  const [unicastIp, setUnicastIp] = useState(server?.unicast_ip ?? '');
  const [anycastOpt, setAnycastOpt] = useState<SelectProps.Option | null>(() => {
    if (!server?.anycast_group_id) return null;
    const g = anycast.find((a) => a.id === server.anycast_group_id);
    return g
      ? { value: g.id, label: `${g.name} (${g.anycast_ipv4 ?? g.anycast_ipv6 ?? '—'})` }
      : null;
  });
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const siteOptions: SelectProps.Option[] = sites.map((s) => ({
    value: s.id, label: `${s.code} · ${s.name}`,
  }));
  const roleOptions: SelectProps.Option[] = [
    { value: 'auth', label: 'Authoritative' },
    { value: 'recursive', label: 'Recursive' },
  ];
  const anycastOptions: SelectProps.Option[] = anycast.map((a) => ({
    value: a.id, label: `${a.name} (${a.anycast_ipv4 ?? a.anycast_ipv6 ?? '—'})`,
  }));

  const role = roleOpt.value as 'auth' | 'recursive';

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Required';
    if (!editing && !siteOpt) errs.site = 'Pick a site';
    if (!unicastIp.trim()) errs.unicast_ip = 'Required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      if (editing && server) {
        await http.patch(`/dns/servers/${server.id}`, {
          name,
          unicast_ip: unicastIp,
          anycast_group_id: server.role === 'recursive' && anycastOpt?.value
            ? anycastOpt.value : null,
        });
        toast.success('DNS server updated');
      } else {
        await http.post('/dns/servers', {
          name,
          site_id: siteOpt!.value,
          fabric_id: fabricId,
          role,
          unicast_ip: unicastIp,
          anycast_group_id: role === 'recursive' && anycastOpt?.value ? anycastOpt.value : null,
        });
        toast.success('DNS server created');
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
          <FormField label="Name" errorText={errors.name}>
            <Input
              value={name}
              onChange={({ detail }) => setName(detail.value)}
              placeholder="e.g. site42-coredns-auth"
            />
          </FormField>
          <ColumnLayout columns={2}>
            <FormField
              label="Site"
              errorText={errors.site}
              description={editing ? 'Site is immutable after creation.' : undefined}
            >
              <Select
                placeholder="Pick a site"
                selectedOption={siteOpt}
                onChange={({ detail }) => setSiteOpt(detail.selectedOption)}
                options={siteOptions}
                disabled={editing}
                expandToViewport
              />
            </FormField>
            <FormField
              label="Role"
              description={editing ? 'Role is immutable after creation.' : undefined}
            >
              <Select
                selectedOption={roleOpt}
                onChange={({ detail }) => {
                  if (detail.selectedOption.value) setRoleOpt(detail.selectedOption);
                }}
                options={roleOptions}
                disabled={editing}
                expandToViewport
              />
            </FormField>
          </ColumnLayout>
          <FormField label="Unicast (mgmt) IP" errorText={errors.unicast_ip}>
            <Input
              value={unicastIp}
              onChange={({ detail }) => setUnicastIp(detail.value)}
              placeholder="10.42.0.53"
            />
          </FormField>
          {role === 'recursive' && (
            <FormField
              label="Anycast group"
              description="Recursive servers must bind an anycast group."
            >
              <Select
                placeholder="Pick an anycast group"
                selectedOption={anycastOpt}
                onChange={({ detail }) => setAnycastOpt(detail.selectedOption)}
                options={anycastOptions}
                expandToViewport
              />
            </FormField>
          )}
        </SpaceBetween>
      </Form>
    </form>
  );
}

function AnycastForm({
  fabricId, group, onSaved,
}: {
  fabricId: string;
  group?: AnycastGroup;
  onSaved: () => void;
}) {
  const editing = !!group;
  const [name, setName] = useState(group?.name ?? '');
  const [serviceOpt, setServiceOpt] = useState<SelectProps.Option>(() => {
    const svc = group?.service ?? 'dns_recursive';
    const label = svc === 'dns_recursive' ? 'DNS recursive'
      : svc === 'ntp' ? 'NTP (reserved)' : 'Log (reserved)';
    return { value: svc, label };
  });
  const [ipv4, setIpv4] = useState(group?.anycast_ipv4 ?? '');
  const [ipv6, setIpv6] = useState(group?.anycast_ipv6 ?? '');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const serviceOptions: SelectProps.Option[] = [
    { value: 'dns_recursive', label: 'DNS recursive' },
    { value: 'ntp', label: 'NTP (reserved)' },
    { value: 'log', label: 'Log (reserved)' },
  ];

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Required';
    if (!ipv4.trim() && !ipv6.trim()) errs.ipv4 = 'At least one of v4 / v6 must be set';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      if (editing && group) {
        await http.patch(`/dns/anycast-groups/${group.id}`, {
          name,
          anycast_ipv4: ipv4 || null,
          anycast_ipv6: ipv6 || null,
        });
        toast.success('Anycast group updated');
      } else {
        await http.post('/dns/anycast-groups', {
          name, fabric_id: fabricId, service: serviceOpt.value,
          anycast_ipv4: ipv4 || null,
          anycast_ipv6: ipv6 || null,
        });
        toast.success('Anycast group created');
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
          <FormField label="Name" errorText={errors.name}>
            <Input
              value={name}
              onChange={({ detail }) => setName(detail.value)}
              placeholder="e.g. prod-dns-recursive"
            />
          </FormField>
          <FormField
            label="Service"
            description={editing ? 'Service is immutable after creation.' : undefined}
          >
            <Select
              selectedOption={serviceOpt}
              onChange={({ detail }) => {
                if (detail.selectedOption.value) setServiceOpt(detail.selectedOption);
              }}
              options={serviceOptions}
              disabled={editing}
              expandToViewport
            />
          </FormField>
          <ColumnLayout columns={2}>
            <FormField label="Anycast IPv4 (optional)" errorText={errors.ipv4}>
              <Input
                value={ipv4}
                onChange={({ detail }) => setIpv4(detail.value)}
                placeholder="10.255.0.53"
              />
            </FormField>
            <FormField label="Anycast IPv6 (optional)">
              <Input
                value={ipv6}
                onChange={({ detail }) => setIpv6(detail.value)}
                placeholder="2001:db8::53"
              />
            </FormField>
          </ColumnLayout>
        </SpaceBetween>
      </Form>
    </form>
  );
}

function BgpPeerForm({ sites, onSaved }: { sites: Site[]; onSaved: () => void }) {
  const [name, setName] = useState('');
  const [siteOpt, setSiteOpt] = useState<SelectProps.Option | null>(null);
  const [localAsn, setLocalAsn] = useState('65000');
  const [peerAsn, setPeerAsn] = useState('65001');
  const [peerIp, setPeerIp] = useState('');
  const [md5, setMd5] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const siteOptions: SelectProps.Option[] = sites.map((s) => ({
    value: s.id, label: `${s.code} · ${s.name}`,
  }));

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Required';
    if (!siteOpt) errs.site = 'Pick a site';
    if (!peerIp.trim()) errs.peer_ip = 'Required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      await http.post('/dns/bgp-peers', {
        name,
        site_id: siteOpt!.value,
        local_asn: Number(localAsn),
        peer_asn: Number(peerAsn),
        peer_ip: peerIp,
        md5_password: md5 || null,
      });
      toast.success('BGP peer created');
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
            <FormField label="Local AS">
              <Input type="number" value={localAsn} onChange={({ detail }) => setLocalAsn(detail.value)} />
            </FormField>
            <FormField label="Peer AS">
              <Input type="number" value={peerAsn} onChange={({ detail }) => setPeerAsn(detail.value)} />
            </FormField>
          </ColumnLayout>
          <FormField label="Peer IP" errorText={errors.peer_ip}>
            <Input
              value={peerIp}
              onChange={({ detail }) => setPeerIp(detail.value)}
              placeholder="10.42.255.1"
            />
          </FormField>
          <FormField label="MD5 password (optional)">
            <Input type="password" value={md5} onChange={({ detail }) => setMd5(detail.value)} />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}
