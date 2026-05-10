import { Fragment, useMemo, useState } from 'react';
import { useTable, useGetIdentity, useList } from '@refinedev/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import {
  Plus, Pencil, Trash2, Network, GitBranch, ChevronRight, ChevronDown, Send,
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
      <div className="space-y-2">
        <h1 className="text-2xl font-semibold tracking-tight">IPAM</h1>
        <p className="text-sm text-muted-foreground">
          You don't have <code className="font-mono">inventory:read</code>.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight flex items-center gap-2">
          <Network className="h-5 w-5" /> IPAM
        </h1>
        <p className="text-sm text-muted-foreground">
          Fabric → VRF → Supernet → Subnet → IP — DHCP leases ingested from Kea
        </p>
      </div>
      <Tabs defaultValue="hierarchy">
        <TabsList>
          <TabsTrigger value="hierarchy">Hierarchy</TabsTrigger>
          <TabsTrigger value="free-space"><Search className="h-3.5 w-3.5" /> Free space</TabsTrigger>
          <TabsTrigger value="overlays">Overlays / VNI</TabsTrigger>
          <TabsTrigger value="dhcp">DHCP servers</TabsTrigger>
        </TabsList>
        <TabsContent value="hierarchy" className="pt-3">
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
        </TabsContent>
        <TabsContent value="free-space" className="pt-3">
          <FreeSpaceTab onSelectSubnet={(id) => setSubnetId(id)} />
        </TabsContent>
        <TabsContent value="overlays" className="pt-3">
          <OverlaysTab canWrite={!!canWrite} />
        </TabsContent>
        <TabsContent value="dhcp" className="pt-3">
          <DhcpServersTab canWrite={!!canWrite} />
        </TabsContent>
      </Tabs>
    </div>
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

