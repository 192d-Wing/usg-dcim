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
import { Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts';
import {
  colorBackgroundContainerContent, colorBorderDividerDefault,
} from '@cloudscape-design/design-tokens';

type Site = { id: string; code: string; name: string };

type DnsZone = {
  id: string;
  name: string;
  kind: 'apex' | 'site' | 'reverse';
  fabric_id: string;
  site_id: string | null;
  description: string | null;
  default_ttl: number;
  soa_mname: string;
  soa_rname: string;
  soa_refresh: number;
  soa_retry: number;
  soa_expire: number;
  soa_minimum: number;
  // Serial is a derived, read-only value the backend computes from the
  // zone's updated_at timestamp. Surfaced here so operators can verify
  // it without inspecting the rendered zone file.
  serial: number;
};

type DnsRecordSource = 'manual' | 'ipam' | 'ddns';

type DnsRecord = {
  id: string;
  zone_id: string;
  name: string;
  type: 'A' | 'AAAA' | 'CNAME' | 'MX' | 'TXT' | 'SRV' | 'NS' | 'CAA' | 'PTR';
  ttl: number | null;
  data: Record<string, any>;
  source: DnsRecordSource;
  ipam_address_id: string | null;
  view_id: string | null;
};

type DnsView = {
  id: string;
  name: string;
  fabric_id: string;
  match_cidrs: string[];
  priority: number;
  description: string | null;
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

type DnsForwarder = {
  id: string;
  name: string;
  fabric_id: string;
  zone_pattern: string;
  upstreams: string[];
  description: string | null;
};

type DnsBlocklist = {
  id: string;
  name: string;
  fabric_id: string;
  action: 'block' | 'sinkhole';
  sink_ipv4: string | null;
  sink_ipv6: string | null;
  enabled: boolean;
  description: string | null;
};

type DnsBlocklistEntry = {
  id: string;
  blocklist_id: string;
  pattern: string;
  description: string | null;
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
// SOA is rendered as a virtual row but never selected via the type
// dropdown — keep the create-form's set narrow.
type RecordType = (typeof RECORD_TYPES)[number] | 'SOA';
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
          {
            id: 'forwarders',
            label: 'Forwarders',
            content: <ForwardersPanel fabricId={fabricId} canWrite={canWrite} />,
          },
          {
            id: 'blocklists',
            label: 'Blocklists',
            content: <BlocklistsPanel fabricId={fabricId} canWrite={canWrite} />,
          },
          {
            id: 'views',
            label: 'Views',
            content: <ViewsPanel fabricId={fabricId} canWrite={canWrite} />,
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
  SOA: 'severity-neutral',
};

function RecordTypeChip({ type }: { type: RecordType }) {
  return <Badge color={RECORD_TYPE_COLOR[type]}>{type}</Badge>;
}

function ZoneKindBadge({ kind }: { kind: DnsZone['kind'] }) {
  if (kind === 'apex') return <Badge color="blue">Apex</Badge>;
  if (kind === 'reverse') return <Badge color="severity-low">Reverse</Badge>;
  return <Badge>Site</Badge>;
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
            cell: (z) => <ZoneKindBadge kind={z.kind} />,
            width: 110,
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
  const [importOpen, setImportOpen] = useState(false);
  const [soaOpen, setSoaOpen] = useState(false);
  const [selected, setSelected] = useState<DnsRecord[]>([]);
  const [filterText, setFilterText] = useState('');

  // Inject the zone's SOA as a synthetic row at the top of the records
  // table. It's not a DnsRecord row in the DB — the SOA lives on the
  // DnsZone — but operators expect to see it alongside the records
  // they manage (matches Route 53's behavior). The row is non-
  // selectable and non-deletable; the "@" label is a Link that opens
  // the edit modal.
  const soaRow: DnsRecord | null = useMemo(() => zone ? {
    id: `soa-${zone.id}`,
    zone_id: zone.id,
    name: '@',
    type: 'SOA' as any,
    ttl: zone.default_ttl,
    data: {
      mname: `${zone.soa_mname}.${zone.name}.`,
      rname: `${zone.soa_rname}.${zone.name}.`,
      serial: zone.serial,
      refresh: zone.soa_refresh,
      retry: zone.soa_retry,
      expire: zone.soa_expire,
      minimum: zone.soa_minimum,
    },
    source: 'manual',
    ipam_address_id: null,
    view_id: null,
  } : null, [zone]);

  const filtered = useMemo(() => {
    const q = filterText.trim().toLowerCase();
    const base = soaRow ? [soaRow, ...records] : records;
    if (!q) return base;
    return base.filter((r) => {
      const name = (r.name || '@').toLowerCase();
      const data = formatRdata(r).toLowerCase();
      return name.includes(q) || data.includes(q) || r.type.toLowerCase().includes(q);
    });
  }, [records, filterText, soaRow]);

  async function refresh() {
    await qc.invalidateQueries({ queryKey: ['dns-zone', zoneId] });
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
    const deletable = selected.filter((r) => r.source === 'manual');
    const skipped = selected.length - deletable.length;
    if (deletable.length === 0) {
      toast.error('Projector-owned records (IPAM / DDNS) are managed via the underlying IPAddress');
      return;
    }
    if (!window.confirm(`Delete ${deletable.length} record(s)?`)) return;
    try {
      await Promise.all(deletable.map((r) => http.delete(`/dns/records/${r.id}`)));
      toast.success(skipped > 0
        ? `Removed ${deletable.length}; skipped ${skipped} projector row(s)`
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
              value: <ZoneKindBadge kind={zone.kind} />,
            },
            { label: 'Default TTL', value: <span style={MONO}>{zone.default_ttl}</span> },
            { label: 'Records', value: records.length },
            { label: 'Hosted zone ID', value: <span style={MONO}>{zone.id.slice(0, 8)}…</span> },
            {
              label: 'Serial',
              value: (
                <span style={MONO} title="Auto-incremented on every record change">
                  {zone.serial}
                </span>
              ),
            },
            { label: 'Description', value: zone.description || <Box color="text-status-inactive">—</Box> },
          ]}
        />
      </Container>

      <Tabs
        variant="container"
        tabs={[
          {
            id: 'records',
            label: 'Records',
            content: (
              <Table<DnsRecord>
        loading={recordsQ.isLoading}
        loadingText="Loading records…"
        items={filtered}
        trackBy="id"
        selectionType={canWrite ? 'multi' : undefined}
        selectedItems={selected}
        onSelectionChange={({ detail }) => setSelected(detail.selectedItems)}
        // SOA is a virtual row backed by zone metadata, not a deletable
        // record; block selection so the bulk-delete button can't fire
        // against it.
        isItemDisabled={(item) => item.id.startsWith('soa-')}
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
                  <Button onClick={() => setImportOpen(true)} iconName="upload">
                    Import
                  </Button>
                )}
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
            cell: (r) => {
              const fq = fqdn(r.name, zone.name);
              // SOA row links to its edit modal — the only way to
              // mutate SOA fields, since they're not first-class
              // records.
              if (r.id.startsWith('soa-') && canWrite) {
                return (
                  <Link
                    href={`#soa-${zone.id}`}
                    onFollow={(e) => { e.preventDefault(); setSoaOpen(true); }}
                  >
                    <span style={MONO}>{fq}</span>
                  </Link>
                );
              }
              return <span style={MONO}>{fq}</span>;
            },
            sortingField: 'name',
            minWidth: 240,
          },
          {
            id: 'type', header: 'Type',
            cell: (r) => <RecordTypeChip type={r.type as RecordType} />,
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
            cell: (r) => {
              if (r.id.startsWith('soa-')) return <Badge>Zone</Badge>;
              if (r.source === 'ipam') return <Badge color="blue">IPAM</Badge>;
              if (r.source === 'ddns') return <Badge color="severity-medium">DDNS</Badge>;
              return <Box color="text-status-inactive" fontSize="body-s">Manual</Box>;
            },
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
            ),
          },
          {
            id: 'activity',
            label: 'Activity',
            content: (
              <ZoneActivityTab
                zoneId={zone.id}
                recordIds={records.map((r) => r.id)}
              />
            ),
          },
        ]}
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
      {canWrite && (
        <Modal
          visible={soaOpen}
          onDismiss={() => setSoaOpen(false)}
          header={<span>Edit SOA: <span style={MONO}>{zone.name}</span></span>}
          size="medium"
        >
          <SoaEditForm
            zone={zone}
            onSaved={async () => { setSoaOpen(false); await refresh(); }}
          />
        </Modal>
      )}
      {canWrite && (
        <Modal
          visible={importOpen}
          onDismiss={() => setImportOpen(false)}
          header={<span>Import zone file: <span style={MONO}>{zone.name}</span></span>}
          size="large"
        >
          <ZoneImportForm
            zone={zone}
            onSaved={async () => { setImportOpen(false); await refresh(); }}
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

// Surfaces audit-log entries scoped to this zone — zone-level events
// (zone.create/update/delete, sync-from-ipam) plus every record event
// for records that currently belong to the zone. Two parallel calls so
// each one is cacheable on its own key.
type AuditEntry = {
  id: string;
  occurred_at: string;
  actor_label: string | null;
  actor_ip: string | null;
  action: string;
  target_type: string | null;
  target_id: string | null;
  request_id: string | null;
  success: boolean;
  diff_json: Record<string, unknown>;
  metadata_json: Record<string, unknown>;
};

function ZoneActivityTab({ zoneId, recordIds }: { zoneId: string; recordIds: string[] }) {
  const [openEntry, setOpenEntry] = useState<AuditEntry | null>(null);
  const zoneEvents = useQuery({
    queryKey: ['audit', 'dns_zone', zoneId],
    queryFn: async () => (await http.get<{ items: AuditEntry[] }>(
      `/audit/log?target_type=dns_zone&target_id=${zoneId}&page_size=200`,
    )).data.items ?? [],
  });
  // Record-level events: ask audit/log for any dns_record entry whose
  // target_id matches one of this zone's *current* records. Records
  // that have been deleted (so they're not in recordIds) won't appear
  // here — accepted tradeoff vs. storing zone_id on every audit row.
  const recordEvents = useQuery({
    queryKey: ['audit', 'dns_record', zoneId, recordIds.join(',')],
    enabled: recordIds.length > 0,
    queryFn: async () => (await http.get<{ items: AuditEntry[] }>(
      `/audit/log?target_type=dns_record&target_ids=${recordIds.join(',')}&page_size=500`,
    )).data.items ?? [],
  });

  const entries = useMemo(() => {
    const merged = [
      ...(zoneEvents.data ?? []),
      ...(recordEvents.data ?? []),
    ];
    return merged.sort(
      (a, b) => new Date(b.occurred_at).getTime() - new Date(a.occurred_at).getTime(),
    );
  }, [zoneEvents.data, recordEvents.data]);

  const loading = zoneEvents.isLoading || recordEvents.isLoading;

  return (
    <>
      <Table<AuditEntry>
        variant="embedded"
        loading={loading}
        loadingText="Loading activity…"
        items={entries}
        trackBy="id"
        // Whole-row click opens the detail modal — the timestamp cell
        // is also a Link for keyboard-only navigation.
        onRowClick={({ detail }) => setOpenEntry(detail.item)}
        columnDefinitions={[
          {
            id: 'when', header: 'When (UTC)',
            cell: (e) => {
              const zulu = new Date(e.occurred_at).toISOString().replace(/\.\d{3}Z$/, 'Z');
              return (
                <Link
                  href={`#audit-${e.id}`}
                  onFollow={(ev) => { ev.preventDefault(); setOpenEntry(e); }}
                >
                  <span style={MONO}>{zulu}</span>
                </Link>
              );
            },
            width: 220,
          },
          {
            id: 'actor', header: 'Actor',
            cell: (e) => e.actor_label
              ?? <Box color="text-status-inactive" fontSize="body-s">—</Box>,
          },
          {
            id: 'action', header: 'Action',
            cell: (e) => <span style={MONO}>{e.action}</span>,
          },
          {
            id: 'target', header: 'Target',
            cell: (e) => e.target_type
              ? <span style={MONO}>{e.target_type}:{(e.target_id ?? '').slice(0, 8)}…</span>
              : <Box color="text-status-inactive" fontSize="body-s">—</Box>,
            width: 220,
          },
          {
            id: 'result', header: 'Result',
            cell: (e) => e.success
              ? <StatusIndicator type="success">ok</StatusIndicator>
              : <StatusIndicator type="error">fail</StatusIndicator>,
            width: 80,
          },
        ]}
        empty={
          <Box textAlign="center" padding="l" color="text-status-inactive">
            No activity recorded yet for this zone.
          </Box>
        }
      />
      <Modal
        visible={openEntry !== null}
        onDismiss={() => setOpenEntry(null)}
        header="Activity detail"
        size="large"
        footer={
          <Box float="right">
            <Button onClick={() => setOpenEntry(null)}>Close</Button>
          </Box>
        }
      >
        {openEntry && <ActivityDetail entry={openEntry} />}
      </Modal>
    </>
  );
}

function ActivityDetail({ entry }: { entry: AuditEntry }) {
  const zulu = new Date(entry.occurred_at).toISOString().replace(/\.\d{3}Z$/, 'Z');
  const hasDiff = Object.keys(entry.diff_json ?? {}).length > 0;
  const hasMeta = Object.keys(entry.metadata_json ?? {}).length > 0;
  return (
    <SpaceBetween size="m">
      <KeyValuePairs
        columns={3}
        items={[
          { label: 'When (UTC)', value: <span style={MONO}>{zulu}</span> },
          {
            label: 'Actor',
            value: entry.actor_label
              ?? <Box color="text-status-inactive">—</Box>,
          },
          {
            label: 'Result',
            value: entry.success
              ? <StatusIndicator type="success">ok</StatusIndicator>
              : <StatusIndicator type="error">fail</StatusIndicator>,
          },
          { label: 'Action', value: <span style={MONO}>{entry.action}</span> },
          {
            label: 'Target',
            value: entry.target_type
              ? <span style={MONO}>{entry.target_type}:{entry.target_id}</span>
              : <Box color="text-status-inactive">—</Box>,
          },
          {
            label: 'Actor IP',
            value: entry.actor_ip
              ? <span style={MONO}>{entry.actor_ip}</span>
              : <Box color="text-status-inactive">—</Box>,
          },
          {
            label: 'Request ID',
            value: entry.request_id
              ? <span style={MONO}>{entry.request_id}</span>
              : <Box color="text-status-inactive">—</Box>,
          },
        ]}
      />
      {hasDiff && (
        <Box>
          <Box variant="awsui-key-label">Diff</Box>
          <pre style={{
            maxHeight: '32vh', overflow: 'auto', padding: '12px',
            fontSize: '12px', fontFamily: 'ui-monospace, monospace', margin: 0,
            background: colorBackgroundContainerContent,
            border: `1px solid ${colorBorderDividerDefault}`,
            borderRadius: '8px',
          }}>
            {JSON.stringify(entry.diff_json, null, 2)}
          </pre>
        </Box>
      )}
      {hasMeta && (
        <Box>
          <Box variant="awsui-key-label">Metadata</Box>
          <pre style={{
            maxHeight: '32vh', overflow: 'auto', padding: '12px',
            fontSize: '12px', fontFamily: 'ui-monospace, monospace', margin: 0,
            background: colorBackgroundContainerContent,
            border: `1px solid ${colorBorderDividerDefault}`,
            borderRadius: '8px',
          }}>
            {JSON.stringify(entry.metadata_json, null, 2)}
          </pre>
        </Box>
      )}
      {!hasDiff && !hasMeta && (
        <Box color="text-status-inactive">
          No diff or metadata recorded for this event.
        </Box>
      )}
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
  const [ttl, setTtl] = useState('60');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  // Reverse zones (in-addr.arpa / ip6.arpa) are intentionally absent
  // from this form — they're created automatically by the IPAM
  // projector whenever an IPAddress has a dns_name set, scoped to the
  // same (fabric, site) as its forward zone. Operators don't pre-stage
  // them by hand.
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
  switch (r.type as RecordType) {
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
    case 'SOA':
      // BIND-style one-line summary: mname rname serial refresh retry expire min
      return `${d.mname ?? ''} ${d.rname ?? ''} ${d.serial ?? 0} ${d.refresh ?? 0} ${d.retry ?? 0} ${d.expire ?? 0} ${d.minimum ?? 0}`;
    default: return JSON.stringify(d);
  }
}

// SOA fields live on the DnsZone row. This is a slim editor that
// PATCHes the zone — the backend's existing DnsZoneUpdate schema
// already accepts the SOA fields, so no new endpoint is needed.
function SoaEditForm({ zone, onSaved }: { zone: DnsZone; onSaved: () => void }) {
  const [mname, setMname] = useState(zone.soa_mname);
  const [rname, setRname] = useState(zone.soa_rname);
  const [refresh, setRefresh] = useState(String(zone.soa_refresh));
  const [retry, setRetry] = useState(String(zone.soa_retry));
  const [expire, setExpire] = useState(String(zone.soa_expire));
  const [minimum, setMinimum] = useState(String(zone.soa_minimum));
  const [defaultTtl, setDefaultTtl] = useState(String(zone.default_ttl));
  const [submitting, setSubmitting] = useState(false);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setSubmitting(true);
    try {
      await http.patch(`/dns/zones/${zone.id}`, {
        soa_mname: mname.trim() || zone.soa_mname,
        soa_rname: rname.trim() || zone.soa_rname,
        soa_refresh: Number(refresh) || zone.soa_refresh,
        soa_retry: Number(retry) || zone.soa_retry,
        soa_expire: Number(expire) || zone.soa_expire,
        soa_minimum: Number(minimum) || zone.soa_minimum,
        default_ttl: Number(defaultTtl) || zone.default_ttl,
      });
      toast.success('SOA updated');
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
            {submitting ? 'Saving…' : 'Save'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <ColumnLayout columns={2}>
            <FormField
              label="Primary nameserver (MNAME)"
              description="Label only — zone suffix is appended automatically."
            >
              <Input value={mname} onChange={({ detail }) => setMname(detail.value)} />
            </FormField>
            <FormField
              label="Responsible person (RNAME)"
              description="Mailbox label (e.g. hostmaster); zone suffix is appended."
            >
              <Input value={rname} onChange={({ detail }) => setRname(detail.value)} />
            </FormField>
          </ColumnLayout>
          <ColumnLayout columns={4}>
            <FormField label="Refresh (s)">
              <Input type="number" value={refresh} onChange={({ detail }) => setRefresh(detail.value)} />
            </FormField>
            <FormField label="Retry (s)">
              <Input type="number" value={retry} onChange={({ detail }) => setRetry(detail.value)} />
            </FormField>
            <FormField label="Expire (s)">
              <Input type="number" value={expire} onChange={({ detail }) => setExpire(detail.value)} />
            </FormField>
            <FormField
              label="Negative TTL (s)"
              description="Caching TTL for NXDOMAIN."
            >
              <Input type="number" value={minimum} onChange={({ detail }) => setMinimum(detail.value)} />
            </FormField>
          </ColumnLayout>
          <FormField
            label="Zone default TTL (s)"
            description="Fallback TTL for records that don't set their own."
          >
            <Input
              type="number" value={defaultTtl}
              onChange={({ detail }) => setDefaultTtl(detail.value)}
            />
          </FormField>
          <FormField
            label="Serial number"
            description="Read-only. Auto-incremented from the zone's last-modified timestamp on every record change."
          >
            <Input type="number" value={String(zone.serial)} readOnly disabled />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}


// Slim BIND-format import. The user pastes a zone file (or drops one
// in via the file picker); a dry-run first shows what would change,
// then the operator confirms to commit. IPAM-projected records stay
// untouched — they're owned by the sync job.
type ImportPreview = {
  zone_id: string;
  would_add: number;
  would_replace_manual: boolean;
  warnings: string[];
  parsed: {
    zone_name: string;
    soa: Record<string, unknown>;
    records: { name: string; type: string }[];
  };
};

function ZoneImportForm({ zone, onSaved }: { zone: DnsZone; onSaved: () => void }) {
  const [text, setText] = useState('');
  const [updateSoa, setUpdateSoa] = useState(false);
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState<ImportPreview | null>(null);

  function onPickFile(e: React.ChangeEvent<HTMLInputElement>) {
    const f = e.target.files?.[0];
    if (!f) return;
    const reader = new FileReader();
    reader.onload = () => setText(String(reader.result ?? ''));
    reader.readAsText(f);
  }

  async function dryRun() {
    if (!text.trim()) {
      toast.error('Paste a zone file or pick one first');
      return;
    }
    setBusy(true);
    try {
      const r = await http.post<ImportPreview>(
        `/dns/zones/${zone.id}/import`,
        { text, dry_run: true, update_soa: updateSoa },
      );
      setPreview(r.data);
    } catch (err: any) {
      toast.error(err?.message ?? 'parse failed');
      setPreview(null);
    } finally {
      setBusy(false);
    }
  }

  async function commit() {
    if (!preview) return;
    setBusy(true);
    try {
      const r = await http.post<{ added: number; removed_manual: number; warnings: string[] }>(
        `/dns/zones/${zone.id}/import`,
        { text, dry_run: false, update_soa: updateSoa },
      );
      toast.success(`Imported ${r.data.added} record(s); replaced ${r.data.removed_manual} manual row(s)`);
      onSaved();
    } catch (err: any) {
      toast.error(err?.message ?? 'import failed');
    } finally {
      setBusy(false);
    }
  }

  return (
    <Form
      actions={
        <SpaceBetween size="xs" direction="horizontal">
          {preview && (
            <Button variant="primary" onClick={commit} loading={busy}>
              Import {preview.would_add} record(s)
            </Button>
          )}
          {!preview && (
            <Button variant="primary" onClick={dryRun} loading={busy}>
              Preview
            </Button>
          )}
        </SpaceBetween>
      }
    >
      <SpaceBetween size="m">
        <FormField
          label="Zone file (BIND format)"
          description="Paste the contents directly or pick a .zone file."
        >
          <SpaceBetween size="xs">
            <input type="file" accept=".zone,.txt,text/plain" onChange={onPickFile} />
            <textarea
              value={text}
              onChange={(e) => { setText(e.target.value); setPreview(null); }}
              rows={10}
              placeholder={'$ORIGIN example.com.\n$TTL 60\n@ IN SOA …\n…'}
              style={{
                width: '100%', padding: 8,
                fontFamily: 'ui-monospace, monospace', fontSize: 12,
                background: 'var(--color-background-input-default, transparent)',
                color: 'inherit',
                border: '1px solid var(--color-border-input-default, #ccc)',
                borderRadius: 6,
              }}
            />
          </SpaceBetween>
        </FormField>
        <FormField
          label="Also adopt SOA timers from the file"
          description="Off by default — public zones often ship with multi-hour timers that don't fit DCIM's push-driven model."
        >
          <input
            type="checkbox"
            checked={updateSoa}
            onChange={(e) => setUpdateSoa(e.target.checked)}
          />
        </FormField>
        {preview && (
          <Container header={<Header variant="h3">Preview</Header>}>
            <SpaceBetween size="s">
              <KeyValuePairs
                columns={3}
                items={[
                  { label: 'Records to add', value: preview.would_add },
                  { label: 'Replaces manual', value: 'all existing source=manual rows' },
                  { label: 'Warnings', value: preview.warnings.length },
                ]}
              />
              {preview.warnings.length > 0 && (
                <Box>
                  <Box variant="awsui-key-label">Warnings</Box>
                  <ul style={{ margin: 0, fontSize: 12 }}>
                    {preview.warnings.map((w, i) => <li key={i}>{w}</li>)}
                  </ul>
                </Box>
              )}
              {preview.parsed.records.length > 0 && (
                <Box>
                  <Box variant="awsui-key-label">First 10 records</Box>
                  <pre style={{
                    maxHeight: '30vh', overflow: 'auto', padding: 8,
                    fontFamily: 'ui-monospace, monospace', fontSize: 11, margin: 0,
                    background: colorBackgroundContainerContent,
                    border: `1px solid ${colorBorderDividerDefault}`,
                    borderRadius: 6,
                  }}>
                    {preview.parsed.records.slice(0, 10).map((r) =>
                      `${r.name}\t${r.type}`).join('\n')}
                  </pre>
                </Box>
              )}
            </SpaceBetween>
          </Container>
        )}
      </SpaceBetween>
    </Form>
  );
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
      case 'SOA':
        // SOA isn't a creatable record type — it's edited via the
        // SoaEditForm modal — but the type union includes it so this
        // branch keeps the switch exhaustive.
        return {};
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
              description="Defaults to 60 seconds"
            >
              <Input
                type="number"
                value={ttl}
                onChange={({ detail }) => setTtl(detail.value)}
                placeholder="60"
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

// ----------------------- Conditional forwarders -----------------------

function ForwardersPanel({ fabricId, canWrite }: { fabricId: string; canWrite: boolean }) {
  const qc = useQueryClient();
  const forwardersQ = useQuery({
    queryKey: ['dns-forwarders', fabricId],
    queryFn: async () => (
      await http.get<{ items: DnsForwarder[] }>(`/dns/forwarders?fabric_id=${fabricId}&page_size=200`)
    ).data.items ?? [],
  });
  const forwarders = forwardersQ.data ?? [];

  const [createOpen, setCreateOpen] = useState(false);
  const [editFwd, setEditFwd] = useState<DnsForwarder | null>(null);

  async function refresh() {
    await qc.invalidateQueries({ queryKey: ['dns-forwarders', fabricId] });
  }

  async function remove(f: DnsForwarder) {
    if (!window.confirm(`Delete forwarder ${f.name} (${f.zone_pattern})?`)) return;
    try {
      await http.delete(`/dns/forwarders/${f.id}`);
      await refresh();
      toast.success('Forwarder removed');
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <>
      <Table<DnsForwarder>
        variant="container"
        loading={forwardersQ.isLoading}
        loadingText="Loading forwarders…"
        items={forwarders}
        trackBy="id"
        header={
          <Header
            counter={`(${forwarders.length})`}
            actions={canWrite && (
              <Button variant="primary" onClick={() => setCreateOpen(true)}>
                Create forwarder
              </Button>
            )}
            description="Route specific zones from the recursive resolver to alternate upstreams (e.g. cloud-private resolvers, partner DNS)."
          >
            Conditional forwarders
          </Header>
        }
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (f) => f.name },
          {
            id: 'zone_pattern', header: 'Zone pattern',
            cell: (f) => <span style={MONO}>{f.zone_pattern}</span>,
            width: 240,
          },
          {
            id: 'upstreams', header: 'Upstreams',
            cell: (f) => (
              <span style={MONO}>
                {(f.upstreams ?? []).join(', ') || <Box color="text-status-inactive">—</Box>}
              </span>
            ),
          },
          {
            id: 'description', header: 'Description',
            cell: (f) => f.description || <Box color="text-status-inactive">—</Box>,
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (f: DnsForwarder) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button iconName="edit" variant="inline-icon" onClick={() => setEditFwd(f)} ariaLabel={`Edit ${f.name}`} />
                <Button iconName="remove" variant="inline-icon" onClick={() => remove(f)} ariaLabel={`Delete ${f.name}`} />
              </SpaceBetween>
            ),
            width: 110,
          }] : []),
        ]}
        empty={
          <Box textAlign="center" padding="l">
            <SpaceBetween size="xs">
              <b>No conditional forwarders</b>
              <Box color="text-status-inactive" fontSize="body-s">
                The recursive resolver forwards every query that doesn't match a
                local zone to the configured global upstreams. Add a forwarder
                to route a specific zone (e.g. <span style={MONO}>aws.internal.</span>)
                elsewhere.
              </Box>
              {canWrite && (
                <Button variant="primary" onClick={() => setCreateOpen(true)}>
                  Create forwarder
                </Button>
              )}
            </SpaceBetween>
          </Box>
        }
      />
      {canWrite && (
        <>
          <Modal
            visible={createOpen}
            onDismiss={() => setCreateOpen(false)}
            header="New conditional forwarder"
            size="medium"
          >
            <ForwarderForm
              fabricId={fabricId}
              onSaved={async () => { setCreateOpen(false); await refresh(); }}
            />
          </Modal>
          <Modal
            visible={editFwd !== null}
            onDismiss={() => setEditFwd(null)}
            header="Edit forwarder"
            size="medium"
          >
            {editFwd && (
              <ForwarderForm
                fabricId={fabricId}
                forwarder={editFwd}
                onSaved={async () => { setEditFwd(null); await refresh(); }}
              />
            )}
          </Modal>
        </>
      )}
    </>
  );
}

function ForwarderForm({
  fabricId, forwarder, onSaved,
}: {
  fabricId: string;
  forwarder?: DnsForwarder;
  onSaved: () => void;
}) {
  const [name, setName] = useState(forwarder?.name ?? '');
  const [zonePattern, setZonePattern] = useState(forwarder?.zone_pattern ?? '');
  // Operator types one upstream per line — easier than fiddling with a
  // chip-style multi-input and gives them room to paste a list.
  const [upstreamsRaw, setUpstreamsRaw] = useState((forwarder?.upstreams ?? []).join('\n'));
  const [description, setDescription] = useState(forwarder?.description ?? '');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Required';
    if (!zonePattern.trim()) errs.zone_pattern = 'Required (e.g. aws.internal)';
    const upstreams = upstreamsRaw
      .split(/\r?\n/)
      .map((s) => s.trim())
      .filter(Boolean);
    if (upstreams.length === 0) errs.upstreams = 'At least one upstream is required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      if (forwarder) {
        await http.patch(`/dns/forwarders/${forwarder.id}`, {
          name, zone_pattern: zonePattern, upstreams, description: description || null,
        });
        toast.success('Forwarder updated');
      } else {
        await http.post('/dns/forwarders', {
          name, fabric_id: fabricId, zone_pattern: zonePattern, upstreams,
          description: description || null,
        });
        toast.success('Forwarder created');
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
            {submitting ? 'Saving…' : forwarder ? 'Save' : 'Create'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <ColumnLayout columns={2}>
            <FormField label="Name" errorText={errors.name}>
              <Input
                value={name}
                onChange={({ detail }) => setName(detail.value)}
                placeholder="aws-vpc-resolver"
              />
            </FormField>
            <FormField
              label="Zone pattern"
              description="Trailing dot is added automatically"
              errorText={errors.zone_pattern}
            >
              <Input
                value={zonePattern}
                onChange={({ detail }) => setZonePattern(detail.value)}
                placeholder="aws.internal"
              />
            </FormField>
          </ColumnLayout>
          <FormField
            label="Upstreams"
            description="One per line. Use ip or ip:port."
            errorText={errors.upstreams}
          >
            <textarea
              value={upstreamsRaw}
              onChange={(e) => setUpstreamsRaw(e.target.value)}
              placeholder={'10.250.0.2\n10.250.0.3'}
              rows={4}
              style={{
                width: '100%', padding: 8,
                fontFamily: 'ui-monospace, monospace', fontSize: 13,
                background: 'var(--color-background-input-default, transparent)',
                color: 'inherit',
                border: '1px solid var(--color-border-input-default, #ccc)',
                borderRadius: 6,
              }}
            />
          </FormField>
          <FormField label="Description (optional)">
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

// ----------------------- Blocklists -----------------------

function BlocklistsPanel({ fabricId, canWrite }: { fabricId: string; canWrite: boolean }) {
  const qc = useQueryClient();
  const blocklistsQ = useQuery({
    queryKey: ['dns-blocklists', fabricId],
    queryFn: async () => (
      await http.get<{ items: DnsBlocklist[] }>(`/dns/blocklists?fabric_id=${fabricId}&page_size=200`)
    ).data.items ?? [],
  });
  const blocklists = blocklistsQ.data ?? [];
  const [openId, setOpenId] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [editBl, setEditBl] = useState<DnsBlocklist | null>(null);
  const opened = blocklists.find((b) => b.id === openId) ?? null;

  async function refresh() {
    await qc.invalidateQueries({ queryKey: ['dns-blocklists', fabricId] });
  }

  async function remove(b: DnsBlocklist) {
    if (!window.confirm(`Delete blocklist ${b.name}? Entries are removed too.`)) return;
    try {
      await http.delete(`/dns/blocklists/${b.id}`);
      if (openId === b.id) setOpenId(null);
      await refresh();
      toast.success('Blocklist removed');
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  async function toggleEnabled(b: DnsBlocklist) {
    try {
      await http.patch(`/dns/blocklists/${b.id}`, { enabled: !b.enabled });
      await refresh();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  if (opened) {
    return (
      <BlocklistDetailView
        blocklist={opened}
        onBack={() => setOpenId(null)}
        canWrite={canWrite}
      />
    );
  }

  return (
    <>
      <Table<DnsBlocklist>
        variant="container"
        loading={blocklistsQ.isLoading}
        loadingText="Loading blocklists…"
        items={blocklists}
        trackBy="id"
        header={
          <Header
            counter={`(${blocklists.length})`}
            actions={canWrite && (
              <Button variant="primary" onClick={() => setCreateOpen(true)}>
                Create blocklist
              </Button>
            )}
            description="DNS-layer block / sinkhole rules applied at the recursive resolver. Click a name to manage its patterns."
          >
            Blocklists
          </Header>
        }
        columnDefinitions={[
          {
            id: 'name', header: 'Name',
            cell: (b) => (
              <Link
                href={`#blocklist-${b.id}`}
                onFollow={(e) => { e.preventDefault(); setOpenId(b.id); }}
              >
                {b.name}
              </Link>
            ),
          },
          {
            id: 'action', header: 'Action',
            cell: (b) => b.action === 'block'
              ? <Badge color="red">Block (NXDOMAIN)</Badge>
              : <Badge color="severity-medium">Sinkhole</Badge>,
            width: 200,
          },
          {
            id: 'sink', header: 'Sink target',
            cell: (b) => b.action === 'sinkhole'
              ? (
                <span style={MONO}>
                  {[b.sink_ipv4, b.sink_ipv6].filter(Boolean).join(', ') || '—'}
                </span>
              )
              : <Box color="text-status-inactive">—</Box>,
          },
          {
            id: 'enabled', header: 'Status',
            cell: (b) => b.enabled
              ? <StatusIndicator type="success">Enabled</StatusIndicator>
              : <StatusIndicator type="stopped">Disabled</StatusIndicator>,
            width: 130,
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (b: DnsBlocklist) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button
                  iconName={b.enabled ? 'status-stopped' : 'status-positive'}
                  variant="inline-icon"
                  onClick={() => toggleEnabled(b)}
                  ariaLabel={`${b.enabled ? 'Disable' : 'Enable'} ${b.name}`}
                />
                <Button iconName="edit" variant="inline-icon" onClick={() => setEditBl(b)} ariaLabel={`Edit ${b.name}`} />
                <Button iconName="remove" variant="inline-icon" onClick={() => remove(b)} ariaLabel={`Delete ${b.name}`} />
              </SpaceBetween>
            ),
            width: 140,
          }] : []),
        ]}
        empty={
          <Box textAlign="center" padding="l">
            <SpaceBetween size="xs">
              <b>No blocklists</b>
              <Box color="text-status-inactive" fontSize="body-s">
                A blocklist groups patterns the recursive should refuse (NXDOMAIN)
                or redirect to a sinkhole IP. Useful for ingesting threat feeds.
              </Box>
              {canWrite && (
                <Button variant="primary" onClick={() => setCreateOpen(true)}>
                  Create blocklist
                </Button>
              )}
            </SpaceBetween>
          </Box>
        }
      />
      {canWrite && (
        <>
          <Modal
            visible={createOpen}
            onDismiss={() => setCreateOpen(false)}
            header="New blocklist"
            size="medium"
          >
            <BlocklistForm
              fabricId={fabricId}
              onSaved={async () => { setCreateOpen(false); await refresh(); }}
            />
          </Modal>
          <Modal
            visible={editBl !== null}
            onDismiss={() => setEditBl(null)}
            header="Edit blocklist"
            size="medium"
          >
            {editBl && (
              <BlocklistForm
                fabricId={fabricId}
                blocklist={editBl}
                onSaved={async () => { setEditBl(null); await refresh(); }}
              />
            )}
          </Modal>
        </>
      )}
    </>
  );
}

function BlocklistForm({
  fabricId, blocklist, onSaved,
}: {
  fabricId: string;
  blocklist?: DnsBlocklist;
  onSaved: () => void;
}) {
  const [name, setName] = useState(blocklist?.name ?? '');
  const [actionOpt, setActionOpt] = useState<SelectProps.Option>(
    { value: blocklist?.action ?? 'block',
      label: blocklist?.action === 'sinkhole' ? 'Sinkhole' : 'Block (NXDOMAIN)' },
  );
  const [sinkV4, setSinkV4] = useState(blocklist?.sink_ipv4 ?? '');
  const [sinkV6, setSinkV6] = useState(blocklist?.sink_ipv6 ?? '');
  const [description, setDescription] = useState(blocklist?.description ?? '');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const isSinkhole = actionOpt.value === 'sinkhole';

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Required';
    if (isSinkhole && !sinkV4.trim() && !sinkV6.trim()) {
      errs.sink = 'At least one sink IP required for sinkhole';
    }
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      const body: Record<string, unknown> = {
        name,
        action: actionOpt.value,
        sink_ipv4: isSinkhole && sinkV4.trim() ? sinkV4.trim() : null,
        sink_ipv6: isSinkhole && sinkV6.trim() ? sinkV6.trim() : null,
        description: description.trim() || null,
      };
      if (blocklist) {
        await http.patch(`/dns/blocklists/${blocklist.id}`, body);
        toast.success('Blocklist updated');
      } else {
        body.fabric_id = fabricId;
        await http.post('/dns/blocklists', body);
        toast.success('Blocklist created');
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
            {submitting ? 'Saving…' : blocklist ? 'Save' : 'Create'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <ColumnLayout columns={2}>
            <FormField label="Name" errorText={errors.name}>
              <Input value={name} onChange={({ detail }) => setName(detail.value)} placeholder="malware-feed-2026" />
            </FormField>
            <FormField label="Action">
              <Select
                selectedOption={actionOpt}
                onChange={({ detail }) => {
                  if (detail.selectedOption.value) setActionOpt(detail.selectedOption);
                }}
                options={[
                  { value: 'block', label: 'Block (NXDOMAIN)' },
                  { value: 'sinkhole', label: 'Sinkhole' },
                ]}
                expandToViewport
              />
            </FormField>
          </ColumnLayout>
          {isSinkhole && (
            <ColumnLayout columns={2}>
              <FormField label="Sink IPv4" errorText={errors.sink}>
                <Input value={sinkV4} onChange={({ detail }) => setSinkV4(detail.value)} placeholder="10.0.0.250" />
              </FormField>
              <FormField label="Sink IPv6">
                <Input value={sinkV6} onChange={({ detail }) => setSinkV6(detail.value)} placeholder="fd00::dead" />
              </FormField>
            </ColumnLayout>
          )}
          <FormField label="Description (optional)">
            <Input value={description} onChange={({ detail }) => setDescription(detail.value)} />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}

function BlocklistDetailView({
  blocklist, onBack, canWrite,
}: {
  blocklist: DnsBlocklist;
  onBack: () => void;
  canWrite: boolean;
}) {
  const qc = useQueryClient();
  const entriesQ = useQuery({
    queryKey: ['dns-blocklist-entries', blocklist.id],
    queryFn: async () => (
      await http.get<{ items: DnsBlocklistEntry[] }>(
        `/dns/blocklists/${blocklist.id}/entries?page_size=500`,
      )
    ).data.items ?? [],
  });
  const entries = entriesQ.data ?? [];
  const [filterText, setFilterText] = useState('');
  const [bulkOpen, setBulkOpen] = useState(false);
  const filtered = useMemo(() => {
    const q = filterText.trim().toLowerCase();
    if (!q) return entries;
    return entries.filter((e) => e.pattern.toLowerCase().includes(q));
  }, [entries, filterText]);

  async function refresh() {
    await qc.invalidateQueries({ queryKey: ['dns-blocklist-entries', blocklist.id] });
  }

  async function remove(e: DnsBlocklistEntry) {
    if (!window.confirm(`Delete pattern "${e.pattern}"?`)) return;
    try {
      await http.delete(`/dns/blocklists/${blocklist.id}/entries/${e.id}`);
      await refresh();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <SpaceBetween size="m">
      <div>
        <Link
          href="#blocklists"
          onFollow={(e) => { e.preventDefault(); onBack(); }}
        >
          ← Blocklists
        </Link>
      </div>
      <Container
        header={<Header variant="h2">{blocklist.name}</Header>}
      >
        <KeyValuePairs
          columns={3}
          items={[
            {
              label: 'Action',
              value: blocklist.action === 'block'
                ? <Badge color="red">Block (NXDOMAIN)</Badge>
                : <Badge color="severity-medium">Sinkhole</Badge>,
            },
            {
              label: 'Status',
              value: blocklist.enabled
                ? <StatusIndicator type="success">Enabled</StatusIndicator>
                : <StatusIndicator type="stopped">Disabled</StatusIndicator>,
            },
            {
              label: 'Sink',
              value: blocklist.action === 'sinkhole'
                ? (
                  <span style={MONO}>
                    {[blocklist.sink_ipv4, blocklist.sink_ipv6].filter(Boolean).join(', ') || '—'}
                  </span>
                )
                : <Box color="text-status-inactive">—</Box>,
            },
            {
              label: 'Description',
              value: blocklist.description || <Box color="text-status-inactive">—</Box>,
            },
          ]}
        />
      </Container>
      <Table<DnsBlocklistEntry>
        variant="container"
        loading={entriesQ.isLoading}
        loadingText="Loading entries…"
        items={filtered}
        trackBy="id"
        filter={
          <TextFilter
            filteringText={filterText}
            filteringPlaceholder="Filter patterns"
            filteringAriaLabel="Filter patterns"
            onChange={({ detail }) => setFilterText(detail.filteringText)}
            countText={`${filtered.length} match${filtered.length === 1 ? '' : 'es'}`}
          />
        }
        header={
          <Header
            counter={`(${entries.length})`}
            actions={canWrite && (
              <Button variant="primary" onClick={() => setBulkOpen(true)}>
                Add patterns
              </Button>
            )}
          >
            Patterns
          </Header>
        }
        columnDefinitions={[
          {
            id: 'pattern', header: 'Pattern',
            cell: (e) => <span style={MONO}>{e.pattern}</span>,
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (e: DnsBlocklistEntry) => (
              <Button
                iconName="remove"
                variant="inline-icon"
                onClick={() => remove(e)}
                ariaLabel={`Delete ${e.pattern}`}
              />
            ),
            width: 80,
          }] : []),
        ]}
        empty={
          <Box textAlign="center" color="text-status-inactive" padding="m">
            No patterns yet. Use "Add patterns" to paste a list.
          </Box>
        }
      />
      {canWrite && (
        <Modal
          visible={bulkOpen}
          onDismiss={() => setBulkOpen(false)}
          header="Add patterns"
          size="medium"
        >
          <BulkPatternForm
            blocklistId={blocklist.id}
            onSaved={async () => { setBulkOpen(false); await refresh(); }}
          />
        </Modal>
      )}
    </SpaceBetween>
  );
}

function BulkPatternForm({
  blocklistId, onSaved,
}: { blocklistId: string; onSaved: () => void }) {
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const patterns = text.split(/\r?\n/).map((s) => s.trim()).filter(Boolean);
    if (patterns.length === 0) {
      toast.error('Paste at least one pattern');
      return;
    }
    setBusy(true);
    try {
      const r = await http.post<{ added: number; skipped: number }>(
        `/dns/blocklists/${blocklistId}/entries/bulk`,
        { patterns },
      );
      toast.success(`Added ${r.data.added}; skipped ${r.data.skipped} duplicate(s)`);
      onSaved();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={onSubmit}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={busy}>
            {busy ? 'Adding…' : 'Add'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField
            label="Patterns"
            description={
              <>One per line. Use <span style={MONO}>*.</span> for leading wildcards
              (e.g. <span style={MONO}>*.evil.example</span>). Bare names match exactly.</>
            }
          >
            <textarea
              value={text}
              onChange={(e) => setText(e.target.value)}
              rows={12}
              placeholder={'*.malware.example\nads.tracker.example\nphish-domain.example'}
              style={{
                width: '100%', padding: 8,
                fontFamily: 'ui-monospace, monospace', fontSize: 12,
                background: 'var(--color-background-input-default, transparent)',
                color: 'inherit',
                border: '1px solid var(--color-border-input-default, #ccc)',
                borderRadius: 6,
              }}
            />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}

// ----------------------- Views (split-horizon) -----------------------

function ViewsPanel({ fabricId, canWrite }: { fabricId: string; canWrite: boolean }) {
  const qc = useQueryClient();
  const viewsQ = useQuery({
    queryKey: ['dns-views', fabricId],
    queryFn: async () => (
      await http.get<{ items: DnsView[] }>(`/dns/views?fabric_id=${fabricId}&page_size=200`)
    ).data.items ?? [],
  });
  const views = viewsQ.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);
  const [editView, setEditView] = useState<DnsView | null>(null);

  async function refresh() {
    await qc.invalidateQueries({ queryKey: ['dns-views', fabricId] });
  }

  async function remove(v: DnsView) {
    if (!window.confirm(`Delete view ${v.name}? Records bound to it become default-view answers.`)) return;
    try {
      await http.delete(`/dns/views/${v.id}`);
      await refresh();
      toast.success('View removed');
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <>
      <Table<DnsView>
        variant="container"
        loading={viewsQ.isLoading}
        loadingText="Loading views…"
        items={views}
        trackBy="id"
        header={
          <Header
            counter={`(${views.length})`}
            actions={canWrite && (
              <Button variant="primary" onClick={() => setCreateOpen(true)}>
                Create view
              </Button>
            )}
            description="Split-horizon answer sets — clients matching a view's CIDRs see a different answer for the same FQDN."
          >
            Views
          </Header>
        }
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (v) => v.name },
          { id: 'priority', header: 'Priority', cell: (v) => v.priority, width: 100 },
          {
            id: 'cidrs', header: 'Match CIDRs',
            cell: (v) => (
              <span style={MONO}>
                {(v.match_cidrs ?? []).join(', ')
                 || <Box color="text-status-inactive">none — matches no clients</Box>}
              </span>
            ),
          },
          {
            id: 'description', header: 'Description',
            cell: (v) => v.description || <Box color="text-status-inactive">—</Box>,
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (v: DnsView) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button iconName="edit" variant="inline-icon" onClick={() => setEditView(v)} ariaLabel={`Edit ${v.name}`} />
                <Button iconName="remove" variant="inline-icon" onClick={() => remove(v)} ariaLabel={`Delete ${v.name}`} />
              </SpaceBetween>
            ),
            width: 110,
          }] : []),
        ]}
        empty={
          <Box textAlign="center" padding="l">
            <SpaceBetween size="xs">
              <b>No split-horizon views</b>
              <Box color="text-status-inactive" fontSize="body-s">
                Without views, every record is served to every client. Create
                a view to deliver different answers to specific CIDRs (e.g.
                an internal-only set of records to the management plane).
              </Box>
              {canWrite && (
                <Button variant="primary" onClick={() => setCreateOpen(true)}>
                  Create view
                </Button>
              )}
            </SpaceBetween>
          </Box>
        }
      />
      {canWrite && (
        <>
          <Modal
            visible={createOpen}
            onDismiss={() => setCreateOpen(false)}
            header="New view"
            size="medium"
          >
            <ViewForm
              fabricId={fabricId}
              onSaved={async () => { setCreateOpen(false); await refresh(); }}
            />
          </Modal>
          <Modal
            visible={editView !== null}
            onDismiss={() => setEditView(null)}
            header="Edit view"
            size="medium"
          >
            {editView && (
              <ViewForm
                fabricId={fabricId}
                view={editView}
                onSaved={async () => { setEditView(null); await refresh(); }}
              />
            )}
          </Modal>
        </>
      )}
    </>
  );
}

function ViewForm({
  fabricId, view, onSaved,
}: {
  fabricId: string;
  view?: DnsView;
  onSaved: () => void;
}) {
  const [name, setName] = useState(view?.name ?? '');
  const [cidrs, setCidrs] = useState((view?.match_cidrs ?? []).join('\n'));
  const [priority, setPriority] = useState(String(view?.priority ?? 100));
  const [description, setDescription] = useState(view?.description ?? '');
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!name.trim()) {
      toast.error('Name required');
      return;
    }
    setBusy(true);
    try {
      const body: Record<string, unknown> = {
        name: name.trim(),
        match_cidrs: cidrs.split(/\r?\n/).map((s) => s.trim()).filter(Boolean),
        priority: Number(priority) || 100,
        description: description.trim() || null,
      };
      if (view) {
        await http.patch(`/dns/views/${view.id}`, body);
        toast.success('View updated');
      } else {
        body.fabric_id = fabricId;
        await http.post('/dns/views', body);
        toast.success('View created');
      }
      onSaved();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={onSubmit}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={busy}>
            {busy ? 'Saving…' : view ? 'Save' : 'Create'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <ColumnLayout columns={2}>
            <FormField label="Name">
              <Input value={name} onChange={({ detail }) => setName(detail.value)} placeholder="internal" />
            </FormField>
            <FormField
              label="Priority"
              description="Lower = wins when multiple views match a client."
            >
              <Input type="number" value={priority} onChange={({ detail }) => setPriority(detail.value)} />
            </FormField>
          </ColumnLayout>
          <FormField
            label="Match CIDRs"
            description="One per line. IPv4 or IPv6. Empty list matches no clients (view becomes inert)."
          >
            <textarea
              value={cidrs}
              onChange={(e) => setCidrs(e.target.value)}
              rows={6}
              placeholder={'10.0.0.0/8\n192.168.0.0/16'}
              style={{
                width: '100%', padding: 8,
                fontFamily: 'ui-monospace, monospace', fontSize: 12,
                background: 'var(--color-background-input-default, transparent)',
                color: 'inherit',
                border: '1px solid var(--color-border-input-default, #ccc)',
                borderRadius: 6,
              }}
            />
          </FormField>
          <FormField label="Description (optional)">
            <Input value={description} onChange={({ detail }) => setDescription(detail.value)} />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
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
            id: 'status', header: 'Status',
            cell: (s) => <RenderStatusCell server={s} />,
            width: 120,
          },
          {
            id: 'last_render', header: 'Last render (UTC)',
            cell: (s) => <LastRenderCell server={s} />,
            width: 220,
          },
          {
            id: 'metrics', header: 'QPS (last hour)',
            cell: (s) => <ServerMetricsCell serverId={s.id} />,
            width: 240,
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


function RenderStatusCell({ server }: { server: DnsServer }) {
  if (!server.last_render_at) {
    return <Box color="text-status-inactive" fontSize="body-s">Never</Box>;
  }
  const ok = server.last_render_status === 'ok';
  // The title gives a hover-over for the error text — the operator's
  // quickest path when last_render_status='error'.
  return (
    <span title={server.last_render_error ?? ''}>
      <StatusIndicator type={ok ? 'success' : 'error'}>
        {ok ? 'OK' : 'Down'}
      </StatusIndicator>
    </span>
  );
}

// Per-server inline sparkline of QPS over the last hour, plus the
// most recent NXDOMAIN%. Each cell fires its own /metrics fetch with
// a 30s staleTime so a Servers tab with N rows produces N polling
// streams rather than one — acceptable while N stays small.
type MetricsSample = {
  observed_at: string;
  interval_seconds: number;
  queries: number;
  nxdomain: number;
  servfail: number;
  noerror: number;
  p50_ms: number | null;
  p95_ms: number | null;
};

function ServerMetricsCell({ serverId }: { serverId: string }) {
  const { data, isLoading } = useQuery({
    queryKey: ['dns-server-metrics', serverId],
    queryFn: async () => (
      await http.get<MetricsSample[]>(`/dns/servers/${serverId}/metrics?minutes=60`)
    ).data ?? [],
    refetchInterval: 30_000,
    staleTime: 25_000,
  });
  const samples = data ?? [];
  if (isLoading) {
    return <Box color="text-status-inactive" fontSize="body-s">…</Box>;
  }
  if (samples.length === 0) {
    return <Box color="text-status-inactive" fontSize="body-s">No samples yet</Box>;
  }
  const chartData = samples.map((s) => ({
    t: new Date(s.observed_at).getTime(),
    qps: s.interval_seconds > 0 ? s.queries / s.interval_seconds : 0,
  }));
  const last = samples[samples.length - 1];
  const lastQps = chartData[chartData.length - 1].qps;
  const nxRate = last.queries > 0 ? (last.nxdomain / last.queries) * 100 : 0;
  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
      <div style={{ flex: 1, height: 32, minWidth: 80 }}>
        <ResponsiveContainer width="100%" height="100%">
          <LineChart data={chartData}>
            <XAxis dataKey="t" hide />
            <YAxis hide domain={[0, 'dataMax + 1']} />
            <Tooltip
              labelFormatter={(t) => new Date(Number(t)).toISOString().replace(/\.\d{3}Z$/, 'Z')}
              formatter={(v: number) => [`${v.toFixed(2)} qps`, 'QPS']}
            />
            <Line
              type="monotone" dataKey="qps"
              stroke="currentColor" strokeWidth={1.5}
              dot={false} isAnimationActive={false}
            />
          </LineChart>
        </ResponsiveContainer>
      </div>
      <div style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12, lineHeight: 1.2 }}>
        <div>{lastQps.toFixed(1)} qps</div>
        <div style={{ color: 'var(--color-text-status-inactive)' }}>
          NX {nxRate.toFixed(1)}%
        </div>
      </div>
    </div>
  );
}

// Render the last-render timestamp in Zulu (UTC ISO 8601, second
// precision) — easier to correlate across sites than localized time.
function LastRenderCell({ server }: { server: DnsServer }) {
  if (!server.last_render_at) {
    return <Box color="text-status-inactive" fontSize="body-s">—</Box>;
  }
  const d = new Date(server.last_render_at);
  const zulu = d.toISOString().replace(/\.\d{3}Z$/, 'Z');
  return <span style={MONO}>{zulu}</span>;
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

