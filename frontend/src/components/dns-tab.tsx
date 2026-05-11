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
import { useFabricScope } from '@/contexts/fabric-scope';
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
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Link from '@cloudscape-design/components/link';
import Modal from '@cloudscape-design/components/modal';
import Multiselect, { MultiselectProps } from '@cloudscape-design/components/multiselect';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Spinner from '@cloudscape-design/components/spinner';
import Table from '@cloudscape-design/components/table';
import Tabs from '@cloudscape-design/components/tabs';
import TextFilter from '@cloudscape-design/components/text-filter';
import {
  colorBackgroundContainerContent, colorBorderDividerDefault,
} from '@cloudscape-design/design-tokens';

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
  local_asn_id: string;
  peer_asn_id: string;
  peer_ip: string;
};
type Asn = { id: string; asn: number; name: string };

const RECORD_TYPES = ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'SRV', 'NS', 'CAA', 'PTR'] as const;
type RecordType = (typeof RECORD_TYPES)[number];
const RECORD_TYPE_OPTS: SelectProps.Option[] = RECORD_TYPES.map((t) => ({ value: t, label: t }));

const MONO = { fontFamily: 'ui-monospace, monospace' } as const;

export function DnsTab({ canWrite }: { canWrite: boolean }) {
  // Fabric scope comes from the TopNavigation's region selector now —
  // see FabricScopeProvider. Drilling into a zone clears when the
  // operator switches fabrics so we don't surface a stale records page.
  const { fabricId, isLoading: fabricsLoading } = useFabricScope();
  const [zoneId, setZoneId] = useState<string | null>(null);
  const [activeTab, setActiveTab] = useState<string>('zones');

  useEffect(() => { setZoneId(null); }, [fabricId]);

  if (!fabricId) {
    return (
      <Container>
        <Box padding="m" color="text-status-inactive">
          {fabricsLoading ? 'Loading fabrics…' : 'No fabrics yet — pick or create one in IPAM → Fabrics.'}
        </Box>
      </Container>
    );
  }

  return (
    <SpaceBetween size="m">
      <Tabs
        activeTabId={activeTab}
        onChange={({ detail }) => {
          // Switching tabs always clears the zone drill-in so we don't
          // surface a stale records page on return.
          setActiveTab(detail.activeTabId);
          if (detail.activeTabId !== 'zones') setZoneId(null);
        }}
        tabs={[
          {
            id: 'zones',
            label: 'Hosted zones',
            content: zoneId
              ? (
                <ZoneDetailView
                  fabricId={fabricId}
                  zoneId={zoneId}
                  onBack={() => setZoneId(null)}
                  canWrite={canWrite}
                />
              )
              : (
                <ZonesListView
                  fabricId={fabricId}
                  onOpenZone={setZoneId}
                  canWrite={canWrite}
                />
              ),
          },
          {
            id: 'servers',
            label: 'Servers',
            content: <ServersPanel fabricId={fabricId} canWrite={canWrite} />,
          },
          {
            id: 'anycast',
            label: 'Anycast groups',
            content: <AnycastGroupsPanel fabricId={fabricId} canWrite={canWrite} />,
          },
        ]}
      />
    </SpaceBetween>
  );
}

// ----------------------- Record type chip -----------------------

const RECORD_TYPE_COLOR: Record<RecordType, 'blue' | 'green' | 'grey' | 'red' | 'severity-medium' | 'severity-low' | 'severity-neutral'> = {
  A: 'blue', AAAA: 'blue',
  CNAME: 'green',
  MX: 'severity-medium', SRV: 'severity-medium',
  NS: 'severity-low', CAA: 'severity-low',
  TXT: 'grey', PTR: 'grey',
};

function RecordTypeChip({ type }: { type: RecordType }) {
  return <Badge color={RECORD_TYPE_COLOR[type]}>{type}</Badge>;
}

// Build the displayed FQDN for a record like Route 53 does: the
// left-hand `name` joined to the zone FQDN. `@` collapses to bare zone.
function fqdn(name: string, zoneName: string): string {
  if (!name || name === '@') return zoneName;
  return `${name}.${zoneName}`;
}

// ----------------------- Zones list view -----------------------