function FreeSpaceTab({ onSelectSubnet }: { onSelectSubnet: (id: string) => void }) {
  const [mode, setMode] = useState<FreeMode>('in-subnets');
  const [fabricId, setFabricId] = useState<string>('');
  const [family, setFamily] = useState<'v4' | 'v6'>('v4');
  const [minFree, setMinFree] = useState<string>('1');
  const [prefixSize, setPrefixSize] = useState<string>('24');
  const fabricsRes = useList<Fabric>({ resource: 'ipam/fabrics', pagination: { pageSize: 200 } });
  const fabrics = fabricsRes.result.data ?? [];

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

  return (
    <div className="space-y-4">
      <Card>
        <CardContent className="grid gap-3 p-4 md:grid-cols-4">
          <div className="space-y-1 md:col-span-2">
            <label className="text-xs font-medium text-muted-foreground">Mode</label>
            <Tabs value={mode} onValueChange={(v) => setMode(v as FreeMode)}>
              <TabsList>
                <TabsTrigger value="in-subnets">Free addresses in existing subnets</TabsTrigger>
                <TabsTrigger value="prefixes">Free prefixes inside supernets</TabsTrigger>
              </TabsList>
            </Tabs>
          </div>
          <div className="space-y-1">
            <label className="text-xs font-medium text-muted-foreground">Fabric</label>
            <Select value={fabricId || '__all__'} onValueChange={(v) => setFabricId(v === '__all__' ? '' : v)}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="__all__">All fabrics</SelectItem>
                {fabrics.map((f) => (
                  <SelectItem key={f.id} value={f.id}>{f.name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1">
            <label className="text-xs font-medium text-muted-foreground">Family</label>
            <Select value={family} onValueChange={(v) => setFamily(v as 'v4' | 'v6')}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value="v4">IPv4</SelectItem>
                <SelectItem value="v6">IPv6</SelectItem>
              </SelectContent>
            </Select>
          </div>
          {mode === 'in-subnets' ? (
            <div className="space-y-1">
              <label className="text-xs font-medium text-muted-foreground">Min free addresses</label>
              <Input
                type="number" min={1}
                value={minFree}
                onChange={(e) => setMinFree(e.target.value)}
              />
            </div>
          ) : (
            <div className="space-y-1">
              <label className="text-xs font-medium text-muted-foreground">
                Prefix size (e.g. 24 for /24, 64 for /64)
              </label>
              <Input
                type="number" min={1} max={128}
                value={prefixSize}
                onChange={(e) => setPrefixSize(e.target.value)}
              />
            </div>
          )}
        </CardContent>
      </Card>

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
    </div>
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
    <Card>
      <CardContent className="p-0">
        {isLoading ? (
          <div className="space-y-2 p-4">
            {Array.from({ length: 3 }).map((_, i) => <Skeleton key={`s-${i}`} className="h-9 w-full" />)}
          </div>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Subnet</TableHead>
                <TableHead>Purpose</TableHead>
                <TableHead className="w-32 text-right">Free</TableHead>
                <TableHead className="w-32 text-right">Capacity</TableHead>
                <TableHead>Next available</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.length === 0 && (
                <TableRow><TableCell colSpan={5} className="text-muted-foreground">
                  No subnets meet that filter.
                </TableCell></TableRow>
              )}
              {rows.map((r) => (
                <TableRow
                  key={r.subnet_id}
                  className="cursor-pointer hover:bg-accent/40"
                  onClick={() => onSelectSubnet(r.subnet_id)}
                >
                  <TableCell className="font-mono">
                    {r.prefix}{r.name && <span className="text-muted-foreground"> · {r.name}</span>}
                  </TableCell>
                  <TableCell>
                    {r.purpose ? <Badge variant="secondary">{r.purpose}</Badge> : '—'}
                  </TableCell>
                  <TableCell className="text-right tabular-nums">{r.free.toLocaleString()}</TableCell>
                  <TableCell className="text-right tabular-nums">{r.capacity.toLocaleString()}</TableCell>
                  <TableCell className="font-mono text-xs">{r.next_available ?? '—'}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
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
      <Card>
        <CardContent className="space-y-2 p-4">
          {Array.from({ length: 3 }).map((_, i) => <Skeleton key={`s-${i}`} className="h-9 w-full" />)}
        </CardContent>
      </Card>
    );
  }
  if (groups.length === 0) {
    return (
      <Card>
        <CardContent className="p-4 text-sm text-muted-foreground">
          No supernet has free space at that prefix size in this scope.
        </CardContent>
      </Card>
    );
  }
  return (
    <div className="space-y-3">
      {groups.map((g) => (
        <Card key={g.supernet_id}>
          <CardContent className="space-y-2 p-4">
            <div className="flex items-baseline justify-between gap-2">
              <div>
                <div className="font-mono font-medium">{g.supernet_prefix}</div>
                <div className="text-xs text-muted-foreground">
                  {g.supernet_name ?? 'unnamed'}
                  {g.purpose && <> · {g.purpose}</>}
                </div>
              </div>
              <Badge variant="secondary">{g.count} candidates</Badge>
            </div>
            <div className="flex flex-wrap gap-1.5">
              {g.candidates.map((c) => (
                <Badge key={c} variant="outline" className="font-mono">{c}</Badge>
              ))}
            </div>
          </CardContent>
        </Card>
      ))}
    </div>
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
  return (
    <div className="flex items-center gap-1 text-sm">
      <button
        type="button"
        onClick={() => onJump('fabrics')}
        className="text-muted-foreground hover:text-foreground"
      >
        Fabrics
      </button>
      {fabricId && (
        <>
          <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />
          <button
            type="button"
            onClick={() => onJump('vrfs')}
            className="text-muted-foreground hover:text-foreground"
          >
            VRFs
          </button>
        </>
      )}
      {vrfId && (
        <>
          <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />
          <button
            type="button"
            onClick={() => onJump('networks')}
            className="text-muted-foreground hover:text-foreground"
          >
            Networks
          </button>
        </>
      )}
      {subnetId && (
        <>
          <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />
          <span className="font-medium">Addresses</span>
        </>
      )}
    </div>
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
    <div className="space-y-3">
      <div className="flex justify-end">
        {canWrite && (
          <Dialog open={createOpen} onOpenChange={setCreateOpen}>
            <DialogTrigger asChild>
              <Button><Plus className="h-4 w-4" /> New fabric</Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader><DialogTitle>New fabric</DialogTitle></DialogHeader>
              <FabricForm onSaved={async () => { setCreateOpen(false); await tableQuery.refetch(); }} />
            </DialogContent>
          </Dialog>
        )}
      </div>
      <Card>
        <CardContent className="p-0">
          {tableQuery.isLoading ? (
            <div className="space-y-2 p-4">
              {Array.from({ length: 3 }).map((_, i) => <Skeleton key={`s-${i}`} className="h-9 w-full" />)}
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Slug</TableHead>
                  <TableHead>Enclave</TableHead>
                  <TableHead>Classification</TableHead>
                  <TableHead>Description</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.length === 0 && (
                  <TableRow><TableCell colSpan={5} className="text-muted-foreground">No fabrics yet.</TableCell></TableRow>
                )}
                {data.map((f) => (
                  <TableRow
                    key={f.id} className="cursor-pointer hover:bg-accent/40"
                    onClick={() => onSelect(f.id)}
                  >
                    <TableCell className="font-medium">{f.name}</TableCell>
                    <TableCell className="font-mono text-xs">{f.slug}</TableCell>
                    <TableCell>{f.enclave ?? '—'}</TableCell>
                    <TableCell>{f.classification ?? '—'}</TableCell>
                    <TableCell className="max-w-md truncate text-sm text-muted-foreground">{f.description ?? '—'}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
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
    <div className="space-y-3">
      <div className="flex justify-end">
        {canWrite && (
          <Dialog open={createOpen} onOpenChange={setCreateOpen}>
            <DialogTrigger asChild>
              <Button><Plus className="h-4 w-4" /> New VRF</Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader><DialogTitle>New VRF</DialogTitle></DialogHeader>
              <VrfForm fabricId={fabricId} onSaved={async () => { setCreateOpen(false); await tableQuery.refetch(); }} />
            </DialogContent>
          </Dialog>
        )}
      </div>
      <Card>
        <CardContent className="p-0">
          {tableQuery.isLoading ? (
            <div className="space-y-2 p-4">
              {Array.from({ length: 2 }).map((_, i) => <Skeleton key={`s-${i}`} className="h-9 w-full" />)}
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>RD</TableHead>
                  <TableHead>Default</TableHead>
                  <TableHead>Description</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.map((v) => (
                  <TableRow
                    key={v.id} className="cursor-pointer hover:bg-accent/40"
                    onClick={() => onSelect(v.id)}
                  >
                    <TableCell className="font-medium flex items-center gap-2">
                      <GitBranch className="h-3.5 w-3.5 text-muted-foreground" />
                      {v.name}
                    </TableCell>
                    <TableCell className="font-mono text-xs">{v.rd ?? '—'}</TableCell>
                    <TableCell>
                      {v.is_default ? <Badge variant="secondary">default</Badge> : '—'}
                    </TableCell>
                    <TableCell className="text-sm text-muted-foreground">{v.description ?? '—'}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
    </div>
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
  onSelectSubnet, onAddSubnet, onAddChildSupernet, onEditSupernet, onEditSubnet,
}: {
  supernet: Supernet;
  depth: number;
  expanded: Set<string>;
  onToggle: (id: string) => void;
  sitesById: Map<string, Site>;
  canWrite: boolean;
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
  return (
    <Fragment>
      <TableRow
        className="cursor-pointer hover:bg-accent/40"
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
  onSelectSubnet, onAddSubnet, onAddChildSupernet, onEditSubnet,
}: {
  supernetId: string;
  depth: number;
  parentPurpose: string | null;
  sitesById: Map<string, Site>;
  canWrite: boolean;
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
        <TableRow
          key={s.id}
          className="cursor-pointer bg-muted/20 hover:bg-accent/40"
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
    <div className="space-y-3">
      <div className="flex justify-end">
        {canWrite && (
          <Dialog open={createOpen} onOpenChange={setCreateOpen}>
            <DialogTrigger asChild>
              <Button><Plus className="h-4 w-4" /> Add Kea server</Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader><DialogTitle>Register Kea DHCP server</DialogTitle></DialogHeader>
              <DhcpForm fabrics={fabrics} onSaved={async () => { setCreateOpen(false); await tableQuery.refetch(); }} />
            </DialogContent>
          </Dialog>
        )}
      </div>
      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Fabric</TableHead>
                <TableHead>Control Agent URL</TableHead>
                <TableHead>Last sync</TableHead>
                <TableHead>Leases</TableHead>
                <TableHead>Status</TableHead>
                {canWrite && <TableHead className="w-32" />}
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.length === 0 && (
                <TableRow><TableCell colSpan={canWrite ? 7 : 6} className="text-muted-foreground">No Kea servers registered.</TableCell></TableRow>
              )}
              {data.map((s) => (
                <TableRow key={s.id}>
                  <TableCell className="font-medium">{s.name}</TableCell>
                  <TableCell className="text-sm">
                    {fabricsById.get(s.fabric_id)?.name ?? s.fabric_id.slice(0, 8) + '…'}
                  </TableCell>
                  <TableCell className="font-mono text-xs">{s.kea_url}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    {s.last_sync_at ? formatDate(s.last_sync_at) : 'never'}
                  </TableCell>
                  <TableCell className="tabular-nums">{s.last_sync_lease_count ?? '—'}</TableCell>
                  <TableCell>
                    {s.last_sync_status === 'ok' && <Badge variant="success">ok</Badge>}
                    {s.last_sync_status === 'error' && <Badge variant="critical">error</Badge>}
                    {!s.last_sync_status && <Badge variant="secondary">pending</Badge>}
                    {!s.enabled && <Badge variant="secondary" className="ml-1">disabled</Badge>}
                  </TableCell>
                  {canWrite && (
                    <TableCell>
                      <div className="flex gap-1">
                        <Button size="sm" variant="ghost" onClick={() => syncNow(s)} title="Sync now">
                          <Send className="h-3.5 w-3.5" />
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => remove(s)}>
                          <Trash2 className="h-3.5 w-3.5" />
                        </Button>
                      </div>
                    </TableCell>
                  )}
                </TableRow>
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
    </div>
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
  const [overlayId, setOverlayId] = useState<string | null>(null);
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

  async function refreshOverlays() {
    await qc.invalidateQueries({ queryKey: ['overlays-for-fabric', fabricId] });
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end gap-3">
        <div className="space-y-1">
          <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Fabric</p>
          <Select value={fabricId} onValueChange={(v) => { setFabricId(v); setOverlayId(null); }}>
            <SelectTrigger className="w-[260px]"><SelectValue placeholder="Pick a fabric" /></SelectTrigger>
            <SelectContent>
              {fabrics.map((f) => <SelectItem key={f.id} value={f.id}>{f.name}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
        {canWrite && fabricId && (
          <Dialog open={createOverlayOpen} onOpenChange={setCreateOverlayOpen}>
            <DialogTrigger asChild>
              <Button><Plus className="h-4 w-4" /> New overlay</Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader><DialogTitle>New overlay</DialogTitle></DialogHeader>
              <OverlayForm
                fabricId={fabricId}
                onSaved={async () => { setCreateOverlayOpen(false); await refreshOverlays(); }}
              />
            </DialogContent>
          </Dialog>
        )}
      </div>

      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Name</TableHead>
                <TableHead>Kind</TableHead>
                <TableHead>UDP port</TableHead>
                <TableHead>MTU</TableHead>
                <TableHead className="w-32" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {overlays.length === 0 && !overlaysQ.isLoading && (
                <TableRow><TableCell colSpan={5} className="text-muted-foreground">
                  No overlays in this fabric yet.
                </TableCell></TableRow>
              )}
              {overlays.map((o) => (
                <TableRow
                  key={o.id}
                  className={'cursor-pointer hover:bg-accent/40 ' + (overlayId === o.id ? 'bg-accent/30' : '')}
                  onClick={() => setOverlayId(o.id === overlayId ? null : o.id)}
                >
                  <TableCell className="font-medium">{o.name}</TableCell>
                  <TableCell><Badge variant="secondary">{o.kind}</Badge></TableCell>
                  <TableCell className="font-mono">{o.udp_port}</TableCell>
                  <TableCell>{o.mtu ?? '—'}</TableCell>
                  <TableCell className="text-right text-xs text-muted-foreground">
                    {overlayId === o.id ? 'selected' : 'click to drill'}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      {overlayId && (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <VnisPanel overlayId={overlayId} canWrite={canWrite} />
          <VtepsPanel overlayId={overlayId} canWrite={canWrite} />
        </div>
      )}
    </div>
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
    <Card>
      <CardContent className="p-0">
        <div className="flex items-center justify-between border-b p-3">
          <h3 className="text-sm font-semibold">VNIs</h3>
          {canWrite && (
            <Dialog open={createOpen} onOpenChange={setCreateOpen}>
              <DialogTrigger asChild>
                <Button size="sm" variant="outline"><Plus className="h-3.5 w-3.5" /> Add VNI</Button>
              </DialogTrigger>
              <DialogContent>
                <DialogHeader><DialogTitle>New VNI</DialogTitle></DialogHeader>
                <VniForm
                  overlayId={overlayId}
                  onSaved={async () => {
                    setCreateOpen(false);
                    await qc.invalidateQueries({ queryKey: ['vnis-for-overlay', overlayId] });
                  }}
                />
              </DialogContent>
            </Dialog>
          )}
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>VNI</TableHead>
              <TableHead>Kind</TableHead>
              <TableHead>Name</TableHead>
              <TableHead>VLAN / RT</TableHead>
              {canWrite && <TableHead className="w-12" />}
            </TableRow>
          </TableHeader>
          <TableBody>
            {vnis.length === 0 && !vnisQ.isLoading && (
              <TableRow><TableCell colSpan={canWrite ? 5 : 4} className="text-muted-foreground">No VNIs yet.</TableCell></TableRow>
            )}
            {vnis.map((v) => (
              <TableRow key={v.id}>
                <TableCell className="font-mono">{v.vni}</TableCell>
                <TableCell><Badge variant={v.kind === 'l3' ? 'default' : 'secondary'}>{v.kind}</Badge></TableCell>
                <TableCell>{v.name ?? '—'}</TableCell>
                <TableCell className="text-xs text-muted-foreground">
                  {v.vlan_id ? `vlan ${v.vlan_id}` : ''}
                  {v.evpn_route_target ? ` · rt ${v.evpn_route_target}` : ''}
                  {v.vrf_id ? ` · vrf bound` : ''}
                </TableCell>
                {canWrite && (
                  <TableCell>
                    <Button size="sm" variant="ghost" onClick={() => remove(v)} title="Delete VNI">
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </TableCell>
                )}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
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
    <Card>
      <CardContent className="p-0">
        <div className="flex items-center justify-between border-b p-3">
          <h3 className="text-sm font-semibold">VTEPs</h3>
          {canWrite && (
            <Dialog open={createOpen} onOpenChange={setCreateOpen}>
              <DialogTrigger asChild>
                <Button size="sm" variant="outline"><Plus className="h-3.5 w-3.5" /> Add VTEP</Button>
              </DialogTrigger>
              <DialogContent>
                <DialogHeader><DialogTitle>New VTEP</DialogTitle></DialogHeader>
                <VtepForm
                  overlayId={overlayId}
                  assets={assets}
                  onSaved={async () => {
                    setCreateOpen(false);
                    await qc.invalidateQueries({ queryKey: ['vteps-for-overlay', overlayId] });
                  }}
                />
              </DialogContent>
            </Dialog>
          )}
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Asset</TableHead>
              <TableHead>Role</TableHead>
              <TableHead>Loopback</TableHead>
              {canWrite && <TableHead className="w-12" />}
            </TableRow>
          </TableHeader>
          <TableBody>
            {vteps.length === 0 && !vtepsQ.isLoading && (
              <TableRow><TableCell colSpan={canWrite ? 4 : 3} className="text-muted-foreground">No VTEPs yet.</TableCell></TableRow>
            )}
            {vteps.map((v) => (
              <TableRow key={v.id}>
                <TableCell>{assetsById.get(v.asset_id)?.name ?? v.asset_id.slice(0, 8) + '…'}</TableCell>
                <TableCell><Badge variant="secondary">{v.role}</Badge></TableCell>
                <TableCell className="font-mono">{v.loopback_ip ?? '—'}</TableCell>
                {canWrite && (
                  <TableCell>
                    <Button size="sm" variant="ghost" onClick={() => remove(v)} title="Delete VTEP">
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </TableCell>
                )}
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
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
