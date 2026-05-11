import { Fragment, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router';
import {
  DndContext, DragEndEvent, DragOverEvent, DragOverlay, DragStartEvent,
  KeyboardSensor, MouseSensor, TouchSensor,
  useDraggable, useDroppable, useSensor, useSensors,
} from '@dnd-kit/core';
import { useTable, useGetIdentity, useList } from '@refinedev/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import {
  colorBackgroundCellShaded, colorBackgroundContainerContent,
  colorBackgroundInputDisabled, colorBackgroundItemSelected,
  colorBorderDividerDefault, colorTextBodySecondary,
  colorTextStatusError, colorTextStatusInactive, colorTextStatusInfo,
  colorTextStatusSuccess, colorTextStatusWarning,
} from '@cloudscape-design/design-tokens';

import { http } from '@/lib/http';
import { formatDate } from '@/lib/utils';
import { CapacityBar } from '@/components/capacity-bar';
import { toast } from 'sonner';
import { DnsTab } from '@/components/dns-tab';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import BreadcrumbGroup from '@cloudscape-design/components/breadcrumb-group';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import SegmentedControl from '@cloudscape-design/components/segmented-control';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import Tabs from '@cloudscape-design/components/tabs';

type Fabric = {
  id: string; name: string; slug: string; description: string | null;
  enclave: string | null; classification: string | null;
};
type Vrf = {
  id: string; fabric_id: string; name: string;
  route_target: string | null;
  description: string | null; is_default: boolean;
};
type Supernet = {
  id: string; fabric_id: string; vrf_id: string;
  parent_supernet_id: string | null;
  site_id: string | null;
  prefix: string; name: string | null; description: string | null;
  purpose: string | null;
};
type Subnet = {
  id: string; supernet_id: string; fabric_id: string; vrf_id: string;
  site_id: string | null; vni_id: string | null;
  prefix: string; name: string | null;
  description: string | null; purpose: string | null;
  vlan_id: number | null; gateway: string | null;
};
type Overlay = {
  id: string; fabric_id: string; name: string;
  kind: 'vxlan' | 'geneve'; udp_port: number;
  mtu: number | null; underlay_vrf_id: string | null;
  description: string | null;
};
type Vni = {
  id: string; overlay_id: string; vni: number;
  kind: 'l2' | 'l3'; name: string | null;
  description: string | null;
  vlan_id: number | null; evpn_route_target: string | null;
  vrf_id: string | null;
};
type Vtep = {
  id: string; overlay_id: string; asset_id: string;
  loopback_ip: string | null;
  role: 'leaf' | 'spine' | 'border' | 'other';
  description: string | null;
};
type IPAddr = {
  id: string; subnet_id: string; asset_id: string | null;
  address: string; role: string; status: string; source: string;
  dns_name: string | null; description: string | null;
  dhcp_lease_expires_at: string | null; dhcp_mac: string | null;
};
type Site = { id: string; code: string; name: string };
type Asset = { id: string; name: string; site_id: string };

type SubnetUtil = {
  prefix: string;
  capacity: number; allocated: number; free: number;
  percent: number; next_available: string | null;
};
type SupernetUtil = {
  capacity: number; allocated_subnet_addresses: number;
  free: number; percent: number; subnet_count: number;
};

const PURPOSES = ['mgmt', 'data', 'storage', 'oob', 'other'];
const ROLES = ['mgmt', 'data', 'ipmi', 'vip', 'storage', 'other'];
const STATUSES = ['active', 'reserved', 'deprecated'];
const SOURCES = ['static', 'dhcp', 'reservation'];

// Inline styles for the supernet/subnet tree — Cloudscape Table doesn't
// expose <tr> refs for DnD-Kit, so we keep a native HTML table and color
// it with Cloudscape design tokens. Imported from @cloudscape-design/
// design-tokens so the values flip with applyMode(Dark) instead of
// falling back to the hard-coded light fallback baked into the var().
const TREE_TABLE_STYLE: React.CSSProperties = {
  width: '100%',
  borderCollapse: 'collapse',
  fontSize: 14,
};
const TREE_TH_STYLE: React.CSSProperties = {
  textAlign: 'left',
  padding: '8px 12px',
  fontSize: 12,
  fontWeight: 600,
  color: colorTextBodySecondary,
  borderBottom: `1px solid ${colorBorderDividerDefault}`,
  background: colorBackgroundCellShaded,
};
const TREE_TD_STYLE: React.CSSProperties = {
  padding: '8px 12px',
  borderBottom: `1px solid ${colorBorderDividerDefault}`,
  verticalAlign: 'top',
};
const SKELETON_STYLE: React.CSSProperties = {
  height: 16,
  width: '100%',
  borderRadius: 4,
  background: colorBackgroundInputDisabled,
};

export function IpamPage() {
  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const canRead = identity?.capabilities.includes('inventory:read');
  const canWrite = identity?.capabilities.includes('inventory:write');

  // Drill-down state. Supernets + Subnets share one view (the tree),
  // so we don't track a selected supernet — clicking a subnet inside the
  // tree drills directly to the addresses view.
  const [fabricId, setFabricId] = useState<string | null>(null);
  const [vrfId, setVrfId] = useState<string | null>(null);
  const [subnetId, setSubnetId] = useState<string | null>(null);

  if (!canRead) {
    return (
      <ContentLayout header={<Header variant="h1">IPAM</Header>}>
        <Box color="text-status-inactive">
          You don't have <code style={{ fontFamily: 'ui-monospace, monospace' }}>inventory:read</code>.
        </Box>
      </ContentLayout>
    );
  }

  // Hierarchy tab content keeps the existing drill-down chain: the
  // breadcrumb hands navigation between fabric / vrf / subnet, and each
  // level swaps in a different inner panel. The inner FabricsTab,
  // VrfsTab, SupernetTreeTab, and AddressesTab still render shadcn
  // primitives — they migrate in their own commits.
  const hierarchyContent = (
    <>
      <Breadcrumbs
        fabricId={fabricId} vrfId={vrfId} subnetId={subnetId}
        onJump={(level) => {
          if (level === 'fabrics') { setFabricId(null); setVrfId(null); setSubnetId(null); }
          if (level === 'vrfs') { setVrfId(null); setSubnetId(null); }
          if (level === 'networks') { setSubnetId(null); }
        }}
      />
      <div style={{ marginTop: 12 }}>
        {!fabricId && (
          <FabricsTab onSelect={setFabricId} canWrite={!!canWrite} />
        )}
        {fabricId && !vrfId && (
          <VrfsTab fabricId={fabricId} onSelect={setVrfId} canWrite={!!canWrite} />
        )}
        {fabricId && vrfId && !subnetId && (
          <SupernetTreeTab
            fabricId={fabricId} vrfId={vrfId}
            onSelectSubnet={setSubnetId} canWrite={!!canWrite}
          />
        )}
        {subnetId && (
          <AddressesTab subnetId={subnetId} canWrite={!!canWrite} />
        )}
      </div>
    </>
  );

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description="Fabric → VRF → Supernet → Subnet → IP — DHCP leases ingested from Kea"
        >
          IPAM
        </Header>
      }
    >
      <Tabs
        tabs={[
          { id: 'hierarchy', label: 'Hierarchy', content: hierarchyContent },
          {
            id: 'free-space',
            label: 'Free space',
            content: <FreeSpaceTab onSelectSubnet={(id) => setSubnetId(id)} />,
          },
          { id: 'overlays', label: 'Overlays / VNI', content: <OverlaysTab canWrite={!!canWrite} /> },
          { id: 'dns', label: 'DNS', content: <DnsTab canWrite={!!canWrite} /> },
          { id: 'dhcp', label: 'DHCP servers', content: <DhcpServersTab canWrite={!!canWrite} /> },
        ]}
      />
    </ContentLayout>
  );
}


// ----------------------- Free space finder -----------------------

type SubnetFreeRow = {
  subnet_id: string;
  prefix: string;
  name: string | null;
  fabric_id: string;
  vrf_id: string;
  purpose: string | null;
  capacity: number;
  allocated: number;
  free: number;
  next_available: string | null;
};

type SupernetCandidates = {
  supernet_id: string;
  supernet_prefix: string;
  supernet_name: string | null;
  fabric_id: string;
  vrf_id: string;
  purpose: string | null;
  candidates: string[];
  count: number;
};

type FreeMode = 'in-subnets' | 'prefixes';

const ALL_FABRICS_OPT: SelectProps.Option = { value: '__all__', label: 'All fabrics' };
const FAMILY_OPTS: SelectProps.Option[] = [
  { value: 'v4', label: 'IPv4' },
  { value: 'v6', label: 'IPv6' },
];

function FreeSpaceTab({ onSelectSubnet }: { onSelectSubnet: (id: string) => void }) {
  const [mode, setMode] = useState<FreeMode>('in-subnets');
  const [fabricOpt, setFabricOpt] = useState<SelectProps.Option>(ALL_FABRICS_OPT);
  const [familyOpt, setFamilyOpt] = useState<SelectProps.Option>(FAMILY_OPTS[0]);
  const [minFree, setMinFree] = useState<string>('1');
  const [prefixSize, setPrefixSize] = useState<string>('24');
  const fabricsRes = useList<Fabric>({ resource: 'ipam/fabrics', pagination: { pageSize: 200 } });
  const fabrics = fabricsRes.result.data ?? [];
  const fabricId = fabricOpt.value === ALL_FABRICS_OPT.value ? '' : fabricOpt.value!;
  const family = (familyOpt.value as 'v4' | 'v6');

  const subnetSearch = useQuery({
    queryKey: ['free-in-subnets', fabricId, family, minFree],
    queryFn: async () => {
      const params: Record<string, string | number> = {
        family, min_free: Number(minFree) || 1, limit: 100,
      };
      if (fabricId) params.fabric_id = fabricId;
      const r = await http.get<{ subnets: SubnetFreeRow[] }>(
        '/ipam/free-space/in-subnets', { params },
      );
      return r.data.subnets;
    },
    enabled: mode === 'in-subnets',
  });

  const prefixSearch = useQuery({
    queryKey: ['free-prefixes', fabricId, family, prefixSize],
    queryFn: async () => {
      const params: Record<string, string | number> = {
        family,
        prefix_size: Number(prefixSize) || (family === 'v4' ? 24 : 64),
        limit_per_supernet: 10,
      };
      if (fabricId) params.fabric_id = fabricId;
      const r = await http.get<{ supernets: SupernetCandidates[] }>(
        '/ipam/free-space/prefixes', { params },
      );
      return r.data.supernets;
    },
    enabled: mode === 'prefixes',
  });

  const fabricOptions: SelectProps.Option[] = [
    ALL_FABRICS_OPT,
    ...fabrics.map((f) => ({ value: f.id, label: f.name })),
  ];

  return (
    <SpaceBetween size="l">
      <Container header={<Header variant="h2">Search</Header>}>
        <SpaceBetween size="m">
          <FormField label="Mode">
            <SegmentedControl
              selectedId={mode}
              onChange={({ detail }) => setMode(detail.selectedId as FreeMode)}
              options={[
                { id: 'in-subnets', text: 'Free addresses in existing subnets' },
                { id: 'prefixes', text: 'Free prefixes inside supernets' },
              ]}
            />
          </FormField>
          <ColumnLayout columns={3}>
            <FormField label="Fabric">
              <Select
                selectedOption={fabricOpt}
                onChange={({ detail }) => setFabricOpt(detail.selectedOption)}
                options={fabricOptions}
                expandToViewport
              />
            </FormField>
            <FormField label="Family">
              <Select
                selectedOption={familyOpt}
                onChange={({ detail }) => setFamilyOpt(detail.selectedOption)}
                options={FAMILY_OPTS}
                expandToViewport
              />
            </FormField>
            {mode === 'in-subnets' ? (
              <FormField label="Min free addresses">
                <Input
                  type="number"
                  value={minFree}
                  onChange={({ detail }) => setMinFree(detail.value)}
                />
              </FormField>
            ) : (
              <FormField
                label="Prefix size"
                description="e.g. 24 for /24, 64 for /64"
              >
                <Input
                  type="number"
                  value={prefixSize}
                  onChange={({ detail }) => setPrefixSize(detail.value)}
                />
              </FormField>
            )}
          </ColumnLayout>
        </SpaceBetween>
      </Container>

      {mode === 'in-subnets' && (
        <SubnetFreeResults
          rows={subnetSearch.data ?? []}
          isLoading={subnetSearch.isLoading}
          onSelectSubnet={onSelectSubnet}
        />
      )}
      {mode === 'prefixes' && (
        <PrefixFreeResults
          groups={prefixSearch.data ?? []}
          isLoading={prefixSearch.isLoading}
        />
      )}
    </SpaceBetween>
  );
}

