import { useMemo, useState } from 'react';
import { useTable, useGetIdentity, useList } from '@refinedev/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import {
  Plus, Trash2, Network, GitBranch, ChevronRight, Send,
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
  prefix: string; name: string | null; description: string | null;
  purpose: string | null;
};
type Subnet = {
  id: string; supernet_id: string; fabric_id: string; vrf_id: string;
  site_id: string | null; prefix: string; name: string | null;
  description: string | null; purpose: string | null;
  vlan_id: number | null; gateway: string | null;
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

  // Drill-down state — one selection per layer.
  const [fabricId, setFabricId] = useState<string | null>(null);
  const [vrfId, setVrfId] = useState<string | null>(null);
  const [supernetId, setSupernetId] = useState<string | null>(null);
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
          <TabsTrigger value="dhcp">DHCP servers</TabsTrigger>
        </TabsList>
        <TabsContent value="hierarchy" className="pt-3">
          <Breadcrumbs
            fabricId={fabricId} vrfId={vrfId} supernetId={supernetId} subnetId={subnetId}
            onJump={(level) => {
              if (level === 'fabrics') { setFabricId(null); setVrfId(null); setSupernetId(null); setSubnetId(null); }
              if (level === 'vrfs') { setVrfId(null); setSupernetId(null); setSubnetId(null); }
              if (level === 'supernets') { setSupernetId(null); setSubnetId(null); }
              if (level === 'subnets') { setSubnetId(null); }
            }}
          />
          <div className="mt-3">
            {!fabricId && (
              <FabricsTab onSelect={setFabricId} canWrite={!!canWrite} />
            )}
            {fabricId && !vrfId && (
              <VrfsTab fabricId={fabricId} onSelect={setVrfId} canWrite={!!canWrite} />
            )}
            {fabricId && vrfId && !supernetId && (
              <SupernetsTab
                fabricId={fabricId} vrfId={vrfId}
                onSelect={setSupernetId} canWrite={!!canWrite}
              />
            )}
            {supernetId && !subnetId && (
              <SubnetsTab supernetId={supernetId} onSelect={setSubnetId} canWrite={!!canWrite} />
            )}
            {subnetId && (
              <AddressesTab subnetId={subnetId} canWrite={!!canWrite} />
            )}
          </div>
        </TabsContent>
        <TabsContent value="dhcp" className="pt-3">
          <DhcpServersTab canWrite={!!canWrite} />
        </TabsContent>
      </Tabs>
    </div>
  );
}

function Breadcrumbs({
  fabricId, vrfId, supernetId, subnetId, onJump,
}: {
  fabricId: string | null;
  vrfId: string | null;
  supernetId: string | null;
  subnetId: string | null;
  onJump: (level: 'fabrics' | 'vrfs' | 'supernets' | 'subnets') => void;
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
            onClick={() => onJump('supernets')}
            className="text-muted-foreground hover:text-foreground"
          >
            Supernets
          </button>
        </>
      )}
      {supernetId && (
        <>
          <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />
          <button
            type="button"
            onClick={() => onJump('subnets')}
            className="text-muted-foreground hover:text-foreground"
          >
            Subnets
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
  purpose: z.string().optional(),
  description: z.string().optional(),
});

