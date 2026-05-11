import { Fragment, useEffect, useMemo, useRef, useState } from 'react';
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
  Plus, Pencil, Trash2, GitBranch, ChevronRight, ChevronDown, Send,
  LayoutGrid, List, Search,
} from 'lucide-react';
import { http } from '@/lib/http';
import { formatDate } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import { CapacityBar } from '@/components/capacity-bar';
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table';
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger,
} from '@/components/ui/dialog';
import {
  Form, FormControl, FormField, FormItem, FormLabel, FormMessage,
} from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { toast } from 'sonner';
import { DnsTab } from '@/components/dns-tab';
// Aliased so the inner FreeSpaceTab + AddressesTab can keep using
// shadcn Tabs (their grid/table + in-subnets/prefixes mode toggles
// migrate later) while the outer IPAM chrome runs on Cloudscape.
import CsTabs from '@cloudscape-design/components/tabs';
import ContentLayout from '@cloudscape-design/components/content-layout';
import CsHeader from '@cloudscape-design/components/header';
import CsBox from '@cloudscape-design/components/box';
// Tab-body Cloudscape primitives. Aliased so they don't collide with
// the shadcn Button/Table/Badge/Modal-equivalents still in use by the
// other tabs.
import CsTable from '@cloudscape-design/components/table';
import CsButton from '@cloudscape-design/components/button';
import CsStatusIndicator from '@cloudscape-design/components/status-indicator';
import CsModal from '@cloudscape-design/components/modal';
import CsSpaceBetween from '@cloudscape-design/components/space-between';
import CsSelect, { SelectProps as CsSelectProps } from '@cloudscape-design/components/select';
import CsInput from '@cloudscape-design/components/input';
import CsFormField from '@cloudscape-design/components/form-field';
import CsContainer from '@cloudscape-design/components/container';
import CsColumnLayout from '@cloudscape-design/components/column-layout';
import CsSegmentedControl from '@cloudscape-design/components/segmented-control';
import CsBadge from '@cloudscape-design/components/badge';
import CsBreadcrumbGroup from '@cloudscape-design/components/breadcrumb-group';

type Fabric = {
  id: string; name: string; slug: string; description: string | null;
  enclave: string | null; classification: string | null;
};
type Vrf = {
  id: string; fabric_id: string; name: string; rd: string | null;
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
      <ContentLayout header={<CsHeader variant="h1">IPAM</CsHeader>}>
        <CsBox color="text-status-inactive">
          You don't have <code style={{ fontFamily: 'ui-monospace, monospace' }}>inventory:read</code>.
        </CsBox>
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
      <div className="mt-3">
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
        <CsHeader
          variant="h1"
          description="Fabric → VRF → Supernet → Subnet → IP — DHCP leases ingested from Kea"
        >
          IPAM
        </CsHeader>
      }
    >
      <CsTabs
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

const ALL_FABRICS_OPT: CsSelectProps.Option = { value: '__all__', label: 'All fabrics' };
const FAMILY_OPTS: CsSelectProps.Option[] = [
  { value: 'v4', label: 'IPv4' },
  { value: 'v6', label: 'IPv6' },
];

function FreeSpaceTab({ onSelectSubnet }: { onSelectSubnet: (id: string) => void }) {
  const [mode, setMode] = useState<FreeMode>('in-subnets');
  const [fabricOpt, setFabricOpt] = useState<CsSelectProps.Option>(ALL_FABRICS_OPT);
  const [familyOpt, setFamilyOpt] = useState<CsSelectProps.Option>(FAMILY_OPTS[0]);
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

  const fabricOptions: CsSelectProps.Option[] = [
    ALL_FABRICS_OPT,
    ...fabrics.map((f) => ({ value: f.id, label: f.name })),
  ];

  return (
    <CsSpaceBetween size="l">
      <CsContainer header={<CsHeader variant="h2">Search</CsHeader>}>
        <CsSpaceBetween size="m">
          <CsFormField label="Mode">
            <CsSegmentedControl
              selectedId={mode}
              onChange={({ detail }) => setMode(detail.selectedId as FreeMode)}
              options={[
                { id: 'in-subnets', text: 'Free addresses in existing subnets' },
                { id: 'prefixes', text: 'Free prefixes inside supernets' },
              ]}
            />
          </CsFormField>
          <CsColumnLayout columns={3}>
            <CsFormField label="Fabric">
              <CsSelect
                selectedOption={fabricOpt}
                onChange={({ detail }) => setFabricOpt(detail.selectedOption)}
                options={fabricOptions}
                expandToViewport
              />
            </CsFormField>
            <CsFormField label="Family">
              <CsSelect
                selectedOption={familyOpt}
                onChange={({ detail }) => setFamilyOpt(detail.selectedOption)}
                options={FAMILY_OPTS}
                expandToViewport
              />
            </CsFormField>
            {mode === 'in-subnets' ? (
              <CsFormField label="Min free addresses">
                <CsInput
                  type="number"
                  value={minFree}
                  onChange={({ detail }) => setMinFree(detail.value)}
                />
              </CsFormField>
            ) : (
              <CsFormField
                label="Prefix size"
                description="e.g. 24 for /24, 64 for /64"
              >
                <CsInput
                  type="number"
                  value={prefixSize}
                  onChange={({ detail }) => setPrefixSize(detail.value)}
                />
              </CsFormField>
            )}
          </CsColumnLayout>
        </CsSpaceBetween>
      </CsContainer>

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
    </CsSpaceBetween>
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
    <CsTable<SubnetFreeRow>
      variant="container"
      loading={isLoading}
      loadingText="Searching subnets…"
      items={rows}
      trackBy="subnet_id"
      header={<CsHeader counter={`(${rows.length})`}>Subnets with free space</CsHeader>}
      onRowClick={({ detail }) => onSelectSubnet(detail.item.subnet_id)}
      columnDefinitions={[
        {
          id: 'subnet', header: 'Subnet',
          cell: (r) => (
            <span style={{ fontFamily: 'ui-monospace, monospace' }}>
              {r.prefix}
              {r.name && (
                <CsBox variant="span" color="text-status-inactive"> · {r.name}</CsBox>
              )}
            </span>
          ),
        },
        {
          id: 'purpose', header: 'Purpose',
          cell: (r) => r.purpose ? <CsBadge>{r.purpose}</CsBadge> : '—',
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
        <CsBox textAlign="center" color="inherit" padding="m">
          No subnets meet that filter.
        </CsBox>
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
      <CsContainer><CsBox padding="m">Searching supernets…</CsBox></CsContainer>
    );
  }
  if (groups.length === 0) {
    return (
      <CsContainer>
        <CsBox padding="m" color="text-status-inactive">
          No supernet has free space at that prefix size in this scope.
        </CsBox>
      </CsContainer>
    );
  }
  // One Container per supernet, each with its prefix + a flat list of
  // candidate Badges. Container's header carries the supernet identity
  // and a counter of candidates.
  return (
    <CsSpaceBetween size="s">
      {groups.map((g) => (
        <CsContainer
          key={g.supernet_id}
          header={
            <CsHeader
              variant="h3"
              counter={`(${g.count})`}
              description={
                [g.supernet_name ?? 'unnamed', g.purpose].filter(Boolean).join(' · ')
              }
            >
              <span style={{ fontFamily: 'ui-monospace, monospace' }}>{g.supernet_prefix}</span>
            </CsHeader>
          }
        >
          <div className="flex flex-wrap gap-1.5">
            {g.candidates.map((c) => (
              <CsBadge key={c}>{c}</CsBadge>
            ))}
          </div>
        </CsContainer>
      ))}
    </CsSpaceBetween>
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
    <CsBreadcrumbGroup
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
      <CsTable<Fabric>
        variant="container"
        loading={tableQuery.isLoading}
        loadingText="Loading fabrics…"
        items={data}
        trackBy="id"
        onRowClick={({ detail }) => onSelect(detail.item.id)}
        header={
          <CsHeader
            counter={`(${data.length})`}
            actions={
              canWrite && (
                <CsButton variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                  New fabric
                </CsButton>
              )
            }
          >
            Fabrics
          </CsHeader>
        }
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (f) => <span style={{ fontWeight: 500 }}>{f.name}</span> },
          {
            id: 'slug', header: 'Slug',
            cell: (f) => <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{f.slug}</span>,
            width: 140,
          },
          { id: 'enclave', header: 'Enclave', cell: (f) => f.enclave ?? '—', width: 140 },
          { id: 'classification', header: 'Classification', cell: (f) => f.classification ?? '—', width: 160 },
          {
            id: 'description', header: 'Description',
            cell: (f) => (
              <CsBox variant="span" color="text-status-inactive" fontSize="body-s">
                {f.description ?? '—'}
              </CsBox>
            ),
          },
        ]}
        empty={<CsBox textAlign="center" color="inherit" padding="m">No fabrics yet.</CsBox>}
      />
      {canWrite && (
        <CsModal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header="New fabric"
          size="medium"
        >
          <FabricForm onSaved={async () => { setCreateOpen(false); await tableQuery.refetch(); }} />
        </CsModal>
      )}
    </>
  );
}