function SubnetFreeResults({
  rows, isLoading, onSelectSubnet,
}: {
  rows: SubnetFreeRow[];
  isLoading: boolean;
  onSelectSubnet: (id: string) => void;
}) {
  return (
    <Table<SubnetFreeRow>
      variant="container"
      loading={isLoading}
      loadingText="Searching subnets…"
      items={rows}
      trackBy="subnet_id"
      header={<Header counter={`(${rows.length})`}>Subnets with free space</Header>}
      onRowClick={({ detail }) => onSelectSubnet(detail.item.subnet_id)}
      columnDefinitions={[
        {
          id: 'subnet', header: 'Subnet',
          cell: (r) => (
            <span style={{ fontFamily: 'ui-monospace, monospace' }}>
              {r.prefix}
              {r.name && (
                <Box variant="span" color="text-status-inactive"> · {r.name}</Box>
              )}
            </span>
          ),
        },
        {
          id: 'purpose', header: 'Purpose',
          cell: (r) => r.purpose ? <Badge>{r.purpose}</Badge> : '—',
          width: 120,
        },
        {
          id: 'free', header: 'Free',
          cell: (r) => <span style={{ fontVariantNumeric: 'tabular-nums' }}>{r.free.toLocaleString()}</span>,
          width: 120,
        },
        {
          id: 'capacity', header: 'Capacity',
          cell: (r) => <span style={{ fontVariantNumeric: 'tabular-nums' }}>{r.capacity.toLocaleString()}</span>,
          width: 120,
        },
        {
          id: 'next', header: 'Next available',
          cell: (r) => <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{r.next_available ?? '—'}</span>,
        },
      ]}
      empty={
        <Box textAlign="center" color="inherit" padding="m">
          No subnets meet that filter.
        </Box>
      }
    />
  );
}

function PrefixFreeResults({
  groups, isLoading,
}: {
  groups: SupernetCandidates[];
  isLoading: boolean;
}) {
  if (isLoading) {
    return (
      <Container><Box padding="m">Searching supernets…</Box></Container>
    );
  }
  if (groups.length === 0) {
    return (
      <Container>
        <Box padding="m" color="text-status-inactive">
          No supernet has free space at that prefix size in this scope.
        </Box>
      </Container>
    );
  }
  // One Container per supernet, each with its prefix + a flat list of
  // candidate Badges. Container's header carries the supernet identity
  // and a counter of candidates.
  return (
    <SpaceBetween size="s">
      {groups.map((g) => (
        <Container
          key={g.supernet_id}
          header={
            <Header
              variant="h3"
              counter={`(${g.count})`}
              description={
                [g.supernet_name ?? 'unnamed', g.purpose].filter(Boolean).join(' · ')
              }
            >
              <span style={{ fontFamily: 'ui-monospace, monospace' }}>{g.supernet_prefix}</span>
            </Header>
          }
        >
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 6 }}>
            {g.candidates.map((c) => (
              <Badge key={c}>{c}</Badge>
            ))}
          </div>
        </Container>
      ))}
    </SpaceBetween>
  );
}

function Breadcrumbs({
  fabricId, vrfId, subnetId, onJump,
}: {
  fabricId: string | null;
  vrfId: string | null;
  subnetId: string | null;
  onJump: (level: 'fabrics' | 'vrfs' | 'networks') => void;
}) {
  // Cloudscape BreadcrumbGroup. Don't render anything at the root —
  // a lone "Fabrics" crumb is redundant with the page header / tab
  // label and looked like a stray text artifact above the table.
  if (!fabricId) return null;
  type Crumb = { text: string; href: string; level: 'fabrics' | 'vrfs' | 'networks' | null };
  const items: Crumb[] = [
    { text: 'Fabrics', href: '#fabrics', level: 'fabrics' },
    { text: 'VRFs', href: '#vrfs', level: 'vrfs' },
  ];
  if (vrfId) items.push({ text: 'Networks', href: '#networks', level: 'networks' });
  if (subnetId) items.push({ text: 'Addresses', href: '#addresses', level: null });
  return (
    <BreadcrumbGroup
      items={items}
      onFollow={(e) => {
        e.preventDefault();
        const i = items.find((c) => c.href === e.detail.href);
        if (i?.level) onJump(i.level);
      }}
    />
  );
}

// ----------------------- Fabrics -----------------------

const fabricSchema = z.object({
  name: z.string().min(1),
  slug: z.string().min(1).regex(/^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$/, 'lowercase alphanumeric + hyphens'),
  description: z.string().optional(),
  enclave: z.string().optional(),
  classification: z.string().optional(),
});

function FabricsTab({ onSelect, canWrite }: { onSelect: (id: string) => void; canWrite: boolean }) {
  const { tableQuery, result } = useTable<Fabric>({
    resource: 'ipam/fabrics', pagination: { pageSize: 200 },
    sorters: { initial: [{ field: 'name', order: 'asc' }] },
  });
  const data = result.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);

  return (
    <>
      <Table<Fabric>
        variant="container"
        loading={tableQuery.isLoading}
        loadingText="Loading fabrics…"
        items={data}
        trackBy="id"
        header={
          <Header
            counter={`(${data.length})`}
            actions={
              canWrite && (
                <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                  New fabric
                </Button>
              )
            }
          >
            Fabrics
          </Header>
        }
        columnDefinitions={[
          {
            id: 'name', header: 'Name',
            cell: (f) => (
              <Button variant="inline-link" onClick={() => onSelect(f.id)}>
                {f.name}
              </Button>
            ),
          },
          {
            id: 'slug', header: 'Slug',
            cell: (f) => <span style={{ fontFamily: 'ui-monospace, monospace' }}>{f.slug}</span>,
            width: 160,
          },
          {
            id: 'enclave', header: 'Enclave',
            cell: (f) => f.enclave ?? '—',
            width: 140,
          },
          {
            id: 'classification', header: 'Classification',
            cell: (f) => f.classification ?? '—',
            width: 160,
          },
          {
            id: 'description', header: 'Description',
            cell: (f) => (
              <Box variant="span" color="text-status-inactive" fontSize="body-s">
                {f.description ?? '—'}
              </Box>
            ),
          },
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="l">
            <SpaceBetween size="xs">
              <b>No fabrics yet</b>
              <Box variant="p" color="inherit">Create one to start carving supernets and subnets.</Box>
            </SpaceBetween>
          </Box>
        }
      />
      {canWrite && (
        <Modal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header="New fabric"
          size="medium"
        >
          <FabricForm onSaved={async () => { setCreateOpen(false); await tableQuery.refetch(); }} />
        </Modal>
      )}
    </>
  );
}

function FabricForm({ onSaved }: { onSaved: () => void }) {
  const [name, setName] = useState('');
  const [slug, setSlug] = useState('');
  const [enclave, setEnclave] = useState('');
  const [classification, setClassification] = useState('');
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Name required';
    if (!slug.trim() || !/^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$/.test(slug)) {
      errs.slug = 'lowercase alphanumeric + hyphens';
    }
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      await http.post('/ipam/fabrics', {
        name, slug,
        description: description || null,
        enclave: enclave || null,
        classification: classification || null,
      });
      toast.success('Fabric created (with default VRF)');
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); } finally { setSubmitting(false); }
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
            <Input value={name} onChange={({ detail }) => setName(detail.value)} placeholder="e.g. Production" />
          </FormField>
          <FormField label="Slug" errorText={errors.slug}>
            <Input value={slug} onChange={({ detail }) => setSlug(detail.value)} placeholder="prod" />
          </FormField>
          <ColumnLayout columns={2}>
            <FormField label="Enclave">
              <Input value={enclave} onChange={({ detail }) => setEnclave(detail.value)} />
            </FormField>
            <FormField label="Classification">
              <Input value={classification} onChange={({ detail }) => setClassification(detail.value)} />
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

// ----------------------- VRFs -----------------------

const vrfSchema = z.object({
  name: z.string().min(1),
  route_target: z.string().optional(),
  description: z.string().optional(),
});

function VrfsTab({
  fabricId, onSelect, canWrite,
}: {
  fabricId: string;
  onSelect: (id: string) => void;
  canWrite: boolean;
}) {
  const nav = useNavigate();
  const { tableQuery, result } = useTable<Vrf>({
    resource: 'ipam/vrfs',
    pagination: { pageSize: 200 },
    filters: { permanent: [{ field: 'fabric_id', operator: 'eq', value: fabricId }] },
    sorters: { initial: [{ field: 'name', order: 'asc' }] },
  });
  const data = result.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);

  return (
    <>
      <Table<Vrf>
        variant="container"
        loading={tableQuery.isLoading}
        loadingText="Loading VRFs…"
        items={data}
        trackBy="id"
        onRowClick={({ detail }) => onSelect(detail.item.id)}
        header={
          <Header
            counter={`(${data.length})`}
            actions={
              canWrite && (
                <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                  New VRF
                </Button>
              )
            }
          >
            VRFs
          </Header>
        }
        columnDefinitions={[
          {
            id: 'name', header: 'Name',
            cell: (v) => (
              <Button variant="inline-link" onClick={() => onSelect(v.id)}>
                {v.name}
              </Button>
            ),
          },
          {
            id: 'route_target', header: 'Route target',
            cell: (v) => (
              <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>
                {v.route_target ?? '—'}
              </span>
            ),
            width: 160,
          },
          {
            id: 'default', header: 'Default',
            cell: (v) => v.is_default ? <Badge>default</Badge> : '—',
            width: 100,
          },
          {
            id: 'description', header: 'Description',
            cell: (v) => (
              <Box variant="span" color="text-status-inactive" fontSize="body-s">
                {v.description ?? '—'}
              </Box>
            ),
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            // Stop the row click (which drills to supernets) from firing
            // when the operator clicks the Edit icon — the icon opens
            // the VRF detail page where metadata + BGP peers are edited.
            cell: (v: Vrf) => (
              <div onClick={(e) => e.stopPropagation()}>
                <Button
                  iconName="edit"
                  variant="inline-icon"
                  ariaLabel={`Edit ${v.name}`}
                  onClick={() => nav(`/ipam/vrfs/${v.id}`)}
                />
              </div>
            ),
            width: 60,
          }] : []),
        ]}
        empty={<Box textAlign="center" color="inherit" padding="m">No VRFs yet.</Box>}
      />
      {canWrite && (
        <Modal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header="New VRF"
          size="medium"
        >
          <VrfForm fabricId={fabricId} onSaved={async () => { setCreateOpen(false); await tableQuery.refetch(); }} />
        </Modal>
      )}
    </>
  );
}

function VrfForm({ fabricId, onSaved }: { fabricId: string; onSaved: () => void }) {
  const [name, setName] = useState('');
  const [routeTarget, setRouteTarget] = useState('');
  const [description, setDescription] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Name required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      await http.post('/ipam/vrfs', {
        fabric_id: fabricId, name,
        route_target: routeTarget || null,
        description: description || null,
        is_default: false,
      });
      toast.success('VRF created');
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); } finally { setSubmitting(false); }
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
            <Input value={name} onChange={({ detail }) => setName(detail.value)} placeholder="e.g. mgmt" />
          </FormField>
          <FormField
            label="Route target (optional)"
            description="Imported/exported by every BGP peer advertising this VRF (e.g. 65000:100)."
          >
            <Input
              value={routeTarget}
              onChange={({ detail }) => setRouteTarget(detail.value)}
              placeholder="e.g. 65000:100"
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

// ----------------------- Supernets -----------------------

const supernetSchema = z.object({
  prefix: z.string().min(1),
  name: z.string().optional(),
  site_id: z.string().optional(),
  purpose: z.string().optional(),
  description: z.string().optional(),
});