function SupernetsTab({
  fabricId, vrfId, onSelect, canWrite,
}: {
  fabricId: string;
  vrfId: string;
  onSelect: (id: string) => void;
  canWrite: boolean;
}) {
  const qc = useQueryClient();
  const { tableQuery, result } = useTable<Supernet>({
    resource: 'ipam/supernets',
    pagination: { pageSize: 200 },
    filters: { permanent: [
      { field: 'fabric_id', operator: 'eq', value: fabricId },
      { field: 'vrf_id', operator: 'eq', value: vrfId },
    ] },
    sorters: { initial: [{ field: 'prefix', order: 'asc' }] },
  });
  const data = result.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        {canWrite && (
          <Dialog open={createOpen} onOpenChange={setCreateOpen}>
            <DialogTrigger asChild>
              <Button><Plus className="h-4 w-4" /> New supernet</Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader><DialogTitle>New supernet</DialogTitle></DialogHeader>
              <SupernetForm
                fabricId={fabricId} vrfId={vrfId}
                onSaved={async () => { setCreateOpen(false); await tableQuery.refetch(); await qc.invalidateQueries({ queryKey: ['supernet-util'] }); }}
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
                <TableHead>Prefix</TableHead>
                <TableHead>Name</TableHead>
                <TableHead>Purpose</TableHead>
                <TableHead className="w-64">Subnet utilization</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.length === 0 && (
                <TableRow><TableCell colSpan={4} className="text-muted-foreground">No supernets yet.</TableCell></TableRow>
              )}
              {data.map((s) => (
                <TableRow
                  key={s.id} className="cursor-pointer hover:bg-accent/40"
                  onClick={() => onSelect(s.id)}
                >
                  <TableCell className="font-mono">{s.prefix}</TableCell>
                  <TableCell>{s.name ?? '—'}</TableCell>
                  <TableCell>{s.purpose ? <Badge variant="secondary">{s.purpose}</Badge> : '—'}</TableCell>
                  <TableCell><SupernetUtilCell supernetId={s.id} /></TableCell>
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

function SupernetForm({
  fabricId, vrfId, onSaved,
}: {
  fabricId: string;
  vrfId: string;
  onSaved: () => void;
}) {
  const form = useForm<z.infer<typeof supernetSchema>>({
    resolver: zodResolver(supernetSchema),
    defaultValues: { prefix: '', name: '', purpose: '', description: '' },
  });
  async function onSubmit(v: z.infer<typeof supernetSchema>) {
    try {
      await http.post('/ipam/supernets', {
        fabric_id: fabricId,
        vrf_id: vrfId,
        prefix: v.prefix,
        name: v.name || null,
        purpose: v.purpose || null,
        description: v.description || null,
      });
      toast.success('Supernet created');
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="prefix" render={({ field }) => (
          <FormItem>
            <FormLabel>Prefix (CIDR)</FormLabel>
            <FormControl><Input placeholder="e.g. 10.0.0.0/8 or 2001:db8::/32" className="font-mono" {...field} /></FormControl>
            <FormMessage />
          </FormItem>
        )} />
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="name" render={({ field }) => (
            <FormItem><FormLabel>Name</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
          )} />
          <FormField control={form.control} name="purpose" render={({ field }) => (
            <FormItem>
              <FormLabel>Purpose</FormLabel>
              <Select value={field.value} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue placeholder="—" /></SelectTrigger></FormControl>
                <SelectContent>
                  {PURPOSES.map((p) => <SelectItem key={p} value={p}>{p}</SelectItem>)}
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

// ----------------------- Subnets -----------------------

const subnetSchema = z.object({
  prefix: z.string().min(1),
  site_id: z.string(),
  name: z.string().optional(),
  purpose: z.string().optional(),
  vlan_id: z.string().optional(),
  gateway: z.string().optional(),
});

function SubnetsTab({
  supernetId, onSelect, canWrite,
}: {
  supernetId: string;
  onSelect: (id: string) => void;
  canWrite: boolean;
}) {
  const { tableQuery, result } = useTable<Subnet>({
    resource: 'ipam/subnets',
    pagination: { pageSize: 200 },
    filters: { permanent: [{ field: 'supernet_id', operator: 'eq', value: supernetId }] },
    sorters: { initial: [{ field: 'prefix', order: 'asc' }] },
  });
  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const sites = sitesRes.result.data ?? [];
  const sitesById = useMemo(() => new Map(sites.map((s) => [s.id, s])), [sites]);
  const data = result.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        {canWrite && (
          <Dialog open={createOpen} onOpenChange={setCreateOpen}>
            <DialogTrigger asChild>
              <Button><Plus className="h-4 w-4" /> New subnet</Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader><DialogTitle>New subnet</DialogTitle></DialogHeader>
              <SubnetForm
                supernetId={supernetId} sites={sites}
                onSaved={async () => { setCreateOpen(false); await tableQuery.refetch(); }}
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
                <TableHead>Prefix</TableHead>
                <TableHead>Site</TableHead>
                <TableHead>VLAN</TableHead>
                <TableHead>Gateway</TableHead>
                <TableHead className="w-64">Allocation</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data.length === 0 && (
                <TableRow><TableCell colSpan={5} className="text-muted-foreground">No subnets yet.</TableCell></TableRow>
              )}
              {data.map((s) => (
                <TableRow
                  key={s.id} className="cursor-pointer hover:bg-accent/40"
                  onClick={() => onSelect(s.id)}
                >
                  <TableCell className="font-mono">{s.prefix}</TableCell>
                  <TableCell className="text-sm">
                    {s.site_id ? (sitesById.get(s.site_id)?.code ?? s.site_id.slice(0, 8) + '…') : '—'}
                  </TableCell>
                  <TableCell className="font-mono text-xs">{s.vlan_id ?? '—'}</TableCell>
                  <TableCell className="font-mono text-xs">{s.gateway ?? '—'}</TableCell>
                  <TableCell><SubnetUtilCell subnetId={s.id} /></TableCell>
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
  supernetId, sites, onSaved,
}: {
  supernetId: string;
  sites: Site[];
  onSaved: () => void;
}) {
  const NONE = '__none__';
  const form = useForm<z.infer<typeof subnetSchema>>({
    resolver: zodResolver(subnetSchema),
    defaultValues: { prefix: '', site_id: NONE, name: '', purpose: '', vlan_id: '', gateway: '' },
  });
  async function onSubmit(v: z.infer<typeof subnetSchema>) {
    try {
      await http.post('/ipam/subnets', {
        supernet_id: supernetId,
        site_id: v.site_id === NONE ? null : v.site_id,
        prefix: v.prefix,
        name: v.name || null,
        purpose: v.purpose || null,
        vlan_id: v.vlan_id ? Number(v.vlan_id) : null,
        gateway: v.gateway || null,
      });
      toast.success('Subnet created');
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="prefix" render={({ field }) => (
          <FormItem>
            <FormLabel>Prefix (CIDR, must be inside the supernet)</FormLabel>
            <FormControl><Input placeholder="e.g. 10.0.5.0/24" className="font-mono" {...field} /></FormControl>
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
              <Select value={field.value} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue placeholder="—" /></SelectTrigger></FormControl>
                <SelectContent>
                  {PURPOSES.map((p) => <SelectItem key={p} value={p}>{p}</SelectItem>)}
                </SelectContent>
              </Select>
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
        <FormField control={form.control} name="name" render={({ field }) => (
          <FormItem><FormLabel>Name (optional)</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : 'Create'}
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
      <div className="flex justify-end">
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
    </div>
  );
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