function ZonesListView({
  fabricId, onOpenZone, canWrite,
}: {
  fabricId: string;
  onOpenZone: (id: string) => void;
  canWrite: boolean;
}) {
  const qc = useQueryClient();
  const zonesQ = useQuery({
    queryKey: ['dns-zones', fabricId],
    queryFn: async () => (
      await http.get<{ items: DnsZone[] }>(`/dns/zones?fabric_id=${fabricId}&page_size=200`)
    ).data.items ?? [],
  });
  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 500 } });
  const sitesById = useMemo(
    () => new Map((sitesRes.result.data ?? []).map((s) => [s.id, s])),
    [sitesRes.result.data],
  );
  const zones = zonesQ.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);
  const [selected, setSelected] = useState<DnsZone[]>([]);
  const [filterText, setFilterText] = useState('');

  const filtered = useMemo(() => {
    const q = filterText.trim().toLowerCase();
    if (!q) return zones;
    return zones.filter((z) => (
      z.name.toLowerCase().includes(q)
      || (z.description ?? '').toLowerCase().includes(q)
      || z.kind.toLowerCase().includes(q)
    ));
  }, [zones, filterText]);

  async function refresh() {
    await qc.invalidateQueries({ queryKey: ['dns-zones', fabricId] });
    setSelected([]);
  }

  async function removeSelected() {
    if (selected.length === 0) return;
    if (!window.confirm(`Delete ${selected.length} zone(s)?`)) return;
    try {
      await Promise.all(selected.map((z) => http.delete(`/dns/zones/${z.id}`)));
      toast.success('Zones removed');
      await refresh();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <>
      <Table<DnsZone>
        loading={zonesQ.isLoading}
        loadingText="Loading hosted zones…"
        items={filtered}
        trackBy="id"
        selectionType={canWrite ? 'multi' : undefined}
        selectedItems={selected}
        onSelectionChange={({ detail }) => setSelected(detail.selectedItems)}
        ariaLabels={{
          selectionGroupLabel: 'Hosted zone selection',
          itemSelectionLabel: (_d, item) => `Select ${item.name}`,
          allItemsSelectionLabel: () => 'Select all hosted zones',
        }}
        filter={
          <TextFilter
            filteringText={filterText}
            filteringPlaceholder="Filter hosted zones"
            filteringAriaLabel="Filter hosted zones"
            onChange={({ detail }) => setFilterText(detail.filteringText)}
            countText={`${filtered.length} match${filtered.length === 1 ? '' : 'es'}`}
          />
        }
        header={
          <Header
            counter={selected.length > 0 ? `(${selected.length}/${zones.length})` : `(${zones.length})`}
            actions={
              <SpaceBetween size="xs" direction="horizontal">
                {canWrite && (
                  <Button
                    disabled={selected.length === 0}
                    onClick={removeSelected}
                  >
                    Delete
                  </Button>
                )}
                {canWrite && (
                  <Button variant="primary" onClick={() => setCreateOpen(true)}>
                    Create hosted zone
                  </Button>
                )}
              </SpaceBetween>
            }
            description="DCIM-managed DNS zones. Click a domain name to view records."
          >
            Hosted zones
          </Header>
        }
        columnDefinitions={[
          {
            id: 'name', header: 'Domain name',
            cell: (z) => (
              <Link
                onFollow={(e) => { e.preventDefault(); onOpenZone(z.id); }}
                href={`#${z.id}`}
              >
                <span style={MONO}>{z.name}</span>
              </Link>
            ),
            sortingField: 'name',
            minWidth: 240,
          },
          {
            id: 'kind', header: 'Type',
            cell: (z) => z.kind === 'apex'
              ? <Badge color="blue">Apex</Badge>
              : <Badge>Site</Badge>,
            width: 100,
          },
          {
            id: 'site', header: 'Site',
            cell: (z) => {
              if (!z.site_id) return <Box color="text-status-inactive">—</Box>;
              const s = sitesById.get(z.site_id);
              return s ? `${s.code} · ${s.name}` : z.site_id.slice(0, 8);
            },
          },
          {
            id: 'ttl', header: 'Default TTL',
            cell: (z) => <span style={MONO}>{z.default_ttl}</span>,
            width: 120,
          },
          {
            id: 'description', header: 'Description',
            cell: (z) => z.description || <Box color="text-status-inactive">—</Box>,
          },
          {
            id: 'id', header: 'Hosted zone ID',
            cell: (z) => <span style={MONO}>{z.id.slice(0, 8)}…</span>,
            width: 140,
          },
        ]}
        empty={
          <Box textAlign="center" padding="l">
            <SpaceBetween size="xs">
              <b>No hosted zones</b>
              <Box color="text-status-inactive" fontSize="body-s">
                Create an apex zone first, then per-site subdomains.
              </Box>
              {canWrite && (
                <Button onClick={() => setCreateOpen(true)} variant="primary">
                  Create hosted zone
                </Button>
              )}
            </SpaceBetween>
          </Box>
        }
      />
      {canWrite && (
        <Modal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header="Create hosted zone"
          size="medium"
        >
          <ZoneForm fabricId={fabricId} onSaved={async () => { setCreateOpen(false); await refresh(); }} />
        </Modal>
      )}
    </>
  );
}