function SupernetTreeTab({
  fabricId, vrfId, onSelectSubnet, canWrite,
}: {
  fabricId: string;
  vrfId: string;
  onSelectSubnet: (subnetId: string) => void;
  canWrite: boolean;
}) {
  const qc = useQueryClient();
  // Top-level supernets only — nested children are loaded lazily by
  // SupernetNode when the user expands a row.
  const { tableQuery, result } = useTable<Supernet>({
    resource: 'ipam/supernets',
    pagination: { pageSize: 200 },
    filters: { permanent: [
      { field: 'fabric_id', operator: 'eq', value: fabricId },
      { field: 'vrf_id', operator: 'eq', value: vrfId },
      { field: 'top_level', operator: 'eq', value: true },
    ] },
    sorters: { initial: [{ field: 'prefix', order: 'asc' }] },
  });
  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const sites = sitesRes.result.data ?? [];
  const sitesById = useMemo(() => new Map(sites.map((s) => [s.id, s])), [sites]);
  const data = result.data ?? [];

  // Track expansion + dialog state per-supernet. Separate sets so the
  // "+ subnet" / edit dialogs don't get tied to the chevron toggle.
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const [createSupernetOpen, setCreateSupernetOpen] = useState(false);
  const [createSupernetUnder, setCreateSupernetUnder] = useState<Supernet | null>(null);
  const [createSubnetFor, setCreateSubnetFor] = useState<Supernet | null>(null);
  const [editSupernet, setEditSupernet] = useState<Supernet | null>(null);
  const [editSubnet, setEditSubnet] = useState<{ subnet: Subnet; parentPurpose: string | null } | null>(null);

  function toggle(id: string) {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  async function refreshSubnets(supernetId: string) {
    await qc.invalidateQueries({ queryKey: ['subnets-for-supernet', supernetId] });
    await qc.invalidateQueries({ queryKey: ['supernet-util', supernetId] });
  }

  async function refreshChildren(parentId: string | null) {
    await qc.invalidateQueries({ queryKey: ['child-supernets', parentId] });
  }

  async function refreshSupernets() {
    await tableQuery.refetch();
    await qc.invalidateQueries({ queryKey: ['supernet-util'] });
    await qc.invalidateQueries({ queryKey: ['child-supernets'] });
  }

  // --- Drag and drop: move a subnet between supernets ---
  // Mouse activation distance avoids hijacking ordinary row clicks; touch
  // delay does the same for tap-to-drill on touch screens.
  const sensors = useSensors(
    useSensor(MouseSensor, { activationConstraint: { distance: 6 } }),
    useSensor(TouchSensor, { activationConstraint: { delay: 250, tolerance: 6 } }),
    useSensor(KeyboardSensor),
  );
  // Dragged subnet for the overlay and the green/red ring on candidates.
  const [draggingSubnet, setDraggingSubnet] = useState<Subnet | null>(null);
  // Most-recent valid drop target — drives the optimistic highlight in
  // SupernetNode without forcing every node to re-subscribe to onDragOver.
  const [hoverTargetId, setHoverTargetId] = useState<string | null>(null);
  // Auto-expand-on-hover for collapsed supernets while dragging.
  const hoverExpandRef = useRef<{ id: string; timer: ReturnType<typeof setTimeout> } | null>(null);

  function clearHoverExpand() {
    if (hoverExpandRef.current) {
      clearTimeout(hoverExpandRef.current.timer);
      hoverExpandRef.current = null;
    }
  }

  function onDragStart(e: DragStartEvent) {
    const id = String(e.active.id);
    const subnet = (e.active.data.current as { subnet?: Subnet })?.subnet;
    if (id.startsWith('subnet:') && subnet) setDraggingSubnet(subnet);
  }

  function onDragOver(e: DragOverEvent) {
    const overId = e.over?.id ? String(e.over.id) : null;
    if (!overId || !overId.startsWith('supernet:')) {
      setHoverTargetId(null);
      clearHoverExpand();
      return;
    }
    const target = overId.slice('supernet:'.length);
    setHoverTargetId(target);

    // After 600ms over a collapsed supernet, expand it so the user can
    // drop into nested children. 600ms is long enough to ignore quick
    // pass-throughs but short enough to feel responsive.
    if (!expanded.has(target)) {
      if (hoverExpandRef.current?.id !== target) {
        clearHoverExpand();
        hoverExpandRef.current = {
          id: target,
          timer: setTimeout(() => {
            setExpanded((prev) => new Set(prev).add(target));
            hoverExpandRef.current = null;
          }, 600),
        };
      }
    } else {
      clearHoverExpand();
    }
  }

  async function onDragEnd(e: DragEndEvent) {
    clearHoverExpand();
    const subnet = draggingSubnet;
    setDraggingSubnet(null);
    setHoverTargetId(null);
    if (!subnet) return;
    const overId = e.over?.id ? String(e.over.id) : null;
    if (!overId || !overId.startsWith('supernet:')) return;
    const targetSupernetId = overId.slice('supernet:'.length);
    if (targetSupernetId === subnet.supernet_id) return;

    try {
      await http.patch(`/ipam/subnets/${subnet.id}`, { supernet_id: targetSupernetId });
      toast.success(`Moved ${subnet.prefix}`);
      await refreshSubnets(subnet.supernet_id);
      await refreshSubnets(targetSupernetId);
      await qc.invalidateQueries({ queryKey: ['child-supernets'] });
    } catch (err: any) {
      toast.error(err?.message ?? 'move failed');
    }
  }

  useEffect(() => clearHoverExpand, []);

  return (
    <SpaceBetween size="s">
      <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
        {canWrite && (
          <Button variant="primary" iconName="add-plus" onClick={() => setCreateSupernetOpen(true)}>
            New supernet
          </Button>
        )}
      </div>

      <DndContext
        sensors={sensors}
        onDragStart={onDragStart}
        onDragOver={onDragOver}
        onDragEnd={onDragEnd}
        onDragCancel={() => { setDraggingSubnet(null); setHoverTargetId(null); clearHoverExpand(); }}
      >
        <Container disableContentPaddings>
          <table style={TREE_TABLE_STYLE}>
            <thead>
              <tr>
                <th style={{ ...TREE_TH_STYLE, width: 32 }} />
                <th style={TREE_TH_STYLE}>Prefix</th>
                <th style={TREE_TH_STYLE}>Name</th>
                <th style={TREE_TH_STYLE}>Purpose</th>
                <th style={{ ...TREE_TH_STYLE, width: 260 }}>Utilization</th>
                {canWrite && <th style={{ ...TREE_TH_STYLE, width: 48 }} />}
              </tr>
            </thead>
            <tbody>
              {data.length === 0 && (
                <tr>
                  <td style={{ ...TREE_TD_STYLE, color: colorTextStatusInactive }} colSpan={canWrite ? 6 : 5}>
                    No supernets yet.
                  </td>
                </tr>
              )}
              {data.map((sn) => (
                <SupernetNode
                  key={sn.id}
                  supernet={sn}
                  depth={0}
                  expanded={expanded}
                  onToggle={toggle}
                  sitesById={sitesById}
                  canWrite={canWrite}
                  draggingSubnet={draggingSubnet}
                  hoverTargetId={hoverTargetId}
                  onSelectSubnet={onSelectSubnet}
                  onAddSubnet={(s) => setCreateSubnetFor(s)}
                  onAddChildSupernet={(s) => setCreateSupernetUnder(s)}
                  onEditSupernet={(s) => setEditSupernet(s)}
                  onEditSubnet={(s, parentPurpose) => setEditSubnet({ subnet: s, parentPurpose })}
                />
              ))}
            </tbody>
          </table>
          {tableQuery.isLoading && (
            <div style={{ padding: 16, display: 'flex', flexDirection: 'column', gap: 8 }}>
              {Array.from({ length: 2 }).map((_, i) => <div key={`s-${i}`} style={SKELETON_STYLE} />)}
            </div>
          )}
        </Container>
        <DragOverlay dropAnimation={{ duration: 150 }}>
          {draggingSubnet ? (
            <div style={{
              padding: '6px 12px',
              fontSize: 14,
              borderRadius: 8,
              background: colorBackgroundContainerContent,
              border: `1px solid ${colorBorderDividerDefault}`,
              boxShadow: '0 2px 8px rgba(0,0,0,0.12)',
            }}>
              <span style={{ fontFamily: 'ui-monospace, monospace', fontWeight: 500 }}>
                {draggingSubnet.prefix}
              </span>
              {draggingSubnet.name && (
                <span style={{ marginLeft: 8, color: colorTextStatusInactive }}>
                  {draggingSubnet.name}
                </span>
              )}
              <span style={{ marginLeft: 8, fontSize: 12, color: colorTextStatusInactive }}>
                drop on a supernet to move
              </span>
            </div>
          ) : null}
        </DragOverlay>
      </DndContext>

      {canWrite && (
        <Modal
          visible={createSupernetOpen}
          onDismiss={() => setCreateSupernetOpen(false)}
          header="New supernet"
          size="medium"
        >
          <SupernetForm
            fabricId={fabricId} vrfId={vrfId}
            sites={sites}
            onSaved={async () => {
              setCreateSupernetOpen(false);
              await tableQuery.refetch();
              await qc.invalidateQueries({ queryKey: ['supernet-util'] });
            }}
          />
        </Modal>
      )}

      <Modal
        visible={createSubnetFor !== null}
        onDismiss={() => setCreateSubnetFor(null)}
        header="New subnet"
        size="medium"
      >
        {createSubnetFor && (
          <SubnetForm
            supernetId={createSubnetFor.id}
            fabricId={fabricId}
            sites={sites}
            parentPurpose={createSubnetFor.purpose}
            onSaved={async () => {
              const sn = createSubnetFor;
              setCreateSubnetFor(null);
              if (sn) await refreshSubnets(sn.id);
            }}
          />
        )}
      </Modal>

      <Modal
        visible={editSupernet !== null}
        onDismiss={() => setEditSupernet(null)}
        header="Edit supernet"
        size="medium"
      >
        {editSupernet && (
          <SupernetForm
            fabricId={fabricId} vrfId={vrfId} supernet={editSupernet}
            sites={sites}
            onSaved={async () => { setEditSupernet(null); await refreshSupernets(); }}
          />
        )}
      </Modal>

      <Modal
        visible={createSupernetUnder !== null}
        onDismiss={() => setCreateSupernetUnder(null)}
        header={`New supernet inside ${createSupernetUnder?.prefix ?? ''}`}
        size="medium"
      >
        {createSupernetUnder && (
          <SupernetForm
            fabricId={fabricId} vrfId={vrfId}
            parent={createSupernetUnder}
            sites={sites}
            onSaved={async () => {
              const parent = createSupernetUnder;
              setCreateSupernetUnder(null);
              await refreshChildren(parent?.id ?? null);
              await tableQuery.refetch();
            }}
          />
        )}
      </Modal>

      <Modal
        visible={editSubnet !== null}
        onDismiss={() => setEditSubnet(null)}
        header="Edit subnet"
        size="medium"
      >
        {editSubnet && (
          <SubnetForm
            supernetId={editSubnet.subnet.supernet_id}
            fabricId={fabricId}
            sites={sites}
            subnet={editSubnet.subnet}
            parentPurpose={editSubnet.parentPurpose}
            onSaved={async () => {
              const sid = editSubnet.subnet.supernet_id;
              setEditSubnet(null);
              await refreshSubnets(sid);
            }}
          />
        )}
      </Modal>
    </SpaceBetween>
  );
}


function SupernetNode({
  supernet, depth, expanded, onToggle, sitesById, canWrite,
  draggingSubnet, hoverTargetId,
  onSelectSubnet, onAddSubnet, onAddChildSupernet, onEditSupernet, onEditSubnet,
}: Readonly<{
  supernet: Supernet;
  depth: number;
  expanded: Set<string>;
  onToggle: (id: string) => void;
  sitesById: Map<string, Site>;
  canWrite: boolean;
  draggingSubnet: Subnet | null;
  hoverTargetId: string | null;
  onSelectSubnet: (subnetId: string) => void;
  onAddSubnet: (sn: Supernet) => void;
  onAddChildSupernet: (sn: Supernet) => void;
  onEditSupernet: (sn: Supernet) => void;
  onEditSubnet: (subnet: Subnet, parentPurpose: string | null) => void;
}>) {
  const isOpen = expanded.has(supernet.id);
  const childrenQ = useQuery({
    enabled: isOpen,
    queryKey: ['child-supernets', supernet.id],
    queryFn: async () => (
      await http.get<{ items: Supernet[] }>(
        `/ipam/supernets?parent_supernet_id=${supernet.id}&page_size=200`,
      )
    ).data.items ?? [],
  });
  const children = childrenQ.data ?? [];
  const branchSpan = canWrite ? 5 : 4;
  const indent = depth * 16;
  const sitePill = supernet.site_id
    ? `site: ${sitesById.get(supernet.site_id)?.code ?? supernet.site_id.slice(0, 8) + '…'}`
    : null;

  const { setNodeRef: setDropRef } = useDroppable({ id: `supernet:${supernet.id}` });

  const isDragging = draggingSubnet !== null;
  const isOver = hoverTargetId === supernet.id;
  const isSelf = isDragging && draggingSubnet?.supernet_id === supernet.id;
  const fits = isDragging && draggingSubnet
    ? cidrContains(supernet.prefix, draggingSubnet.prefix)
    : false;
  const isValidTarget = isDragging && !isSelf && fits;

  let rowStyle: React.CSSProperties = { cursor: 'pointer' };
  if (isOver && isValidTarget) {
    rowStyle = { ...rowStyle, boxShadow: `inset 0 0 0 2px ${colorTextStatusSuccess}`, background: 'rgba(3, 127, 12, 0.05)' };
  } else if (isOver && isDragging) {
    rowStyle = { ...rowStyle, boxShadow: `inset 0 0 0 2px ${colorTextStatusError}`, background: 'rgba(217, 21, 21, 0.05)' };
  } else if (isValidTarget) {
    rowStyle = { ...rowStyle, boxShadow: 'inset 0 0 0 1px rgba(3, 127, 12, 0.4)' };
  }

  return (
    <Fragment>
      <tr
        ref={setDropRef}
        style={rowStyle}
        onClick={() => onToggle(supernet.id)}
      >
        <td style={{ ...TREE_TD_STYLE, color: colorTextStatusInactive }}>
          <Box>{isOpen ? '▾' : '▸'}</Box>
        </td>
        <td style={{ ...TREE_TD_STYLE, paddingLeft: 16 + indent, fontFamily: 'ui-monospace, monospace', fontWeight: 500 }}>
          {depth > 0 && <span style={{ color: colorTextStatusInactive }}>└─ </span>}
          {supernet.prefix}
        </td>
        <td style={TREE_TD_STYLE}>
          <div>{supernet.name ?? '—'}</div>
          {sitePill && (
            <div style={{ fontSize: 12, color: colorTextStatusInactive }}>{sitePill}</div>
          )}
        </td>
        <td style={TREE_TD_STYLE}>
          {supernet.purpose ? <Badge>{supernet.purpose}</Badge> : '—'}
        </td>
        <td style={TREE_TD_STYLE}><SupernetUtilCell supernetId={supernet.id} /></td>
        {canWrite && (
          <td style={TREE_TD_STYLE} onClick={(e) => e.stopPropagation()}>
            <Button
              iconName="edit"
              variant="inline-icon"
              onClick={() => onEditSupernet(supernet)}
              ariaLabel="Edit supernet"
            />
          </td>
        )}
      </tr>
      {isOpen && (
        <>
          {childrenQ.isLoading && (
            <tr>
              <td />
              <td colSpan={branchSpan} style={{ ...TREE_TD_STYLE, paddingLeft: 24 + indent }}>
                <div style={SKELETON_STYLE} />
              </td>
            </tr>
          )}
          {children.map((child) => (
            <SupernetNode
              key={child.id}
              supernet={child}
              depth={depth + 1}
              expanded={expanded}
              onToggle={onToggle}
              sitesById={sitesById}
              canWrite={canWrite}
              draggingSubnet={draggingSubnet}
              hoverTargetId={hoverTargetId}
              onSelectSubnet={onSelectSubnet}
              onAddSubnet={onAddSubnet}
              onAddChildSupernet={onAddChildSupernet}
              onEditSupernet={onEditSupernet}
              onEditSubnet={onEditSubnet}
            />
          ))}
          <SubnetBranch
            supernetId={supernet.id}
            depth={depth + 1}
            parentPurpose={supernet.purpose}
            sitesById={sitesById}
            canWrite={canWrite}
            draggingSubnetId={draggingSubnet?.id ?? null}
            onSelectSubnet={onSelectSubnet}
            onAddSubnet={() => onAddSubnet(supernet)}
            onAddChildSupernet={() => onAddChildSupernet(supernet)}
            onEditSubnet={(s) => onEditSubnet(s, supernet.purpose)}
          />
        </>
      )}
    </Fragment>
  );
}


function SubnetBranch({
  supernetId, depth, parentPurpose, sitesById, canWrite,
  draggingSubnetId,
  onSelectSubnet, onAddSubnet, onAddChildSupernet, onEditSubnet,
}: Readonly<{
  supernetId: string;
  depth: number;
  parentPurpose: string | null;
  sitesById: Map<string, Site>;
  canWrite: boolean;
  draggingSubnetId: string | null;
  onSelectSubnet: (subnetId: string) => void;
  onAddSubnet: () => void;
  onAddChildSupernet: () => void;
  onEditSubnet: (subnet: Subnet) => void;
}>) {
  const { data, isLoading } = useQuery({
    queryKey: ['subnets-for-supernet', supernetId],
    queryFn: async () => (
      await http.get<{ items: Subnet[] }>(`/ipam/subnets?supernet_id=${supernetId}&page_size=200`)
    ).data.items ?? [],
  });

  const branchSpan = canWrite ? 5 : 4;
  const indent = depth * 16;

  if (isLoading) {
    return (
      <tr>
        <td />
        <td colSpan={branchSpan} style={{ ...TREE_TD_STYLE, paddingLeft: 16 + indent }}>
          <div style={SKELETON_STYLE} />
        </td>
      </tr>
    );
  }

  const subnets = data ?? [];
  return (
    <>
      {subnets.map((s) => (
        <SubnetRow
          key={s.id}
          subnet={s}
          indent={indent}
          canWrite={canWrite}
          isDraggingThis={draggingSubnetId === s.id}
          sitesById={sitesById}
          onSelectSubnet={onSelectSubnet}
          onEditSubnet={onEditSubnet}
        />
      ))}
      {canWrite && (
        <tr style={{ background: colorBackgroundCellShaded }}>
          <td />
          <td colSpan={branchSpan} style={{ ...TREE_TD_STYLE, paddingLeft: 16 + indent }}>
            <Button iconName="add-plus" variant="link" onClick={onAddSubnet}>
              Add subnet here{parentPurpose ? ` (${parentPurpose})` : ''}
            </Button>
            <Button iconName="add-plus" variant="link" onClick={onAddChildSupernet}>
              Add child supernet
            </Button>
          </td>
        </tr>
      )}
    </>
  );
}

function SubnetRow({
  subnet: s, indent, canWrite, isDraggingThis, sitesById,
  onSelectSubnet, onEditSubnet,
}: Readonly<{
  subnet: Subnet;
  indent: number;
  canWrite: boolean;
  isDraggingThis: boolean;
  sitesById: Map<string, Site>;
  onSelectSubnet: (subnetId: string) => void;
  onEditSubnet: (subnet: Subnet) => void;
}>) {
  const { attributes, listeners, setNodeRef, isDragging } = useDraggable({
    id: `subnet:${s.id}`,
    data: { subnet: s },
  });
  const opacity = isDragging || isDraggingThis ? 0.3 : 1;
  return (
    <tr
      ref={setNodeRef}
      {...attributes}
      {...listeners}
      style={{
        cursor: 'pointer',
        background: colorBackgroundCellShaded,
        opacity,
      }}
      onClick={() => onSelectSubnet(s.id)}
    >
      <td style={TREE_TD_STYLE} />
      <td style={{ ...TREE_TD_STYLE, paddingLeft: 16 + indent, fontFamily: 'ui-monospace, monospace' }}>
        <span style={{ color: colorTextStatusInactive }}>└─</span> {s.prefix}
      </td>
      <td style={TREE_TD_STYLE}>
        <div>{s.name ?? '—'}</div>
        <div style={{ fontSize: 12, color: colorTextStatusInactive }}>
          {s.site_id
            ? `site: ${sitesById.get(s.site_id)?.code ?? s.site_id.slice(0, 8) + '…'}`
            : 'unassigned'}
          {s.vlan_id ? ` · vlan ${s.vlan_id}` : ''}
          {s.gateway ? ` · gw ${s.gateway}` : ''}
          {s.vni_id ? ` · vni ${s.vni_id.slice(0, 8)}…` : ''}
        </div>
      </td>
      <td style={TREE_TD_STYLE}>
        {s.purpose ? <Badge>{s.purpose}</Badge> : '—'}
      </td>
      <td style={TREE_TD_STYLE}><SubnetUtilCell subnetId={s.id} /></td>
      {canWrite && (
        <td style={TREE_TD_STYLE} onClick={(e) => e.stopPropagation()}>
          <Button
            iconName="edit"
            variant="inline-icon"
            onClick={() => onEditSubnet(s)}
            ariaLabel="Edit subnet"
          />
        </td>
      )}
    </tr>
  );
}

function SupernetUtilCell({ supernetId }: { supernetId: string }) {
  const { data } = useQuery({
    queryKey: ['supernet-util', supernetId],
    queryFn: async () => (await http.get<SupernetUtil>(`/ipam/supernets/${supernetId}/utilization`)).data,
  });
  if (!data) return <Box color="text-status-inactive" fontSize="body-s">…</Box>;
  return (
    <CapacityBar
      used={data.allocated_subnet_addresses}
      total={data.capacity}
      compact
      leftLabel={`${data.subnet_count} subnet${data.subnet_count === 1 ? '' : 's'}`}
    />
  );
}

const PURPOSE_NONE = '__none__';


function SupernetForm({
  fabricId, vrfId, supernet, parent, sites, onSaved,
}: {
  fabricId: string;
  vrfId: string;
  supernet?: Supernet;
  /** When set, the new supernet is created underneath this one. The form
   * locks the purpose to the parent's purpose (same inheritance rule we
   * apply to subnets, applied one level up). */
  parent?: Supernet | null;
  sites: Site[];
  onSaved: () => void;
}) {
  const NONE = '__none__';
  const editing = !!supernet;
  // Purpose: editing → existing; creating under a parent → parent's; new → unset.
  const initialPurpose =
    supernet?.purpose
    ?? parent?.purpose
    ?? PURPOSE_NONE;
  const form = useForm<z.infer<typeof supernetSchema>>({
    resolver: zodResolver(supernetSchema),
    defaultValues: {
      prefix: supernet?.prefix ?? '',
      name: supernet?.name ?? '',
      site_id: supernet?.site_id ?? NONE,
      purpose: initialPurpose,
      description: supernet?.description ?? '',
    },
  });
  async function onSubmit(v: z.infer<typeof supernetSchema>) {
    const purpose = v.purpose && v.purpose !== PURPOSE_NONE ? v.purpose : null;
    const siteId = v.site_id && v.site_id !== NONE ? v.site_id : null;
    try {
      if (editing && supernet) {
        // PATCH only the fields the operator can edit. Prefix is intentionally
        // immutable — changing CIDR after subnets exist is a containment-
        // invariant landmine, easier to delete + recreate.
        await http.patch(`/ipam/supernets/${supernet.id}`, {
          name: v.name || null,
          site_id: siteId,
          purpose,
          description: v.description || null,
        });
        toast.success('Supernet updated');
      } else {
        await http.post('/ipam/supernets', {
          fabric_id: fabricId,
          vrf_id: vrfId,
          parent_supernet_id: parent?.id ?? null,
          site_id: siteId,
          prefix: v.prefix,
          name: v.name || null,
          purpose,
          description: v.description || null,
        });
        toast.success('Supernet created');
      }
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }
  const purposeLocked = !!parent?.purpose && !editing;
  const purposeOptions: SelectProps.Option[] = [
    { value: PURPOSE_NONE, label: '(unset)' },
    ...PURPOSES.map((p) => ({ value: p, label: p })),
  ];
  const siteOptions: SelectProps.Option[] = [
    { value: NONE, label: '(unassigned)' },
    ...sites.map((s) => ({ value: s.id, label: `${s.code} · ${s.name}` })),
  ];

  // form.watch() values
  const prefixV = form.watch('prefix') ?? '';
  const nameV = form.watch('name') ?? '';
  const siteV = form.watch('site_id') ?? NONE;
  const purposeV = form.watch('purpose') ?? PURPOSE_NONE;
  const descV = form.watch('description') ?? '';

  return (
    <form onSubmit={form.handleSubmit(onSubmit)}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={form.formState.isSubmitting}>
            {form.formState.isSubmitting ? 'Saving…' : editing ? 'Save' : 'Create'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          {parent && !editing && (
            <Box color="text-status-inactive" fontSize="body-s">
              Carving inside <span style={{ fontFamily: 'ui-monospace, monospace' }}>{parent.prefix}</span>.
              Prefix must fit inside the parent.
            </Box>
          )}
          <FormField
            label="Prefix (CIDR)"
            description={editing ? 'Prefix is immutable after creation. Delete + recreate to change it.' : undefined}
            errorText={form.formState.errors.prefix?.message as string | undefined}
          >
            <Input
              disabled={editing}
              value={prefixV}
              onChange={({ detail }) => form.setValue('prefix', detail.value)}
              placeholder="e.g. 10.0.0.0/8 or 2001:db8::/32"
            />
          </FormField>
          <ColumnLayout columns={2}>
            <FormField label="Name">
              <Input value={nameV} onChange={({ detail }) => form.setValue('name', detail.value)} />
            </FormField>
            <FormField label="Site (optional)">
              <Select
                selectedOption={siteOptions.find((o) => o.value === siteV) ?? siteOptions[0]}
                onChange={({ detail }) => form.setValue('site_id', detail.selectedOption.value!)}
                options={siteOptions}
                expandToViewport
              />
            </FormField>
          </ColumnLayout>
          <ColumnLayout columns={2}>
            <FormField
              label="Purpose"
              description={
                purposeLocked
                  ? `Locked to ${parent?.purpose} — parent's purpose.`
                  : editing
                    ? 'Setting a purpose locks every subnet under this supernet to the same purpose.'
                    : undefined
              }
            >
              <Select
                selectedOption={purposeOptions.find((o) => o.value === purposeV) ?? purposeOptions[0]}
                onChange={({ detail }) => form.setValue('purpose', detail.selectedOption.value!)}
                options={purposeOptions}
                disabled={purposeLocked}
                expandToViewport
              />
            </FormField>
            <FormField label="Description">
              <Input value={descV} onChange={({ detail }) => form.setValue('description', detail.value)} />
            </FormField>
          </ColumnLayout>
        </SpaceBetween>
      </Form>
    </form>
  );
}

// ----------------------- Subnets -----------------------

const subnetSchema = z.object({
  prefix: z.string().min(1),
  site_id: z.string(),
  vni_id: z.string().optional(),
  name: z.string().optional(),
  purpose: z.string().optional(),
  vlan_id: z.string().optional(),
  gateway: z.string().optional(),
});

function SubnetUtilCell({ subnetId }: { subnetId: string }) {
  const { data } = useQuery({
    queryKey: ['subnet-util', subnetId],
    queryFn: async () => (await http.get<SubnetUtil>(`/ipam/subnets/${subnetId}/utilization`)).data,
  });
  if (!data) return <Box color="text-status-inactive" fontSize="body-s">…</Box>;
  return (
    <CapacityBar
      used={data.allocated} total={data.capacity}
      compact leftLabel={`${data.allocated}/${data.capacity}`}
    />
  );
}

function SubnetForm({
  supernetId, fabricId, sites, subnet, parentPurpose, onSaved,
}: {
  supernetId: string;
  fabricId: string;
  sites: Site[];
  /** Existing subnet to edit. When omitted the form creates a new one. */
  subnet?: Subnet;
  /** Parent supernet's purpose, if any. When set, the subnet must adopt it
   * — the field is locked in the UI and the backend re-checks. */
  parentPurpose: string | null;
  onSaved: () => void;
}) {
  const NONE = '__none__';
  const editing = !!subnet;
  // L2 VNIs in this fabric — only L2 can be bound to a subnet (the
  // backend enforces this; we filter here so the dropdown stays honest).
  const vnisQ = useQuery({
    queryKey: ['vnis-for-fabric', fabricId, 'l2'],
    queryFn: async () => (
      await http.get<{ items: Vni[] }>(
        `/ipam/vnis?fabric_id=${fabricId}&kind=l2&page_size=200`,
      )
    ).data.items ?? [],
  });
  const l2Vnis = vnisQ.data ?? [];
  // If the parent supernet has a purpose, the new subnet must adopt it.
  // Pre-fill + lock the field so the operator can't even attempt a value
  // the backend will reject.
  const initialPurpose =
    subnet?.purpose
    ?? parentPurpose
    ?? PURPOSE_NONE;
  const form = useForm<z.infer<typeof subnetSchema>>({
    resolver: zodResolver(subnetSchema),
    defaultValues: {
      prefix: subnet?.prefix ?? '',
      site_id: subnet?.site_id ?? NONE,
      vni_id: subnet?.vni_id ?? NONE,
      name: subnet?.name ?? '',
      purpose: initialPurpose,
      vlan_id: subnet?.vlan_id != null ? String(subnet.vlan_id) : '',
      gateway: subnet?.gateway ?? '',
    },
  });
  async function onSubmit(v: z.infer<typeof subnetSchema>) {
    const purpose = v.purpose && v.purpose !== PURPOSE_NONE ? v.purpose : null;
    const vniId = v.vni_id && v.vni_id !== NONE ? v.vni_id : null;
    try {
      if (editing && subnet) {
        await http.patch(`/ipam/subnets/${subnet.id}`, {
          site_id: v.site_id === NONE ? null : v.site_id,
          vni_id: vniId,
          name: v.name || null,
          purpose,
          vlan_id: v.vlan_id ? Number(v.vlan_id) : null,
          gateway: v.gateway || null,
        });
        toast.success('Subnet updated');
      } else {
        await http.post('/ipam/subnets', {
          supernet_id: supernetId,
          site_id: v.site_id === NONE ? null : v.site_id,
          vni_id: vniId,
          prefix: v.prefix,
          name: v.name || null,
          purpose,
          vlan_id: v.vlan_id ? Number(v.vlan_id) : null,
          gateway: v.gateway || null,
        });
        toast.success('Subnet created');
      }
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }
  const purposeLocked = !!parentPurpose;
  const siteOptions: SelectProps.Option[] = [
    { value: NONE, label: '(unassigned)' },
    ...sites.map((s) => ({ value: s.id, label: `${s.code} · ${s.name}` })),
  ];
  const purposeOptions: SelectProps.Option[] = [
    { value: PURPOSE_NONE, label: '(unset)' },
    ...PURPOSES.map((p) => ({ value: p, label: p })),
  ];
  const vniOptions: SelectProps.Option[] = [
    { value: NONE, label: '(none)' },
    ...l2Vnis.map((v) => ({
      value: v.id,
      label: `${v.vni}${v.name ? ` · ${v.name}` : ''}${v.vlan_id ? ` · vlan ${v.vlan_id}` : ''}`,
    })),
  ];
  const prefixV = form.watch('prefix') ?? '';
  const siteV = form.watch('site_id') ?? NONE;
  const purposeV = form.watch('purpose') ?? PURPOSE_NONE;
  const vlanV = form.watch('vlan_id') ?? '';
  const gatewayV = form.watch('gateway') ?? '';
  const vniV = form.watch('vni_id') ?? NONE;
  const nameV = form.watch('name') ?? '';

  return (
    <form onSubmit={form.handleSubmit(onSubmit)}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={form.formState.isSubmitting}>
            {form.formState.isSubmitting ? 'Saving…' : editing ? 'Save' : 'Create'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField
            label="Prefix (CIDR, must be inside the supernet)"
            description={editing ? 'Prefix is immutable after creation. Delete + recreate to change it.' : undefined}
            errorText={form.formState.errors.prefix?.message as string | undefined}
          >
            <Input
              disabled={editing}
              value={prefixV}
              onChange={({ detail }) => form.setValue('prefix', detail.value)}
              placeholder="e.g. 10.0.5.0/24 or 2001:db8:1::/48"
            />
          </FormField>
          <ColumnLayout columns={2}>
            <FormField label="Site">
              <Select
                selectedOption={siteOptions.find((o) => o.value === siteV) ?? siteOptions[0]}
                onChange={({ detail }) => form.setValue('site_id', detail.selectedOption.value!)}
                options={siteOptions}
                expandToViewport
              />
            </FormField>
            <FormField
              label="Purpose"
              description={purposeLocked ? `Locked to ${parentPurpose} — parent supernet's purpose.` : undefined}
            >
              <Select
                selectedOption={purposeOptions.find((o) => o.value === purposeV) ?? purposeOptions[0]}
                onChange={({ detail }) => form.setValue('purpose', detail.selectedOption.value!)}
                options={purposeOptions}
                disabled={purposeLocked}
                expandToViewport
              />
            </FormField>
          </ColumnLayout>
          <ColumnLayout columns={2}>
            <FormField label="VLAN">
              <Input type="number" value={vlanV} onChange={({ detail }) => form.setValue('vlan_id', detail.value)} />
            </FormField>
            <FormField label="Gateway (optional)">
              <Input value={gatewayV} onChange={({ detail }) => form.setValue('gateway', detail.value)} />
            </FormField>
          </ColumnLayout>
          <FormField
            label="L2 VNI (optional)"
            description="Bind this subnet to an L2 VNI to track which broadcast domain it rides. Only L2 VNIs in this fabric are eligible."
          >
            <Select
              selectedOption={vniOptions.find((o) => o.value === vniV) ?? vniOptions[0]}
              onChange={({ detail }) => form.setValue('vni_id', detail.selectedOption.value!)}
              options={vniOptions}
              expandToViewport
            />
          </FormField>
          <FormField label="Name (optional)">
            <Input value={nameV} onChange={({ detail }) => form.setValue('name', detail.value)} />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}

// ----------------------- IP Addresses -----------------------

const ipSchema = z.object({
  address: z.string().min(1),
  asset_id: z.string(),
  role: z.enum(['mgmt', 'data', 'ipmi', 'vip', 'storage', 'other']),
  status: z.enum(['active', 'reserved', 'deprecated']),
  source: z.enum(['static', 'dhcp', 'reservation']),
  dns_name: z.string().optional(),
  description: z.string().optional(),
});

function AddressesTab({ subnetId, canWrite }: Readonly<{ subnetId: string; canWrite: boolean }>) {
  const qc = useQueryClient();
  const { tableQuery, result } = useTable<IPAddr>({
    resource: 'ipam/addresses',
    pagination: { pageSize: 100 },
    filters: { permanent: [{ field: 'subnet_id', operator: 'eq', value: subnetId }] },
    sorters: { initial: [{ field: 'address', order: 'asc' }] },
  });
  const util = useQuery({
    queryKey: ['subnet-util', subnetId],
    queryFn: async () => (await http.get<SubnetUtil>(`/ipam/subnets/${subnetId}/utilization`)).data,
  });
  const assetsRes = useList<Asset>({ resource: 'inventory/assets', pagination: { pageSize: 500 } });
  const assets = assetsRes.result.data ?? [];
  const assetsById = useMemo(() => new Map(assets.map((a) => [a.id, a])), [assets]);
  const data = result.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);
  const [view, setView] = useState<'table' | 'grid'>('table');

  async function remove(ip: IPAddr) {
    if (!globalThis.confirm(`Release ${ip.address}?`)) return;
    try {
      await http.delete(`/ipam/addresses/${ip.id}`);
      toast.success('Address released');
      await tableQuery.refetch();
      await qc.invalidateQueries({ queryKey: ['subnet-util', subnetId] });
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <SpaceBetween size="s">
      {util.data && (
        <Container>
          <SpaceBetween size="xxs">
            <CapacityBar
              used={util.data.allocated} total={util.data.capacity}
              leftLabel={`${util.data.allocated}/${util.data.capacity} addresses allocated`}
            />
            <Box color="text-status-inactive" fontSize="body-s">
              {util.data.next_available
                ? <>Next available: <span style={{ fontFamily: 'ui-monospace, monospace' }}>{util.data.next_available}</span></>
                : 'Subnet is full'}
            </Box>
          </SpaceBetween>
        </Container>
      )}

      {view === 'grid' && util.data && (
        <IpGrid
          subnetPrefix={util.data.prefix}
          capacity={util.data.capacity}
          allocated={data}
          assetsById={assetsById}
        />
      )}

      {view === 'table' && (
        <Table<IPAddr>
          variant="container"
          loading={tableQuery.isLoading}
          loadingText="Loading addresses…"
          items={data}
          trackBy="id"
          header={
            <Header
              counter={`(${data.length})`}
              actions={
                <SpaceBetween size="xs" direction="horizontal">
                  <SegmentedControl
                    selectedId={view}
                    onChange={({ detail }) => setView(detail.selectedId as 'table' | 'grid')}
                    options={[
                      { id: 'table', text: 'Table' },
                      { id: 'grid', text: 'Grid' },
                    ]}
                  />
                  {canWrite && (
                    <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                      Allocate IP
                    </Button>
                  )}
                </SpaceBetween>
              }
            >
              Addresses
            </Header>
          }
          columnDefinitions={[
            { id: 'address', header: 'Address', cell: (ip) => <span style={{ fontFamily: 'ui-monospace, monospace' }}>{ip.address}</span> },
            {
              id: 'asset', header: 'Asset',
              cell: (ip) => ip.asset_id
                ? (assetsById.get(ip.asset_id)?.name ?? ip.asset_id.slice(0, 8) + '…')
                : <Box color="text-status-inactive">—</Box>,
            },
            { id: 'role', header: 'Role', cell: (ip) => <Badge>{ip.role}</Badge>, width: 90 },
            {
              id: 'source', header: 'Source',
              cell: (ip) => <Badge color={ip.source === 'dhcp' ? 'severity-medium' : 'grey'}>{ip.source}</Badge>,
              width: 110,
            },
            {
              id: 'status', header: 'Status',
              cell: (ip) => (
                <StatusIndicator type={ip.status === 'active' ? 'success' : 'info'}>{ip.status}</StatusIndicator>
              ),
              width: 110,
            },
            {
              id: 'dns', header: 'DNS',
              cell: (ip) => ip.dns_name ?? <Box color="text-status-inactive">—</Box>,
            },
            {
              id: 'lease', header: 'Lease ends',
              cell: (ip) => (
                <Box color="text-status-inactive" fontSize="body-s">
                  {ip.dhcp_lease_expires_at ? formatDate(ip.dhcp_lease_expires_at) : '—'}
                </Box>
              ),
              width: 160,
            },
            ...(canWrite ? [{
              id: 'actions', header: '',
              cell: (ip: IPAddr) => (
                <Button
                  iconName="remove"
                  variant="inline-icon"
                  onClick={() => remove(ip)}
                  ariaLabel={`Release ${ip.address}`}
                />
              ),
              width: 60,
            }] : []),
          ]}
          empty={
            <Box textAlign="center" color="inherit" padding="m">
              No allocations yet.
            </Box>
          }
        />
      )}

      {canWrite && (
        <Modal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header="Allocate IP address"
          size="medium"
        >
          <IpForm
            subnetId={subnetId}
            suggestedAddress={util.data?.next_available ?? ''}
            assets={assets}
            onSaved={async () => {
              setCreateOpen(false);
              await tableQuery.refetch();
              await qc.invalidateQueries({ queryKey: ['subnet-util', subnetId] });
            }}
          />
        </Modal>
      )}
    </SpaceBetween>
  );
}


// Hard cap on grid cells. /24 (256) and /22 (1024) are reasonable; an IPv6
// /64 is 2^64 cells which obviously can't render, and even an IPv4 /20 at
// 4096 cells is too noisy to be useful.
const IP_GRID_MAX_CELLS = 1024;


function IpGrid({
  subnetPrefix, capacity, allocated, assetsById,
}: Readonly<{
  subnetPrefix: string;
  capacity: number;
  allocated: IPAddr[];
  assetsById: Map<string, Asset>;
}>) {
  const cells = useMemo(
    () => (capacity > IP_GRID_MAX_CELLS ? [] : buildGridCells(subnetPrefix, capacity, allocated)),
    [subnetPrefix, capacity, allocated],
  );
  if (capacity > IP_GRID_MAX_CELLS) {
    return (
      <Container>
        <Box padding="m" color="text-status-inactive" fontSize="body-s">
          Grid view is hidden for prefixes larger than /22 (this subnet has{' '}
          <span style={{ fontFamily: 'ui-monospace, monospace' }}>{capacity.toLocaleString()}</span> addresses).
          Use the table for now — search by address to find a specific allocation.
        </Box>
      </Container>
    );
  }
  if (cells.length === 0) {
    return (
      <Container>
        <Box padding="m" color="text-status-inactive" fontSize="body-s">
          Couldn't render this prefix as a grid (unparseable CIDR).
        </Box>
      </Container>
    );
  }
  return (
    <Container>
      <SpaceBetween size="s">
        <SpaceBetween size="s" direction="horizontal">
          <Legend color={colorBackgroundInputDisabled} label="free" />
          <Legend color={colorTextStatusSuccess} label="static" />
          <Legend color={colorTextStatusWarning} label="dhcp" />
          <Legend color={colorTextStatusInfo} label="reservation" />
          <Legend color={colorTextStatusInactive} label="deprecated" />
        </SpaceBetween>
        <div
          style={{
            display: 'grid',
            gap: 2,
            gridTemplateColumns: 'repeat(32, minmax(0, 1fr))',
          }}
        >
          {cells.map((cell) => (
            <IpCell key={cell.address} cell={cell} assetsById={assetsById} />
          ))}
        </div>
        <Box color="text-status-inactive" fontSize="body-s">
          Hover a cell for details · {allocated.length} of {capacity} allocated
        </Box>
      </SpaceBetween>
    </Container>
  );
}

function Legend({ color, label }: Readonly<{ color: string; label: string }>) {
  return (
    <span style={{ display: 'inline-flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
      <span style={{ display: 'inline-block', width: 12, height: 12, borderRadius: 2, background: color }} />
      {label}
    </span>
  );
}

type IpCellInfo = {
  address: string;
  ip: IPAddr | null;
};

function buildGridCells(
  prefix: string, capacity: number, allocated: IPAddr[],
): IpCellInfo[] {
  // Walk the network's hosts in numeric order and pair them with any
  // existing allocation. Uses the same "skip network/broadcast for /30
  // and shorter" rule as the backend so cell counts line up with capacity.
  const slash = prefix.indexOf('/');
  if (slash < 0) return [];
  const isV6 = prefix.includes(':');
  const prefixLen = Number(prefix.slice(slash + 1));
  if (Number.isNaN(prefixLen)) return [];
  const networkAddr = prefix.slice(0, slash);
  const allocatedByAddress = new Map<string, IPAddr>();
  for (const a of allocated) allocatedByAddress.set(a.address, a);

  const skipEdges = isV6 ? prefixLen < 127 : prefixLen < 31;
  let netInt: bigint;
  try {
    netInt = ipToBigInt(networkAddr, isV6);
  } catch { return []; }
  const start = skipEdges ? netInt + 1n : netInt;
  const limit = BigInt(Math.min(capacity, IP_GRID_MAX_CELLS));
  const cells: IpCellInfo[] = [];
  for (let i = 0n; i < limit; i++) {
    const a = bigIntToIp(start + i, isV6);
    cells.push({ address: a, ip: allocatedByAddress.get(a) ?? null });
  }
  return cells;
}

function ipToBigInt(addr: string, isV6: boolean): bigint {
  if (!isV6) {
    const parts = addr.split('.').map(Number);
    if (parts.length !== 4 || parts.some((n) => Number.isNaN(n))) throw new Error('bad v4');
    return (BigInt(parts[0]) << 24n)
      + (BigInt(parts[1]) << 16n)
      + (BigInt(parts[2]) << 8n)
      + BigInt(parts[3]);
  }
  // Expand `::` and pad each group, then concatenate as one big hex.
  const sides = addr.split('::');
  const left = sides[0] ? sides[0].split(':') : [];
  const right = sides.length > 1 && sides[1] ? sides[1].split(':') : [];
  const missing = 8 - left.length - right.length;
  const groups = [...left, ...Array(missing).fill('0'), ...right];
  const hex = groups.map((g) => g.padStart(4, '0')).join('');
  return BigInt('0x' + hex);
}

// Pure CIDR containment check used by the tree's drag-and-drop to grey out
// invalid drop targets before the request goes out. Reuses ipToBigInt so we
// only carry one address-parser in the bundle.
function cidrContains(parentCidr: string, childCidr: string): boolean {
  try {
    const [pIp, pBitsRaw] = parentCidr.split('/');
    const [cIp, cBitsRaw] = childCidr.split('/');
    const isV6 = pIp.includes(':');
    if (isV6 !== cIp.includes(':')) return false;
    const totalBits = isV6 ? 128 : 32;
    const pBits = Number(pBitsRaw ?? totalBits);
    const cBits = Number(cBitsRaw ?? totalBits);
    if (cBits < pBits) return false;
    const pn = ipToBigInt(pIp, isV6);
    const cn = ipToBigInt(cIp, isV6);
    if (pBits === 0) return true;
    const mask = ((1n << BigInt(pBits)) - 1n) << BigInt(totalBits - pBits);
    return (pn & mask) === (cn & mask);
  } catch {
    return false;
  }
}

function bigIntToIp(n: bigint, isV6: boolean): string {
  if (!isV6) {
    return [
      Number((n >> 24n) & 0xffn),
      Number((n >> 16n) & 0xffn),
      Number((n >> 8n) & 0xffn),
      Number(n & 0xffn),
    ].join('.');
  }
  const groups: string[] = [];
  for (let i = 7n; i >= 0n; i--) {
    const part = Number((n >> (i * 16n)) & 0xffffn).toString(16);
    groups.push(part);
  }
  // Compress the longest run of zero groups into `::` for readability.
  return compressV6(groups);
}

function compressV6(groups: string[]): string {
  let bestStart = -1, bestLen = 0;
  let curStart = -1, curLen = 0;
  for (let i = 0; i < groups.length; i++) {
    if (groups[i] === '0') {
      if (curStart < 0) { curStart = i; curLen = 1; }
      else curLen++;
      if (curLen > bestLen) { bestStart = curStart; bestLen = curLen; }
    } else {
      curStart = -1; curLen = 0;
    }
  }
  if (bestLen < 2) return groups.join(':');
  return [
    groups.slice(0, bestStart).join(':'),
    groups.slice(bestStart + bestLen).join(':'),
  ].join('::');
}


function IpCell({
  cell, assetsById,
}: Readonly<{
  cell: IpCellInfo;
  assetsById: Map<string, Asset>;
}>) {
  const ip = cell.ip;
  const asset = ip?.asset_id ? assetsById.get(ip.asset_id) : null;
  const tooltip = ip
    ? [
        ip.address,
        `source: ${ip.source}`,
        `status: ${ip.status}`,
        `role: ${ip.role}`,
        asset ? `asset: ${asset.name}` : null,
        ip.dns_name ? `dns: ${ip.dns_name}` : null,
      ].filter(Boolean).join(' · ')
    : `${cell.address} · free`;
  return (
    <span
      title={tooltip}
      style={{
        display: 'inline-block',
        aspectRatio: '1 / 1',
        borderRadius: 2,
        background: cellColor(ip),
      }}
    />
  );
}

function cellColor(ip: IPAddr | null): string {
  if (!ip) return colorBackgroundInputDisabled;
  if (ip.status === 'deprecated') return colorTextStatusInactive;
  if (ip.source === 'dhcp') return colorTextStatusWarning;
  if (ip.source === 'reservation') return colorTextStatusInfo;
  return colorTextStatusSuccess;
}


function IpForm({
  subnetId, suggestedAddress, assets, onSaved,
}: Readonly<{
  subnetId: string;
  suggestedAddress: string;
  assets: Asset[];
  onSaved: () => void;
}>) {
  const NONE = '__none__';
  const [address, setAddress] = useState(suggestedAddress);
  const [assetOpt, setAssetOpt] = useState<SelectProps.Option>({ value: NONE, label: '(reservation / unbound)' });
  const [roleOpt, setRoleOpt] = useState<SelectProps.Option>({ value: 'data', label: 'data' });
  const [statusOpt, setStatusOpt] = useState<SelectProps.Option>({ value: 'active', label: 'active' });
  const [sourceOpt, setSourceOpt] = useState<SelectProps.Option>({ value: 'static', label: 'static' });
  const [dnsName, setDnsName] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const assetOptions: SelectProps.Option[] = [
    { value: NONE, label: '(reservation / unbound)' },
    ...assets.slice(0, 200).map((a) => ({ value: a.id, label: a.name })),
  ];
  const roleOptions: SelectProps.Option[] = ROLES.map((r) => ({ value: r, label: r }));
  const sourceOptions: SelectProps.Option[] = SOURCES.map((r) => ({ value: r, label: r }));
  const statusOptions: SelectProps.Option[] = STATUSES.map((r) => ({ value: r, label: r }));

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!address.trim()) errs.address = 'Address required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      await http.post('/ipam/addresses', {
        subnet_id: subnetId,
        address,
        asset_id: assetOpt.value === NONE ? null : assetOpt.value,
        role: roleOpt.value,
        status: statusOpt.value,
        source: sourceOpt.value,
        dns_name: dnsName || null,
        description: null,
      });
      toast.success('IP allocated');
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
            {submitting ? 'Saving…' : 'Allocate'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField label="Address" errorText={errors.address}>
            <Input value={address} onChange={({ detail }) => setAddress(detail.value)} placeholder="e.g. 10.0.5.42" />
          </FormField>
          <FormField label="Bound asset (optional)">
            <Select
              selectedOption={assetOpt}
              onChange={({ detail }) => {
                if (detail.selectedOption.value) setAssetOpt(detail.selectedOption);
              }}
              options={assetOptions}
              expandToViewport
            />
          </FormField>
          <ColumnLayout columns={3}>
            <FormField label="Role">
              <Select
                selectedOption={roleOpt}
                onChange={({ detail }) => {
                  if (detail.selectedOption.value) setRoleOpt(detail.selectedOption);
                }}
                options={roleOptions}
                expandToViewport
              />
            </FormField>
            <FormField label="Source">
              <Select
                selectedOption={sourceOpt}
                onChange={({ detail }) => {
                  if (detail.selectedOption.value) setSourceOpt(detail.selectedOption);
                }}
                options={sourceOptions}
                expandToViewport
              />
            </FormField>
            <FormField label="Status">
              <Select
                selectedOption={statusOpt}
                onChange={({ detail }) => {
                  if (detail.selectedOption.value) setStatusOpt(detail.selectedOption);
                }}
                options={statusOptions}
                expandToViewport
              />
            </FormField>
          </ColumnLayout>
          <FormField label="DNS name (optional)">
            <Input value={dnsName} onChange={({ detail }) => setDnsName(detail.value)} />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}

// ----------------------- DHCP servers -----------------------

type DhcpServer = {
  id: string; name: string; fabric_id: string; kea_url: string;
  auth_username: string | null; enabled: boolean;
  last_sync_at: string | null; last_sync_status: string | null;
  last_sync_error: string | null; last_sync_lease_count: number | null;
};

const dhcpSchema = z.object({
  name: z.string().min(1),
  fabric_id: z.string().min(1),
  kea_url: z.string().url(),
  auth_username: z.string().optional(),
  auth_password: z.string().optional(),
  enabled: z.boolean(),
});

function DhcpServersTab({ canWrite }: { canWrite: boolean }) {
  const { tableQuery, result } = useTable<DhcpServer>({
    resource: 'ipam/dhcp/servers',
    pagination: { pageSize: 50 },
    sorters: { initial: [{ field: 'name', order: 'asc' }] },
  });
  const fabricsRes = useList<Fabric>({ resource: 'ipam/fabrics', pagination: { pageSize: 200 } });
  const fabrics = fabricsRes.result.data ?? [];
  const fabricsById = useMemo(() => new Map(fabrics.map((f) => [f.id, f])), [fabrics]);
  const data = result.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);

  async function syncNow(s: DhcpServer) {
    try {
      const r = await http.post<{
        upserted: number; skipped_no_subnet: number; leases_seen: number; error: string | null;
      }>(`/ipam/dhcp/servers/${s.id}/sync`, {});
      if (r.data.error) toast.error(`Sync failed: ${r.data.error}`);
      else toast.success(
        `Synced — ${r.data.upserted} upserted / ${r.data.leases_seen} seen / ${r.data.skipped_no_subnet} skipped`,
      );
      await tableQuery.refetch();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  async function remove(s: DhcpServer) {
    if (!window.confirm(`Delete DHCP server "${s.name}"?`)) return;
    try {
      await http.delete(`/ipam/dhcp/servers/${s.id}`);
      toast.success('DHCP server removed');
      await tableQuery.refetch();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <>
      <Table<DhcpServer>
        variant="container"
        loading={tableQuery.isLoading}
        loadingText="Loading DHCP servers…"
        items={data}
        trackBy="id"
        header={
          <Header
            counter={`(${data.length})`}
            actions={
              canWrite && (
                <Button
                  variant="primary"
                  iconName="add-plus"
                  onClick={() => setCreateOpen(true)}
                >
                  Add Kea server
                </Button>
              )
            }
          >
            DHCP servers
          </Header>
        }
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (s) => <span style={{ fontWeight: 500 }}>{s.name}</span> },
          {
            id: 'fabric', header: 'Fabric',
            cell: (s) => fabricsById.get(s.fabric_id)?.name ?? s.fabric_id.slice(0, 8) + '…',
          },
          {
            id: 'kea_url', header: 'Control Agent URL',
            cell: (s) => <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{s.kea_url}</span>,
          },
          {
            id: 'last_sync', header: 'Last sync',
            cell: (s) => (
              <Box variant="span" color="text-status-inactive" fontSize="body-s">
                {s.last_sync_at ? formatDate(s.last_sync_at) : 'never'}
              </Box>
            ),
            width: 200,
          },
          {
            id: 'leases', header: 'Leases',
            cell: (s) => <span style={{ fontVariantNumeric: 'tabular-nums' }}>{s.last_sync_lease_count ?? '—'}</span>,
            width: 100,
          },
          {
            id: 'status', header: 'Status',
            cell: (s) => (
              <SpaceBetween size="xxs" direction="horizontal">
                {s.last_sync_status === 'ok' && <StatusIndicator type="success">ok</StatusIndicator>}
                {s.last_sync_status === 'error' && <StatusIndicator type="error">error</StatusIndicator>}
                {!s.last_sync_status && <StatusIndicator type="pending">pending</StatusIndicator>}
                {!s.enabled && <StatusIndicator type="stopped">disabled</StatusIndicator>}
              </SpaceBetween>
            ),
            width: 180,
          },
          ...(canWrite ? [{
            id: 'actions',
            header: '',
            // Inline row actions: sync triggers a synchronous /sync call
            // and refreshes; delete confirms then removes.
            cell: (s: DhcpServer) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button iconName="upload" variant="inline-icon" onClick={() => syncNow(s)} ariaLabel={`Sync ${s.name}`} />
                <Button iconName="remove" variant="inline-icon" onClick={() => remove(s)} ariaLabel={`Delete ${s.name}`} />
              </SpaceBetween>
            ),
            width: 120,
          }] : []),
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            No Kea servers registered.
          </Box>
        }
      />
      {/* Cloudscape Modal for the create flow. The form inside still
          uses shadcn react-hook-form primitives — Cloudscape doesn't
          ship a react-hook-form integration and rewriting every form
          input is out of scope for this commit. */}
      {canWrite && (
        <Modal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header="Register Kea DHCP server"
          size="medium"
        >
          <DhcpForm
            fabrics={fabrics}
            onSaved={async () => { setCreateOpen(false); await tableQuery.refetch(); }}
          />
        </Modal>
      )}
    </>
  );
}

function DhcpForm({ fabrics, onSaved }: { fabrics: Fabric[]; onSaved: () => void }) {
  const fabricOpts: SelectProps.Option[] = fabrics.map((f) => ({ value: f.id, label: f.name }));
  const [name, setName] = useState('');
  const [fabricOpt, setFabricOpt] = useState<SelectProps.Option | null>(null);
  const [keaUrl, setKeaUrl] = useState('');
  const [authUsername, setAuthUsername] = useState('');
  const [authPassword, setAuthPassword] = useState('');
  const [enabled, setEnabled] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Name required';
    if (!fabricOpt?.value) errs.fabric = 'Fabric required';
    if (!keaUrl.trim()) errs.kea_url = 'URL required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      await http.post('/ipam/dhcp/servers', {
        name, fabric_id: fabricOpt!.value, kea_url: keaUrl,
        auth_username: authUsername || null,
        auth_password: authPassword || null,
        enabled,
      });
      toast.success('DHCP server registered');
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); } finally { setSubmitting(false); }
  }
  return (
    <form onSubmit={onSubmit}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={submitting}>
            {submitting ? 'Saving…' : 'Register'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField label="Name" errorText={errors.name}>
            <Input value={name} onChange={({ detail }) => setName(detail.value)} placeholder="e.g. kea-prod-east" />
          </FormField>
          <FormField label="Fabric" errorText={errors.fabric}>
            <Select
              placeholder="Pick a fabric"
              selectedOption={fabricOpt}
              onChange={({ detail }) => setFabricOpt(detail.selectedOption)}
              options={fabricOpts}
              expandToViewport
            />
          </FormField>
          <FormField label="Kea Control Agent URL" errorText={errors.kea_url}>
            <Input type="url" value={keaUrl} onChange={({ detail }) => setKeaUrl(detail.value)}
              placeholder="http://kea-ctrl-agent:8000" />
          </FormField>
          <ColumnLayout columns={2}>
            <FormField label="Username (optional)">
              <Input value={authUsername} onChange={({ detail }) => setAuthUsername(detail.value)} />
            </FormField>
            <FormField label="Password (optional)">
              <Input type="password" value={authPassword} onChange={({ detail }) => setAuthPassword(detail.value)} />
            </FormField>
          </ColumnLayout>
          <Checkbox checked={enabled} onChange={({ detail }) => setEnabled(detail.checked)}>
            Enabled (sync every 5 minutes)
          </Checkbox>
        </SpaceBetween>
      </Form>
    </form>
  );
}



// ----------------------- Overlays / VNI / VTEP -----------------------

const overlaySchema = z.object({
  fabric_id: z.string().min(1),
  name: z.string().min(1),
  kind: z.enum(['vxlan', 'geneve']),
  udp_port: z.string().min(1),
  mtu: z.string().optional(),
  underlay_vrf_id: z.string().optional(),
  description: z.string().optional(),
});

const vniSchema = z.object({
  vni: z.string().min(1),
  kind: z.enum(['l2', 'l3']),
  name: z.string().optional(),
  vlan_id: z.string().optional(),
  evpn_route_target: z.string().optional(),
  vrf_id: z.string().optional(),
  description: z.string().optional(),
});

const vtepSchema = z.object({
  asset_id: z.string().min(1),
  loopback_ip: z.string().optional(),
  role: z.enum(['leaf', 'spine', 'border', 'other']),
  description: z.string().optional(),
});

function OverlaysTab({ canWrite }: { canWrite: boolean }) {
  const qc = useQueryClient();
  const fabricsRes = useList<Fabric>({ resource: 'ipam/fabrics', pagination: { pageSize: 200 } });
  const fabrics = fabricsRes.result.data ?? [];
  const [fabricId, setFabricId] = useState<string>('');
  const [selectedOverlay, setSelectedOverlay] = useState<Overlay[]>([]);
  const [createOverlayOpen, setCreateOverlayOpen] = useState(false);

  // First fabric becomes the default once fabrics arrive — saves a click.
  if (!fabricId && fabrics.length > 0) {
    setFabricId(fabrics[0].id);
  }

  const overlaysQ = useQuery({
    enabled: !!fabricId,
    queryKey: ['overlays-for-fabric', fabricId],
    queryFn: async () => (
      await http.get<{ items: Overlay[] }>(`/ipam/overlays?fabric_id=${fabricId}&page_size=200`)
    ).data.items ?? [],
  });
  const overlays = overlaysQ.data ?? [];
  const overlayId = selectedOverlay[0]?.id ?? null;

  async function refreshOverlays() {
    await qc.invalidateQueries({ queryKey: ['overlays-for-fabric', fabricId] });
  }

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
                setSelectedOverlay([]);
              }
            }}
            options={fabricOptions}
            expandToViewport
          />
        </FormField>
      </Container>

      <Table<Overlay>
        variant="container"
        loading={overlaysQ.isLoading}
        loadingText="Loading overlays…"
        items={overlays}
        trackBy="id"
        selectionType="single"
        selectedItems={selectedOverlay}
        onSelectionChange={({ detail }) => setSelectedOverlay(detail.selectedItems)}
        ariaLabels={{
          selectionGroupLabel: 'Overlay selection',
          itemSelectionLabel: (_d, item) => `Select overlay ${item.name}`,
          allItemsSelectionLabel: () => 'select all',
        }}
        header={
          <Header
            counter={
              selectedOverlay.length
                ? `(${selectedOverlay.length}/${overlays.length})`
                : `(${overlays.length})`
            }
            actions={
              canWrite && fabricId && (
                <Button variant="primary" iconName="add-plus" onClick={() => setCreateOverlayOpen(true)}>
                  New overlay
                </Button>
              )
            }
            description="Select an overlay to drill into its VNIs and VTEPs."
          >
            Overlays
          </Header>
        }
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (o) => <span style={{ fontWeight: 500 }}>{o.name}</span> },
          { id: 'kind', header: 'Kind', cell: (o) => <Badge>{o.kind}</Badge>, width: 120 },
          {
            id: 'udp', header: 'UDP port',
            cell: (o) => <span style={{ fontFamily: 'ui-monospace, monospace' }}>{o.udp_port}</span>,
            width: 120,
          },
          { id: 'mtu', header: 'MTU', cell: (o) => o.mtu ?? '—', width: 100 },
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            No overlays in this fabric yet.
          </Box>
        }
      />

      {overlayId && (
        <ColumnLayout columns={2}>
          <VnisPanel overlayId={overlayId} canWrite={canWrite} />
          <VtepsPanel overlayId={overlayId} canWrite={canWrite} />
        </ColumnLayout>
      )}

      {canWrite && fabricId && (
        <Modal
          visible={createOverlayOpen}
          onDismiss={() => setCreateOverlayOpen(false)}
          header="New overlay"
          size="medium"
        >
          <OverlayForm
            fabricId={fabricId}
            onSaved={async () => { setCreateOverlayOpen(false); await refreshOverlays(); }}
          />
        </Modal>
      )}
    </SpaceBetween>
  );
}