function FabricForm({ onSaved }: { onSaved: () => void }) {
  const form = useForm<z.infer<typeof fabricSchema>>({
    resolver: zodResolver(fabricSchema),
    defaultValues: { name: '', slug: '', description: '', enclave: '', classification: '' },
  });
  async function onSubmit(v: z.infer<typeof fabricSchema>) {
    try {
      await http.post('/ipam/fabrics', {
        name: v.name, slug: v.slug,
        description: v.description || null,
        enclave: v.enclave || null,
        classification: v.classification || null,
      });
      toast.success('Fabric created (with default VRF)');
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="name" render={({ field }) => (
          <FormItem><FormLabel>Name</FormLabel><FormControl><Input placeholder="e.g. Production" {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <FormField control={form.control} name="slug" render={({ field }) => (
          <FormItem><FormLabel>Slug</FormLabel><FormControl><Input placeholder="prod" className="font-mono" {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="enclave" render={({ field }) => (
            <FormItem><FormLabel>Enclave</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
          )} />
          <FormField control={form.control} name="classification" render={({ field }) => (
            <FormItem><FormLabel>Classification</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
          )} />
        </div>
        <FormField control={form.control} name="description" render={({ field }) => (
          <FormItem><FormLabel>Description</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : 'Create'}
        </Button>
      </form>
    </Form>
  );
}

// ----------------------- VRFs -----------------------

const vrfSchema = z.object({
  name: z.string().min(1),
  rd: z.string().optional(),
  description: z.string().optional(),
});

function VrfsTab({
  fabricId, onSelect, canWrite,
}: {
  fabricId: string;
  onSelect: (id: string) => void;
  canWrite: boolean;
}) {
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
      <CsTable<Vrf>
        variant="container"
        loading={tableQuery.isLoading}
        loadingText="Loading VRFs…"
        items={data}
        trackBy="id"
        onRowClick={({ detail }) => onSelect(detail.item.id)}
        header={
          <CsHeader
            counter={`(${data.length})`}
            actions={
              canWrite && (
                <CsButton variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                  New VRF
                </CsButton>
              )
            }
          >
            VRFs
          </CsHeader>
        }
        columnDefinitions={[
          {
            id: 'name', header: 'Name',
            cell: (v) => (
              <span style={{ fontWeight: 500, display: 'inline-flex', alignItems: 'center', gap: 8 }}>
                <GitBranch className="h-3.5 w-3.5 text-muted-foreground" />
                {v.name}
              </span>
            ),
          },
          {
            id: 'rd', header: 'RD',
            cell: (v) => <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{v.rd ?? '—'}</span>,
            width: 160,
          },
          {
            id: 'default', header: 'Default',
            cell: (v) => v.is_default ? <CsBadge>default</CsBadge> : '—',
            width: 100,
          },
          {
            id: 'description', header: 'Description',
            cell: (v) => (
              <CsBox variant="span" color="text-status-inactive" fontSize="body-s">
                {v.description ?? '—'}
              </CsBox>
            ),
          },
        ]}
        empty={<CsBox textAlign="center" color="inherit" padding="m">No VRFs yet.</CsBox>}
      />
      {canWrite && (
        <CsModal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header="New VRF"
          size="medium"
        >
          <VrfForm fabricId={fabricId} onSaved={async () => { setCreateOpen(false); await tableQuery.refetch(); }} />
        </CsModal>
      )}
    </>
  );
}

function VrfForm({ fabricId, onSaved }: { fabricId: string; onSaved: () => void }) {
  const form = useForm<z.infer<typeof vrfSchema>>({
    resolver: zodResolver(vrfSchema),
    defaultValues: { name: '', rd: '', description: '' },
  });
  async function onSubmit(v: z.infer<typeof vrfSchema>) {
    try {
      await http.post('/ipam/vrfs', {
        fabric_id: fabricId,
        name: v.name,
        rd: v.rd || null,
        description: v.description || null,
        is_default: false,
      });
      toast.success('VRF created');
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="name" render={({ field }) => (
          <FormItem><FormLabel>Name</FormLabel><FormControl><Input placeholder="e.g. mgmt" {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <FormField control={form.control} name="rd" render={({ field }) => (
          <FormItem><FormLabel>Route distinguisher (optional)</FormLabel><FormControl><Input placeholder="e.g. 65000:100" className="font-mono" {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <FormField control={form.control} name="description" render={({ field }) => (
          <FormItem><FormLabel>Description</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : 'Create'}
        </Button>
      </form>
    </Form>
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
    <div className="space-y-3">
      <div className="flex justify-end">
        {canWrite && (
          <Dialog open={createSupernetOpen} onOpenChange={setCreateSupernetOpen}>
            <DialogTrigger asChild>
              <Button><Plus className="h-4 w-4" /> New supernet</Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader><DialogTitle>New supernet</DialogTitle></DialogHeader>
              <SupernetForm
                fabricId={fabricId} vrfId={vrfId}
                sites={sites}
                onSaved={async () => {
                  setCreateSupernetOpen(false);
                  await tableQuery.refetch();
                  await qc.invalidateQueries({ queryKey: ['supernet-util'] });
                }}
              />
            </DialogContent>
          </Dialog>
        )}
      </div>

      <DndContext
        sensors={sensors}
        onDragStart={onDragStart}
        onDragOver={onDragOver}
        onDragEnd={onDragEnd}
        onDragCancel={() => { setDraggingSubnet(null); setHoverTargetId(null); clearHoverExpand(); }}
      >
      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-8" />
                <TableHead>Prefix</TableHead>
                <TableHead>Name</TableHead>
                <TableHead>Purpose</TableHead>
                <TableHead className="w-64">Utilization</TableHead>
                {canWrite && <TableHead className="w-12" />}
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.length === 0 && (
                <TableRow>
                  <TableCell colSpan={canWrite ? 6 : 5} className="text-muted-foreground">
                    No supernets yet.
                  </TableCell>
                </TableRow>
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
            </TableBody>
          </Table>
          {tableQuery.isLoading && (
            <div className="space-y-2 p-4">
              {Array.from({ length: 2 }).map((_, i) => <Skeleton key={`s-${i}`} className="h-9 w-full" />)}
            </div>
          )}
        </CardContent>
      </Card>
      <DragOverlay dropAnimation={{ duration: 150 }}>
        {draggingSubnet ? (
          <div className="rounded-md border bg-background px-3 py-2 text-sm shadow-lg">
            <span className="font-mono font-medium">{draggingSubnet.prefix}</span>
            {draggingSubnet.name && <span className="ml-2 text-muted-foreground">{draggingSubnet.name}</span>}
            <span className="ml-2 text-xs text-muted-foreground">drop on a supernet to move</span>
          </div>
        ) : null}
      </DragOverlay>
      </DndContext>

      <Dialog
        open={createSubnetFor !== null}
        onOpenChange={(o) => { if (!o) setCreateSubnetFor(null); }}
      >
        <DialogContent>
          <DialogHeader><DialogTitle>New subnet</DialogTitle></DialogHeader>
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
        </DialogContent>
      </Dialog>

      <Dialog
        open={editSupernet !== null}
        onOpenChange={(o) => { if (!o) setEditSupernet(null); }}
      >
        <DialogContent>
          <DialogHeader><DialogTitle>Edit supernet</DialogTitle></DialogHeader>
          {editSupernet && (
            <SupernetForm
              fabricId={fabricId} vrfId={vrfId} supernet={editSupernet}
              sites={sites}
              onSaved={async () => { setEditSupernet(null); await refreshSupernets(); }}
            />
          )}
        </DialogContent>
      </Dialog>

      <Dialog
        open={createSupernetUnder !== null}
        onOpenChange={(o) => { if (!o) setCreateSupernetUnder(null); }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>
              New supernet inside {createSupernetUnder?.prefix}
            </DialogTitle>
          </DialogHeader>
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
        </DialogContent>
      </Dialog>

      <Dialog
        open={editSubnet !== null}
        onOpenChange={(o) => { if (!o) setEditSubnet(null); }}
      >
        <DialogContent>
          <DialogHeader><DialogTitle>Edit subnet</DialogTitle></DialogHeader>
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
        </DialogContent>
      </Dialog>
    </div>
  );
}


function SupernetNode({
  supernet, depth, expanded, onToggle, sitesById, canWrite,
  draggingSubnet, hoverTargetId,
  onSelectSubnet, onAddSubnet, onAddChildSupernet, onEditSupernet, onEditSubnet,
}: {
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
}) {
  const isOpen = expanded.has(supernet.id);
  // Lazy-load child supernets only once the row is expanded — keeps the
  // initial page render to a single query and avoids fanning out into
  // every nested supernet on mount.
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

  // Drop target wiring. We always register so empty supernets can accept
  // a drag; the visual ring + accept decision use the validity check.
  const { setNodeRef: setDropRef } = useDroppable({ id: `supernet:${supernet.id}` });

  // A drop is "valid" iff the subnet would still fit and isn't already
  // here. Purpose mismatches are surfaced by the backend (we'd need to
  // know the whole purpose chain client-side to forecast it perfectly).
  const isDragging = draggingSubnet !== null;
  const isOver = hoverTargetId === supernet.id;
  const isSelf = isDragging && draggingSubnet?.supernet_id === supernet.id;
  const fits = isDragging && draggingSubnet
    ? cidrContains(supernet.prefix, draggingSubnet.prefix)
    : false;
  const isValidTarget = isDragging && !isSelf && fits;

  let ringClass = '';
  if (isOver && isValidTarget) ringClass = 'ring-2 ring-emerald-500 ring-inset bg-emerald-500/5';
  else if (isOver && isDragging) ringClass = 'ring-2 ring-rose-500 ring-inset bg-rose-500/5';
  else if (isValidTarget) ringClass = 'ring-1 ring-emerald-500/40 ring-inset';

  return (
    <Fragment>
      <TableRow
        ref={setDropRef}
        className={`cursor-pointer hover:bg-accent/40 ${ringClass}`}
        onClick={() => onToggle(supernet.id)}
      >
        <TableCell className="text-muted-foreground">
          {isOpen
            ? <ChevronDown className="h-4 w-4" />
            : <ChevronRight className="h-4 w-4" />}
        </TableCell>
        <TableCell className="font-mono font-medium" style={{ paddingLeft: 16 + indent }}>
          {depth > 0 && <span className="text-muted-foreground">└─ </span>}
          {supernet.prefix}
        </TableCell>
        <TableCell>
          <div>{supernet.name ?? '—'}</div>
          {sitePill && (
            <div className="text-xs text-muted-foreground">{sitePill}</div>
          )}
        </TableCell>
        <TableCell>
          {supernet.purpose ? <Badge variant="secondary">{supernet.purpose}</Badge> : '—'}
        </TableCell>
        <TableCell><SupernetUtilCell supernetId={supernet.id} /></TableCell>
        {canWrite && (
          <TableCell onClick={(e) => e.stopPropagation()}>
            <Button size="sm" variant="ghost" onClick={() => onEditSupernet(supernet)} title="Edit supernet">
              <Pencil className="h-3.5 w-3.5" />
            </Button>
          </TableCell>
        )}
      </TableRow>
      {isOpen && (
        <>
          {childrenQ.isLoading && (
            <TableRow>
              <TableCell />
              <TableCell colSpan={branchSpan} style={{ paddingLeft: 24 + indent }}>
                <Skeleton className="h-6 w-full" />
              </TableCell>
            </TableRow>
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
}: {
  supernetId: string;
  depth: number;
  parentPurpose: string | null;
  sitesById: Map<string, Site>;
  canWrite: boolean;
  /** When a subnet is being dragged, hide its row in the source branch so
   * the user doesn't see two copies (the original + the DragOverlay). */
  draggingSubnetId: string | null;
  onSelectSubnet: (subnetId: string) => void;
  onAddSubnet: () => void;
  onAddChildSupernet: () => void;
  onEditSubnet: (subnet: Subnet) => void;
}) {
  const { data, isLoading } = useQuery({
    queryKey: ['subnets-for-supernet', supernetId],
    queryFn: async () => (
      await http.get<{ items: Subnet[] }>(`/ipam/subnets?supernet_id=${supernetId}&page_size=200`)
    ).data.items ?? [],
  });

  // The branch's column count matches the parent table — chevron, prefix,
  // name, purpose, utilization, and the edit-button column when canWrite.
  const branchSpan = canWrite ? 5 : 4;
  const indent = depth * 16;

  if (isLoading) {
    return (
      <TableRow>
        <TableCell />
        <TableCell colSpan={branchSpan} style={{ paddingLeft: 16 + indent }}>
          <Skeleton className="h-6 w-full" />
        </TableCell>
      </TableRow>
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
        <TableRow className="bg-muted/20">
          <TableCell />
          <TableCell colSpan={branchSpan} style={{ paddingLeft: 16 + indent }}>
            <Button size="sm" variant="ghost" onClick={onAddSubnet}>
              <Plus className="h-3.5 w-3.5" /> Add subnet here
              {parentPurpose && <span className="ml-1 text-[10px] text-muted-foreground">({parentPurpose})</span>}
            </Button>
            <Button size="sm" variant="ghost" className="ml-2" onClick={onAddChildSupernet}>
              <Plus className="h-3.5 w-3.5" /> Add child supernet
            </Button>
          </TableCell>
        </TableRow>
      )}
    </>
  );
}

function SubnetRow({
  subnet: s, indent, canWrite, isDraggingThis, sitesById,
  onSelectSubnet, onEditSubnet,
}: {
  subnet: Subnet;
  indent: number;
  canWrite: boolean;
  isDraggingThis: boolean;
  sitesById: Map<string, Site>;
  onSelectSubnet: (subnetId: string) => void;
  onEditSubnet: (subnet: Subnet) => void;
}) {
  // useDraggable wires this row up to the page-level DndContext. We pass
  // the subnet object via `data` so the drag handlers don't have to look
  // it up again on drop.
  const { attributes, listeners, setNodeRef, isDragging } = useDraggable({
    id: `subnet:${s.id}`,
    data: { subnet: s },
  });
  // Source row stays in place; the dnd-kit DragOverlay handles the floating
  // preview. We just dim the row so the user sees what's being moved.
  const dragClass = isDragging || isDraggingThis ? 'opacity-30' : '';
  return (
    <TableRow
      ref={setNodeRef}
      {...attributes}
      {...listeners}
      className={`cursor-pointer bg-muted/20 hover:bg-accent/40 ${dragClass}`}
      onClick={() => onSelectSubnet(s.id)}
    >
      <TableCell />
      <TableCell className="font-mono" style={{ paddingLeft: 16 + indent }}>
        <span className="text-muted-foreground">└─</span> {s.prefix}
      </TableCell>
      <TableCell className="text-sm">
        <div>{s.name ?? '—'}</div>
        <div className="text-xs text-muted-foreground">
          {s.site_id
            ? `site: ${sitesById.get(s.site_id)?.code ?? s.site_id.slice(0, 8) + '…'}`
            : 'unassigned'}
          {s.vlan_id ? ` · vlan ${s.vlan_id}` : ''}
          {s.gateway ? ` · gw ${s.gateway}` : ''}
          {s.vni_id ? ` · vni ${s.vni_id.slice(0, 8)}…` : ''}
        </div>
      </TableCell>
      <TableCell>
        {s.purpose ? <Badge variant="secondary">{s.purpose}</Badge> : '—'}
      </TableCell>
      <TableCell><SubnetUtilCell subnetId={s.id} /></TableCell>
      {canWrite && (
        <TableCell onClick={(e) => e.stopPropagation()}>
          <Button size="sm" variant="ghost" onClick={() => onEditSubnet(s)} title="Edit subnet">
            <Pencil className="h-3.5 w-3.5" />
          </Button>
        </TableCell>
      )}
    </TableRow>
  );
}

function SupernetUtilCell({ supernetId }: { supernetId: string }) {
  const { data } = useQuery({
    queryKey: ['supernet-util', supernetId],
    queryFn: async () => (await http.get<SupernetUtil>(`/ipam/supernets/${supernetId}/utilization`)).data,
  });
  if (!data) return <Skeleton className="h-4 w-full" />;
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
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        {parent && !editing && (
          <p className="rounded-md border bg-muted/30 px-3 py-2 text-xs text-muted-foreground">
            Carving inside <span className="font-mono">{parent.prefix}</span>.
            Prefix must fit inside the parent.
          </p>
        )}
        <FormField control={form.control} name="prefix" render={({ field }) => (
          <FormItem>
            <FormLabel>Prefix (CIDR)</FormLabel>
            <FormControl>
              <Input
                placeholder="e.g. 10.0.0.0/8 or 2001:db8::/32"
                className="font-mono"
                disabled={editing}
                {...field}
              />
            </FormControl>
            {editing && (
              <p className="text-xs text-muted-foreground">
                Prefix is immutable after creation. Delete + recreate to change it.
              </p>
            )}
            <FormMessage />
          </FormItem>
        )} />
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="name" render={({ field }) => (
            <FormItem><FormLabel>Name</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
          )} />
          <FormField control={form.control} name="site_id" render={({ field }) => (
            <FormItem>
              <FormLabel>Site (optional)</FormLabel>
              <Select value={field.value ?? NONE} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
                <SelectContent>
                  <SelectItem value={NONE}>(unassigned)</SelectItem>
                  {sites.map((s) => (
                    <SelectItem key={s.id} value={s.id}>{s.code} · {s.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <FormMessage />
            </FormItem>
          )} />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="purpose" render={({ field }) => (
            <FormItem>
              <FormLabel>Purpose</FormLabel>
              <Select
                value={field.value}
                onValueChange={field.onChange}
                disabled={purposeLocked}
              >
                <FormControl><SelectTrigger><SelectValue placeholder="—" /></SelectTrigger></FormControl>
                <SelectContent>
                  <SelectItem value={PURPOSE_NONE}>(unset)</SelectItem>
                  {PURPOSES.map((p) => <SelectItem key={p} value={p}>{p}</SelectItem>)}
                </SelectContent>
              </Select>
              {purposeLocked && (
                <p className="text-xs text-muted-foreground">
                  Locked to <span className="font-mono">{parent?.purpose}</span> — parent's purpose.
                </p>
              )}
              {editing && !purposeLocked && (
                <p className="text-xs text-muted-foreground">
                  Setting a purpose locks every subnet under this supernet to the same purpose.
                </p>
              )}
              <FormMessage />
            </FormItem>
          )} />
          <FormField control={form.control} name="description" render={({ field }) => (
            <FormItem><FormLabel>Description</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
          )} />
        </div>
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : editing ? 'Save' : 'Create'}
        </Button>
      </form>
    </Form>
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
  if (!data) return <Skeleton className="h-4 w-full" />;
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
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="prefix" render={({ field }) => (
          <FormItem>
            <FormLabel>Prefix (CIDR, must be inside the supernet)</FormLabel>
            <FormControl>
              <Input
                placeholder="e.g. 10.0.5.0/24 or 2001:db8:1::/48"
                className="font-mono"
                disabled={editing}
                {...field}
              />
            </FormControl>
            {editing && (
              <p className="text-xs text-muted-foreground">
                Prefix is immutable after creation. Delete + recreate to change it.
              </p>
            )}
            <FormMessage />
          </FormItem>
        )} />
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="site_id" render={({ field }) => (
            <FormItem>
              <FormLabel>Site</FormLabel>
              <Select value={field.value} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
                <SelectContent>
                  <SelectItem value={NONE}>(unassigned)</SelectItem>
                  {sites.map((s) => (
                    <SelectItem key={s.id} value={s.id}>{s.code} · {s.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <FormMessage />
            </FormItem>
          )} />
          <FormField control={form.control} name="purpose" render={({ field }) => (
            <FormItem>
              <FormLabel>Purpose</FormLabel>
              <Select
                value={field.value}
                onValueChange={field.onChange}
                disabled={purposeLocked}
              >
                <FormControl><SelectTrigger><SelectValue placeholder="—" /></SelectTrigger></FormControl>
                <SelectContent>
                  <SelectItem value={PURPOSE_NONE}>(unset)</SelectItem>
                  {PURPOSES.map((p) => <SelectItem key={p} value={p}>{p}</SelectItem>)}
                </SelectContent>
              </Select>
              {purposeLocked && (
                <p className="text-xs text-muted-foreground">
                  Locked to <span className="font-mono">{parentPurpose}</span> — parent supernet's purpose.
                </p>
              )}
              <FormMessage />
            </FormItem>
          )} />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="vlan_id" render={({ field }) => (
            <FormItem><FormLabel>VLAN</FormLabel><FormControl><Input type="number" min={1} max={4094} {...field} /></FormControl><FormMessage /></FormItem>
          )} />
          <FormField control={form.control} name="gateway" render={({ field }) => (
            <FormItem><FormLabel>Gateway (optional)</FormLabel><FormControl><Input className="font-mono" {...field} /></FormControl><FormMessage /></FormItem>
          )} />
        </div>
        <FormField control={form.control} name="vni_id" render={({ field }) => (
          <FormItem>
            <FormLabel>L2 VNI (optional)</FormLabel>
            <Select value={field.value ?? NONE} onValueChange={field.onChange}>
              <FormControl><SelectTrigger><SelectValue placeholder="—" /></SelectTrigger></FormControl>
              <SelectContent>
                <SelectItem value={NONE}>(none)</SelectItem>
                {l2Vnis.map((v) => (
                  <SelectItem key={v.id} value={v.id}>
                    {v.vni}{v.name ? ` · ${v.name}` : ''}
                    {v.vlan_id ? ` · vlan ${v.vlan_id}` : ''}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">
              Bind this subnet to an L2 VNI to track which broadcast domain it rides.
              Only L2 VNIs in this fabric are eligible.
            </p>
            <FormMessage />
          </FormItem>
        )} />
        <FormField control={form.control} name="name" render={({ field }) => (
          <FormItem><FormLabel>Name (optional)</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : editing ? 'Save' : 'Create'}
        </Button>
      </form>
    </Form>
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

function AddressesTab({ subnetId, canWrite }: { subnetId: string; canWrite: boolean }) {
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
    if (!window.confirm(`Release ${ip.address}?`)) return;
    try {
      await http.delete(`/ipam/addresses/${ip.id}`);
      toast.success('Address released');
      await tableQuery.refetch();
      await qc.invalidateQueries({ queryKey: ['subnet-util', subnetId] });
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <div className="space-y-3">
      {util.data && (
        <Card>
          <CardContent className="space-y-2 p-4">
            <CapacityBar
              used={util.data.allocated} total={util.data.capacity}
              leftLabel={`${util.data.allocated}/${util.data.capacity} addresses allocated`}
            />
            <p className="text-xs text-muted-foreground">
              {util.data.next_available
                ? <>Next available: <span className="font-mono">{util.data.next_available}</span></>
                : 'Subnet is full'}
            </p>
          </CardContent>
        </Card>
      )}
      <div className="flex items-center justify-between gap-2">
        <Tabs value={view} onValueChange={(v) => setView(v as 'table' | 'grid')}>
          <TabsList>
            <TabsTrigger value="table"><List className="h-3.5 w-3.5" /> Table</TabsTrigger>
            <TabsTrigger value="grid"><LayoutGrid className="h-3.5 w-3.5" /> Grid</TabsTrigger>
          </TabsList>
        </Tabs>
        {canWrite && (
          <Dialog open={createOpen} onOpenChange={setCreateOpen}>
            <DialogTrigger asChild>
              <Button><Plus className="h-4 w-4" /> Allocate IP</Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader><DialogTitle>Allocate IP address</DialogTitle></DialogHeader>
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
            </DialogContent>
          </Dialog>
        )}
      </div>

      {view === 'grid' && util.data && (
        <IpGrid
          subnetPrefix={util.data.prefix}
          capacity={util.data.capacity}
          allocated={data}
          assetsById={assetsById}
        />
      )}

      {view === 'table' && (
      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Address</TableHead>
                <TableHead>Asset</TableHead>
                <TableHead>Role</TableHead>
                <TableHead>Source</TableHead>
                <TableHead>Status</TableHead>
                <TableHead>DNS</TableHead>
                <TableHead>Lease ends</TableHead>
                {canWrite && <TableHead className="w-12" />}
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.length === 0 && (
                <TableRow><TableCell colSpan={canWrite ? 8 : 7} className="text-muted-foreground">No allocations yet.</TableCell></TableRow>
              )}
              {data.map((ip) => (
                <TableRow key={ip.id}>
                  <TableCell className="font-mono">{ip.address}</TableCell>
                  <TableCell className="text-sm">
                    {ip.asset_id
                      ? (assetsById.get(ip.asset_id)?.name ?? ip.asset_id.slice(0, 8) + '…')
                      : <span className="text-muted-foreground">—</span>}
                  </TableCell>
                  <TableCell><Badge variant="secondary">{ip.role}</Badge></TableCell>
                  <TableCell>
                    <Badge variant={ip.source === 'dhcp' ? 'warning' : 'outline'}>{ip.source}</Badge>
                  </TableCell>
                  <TableCell>
                    <Badge variant={ip.status === 'active' ? 'success' : 'secondary'}>{ip.status}</Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground">{ip.dns_name ?? '—'}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {ip.dhcp_lease_expires_at ? formatDate(ip.dhcp_lease_expires_at) : '—'}
                  </TableCell>
                  {canWrite && (
                    <TableCell>
                      <Button size="sm" variant="ghost" onClick={() => remove(ip)} title="Release">
                        <Trash2 className="h-3.5 w-3.5" />
                      </Button>
                    </TableCell>
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
          {tableQuery.isLoading && (
            <div className="space-y-2 p-4">
              {Array.from({ length: 3 }).map((_, i) => <Skeleton key={`s-${i}`} className="h-9 w-full" />)}
            </div>
          )}
        </CardContent>
      </Card>
      )}
    </div>
  );
}


// Hard cap on grid cells. /24 (256) and /22 (1024) are reasonable; an IPv6
// /64 is 2^64 cells which obviously can't render, and even an IPv4 /20 at
// 4096 cells is too noisy to be useful.
const IP_GRID_MAX_CELLS = 1024;


function IpGrid({
  subnetPrefix, capacity, allocated, assetsById,
}: {
  subnetPrefix: string;
  capacity: number;
  allocated: IPAddr[];
  assetsById: Map<string, Asset>;
}) {
  if (capacity > IP_GRID_MAX_CELLS) {
    return (
      <Card>
        <CardContent className="p-6 text-sm text-muted-foreground">
          Grid view is hidden for prefixes larger than /22 (this subnet has{' '}
          <span className="font-mono">{capacity.toLocaleString()}</span> addresses).
          Use the table for now — search by address to find a specific allocation.
        </CardContent>
      </Card>
    );
  }
  const cells = useMemo(
    () => buildGridCells(subnetPrefix, capacity, allocated),
    [subnetPrefix, capacity, allocated],
  );
  if (cells.length === 0) {
    return (
      <Card>
        <CardContent className="p-4 text-sm text-muted-foreground">
          Couldn't render this prefix as a grid (unparseable CIDR).
        </CardContent>
      </Card>
    );
  }
  return (
    <Card>
      <CardContent className="space-y-3 p-4">
        <div className="flex items-center gap-3 text-xs text-muted-foreground">
          <Legend swatch="bg-muted" label="free" />
          <Legend swatch="bg-primary" label="static" />
          <Legend swatch="bg-warning" label="dhcp" />
          <Legend swatch="bg-secondary" label="reservation" />
          <Legend swatch="bg-muted-foreground/30" label="deprecated" />
        </div>
        <div
          className="grid gap-0.5"
          style={{ gridTemplateColumns: 'repeat(32, minmax(0, 1fr))' }}
        >
          {cells.map((cell) => (
            <IpCell key={cell.address} cell={cell} assetsById={assetsById} />
          ))}
        </div>
        <p className="text-[10px] text-muted-foreground">
          Hover a cell for details · {allocated.length} of {capacity} allocated
        </p>
      </CardContent>
    </Card>
  );
}

function Legend({ swatch, label }: { swatch: string; label: string }) {
  return (
    <span className="flex items-center gap-1.5">
      <span className={`inline-block h-3 w-3 rounded-sm ${swatch}`} />
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
}: {
  cell: IpCellInfo;
  assetsById: Map<string, Asset>;
}) {
  const ip = cell.ip;
  const tone = cellTone(ip);
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
      className={`aspect-square rounded-sm ${tone}`}
    />
  );
}

function cellTone(ip: IPAddr | null): string {
  if (!ip) return 'bg-muted hover:bg-muted-foreground/20';
  if (ip.status === 'deprecated') return 'bg-muted-foreground/30';
  if (ip.source === 'dhcp') return 'bg-warning hover:bg-warning/80';
  if (ip.source === 'reservation') return 'bg-secondary hover:bg-secondary/80';
  return 'bg-primary hover:bg-primary/80';
}


function IpForm({
  subnetId, suggestedAddress, assets, onSaved,
}: {
  subnetId: string;
  suggestedAddress: string;
  assets: Asset[];
  onSaved: () => void;
}) {
  const NONE = '__none__';
  const form = useForm<z.infer<typeof ipSchema>>({
    resolver: zodResolver(ipSchema),
    defaultValues: {
      address: suggestedAddress,
      asset_id: NONE,
      role: 'data',
      status: 'active',
      source: 'static',
      dns_name: '',
      description: '',
    },
  });
  async function onSubmit(v: z.infer<typeof ipSchema>) {
    try {
      await http.post('/ipam/addresses', {
        subnet_id: subnetId,
        address: v.address,
        asset_id: v.asset_id === NONE ? null : v.asset_id,
        role: v.role,
        status: v.status,
        source: v.source,
        dns_name: v.dns_name || null,
        description: v.description || null,
      });
      toast.success('IP allocated');
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="address" render={({ field }) => (
          <FormItem>
            <FormLabel>Address</FormLabel>
            <FormControl><Input placeholder="e.g. 10.0.5.42" className="font-mono" {...field} /></FormControl>
            <FormMessage />
          </FormItem>
        )} />
        <FormField control={form.control} name="asset_id" render={({ field }) => (
          <FormItem>
            <FormLabel>Bound asset (optional)</FormLabel>
            <Select value={field.value} onValueChange={field.onChange}>
              <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
              <SelectContent>
                <SelectItem value={NONE}>(reservation / unbound)</SelectItem>
                {assets.slice(0, 200).map((a) => (
                  <SelectItem key={a.id} value={a.id}>{a.name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <FormMessage />
          </FormItem>
        )} />
        <div className="grid grid-cols-3 gap-3">
          <FormField control={form.control} name="role" render={({ field }) => (
            <FormItem>
              <FormLabel>Role</FormLabel>
              <Select value={field.value} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
                <SelectContent>{ROLES.map((r) => <SelectItem key={r} value={r}>{r}</SelectItem>)}</SelectContent>
              </Select>
              <FormMessage />
            </FormItem>
          )} />
          <FormField control={form.control} name="source" render={({ field }) => (
            <FormItem>
              <FormLabel>Source</FormLabel>
              <Select value={field.value} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
                <SelectContent>{SOURCES.map((r) => <SelectItem key={r} value={r}>{r}</SelectItem>)}</SelectContent>
              </Select>
              <FormMessage />
            </FormItem>
          )} />
          <FormField control={form.control} name="status" render={({ field }) => (
            <FormItem>
              <FormLabel>Status</FormLabel>
              <Select value={field.value} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
                <SelectContent>{STATUSES.map((r) => <SelectItem key={r} value={r}>{r}</SelectItem>)}</SelectContent>
              </Select>
              <FormMessage />
            </FormItem>
          )} />
        </div>
        <FormField control={form.control} name="dns_name" render={({ field }) => (
          <FormItem><FormLabel>DNS name (optional)</FormLabel><FormControl><Input className="font-mono" {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : 'Allocate'}
        </Button>
      </form>
    </Form>
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
      <CsTable<DhcpServer>
        variant="container"
        loading={tableQuery.isLoading}
        loadingText="Loading DHCP servers…"
        items={data}
        trackBy="id"
        header={
          <CsHeader
            counter={`(${data.length})`}
            actions={
              canWrite && (
                <CsButton
                  variant="primary"
                  iconName="add-plus"
                  onClick={() => setCreateOpen(true)}
                >
                  Add Kea server
                </CsButton>
              )
            }
          >
            DHCP servers
          </CsHeader>
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
              <CsBox variant="span" color="text-status-inactive" fontSize="body-s">
                {s.last_sync_at ? formatDate(s.last_sync_at) : 'never'}
              </CsBox>
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
              <CsSpaceBetween size="xxs" direction="horizontal">
                {s.last_sync_status === 'ok' && <CsStatusIndicator type="success">ok</CsStatusIndicator>}
                {s.last_sync_status === 'error' && <CsStatusIndicator type="error">error</CsStatusIndicator>}
                {!s.last_sync_status && <CsStatusIndicator type="pending">pending</CsStatusIndicator>}
                {!s.enabled && <CsStatusIndicator type="stopped">disabled</CsStatusIndicator>}
              </CsSpaceBetween>
            ),
            width: 180,
          },
          ...(canWrite ? [{
            id: 'actions',
            header: '',
            // Inline row actions: sync triggers a synchronous /sync call
            // and refreshes; delete confirms then removes.
            cell: (s: DhcpServer) => (
              <CsSpaceBetween size="xxs" direction="horizontal">
                <CsButton iconName="upload" variant="inline-icon" onClick={() => syncNow(s)} ariaLabel={`Sync ${s.name}`} />
                <CsButton iconName="remove" variant="inline-icon" onClick={() => remove(s)} ariaLabel={`Delete ${s.name}`} />
              </CsSpaceBetween>
            ),
            width: 120,
          }] : []),
        ]}
        empty={
          <CsBox textAlign="center" color="inherit" padding="m">
            No Kea servers registered.
          </CsBox>
        }
      />
      {/* Cloudscape Modal for the create flow. The form inside still
          uses shadcn react-hook-form primitives — Cloudscape doesn't
          ship a react-hook-form integration and rewriting every form
          input is out of scope for this commit. */}
      {canWrite && (
        <CsModal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header="Register Kea DHCP server"
          size="medium"
        >
          <DhcpForm
            fabrics={fabrics}
            onSaved={async () => { setCreateOpen(false); await tableQuery.refetch(); }}
          />
        </CsModal>
      )}
    </>
  );
}

function DhcpForm({ fabrics, onSaved }: { fabrics: Fabric[]; onSaved: () => void }) {
  const form = useForm<z.infer<typeof dhcpSchema>>({
    resolver: zodResolver(dhcpSchema),
    defaultValues: {
      name: '', fabric_id: '', kea_url: '',
      auth_username: '', auth_password: '', enabled: true,
    },
  });
  async function onSubmit(v: z.infer<typeof dhcpSchema>) {
    try {
      await http.post('/ipam/dhcp/servers', {
        name: v.name, fabric_id: v.fabric_id, kea_url: v.kea_url,
        auth_username: v.auth_username || null,
        auth_password: v.auth_password || null,
        enabled: v.enabled,
      });
      toast.success('DHCP server registered');
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="name" render={({ field }) => (
          <FormItem><FormLabel>Name</FormLabel><FormControl><Input placeholder="e.g. kea-prod-east" {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <FormField control={form.control} name="fabric_id" render={({ field }) => (
          <FormItem>
            <FormLabel>Fabric</FormLabel>
            <Select value={field.value} onValueChange={field.onChange}>
              <FormControl><SelectTrigger><SelectValue placeholder="Pick a fabric" /></SelectTrigger></FormControl>
              <SelectContent>
                {fabrics.map((f) => <SelectItem key={f.id} value={f.id}>{f.name}</SelectItem>)}
              </SelectContent>
            </Select>
            <FormMessage />
          </FormItem>
        )} />
        <FormField control={form.control} name="kea_url" render={({ field }) => (
          <FormItem>
            <FormLabel>Kea Control Agent URL</FormLabel>
            <FormControl><Input type="url" placeholder="http://kea-ctrl-agent:8000" className="font-mono" {...field} /></FormControl>
            <FormMessage />
          </FormItem>
        )} />
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="auth_username" render={({ field }) => (
            <FormItem><FormLabel>Username (optional)</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
          )} />
          <FormField control={form.control} name="auth_password" render={({ field }) => (
            <FormItem><FormLabel>Password (optional)</FormLabel><FormControl><Input type="password" {...field} /></FormControl><FormMessage /></FormItem>
          )} />
        </div>
        <FormField control={form.control} name="enabled" render={({ field }) => (
          <FormItem className="flex items-center gap-3 space-y-0">
            <FormControl>
              <input
                type="checkbox" className="h-4 w-4"
                checked={field.value} onChange={(e) => field.onChange(e.target.checked)}
              />
            </FormControl>
            <FormLabel className="!mt-0 text-sm font-normal">Enabled (sync every 5 minutes)</FormLabel>
          </FormItem>
        )} />
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : 'Register'}
        </Button>
      </form>
    </Form>
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

  const fabricOptions: CsSelectProps.Option[] =
    fabrics.map((f) => ({ value: f.id, label: f.name }));
  const fabricOpt = fabricOptions.find((o) => o.value === fabricId) ?? null;

  return (
    <CsSpaceBetween size="l">
      <CsContainer header={<CsHeader variant="h2">Fabric</CsHeader>}>
        <CsFormField label="Fabric">
          <CsSelect
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
        </CsFormField>
      </CsContainer>

      <CsTable<Overlay>
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
          <CsHeader
            counter={
              selectedOverlay.length
                ? `(${selectedOverlay.length}/${overlays.length})`
                : `(${overlays.length})`
            }
            actions={
              canWrite && fabricId && (
                <CsButton variant="primary" iconName="add-plus" onClick={() => setCreateOverlayOpen(true)}>
                  New overlay
                </CsButton>
              )
            }
            description="Select an overlay to drill into its VNIs and VTEPs."
          >
            Overlays
          </CsHeader>
        }
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (o) => <span style={{ fontWeight: 500 }}>{o.name}</span> },
          { id: 'kind', header: 'Kind', cell: (o) => <CsBadge>{o.kind}</CsBadge>, width: 120 },
          {
            id: 'udp', header: 'UDP port',
            cell: (o) => <span style={{ fontFamily: 'ui-monospace, monospace' }}>{o.udp_port}</span>,
            width: 120,
          },
          { id: 'mtu', header: 'MTU', cell: (o) => o.mtu ?? '—', width: 100 },
        ]}
        empty={
          <CsBox textAlign="center" color="inherit" padding="m">
            No overlays in this fabric yet.
          </CsBox>
        }
      />

      {overlayId && (
        <CsColumnLayout columns={2}>
          <VnisPanel overlayId={overlayId} canWrite={canWrite} />
          <VtepsPanel overlayId={overlayId} canWrite={canWrite} />
        </CsColumnLayout>
      )}

      {canWrite && fabricId && (
        <CsModal
          visible={createOverlayOpen}
          onDismiss={() => setCreateOverlayOpen(false)}
          header="New overlay"
          size="medium"
        >
          <OverlayForm
            fabricId={fabricId}
            onSaved={async () => { setCreateOverlayOpen(false); await refreshOverlays(); }}
          />
        </CsModal>
      )}
    </CsSpaceBetween>
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
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="name" render={({ field }) => (
          <FormItem><FormLabel>Name</FormLabel><FormControl><Input placeholder="e.g. evpn-fabric-east" {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <div className="grid grid-cols-2 gap-3">
          <FormItem>
            <FormLabel>Kind</FormLabel>
            <Select value={kind} onValueChange={(v) => syncPort(v as 'vxlan' | 'geneve')}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="vxlan">VXLAN</SelectItem>
                <SelectItem value="geneve">GENEVE</SelectItem>
              </SelectContent>
            </Select>
          </FormItem>
          <FormField control={form.control} name="udp_port" render={({ field }) => (
            <FormItem><FormLabel>UDP port</FormLabel><FormControl><Input type="number" {...field} /></FormControl><FormMessage /></FormItem>
          )} />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="mtu" render={({ field }) => (
            <FormItem><FormLabel>MTU (optional)</FormLabel><FormControl><Input type="number" placeholder="9000" {...field} /></FormControl><FormMessage /></FormItem>
          )} />
          <FormField control={form.control} name="underlay_vrf_id" render={({ field }) => (
            <FormItem>
              <FormLabel>Underlay VRF (optional)</FormLabel>
              <Select value={field.value ?? NONE} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
                <SelectContent>
                  <SelectItem value={NONE}>(none)</SelectItem>
                  {vrfs.map((v) => <SelectItem key={v.id} value={v.id}>{v.name}</SelectItem>)}
                </SelectContent>
              </Select>
              <FormMessage />
            </FormItem>
          )} />
        </div>
        <FormField control={form.control} name="description" render={({ field }) => (
          <FormItem><FormLabel>Description</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : 'Create'}
        </Button>
      </form>
    </Form>
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
      <CsTable<Vni>
        variant="container"
        loading={vnisQ.isLoading}
        loadingText="Loading VNIs…"
        items={vnis}
        trackBy="id"
        header={
          <CsHeader
            counter={`(${vnis.length})`}
            actions={canWrite && (
              <CsButton iconName="add-plus" onClick={() => setCreateOpen(true)}>
                Add VNI
              </CsButton>
            )}
          >
            VNIs
          </CsHeader>
        }
        columnDefinitions={[
          {
            id: 'vni', header: 'VNI',
            cell: (v) => <span style={{ fontFamily: 'ui-monospace, monospace' }}>{v.vni}</span>,
            width: 100,
          },
          { id: 'kind', header: 'Kind', cell: (v) => <CsBadge>{v.kind}</CsBadge>, width: 80 },
          { id: 'name', header: 'Name', cell: (v) => v.name ?? '—' },
          {
            id: 'vlan_rt', header: 'VLAN / RT',
            cell: (v) => (
              <CsBox variant="span" color="text-status-inactive" fontSize="body-s">
                {v.vlan_id ? `vlan ${v.vlan_id}` : ''}
                {v.evpn_route_target ? ` · rt ${v.evpn_route_target}` : ''}
                {v.vrf_id ? ` · vrf bound` : ''}
              </CsBox>
            ),
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (v: Vni) => (
              <CsButton iconName="remove" variant="inline-icon" onClick={() => remove(v)} ariaLabel={`Delete VNI ${v.vni}`} />
            ),
            width: 60,
          }] : []),
        ]}
        empty={<CsBox textAlign="center" color="inherit" padding="m">No VNIs yet.</CsBox>}
      />
      {canWrite && (
        <CsModal
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
        </CsModal>
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
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="vni" render={({ field }) => (
            <FormItem><FormLabel>VNI (1..16777214)</FormLabel><FormControl><Input type="number" min={1} max={16777214} {...field} /></FormControl><FormMessage /></FormItem>
          )} />
          <FormField control={form.control} name="kind" render={({ field }) => (
            <FormItem>
              <FormLabel>Kind</FormLabel>
              <Select value={field.value} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
                <SelectContent>
                  <SelectItem value="l2">L2 (broadcast domain)</SelectItem>
                  <SelectItem value="l3">L3 (tenant VRF)</SelectItem>
                </SelectContent>
              </Select>
              <FormMessage />
            </FormItem>
          )} />
        </div>
        <FormField control={form.control} name="name" render={({ field }) => (
          <FormItem><FormLabel>Name (optional)</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        {kind === 'l2' && (
          <div className="grid grid-cols-2 gap-3">
            <FormField control={form.control} name="vlan_id" render={({ field }) => (
              <FormItem><FormLabel>Mapped VLAN (optional)</FormLabel><FormControl><Input type="number" min={1} max={4094} {...field} /></FormControl><FormMessage /></FormItem>
            )} />
            <FormField control={form.control} name="evpn_route_target" render={({ field }) => (
              <FormItem><FormLabel>EVPN RT (optional)</FormLabel><FormControl><Input placeholder="65000:10010" className="font-mono" {...field} /></FormControl><FormMessage /></FormItem>
            )} />
          </div>
        )}
        {kind === 'l3' && (
          <FormField control={form.control} name="vrf_id" render={({ field }) => (
            <FormItem>
              <FormLabel>Tenant VRF</FormLabel>
              <Select value={field.value ?? NONE} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue placeholder="Pick a VRF" /></SelectTrigger></FormControl>
                <SelectContent>
                  <SelectItem value={NONE}>(unset)</SelectItem>
                  {vrfs.map((v) => <SelectItem key={v.id} value={v.id}>{v.name}</SelectItem>)}
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground">L3 VNIs map a tenant VRF — required.</p>
              <FormMessage />
            </FormItem>
          )} />
        )}
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : 'Create'}
        </Button>
      </form>
    </Form>
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
      <CsTable<Vtep>
        variant="container"
        loading={vtepsQ.isLoading}
        loadingText="Loading VTEPs…"
        items={vteps}
        trackBy="id"
        header={
          <CsHeader
            counter={`(${vteps.length})`}
            actions={canWrite && (
              <CsButton iconName="add-plus" onClick={() => setCreateOpen(true)}>
                Add VTEP
              </CsButton>
            )}
          >
            VTEPs
          </CsHeader>
        }
        columnDefinitions={[
          {
            id: 'asset', header: 'Asset',
            cell: (v) => assetsById.get(v.asset_id)?.name ?? v.asset_id.slice(0, 8) + '…',
          },
          { id: 'role', header: 'Role', cell: (v) => <CsBadge>{v.role}</CsBadge>, width: 100 },
          {
            id: 'loopback', header: 'Loopback',
            cell: (v) => <span style={{ fontFamily: 'ui-monospace, monospace' }}>{v.loopback_ip ?? '—'}</span>,
          },
          ...(canWrite ? [{
            id: 'actions', header: '',
            cell: (v: Vtep) => (
              <CsButton iconName="remove" variant="inline-icon" onClick={() => remove(v)} ariaLabel="Delete VTEP" />
            ),
            width: 60,
          }] : []),
        ]}
        empty={<CsBox textAlign="center" color="inherit" padding="m">No VTEPs yet.</CsBox>}
      />
      {canWrite && (
        <CsModal
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
        </CsModal>
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
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="asset_id" render={({ field }) => (
          <FormItem>
            <FormLabel>Asset</FormLabel>
            <Select value={field.value} onValueChange={field.onChange}>
              <FormControl><SelectTrigger><SelectValue placeholder="Pick an asset" /></SelectTrigger></FormControl>
              <SelectContent>
                {assets.map((a) => <SelectItem key={a.id} value={a.id}>{a.name}</SelectItem>)}
              </SelectContent>
            </Select>
            <FormMessage />
          </FormItem>
        )} />
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="role" render={({ field }) => (
            <FormItem>
              <FormLabel>Role</FormLabel>
              <Select value={field.value} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
                <SelectContent>
                  {(['leaf', 'spine', 'border', 'other'] as const).map((r) => (
                    <SelectItem key={r} value={r}>{r}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <FormMessage />
            </FormItem>
          )} />
          <FormField control={form.control} name="loopback_ip" render={({ field }) => (
            <FormItem><FormLabel>Loopback IP (optional)</FormLabel><FormControl><Input className="font-mono" {...field} /></FormControl><FormMessage /></FormItem>
          )} />
        </div>
        <FormField control={form.control} name="description" render={({ field }) => (
          <FormItem><FormLabel>Description</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : 'Create'}
        </Button>
      </form>
    </Form>
  );
}