// ----------------------- Zone detail view (drill-in) -----------------------

function ZoneDetailView({
  fabricId, zoneId, onBack, canWrite,
}: {
  fabricId: string;
  zoneId: string;
  onBack: () => void;
  canWrite: boolean;
}) {
  const qc = useQueryClient();
  const zoneQ = useQuery({
    queryKey: ['dns-zone', zoneId],
    queryFn: async () => (await http.get<DnsZone>(`/dns/zones/${zoneId}`)).data,
  });
  const recordsQ = useQuery({
    queryKey: ['dns-records', zoneId],
    queryFn: async () => (
      await http.get<{ items: DnsRecord[] }>(`/dns/records?zone_id=${zoneId}&page_size=500`)
    ).data.items ?? [],
  });

  const zone = zoneQ.data;
  const records = recordsQ.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);
  const [previewOpen, setPreviewOpen] = useState(false);
  const [selected, setSelected] = useState<DnsRecord[]>([]);
  const [filterText, setFilterText] = useState('');

  const filtered = useMemo(() => {
    const q = filterText.trim().toLowerCase();
    if (!q) return records;
    return records.filter((r) => {
      const name = (r.name || '@').toLowerCase();
      const data = formatRdata(r).toLowerCase();
      return name.includes(q) || data.includes(q) || r.type.toLowerCase().includes(q);
    });
  }, [records, filterText]);

  async function refresh() {
    await qc.invalidateQueries({ queryKey: ['dns-records', zoneId] });
    await qc.invalidateQueries({ queryKey: ['dns-zone-preview', zoneId] });
    setSelected([]);
  }

  async function syncFromIpam() {
    if (!zone) return;
    try {
      const r = await http.post<{ added: number; removed: number }>(
        `/dns/zones/${zone.id}/sync-from-ipam`, {},
      );
      toast.success(`Synced ${zone.name}: +${r.data.added}, -${r.data.removed}`);
      await refresh();
    } catch (err: any) { toast.error(err?.message ?? 'sync failed'); }
  }

  async function removeSelected() {
    const deletable = selected.filter((r) => r.source !== 'ipam');
    const skipped = selected.length - deletable.length;
    if (deletable.length === 0) {
      toast.error('IPAM-projected records are managed by clearing dns_name on the IPAddress');
      return;
    }
    if (!window.confirm(`Delete ${deletable.length} record(s)?`)) return;
    try {
      await Promise.all(deletable.map((r) => http.delete(`/dns/records/${r.id}`)));
      toast.success(skipped > 0
        ? `Removed ${deletable.length}; skipped ${skipped} IPAM row(s)`
        : 'Records removed');
      await refresh();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  async function exportZoneFile() {
    if (!zone) return;
    try {
      const r = await http.get<{ text: string; record_count: number }>(
        `/dns/zones/${zone.id}/preview`,
      );
      const blob = new Blob([r.data.text], { type: 'text/plain' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `${zone.name}.zone`;
      document.body.appendChild(a);
      a.click();
      document.body.removeChild(a);
      URL.revokeObjectURL(url);
    } catch (err: any) { toast.error(err?.message ?? 'export failed'); }
  }

  if (zoneQ.isLoading || !zone) {
    return <Box padding="m" color="text-status-inactive"><Spinner /> Loading zone…</Box>;
  }

  return (
    <SpaceBetween size="m">
      <div>
        <Link
          href="#zones"
          onFollow={(e) => { e.preventDefault(); onBack(); }}
        >
          ← Hosted zones
        </Link>
      </div>

      <Container
        header={
          <Header
            variant="h2"
            actions={
              canWrite && zone.kind === 'site' && (
                <Button iconName="refresh" onClick={syncFromIpam}>
                  Sync from IPAM
                </Button>
              )
            }
          >
            <span style={MONO}>{zone.name}</span>
          </Header>
        }
      >
        <KeyValuePairs
          columns={4}
          items={[
            {
              label: 'Type',
              value: zone.kind === 'apex'
                ? <Badge color="blue">Apex</Badge>
                : <Badge>Site</Badge>,
            },
            { label: 'Default TTL', value: <span style={MONO}>{zone.default_ttl}</span> },
            { label: 'Records', value: records.length },
            { label: 'Hosted zone ID', value: <span style={MONO}>{zone.id.slice(0, 8)}…</span> },
            { label: 'Description', value: zone.description || <Box color="text-status-inactive">—</Box> },
          ]}
        />
      </Container>

      <Table<DnsRecord>
        loading={recordsQ.isLoading}
        loadingText="Loading records…"
        items={filtered}
        trackBy="id"
        selectionType={canWrite ? 'multi' : undefined}
        selectedItems={selected}
        onSelectionChange={({ detail }) => setSelected(detail.selectedItems)}
        ariaLabels={{
          selectionGroupLabel: 'Record selection',
          itemSelectionLabel: (_d, item) => `Select ${item.name || '@'} ${item.type}`,
          allItemsSelectionLabel: () => 'Select all records',
        }}
        filter={
          <TextFilter
            filteringText={filterText}
            filteringPlaceholder="Filter records by name, type, or value"
            filteringAriaLabel="Filter records"
            onChange={({ detail }) => setFilterText(detail.filteringText)}
            countText={`${filtered.length} match${filtered.length === 1 ? '' : 'es'}`}
          />
        }
        header={
          <Header
            counter={selected.length > 0 ? `(${selected.length}/${records.length})` : `(${records.length})`}
            actions={
              <SpaceBetween size="xs" direction="horizontal">
                <Button onClick={() => setPreviewOpen(true)} iconName="file">
                  Preview zone file
                </Button>
                <Button onClick={exportZoneFile} iconName="download">
                  Export
                </Button>
                {canWrite && (
                  <Button
                    disabled={selected.length === 0}
                    onClick={removeSelected}
                  >
                    Delete
                  </Button>
                )}
                {canWrite && (
                  <Button variant="primary" onClick={() => setCreateOpen(true)}>
                    Create record
                  </Button>
                )}
              </SpaceBetween>
            }
          >
            Records
          </Header>
        }
        columnDefinitions={[
          {
            id: 'name', header: 'Record name',
            cell: (r) => <span style={MONO}>{fqdn(r.name, zone.name)}</span>,
            sortingField: 'name',
            minWidth: 240,
          },
          {
            id: 'type', header: 'Type',
            cell: (r) => <RecordTypeChip type={r.type} />,
            width: 90,
          },
          {
            id: 'data', header: 'Value/Route traffic to',
            cell: (r) => <span style={MONO}>{formatRdata(r)}</span>,
            minWidth: 240,
          },
          {
            id: 'ttl', header: 'TTL (seconds)',
            cell: (r) => (
              <span style={MONO}>
                {r.ttl ?? <Box color="text-status-inactive" display="inline">{zone.default_ttl} (zone)</Box>}
              </span>
            ),
            width: 160,
          },
          {
            id: 'source', header: 'Source',
            cell: (r) => r.source === 'ipam'
              ? <Badge color="blue">From IPAM</Badge>
              : <Box color="text-status-inactive" fontSize="body-s">Manual</Box>,
            width: 110,
          },
        ]}
        empty={
          <Box textAlign="center" padding="l">
            <SpaceBetween size="xs">
              <b>No records yet</b>
              {zone.kind === 'site' && (
                <Box color="text-status-inactive" fontSize="body-s">
                  Site zones auto-populate when you sync from IPAM, or you can
                  add records manually.
                </Box>
              )}
              {canWrite && (
                <SpaceBetween size="xs" direction="horizontal">
                  <Button onClick={() => setCreateOpen(true)} variant="primary">
                    Create record
                  </Button>
                  {zone.kind === 'site' && (
                    <Button iconName="refresh" onClick={syncFromIpam}>
                      Sync from IPAM
                    </Button>
                  )}
                </SpaceBetween>
              )}
            </SpaceBetween>
          </Box>
        }
      />

      {canWrite && (
        <Modal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header={<span>Create record in <span style={MONO}>{zone.name}</span></span>}
          size="medium"
        >
          <RecordForm
            zone={zone}
            onSaved={async () => { setCreateOpen(false); await refresh(); }}
          />
        </Modal>
      )}
      <Modal
        visible={previewOpen}
        onDismiss={() => setPreviewOpen(false)}
        header={<span>Zone file preview: <span style={MONO}>{zone.name}</span></span>}
        size="large"
        footer={
          <Box float="right">
            <Button onClick={exportZoneFile} iconName="download">Download .zone</Button>
          </Box>
        }
      >
        <ZonePreviewBody zoneId={zone.id} />
      </Modal>
    </SpaceBetween>
  );
}

function ZonePreviewBody({ zoneId }: { zoneId: string }) {
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

// ----------------------- Record helpers -----------------------

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

function RecordForm({ zone, onSaved }: { zone: DnsZone; onSaved: () => void }) {
  const [name, setName] = useState('');
  const [typeOpt, setTypeOpt] = useState<SelectProps.Option>({ value: 'A', label: 'A' });
  const [ttl, setTtl] = useState('60');
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
    // Empty subdomain means the apex itself; we send `@` over the wire
    // to match how the backend treats the zone origin.
    const lhs = name.trim() === '' ? '@' : name.trim();
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
        zone_id: zone.id,
        name: lhs,
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
          <FormField
            label="Record name"
            description={`Leave blank to create a record at the zone apex (${zone.name})`}
            errorText={errors.name}
          >
            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
              <div style={{ flex: 1 }}>
                <Input
                  value={name}
                  onChange={({ detail }) => setName(detail.value)}
                  placeholder="leaf-01"
                />
              </div>
              <Box color="text-status-inactive" fontSize="body-m">
                <span style={MONO}>.{zone.name}</span>
              </Box>
            </div>
          </FormField>

          <ColumnLayout columns={2}>
            <FormField label="Record type">
              <Select
                selectedOption={typeOpt}
                onChange={({ detail }) => {
                  if (detail.selectedOption.value) setTypeOpt(detail.selectedOption);
                }}
                options={RECORD_TYPE_OPTS}
                expandToViewport
              />
            </FormField>
            <FormField
              label="TTL (seconds)"
              description={`Clear to inherit the zone default (${zone.default_ttl})`}
            >
              <Input
                type="number"
                value={ttl}
                onChange={({ detail }) => setTtl(detail.value)}
                placeholder={String(zone.default_ttl)}
              />
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
  // ASN catalog feeds the "AS65000" label on the BindingsCell pickers
  // and on the per-binding chips. BgpPeer rows reference ASN ids only.
  const asnsQ = useQuery({
    queryKey: ['bgp-asns'],
    queryFn: async () => (
      await http.get<{ items: Asn[] }>('/bgp/asns?page_size=500')
    ).data.items ?? [],
  });
  const servers = serversQ.data ?? [];
  const anycast = anycastQ.data ?? [];
  const peers = peersQ.data ?? [];
  const asnsById = useMemo(
    () => new Map((asnsQ.data ?? []).map((a) => [a.id, a])),
    [asnsQ.data],
  );

  const [serverOpen, setServerOpen] = useState(false);
  const [editServer, setEditServer] = useState<DnsServer | null>(null);

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

  // Build a tooltip for the role chip so anycast IPs are still
  // reachable without dedicating two columns to them.
  function roleTooltip(s: DnsServer): string {
    if (s.role !== 'recursive') return 'authoritative';
    const ag = anycast.find((a) => a.id === s.anycast_group_id);
    if (!ag) return 'recursive (no anycast group bound)';
    const ips = [ag.anycast_ipv4, ag.anycast_ipv6].filter(Boolean).join(' · ');
    return `recursive · anycast: ${ips || '—'}`;
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
            description="Authoritative servers serve site zones; recursive servers forward everything else and announce an anycast IP via BGP."
            actions={canWrite && (
              <Button variant="primary" iconName="add-plus" onClick={() => setServerOpen(true)}>
                New DNS server
              </Button>
            )}
          >
            DNS servers
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
            cell: (s) => (
              <span title={roleTooltip(s)}>
                <Badge color={s.role === 'recursive' ? 'blue' : 'grey'}>{s.role}</Badge>
              </span>
            ),
            width: 110,
          },
          {
            id: 'unicast', header: 'Unicast IP',
            cell: (s) => <span style={MONO}>{s.unicast_ip}</span>,
            width: 160,
          },
          {
            id: 'render', header: 'Last render',
            cell: (s) => <RenderStatusBadge server={s} />,
          },
          {
            id: 'announced_peer', header: 'Announced to peer',
            cell: (s) => s.role === 'recursive'
              ? <BindingsCell server={s} peers={peers} asnsById={asnsById} mode="peer" canWrite={canWrite} />
              : <Box color="text-status-inactive" fontSize="body-s">—</Box>,
          },
          {
            id: 'announced_asn', header: 'Announced to ASN',
            cell: (s) => s.role === 'recursive'
              ? <BindingsCell server={s} peers={peers} asnsById={asnsById} mode="asn" canWrite={canWrite} />
              : <Box color="text-status-inactive" fontSize="body-s">—</Box>,
            width: 140,
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
            visible={serverOpen}
            onDismiss={() => setServerOpen(false)}
            header="New DNS server"
            size="medium"
          >
            <ServerForm
              fabricId={fabricId}
              sites={sites}
              anycast={anycast}
              peers={peers}
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
                peers={peers}
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

// The Servers table renders bindings across two columns ("Announced to
// peer" + "Announced to ASN"). Both cells share the same react-query
// cache (queryKey: ['anycast-bindings', server.id]) so the second cell
// reuses the first's fetch — no duplicate network. The `mode` prop
// picks what to render:
//   peer — peer-name chip + IP, with the Add/Remove affordances
//   asn  — read-only AS number, one row per binding, aligned with peer
type BindingsCellMode = 'peer' | 'asn';

function BindingsCell({
  server, peers, asnsById, canWrite, mode,
}: Readonly<{
  server: DnsServer;
  peers: BgpPeer[];
  asnsById: Map<string, Asn>;
  canWrite: boolean;
  mode: BindingsCellMode;
}>) {
  const qc = useQueryClient();
  const bindingsQ = useQuery({
    queryKey: ['anycast-bindings', server.id],
    queryFn: async () => (
      await http.get<{ items: { id: string; bgp_peer_id: string }[] }>(`/dns/anycast-bindings?dns_server_id=${server.id}&page_size=50`)
    ).data.items ?? [],
  });
  const bindings = bindingsQ.data ?? [];
  const sitePeers = peers.filter((p) => p.site_id === server.site_id);

  function asLabel(p: BgpPeer): string {
    const a = asnsById.get(p.peer_asn_id);
    return a ? `AS${a.asn}` : '';
  }

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

  if (bindings.length === 0 && (mode === 'asn' || !canWrite)) {
    return <Box color="text-status-inactive" fontSize="body-s">—</Box>;
  }

  if (mode === 'asn') {
    // Read-only column. One AS chip per binding; aligned row-for-row
    // with the peer cell next to it (same iteration order).
    return (
      <SpaceBetween size="xxs">
        {bindings.map((b) => {
          const p = peers.find((x) => x.id === b.bgp_peer_id);
          const as = p ? asLabel(p) : '';
          return (
            <Box key={b.id} fontSize="body-s">
              {as || <span style={{ color: 'var(--color-text-status-inactive-gy7337, #757575)' }}>—</span>}
            </Box>
          );
        })}
      </SpaceBetween>
    );
  }

  // mode === 'peer' — name + IP per binding, plus the add/remove
  // affordances. This is the writable side of the split.
  const peerOptions: SelectProps.Option[] = sitePeers
    .filter((p) => !bindings.some((b) => b.bgp_peer_id === p.id))
    .map((p) => ({ value: p.id, label: p.name, description: p.peer_ip }));

  return (
    <SpaceBetween size="xxs">
      {bindings.map((b) => {
        const p = peers.find((x) => x.id === b.bgp_peer_id);
        return (
          <SpaceBetween key={b.id} size="xxs" direction="horizontal">
            <Box fontSize="body-s">
              {p?.name ?? b.bgp_peer_id.slice(0, 8) + '…'}
              {p && (
                <span style={{ marginLeft: 6, color: 'var(--color-text-status-inactive-gy7337, #757575)', fontFamily: 'ui-monospace, monospace' }}>
                  {p.peer_ip}
                </span>
              )}
            </Box>
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
  fabricId, sites, anycast, peers, server, onSaved,
}: {
  fabricId: string;
  sites: Site[];
  anycast: AnycastGroup[];
  peers: BgpPeer[];
  server?: DnsServer;
  onSaved: () => void;
}) {
  const editing = !!server;
  const qc = useQueryClient();
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

  // Existing peer-bindings for this server (recursive only). The form
  // tracks the operator's intent as a set of peer IDs; on save we diff
  // against the server-side state and emit per-peer POST/DELETE calls.
  const bindingsQ = useQuery({
    queryKey: ['anycast-bindings', server?.id],
    queryFn: async () => server
      ? (await http.get<{ items: { id: string; bgp_peer_id: string }[] }>(
        `/dns/anycast-bindings?dns_server_id=${server.id}&page_size=50`,
      )).data.items ?? []
      : [],
    enabled: !!server && server.role === 'recursive',
  });
  const existingBindings = bindingsQ.data ?? [];
  const [selectedPeerIds, setSelectedPeerIds] = useState<Set<string>>(
    () => new Set(),
  );
  // Seed the selection once bindings load (only if the operator hasn't
  // already touched it — initial empty set means "untouched").
  const [bindingsSeeded, setBindingsSeeded] = useState(false);
  if (!bindingsSeeded && existingBindings.length > 0) {
    setSelectedPeerIds(new Set(existingBindings.map((b) => b.bgp_peer_id)));
    setBindingsSeeded(true);
  }

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

  async function syncBindings(serverId: string) {
    // Diff selection vs server-side state and apply the difference.
    // Per-peer POST/DELETE because the backend doesn't expose a bulk-set
    // endpoint — fine for the small fanout (typically 1-3 peers).
    const have = new Map(existingBindings.map((b) => [b.bgp_peer_id, b.id]));
    const want = selectedPeerIds;
    const toAdd = [...want].filter((pid) => !have.has(pid));
    const toRemove = [...have.entries()].filter(([pid]) => !want.has(pid));
    for (const pid of toAdd) {
      await http.post('/dns/anycast-bindings', {
        dns_server_id: serverId, bgp_peer_id: pid,
      });
    }
    for (const [, bindingId] of toRemove) {
      await http.delete(`/dns/anycast-bindings/${bindingId}`);
    }
    await qc.invalidateQueries({ queryKey: ['anycast-bindings', serverId] });
  }

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
      let serverId: string;
      if (editing && server) {
        await http.patch(`/dns/servers/${server.id}`, {
          name,
          unicast_ip: unicastIp,
          anycast_group_id: server.role === 'recursive' && anycastOpt?.value
            ? anycastOpt.value : null,
        });
        serverId = server.id;
        toast.success('DNS server updated');
      } else {
        const r = await http.post<{ id: string }>('/dns/servers', {
          name,
          site_id: siteOpt!.value,
          fabric_id: fabricId,
          role,
          unicast_ip: unicastIp,
          anycast_group_id: role === 'recursive' && anycastOpt?.value ? anycastOpt.value : null,
        });
        serverId = r.data.id;
        toast.success('DNS server created');
      }
      // Sync peer bindings only for recursive servers — auth servers
      // don't announce anything.
      if (role === 'recursive') {
        await syncBindings(serverId);
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
            <>
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
              <FormField
                label="Announced to"
                description={
                  siteOpt
                    ? 'BGP peers that this recursive server advertises its anycast IP to. Scoped to peers at the same site.'
                    : 'Pick a site first — peers scope to the server site.'
                }
              >
                <Multiselect
                  placeholder="Pick BGP peers"
                  selectedOptions={
                    peers
                      .filter((p) => selectedPeerIds.has(p.id))
                      .map((p) => ({ value: p.id, label: p.name, description: p.peer_ip }))
                  }
                  onChange={({ detail }) => {
                    const next = new Set<string>();
                    for (const o of detail.selectedOptions) {
                      if (o.value) next.add(o.value);
                    }
                    setSelectedPeerIds(next);
                  }}
                  options={
                    peers
                      .filter((p) => !siteOpt || p.site_id === siteOpt.value)
                      .map((p) => ({ value: p.id, label: p.name, description: p.peer_ip } satisfies MultiselectProps.Option))
                  }
                  filteringType="auto"
                  expandToViewport
                  empty="No BGP peers available for this site"
                />
              </FormField>
            </>
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