function OverlayForm({
  fabricId, onSaved,
}: { fabricId: string; onSaved: () => void }) {
  const NONE = '__none__';
  const vrfsQ = useList<Vrf>({
    resource: 'ipam/vrfs',
    filters: [{ field: 'fabric_id', operator: 'eq', value: fabricId }],
    pagination: { pageSize: 200 },
  });
  const vrfs = vrfsQ.result.data ?? [];
  const form = useForm<z.infer<typeof overlaySchema>>({
    resolver: zodResolver(overlaySchema),
    defaultValues: {
      fabric_id: fabricId,
      name: '',
      kind: 'vxlan',
      udp_port: '4789',
      mtu: '',
      underlay_vrf_id: NONE,
      description: '',
    },
  });
  const kind = form.watch('kind');
  // Snap the default UDP port when the operator flips kind so they don't
  // have to remember 4789 vs 6081 — they can still override it.
  function syncPort(next: 'vxlan' | 'geneve') {
    form.setValue('kind', next);
    form.setValue('udp_port', next === 'vxlan' ? '4789' : '6081');
  }
  async function onSubmit(v: z.infer<typeof overlaySchema>) {
    try {
      await http.post('/ipam/overlays', {
        fabric_id: v.fabric_id,
        name: v.name,
        kind: v.kind,
        udp_port: Number(v.udp_port),
        mtu: v.mtu ? Number(v.mtu) : null,
        underlay_vrf_id: v.underlay_vrf_id && v.underlay_vrf_id !== NONE ? v.underlay_vrf_id : null,
        description: v.description || null,
      });
      toast.success('Overlay created');
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }
  const kindOptions: SelectProps.Option[] = [
    { value: 'vxlan', label: 'VXLAN' },
    { value: 'geneve', label: 'GENEVE' },
  ];
  const vrfOptions: SelectProps.Option[] = [
    { value: NONE, label: '(none)' },
    ...vrfs.map((v) => ({ value: v.id, label: v.name })),
  ];
  const nameV = form.watch('name') ?? '';
  const udpV = form.watch('udp_port') ?? '';
  const mtuV = form.watch('mtu') ?? '';
  const vrfV = form.watch('underlay_vrf_id') ?? NONE;
  const descV = form.watch('description') ?? '';

  return (
    <form onSubmit={form.handleSubmit(onSubmit)}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={form.formState.isSubmitting}>
            {form.formState.isSubmitting ? 'Saving…' : 'Create'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField label="Name" errorText={form.formState.errors.name?.message as string | undefined}>
            <Input value={nameV} onChange={({ detail }) => form.setValue('name', detail.value)}
              placeholder="e.g. evpn-fabric-east" />
          </FormField>
          <ColumnLayout columns={2}>
            <FormField label="Kind">
              <Select
                selectedOption={kindOptions.find((o) => o.value === kind) ?? kindOptions[0]}
                onChange={({ detail }) => syncPort(detail.selectedOption.value as 'vxlan' | 'geneve')}
                options={kindOptions}
                expandToViewport
              />
            </FormField>
            <FormField label="UDP port">
              <Input type="number" value={udpV} onChange={({ detail }) => form.setValue('udp_port', detail.value)} />
            </FormField>
          </ColumnLayout>
          <ColumnLayout columns={2}>
            <FormField label="MTU (optional)">
              <Input type="number" value={mtuV} onChange={({ detail }) => form.setValue('mtu', detail.value)} placeholder="9000" />
            </FormField>
            <FormField label="Underlay VRF (optional)">
              <Select
                selectedOption={vrfOptions.find((o) => o.value === vrfV) ?? vrfOptions[0]}
                onChange={({ detail }) => form.setValue('underlay_vrf_id', detail.selectedOption.value!)}
                options={vrfOptions}
                expandToViewport
              />
            </FormField>
          </ColumnLayout>
          <FormField label="Description">
            <Input value={descV} onChange={({ detail }) => form.setValue('description', detail.value)} />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}

function VnisPanel({ overlayId, canWrite }: { overlayId: string; canWrite: boolean }) {
  const qc = useQueryClient();
  const vnisQ = useQuery({
    queryKey: ['vnis-for-overlay', overlayId],
    queryFn: async () => (
      await http.get<{ items: Vni[] }>(`/ipam/vnis?overlay_id=${overlayId}&page_size=200`)
    ).data.items ?? [],
  });
  const vnis = vnisQ.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);

  async function remove(v: Vni) {
    if (!window.confirm(`Delete VNI ${v.vni}?`)) return;
    try {
      await http.delete(`/ipam/vnis/${v.id}`);
      await qc.invalidateQueries({ queryKey: ['vnis-for-overlay', overlayId] });
      toast.success('VNI removed');
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <>
      <Table<Vni>
        variant="container"
        loading={vnisQ.isLoading}
        loadingText="Loading VNIs…"
        items={vnis}
        trackBy="id"
        header={
          <Header
            counter={`(${vnis.length})`}
            actions={canWrite && (
              <Button iconName="add-plus" onClick={() => setCreateOpen(true)}>
                Add VNI
              </Button>
            )}
          >
            VNIs
          </Header>
        }
        columnDefinitions={[
          {
            id: 'vni', header: 'VNI',
            cell: (v) => <span style={{ fontFamily: 'ui-monospace, monospace' }}>{v.vni}</span>,
            width: 100,
          },
          { id: 'kind', header: 'Kind', cell: (v) => <Badge>{v.kind}</Badge>, width: 80 },
          { id: 'name', header: 'Name', cell: (v) => v.name ?? '—' },
          {
            id: 'vlan_rt', header: 'VLAN / RT',
            cell: (v) => (
              <Box variant="span" color="text-status-inactive" fontSize="body-s">
                {v.vlan_id ? `vlan ${v.vlan_id}` : ''}
                {v.evpn_route_target ? ` · rt ${v.evpn_route_target}` : ''}
                {v.vrf_id ? ` · vrf bound` : ''}
              </Box>
            ),
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (v: Vni) => (
              <Button iconName="remove" variant="inline-icon" onClick={() => remove(v)} ariaLabel={`Delete VNI ${v.vni}`} />
            ),
            width: 60,
          }] : []),
        ]}
        empty={<Box textAlign="center" color="inherit" padding="m">No VNIs yet.</Box>}
      />
      {canWrite && (
        <Modal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header="New VNI"
          size="medium"
        >
          <VniForm
            overlayId={overlayId}
            onSaved={async () => {
              setCreateOpen(false);
              await qc.invalidateQueries({ queryKey: ['vnis-for-overlay', overlayId] });
            }}
          />
        </Modal>
      )}
    </>
  );
}

function VniForm({ overlayId, onSaved }: { overlayId: string; onSaved: () => void }) {
  const NONE = '__none__';
  // VNI VRF dropdown — populated from ALL VRFs for now since we don't
  // know the overlay's fabric here without an extra fetch. Backend rechecks
  // that the chosen VRF lives in the overlay's fabric.
  const vrfsQ = useList<Vrf>({ resource: 'ipam/vrfs', pagination: { pageSize: 500 } });
  const vrfs = vrfsQ.result.data ?? [];
  const form = useForm<z.infer<typeof vniSchema>>({
    resolver: zodResolver(vniSchema),
    defaultValues: {
      vni: '', kind: 'l2', name: '',
      vlan_id: '', evpn_route_target: '', vrf_id: NONE, description: '',
    },
  });
  const kind = form.watch('kind');
  async function onSubmit(v: z.infer<typeof vniSchema>) {
    try {
      await http.post('/ipam/vnis', {
        overlay_id: overlayId,
        vni: Number(v.vni),
        kind: v.kind,
        name: v.name || null,
        vlan_id: v.kind === 'l2' && v.vlan_id ? Number(v.vlan_id) : null,
        evpn_route_target: v.kind === 'l2' ? (v.evpn_route_target || null) : null,
        vrf_id: v.kind === 'l3' && v.vrf_id && v.vrf_id !== NONE ? v.vrf_id : null,
        description: v.description || null,
      });
      toast.success('VNI created');
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }
  const kindOptions: SelectProps.Option[] = [
    { value: 'l2', label: 'L2 (broadcast domain)' },
    { value: 'l3', label: 'L3 (tenant VRF)' },
  ];
  const vrfOptions: SelectProps.Option[] = [
    { value: NONE, label: '(unset)' },
    ...vrfs.map((v) => ({ value: v.id, label: v.name })),
  ];
  const vniV = form.watch('vni') ?? '';
  const nameV = form.watch('name') ?? '';
  const vlanV = form.watch('vlan_id') ?? '';
  const rtV = form.watch('evpn_route_target') ?? '';
  const vrfV = form.watch('vrf_id') ?? NONE;

  return (
    <form onSubmit={form.handleSubmit(onSubmit)}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={form.formState.isSubmitting}>
            {form.formState.isSubmitting ? 'Saving…' : 'Create'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <ColumnLayout columns={2}>
            <FormField label="VNI (1..16777214)">
              <Input type="number" value={vniV} onChange={({ detail }) => form.setValue('vni', detail.value)} />
            </FormField>
            <FormField label="Kind">
              <Select
                selectedOption={kindOptions.find((o) => o.value === kind) ?? kindOptions[0]}
                onChange={({ detail }) => form.setValue('kind', detail.selectedOption.value as 'l2' | 'l3')}
                options={kindOptions}
                expandToViewport
              />
            </FormField>
          </ColumnLayout>
          <FormField label="Name (optional)">
            <Input value={nameV} onChange={({ detail }) => form.setValue('name', detail.value)} />
          </FormField>
          {kind === 'l2' && (
            <ColumnLayout columns={2}>
              <FormField label="Mapped VLAN (optional)">
                <Input type="number" value={vlanV} onChange={({ detail }) => form.setValue('vlan_id', detail.value)} />
              </FormField>
              <FormField label="EVPN RT (optional)">
                <Input value={rtV} onChange={({ detail }) => form.setValue('evpn_route_target', detail.value)}
                  placeholder="65000:10010" />
              </FormField>
            </ColumnLayout>
          )}
          {kind === 'l3' && (
            <FormField label="Tenant VRF" description="L3 VNIs map a tenant VRF — required.">
              <Select
                selectedOption={vrfOptions.find((o) => o.value === vrfV) ?? vrfOptions[0]}
                onChange={({ detail }) => form.setValue('vrf_id', detail.selectedOption.value!)}
                options={vrfOptions}
                expandToViewport
              />
            </FormField>
          )}
        </SpaceBetween>
      </Form>
    </form>
  );
}

function VtepsPanel({ overlayId, canWrite }: { overlayId: string; canWrite: boolean }) {
  const qc = useQueryClient();
  const vtepsQ = useQuery({
    queryKey: ['vteps-for-overlay', overlayId],
    queryFn: async () => (
      await http.get<{ items: Vtep[] }>(`/ipam/vteps?overlay_id=${overlayId}&page_size=200`)
    ).data.items ?? [],
  });
  const vteps = vtepsQ.data ?? [];
  const assetsRes = useList<Asset>({ resource: 'inventory/assets', pagination: { pageSize: 500 } });
  const assets = assetsRes.result.data ?? [];
  const assetsById = useMemo(() => new Map(assets.map((a) => [a.id, a])), [assets]);
  const [createOpen, setCreateOpen] = useState(false);

  async function remove(v: Vtep) {
    if (!window.confirm('Delete this VTEP and all its VNI memberships?')) return;
    try {
      await http.delete(`/ipam/vteps/${v.id}`);
      await qc.invalidateQueries({ queryKey: ['vteps-for-overlay', overlayId] });
      toast.success('VTEP removed');
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <>
      <Table<Vtep>
        variant="container"
        loading={vtepsQ.isLoading}
        loadingText="Loading VTEPs…"
        items={vteps}
        trackBy="id"
        header={
          <Header
            counter={`(${vteps.length})`}
            actions={canWrite && (
              <Button iconName="add-plus" onClick={() => setCreateOpen(true)}>
                Add VTEP
              </Button>
            )}
          >
            VTEPs
          </Header>
        }
        columnDefinitions={[
          {
            id: 'asset', header: 'Asset',
            cell: (v) => assetsById.get(v.asset_id)?.name ?? v.asset_id.slice(0, 8) + '…',
          },
          { id: 'role', header: 'Role', cell: (v) => <Badge>{v.role}</Badge>, width: 100 },
          {
            id: 'loopback', header: 'Loopback',
            cell: (v) => <span style={{ fontFamily: 'ui-monospace, monospace' }}>{v.loopback_ip ?? '—'}</span>,
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (v: Vtep) => (
              <Button iconName="remove" variant="inline-icon" onClick={() => remove(v)} ariaLabel="Delete VTEP" />
            ),
            width: 60,
          }] : []),
        ]}
        empty={<Box textAlign="center" color="inherit" padding="m">No VTEPs yet.</Box>}
      />
      {canWrite && (
        <Modal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header="New VTEP"
          size="medium"
        >
          <VtepForm
            overlayId={overlayId}
            assets={assets}
            onSaved={async () => {
              setCreateOpen(false);
              await qc.invalidateQueries({ queryKey: ['vteps-for-overlay', overlayId] });
            }}
          />
        </Modal>
      )}
    </>
  );
}

function VtepForm({
  overlayId, assets, onSaved,
}: { overlayId: string; assets: Asset[]; onSaved: () => void }) {
  const form = useForm<z.infer<typeof vtepSchema>>({
    resolver: zodResolver(vtepSchema),
    defaultValues: { asset_id: '', loopback_ip: '', role: 'leaf', description: '' },
  });
  async function onSubmit(v: z.infer<typeof vtepSchema>) {
    try {
      await http.post('/ipam/vteps', {
        overlay_id: overlayId,
        asset_id: v.asset_id,
        loopback_ip: v.loopback_ip || null,
        role: v.role,
        description: v.description || null,
      });
      toast.success('VTEP created');
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }
  const assetOptions: SelectProps.Option[] =
    assets.map((a) => ({ value: a.id, label: a.name }));
  const roleOptions: SelectProps.Option[] = (['leaf', 'spine', 'border', 'other'] as const).map((r) => ({ value: r, label: r }));
  const assetV = form.watch('asset_id') ?? '';
  const roleV = form.watch('role') ?? 'leaf';
  const loopV = form.watch('loopback_ip') ?? '';
  const descV = form.watch('description') ?? '';

  return (
    <form onSubmit={form.handleSubmit(onSubmit)}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={form.formState.isSubmitting}>
            {form.formState.isSubmitting ? 'Saving…' : 'Create'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField label="Asset" errorText={form.formState.errors.asset_id?.message as string | undefined}>
            <Select
              placeholder="Pick an asset"
              selectedOption={assetOptions.find((o) => o.value === assetV) ?? null}
              onChange={({ detail }) => form.setValue('asset_id', detail.selectedOption.value!)}
              options={assetOptions}
              expandToViewport
            />
          </FormField>
          <ColumnLayout columns={2}>
            <FormField label="Role">
              <Select
                selectedOption={roleOptions.find((o) => o.value === roleV) ?? roleOptions[0]}
                onChange={({ detail }) => form.setValue('role', detail.selectedOption.value as 'leaf' | 'spine' | 'border' | 'other')}
                options={roleOptions}
                expandToViewport
              />
            </FormField>
            <FormField label="Loopback IP (optional)">
              <Input value={loopV} onChange={({ detail }) => form.setValue('loopback_ip', detail.value)} />
            </FormField>
          </ColumnLayout>
          <FormField label="Description">
            <Input value={descV} onChange={({ detail }) => form.setValue('description', detail.value)} />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}
