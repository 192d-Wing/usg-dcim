import { useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useUpdate } from '@refinedev/core';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { ArrowLeft, Pencil, Plus, ChevronsUpDown } from 'lucide-react';
import { http } from '@/lib/http';
import { hasCapability } from '@/lib/access-control-provider';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { Badge } from '@/components/ui/badge';
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table';
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs';
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
import { RackVisualization } from '@/components/rack-visualization';
import { RackHeightPicker } from '@/components/rack-height-picker';
import { useStencilCatalog } from '@/components/stencil';
import { toast } from 'sonner';

type RackDetail = {
  rack: {
    id: string; site_id: string; row_id: string; name: string; code: string;
    u_height: number; max_kw: number | null; serial: string | null;
  };
  assets: Array<{
    id: string; name: string; hostname: string | null; kind: string;
    manufacturer: string | null; model: string | null; serial: string | null;
    rack_position_u: number | null; rack_units: number;
    lifecycle_state: string; open_alerts: number;
  }>;
};

export function RackShowPage() {
  const { id = '' } = useParams<{ id: string }>();
  const nav = useNavigate();
  const qc = useQueryClient();
  const [mode, setMode] = useState<'stencil' | 'block'>('stencil');
  const [editOpen, setEditOpen] = useState(false);
  const [addOpen, setAddOpen] = useState(false);

  const detail = useQuery({
    queryKey: ['rack-detail', id],
    queryFn: async () => (await http.get<RackDetail>(`/dashboards/racks/${id}`)).data,
    refetchInterval: 30_000,
    enabled: !!id,
  });

  if (detail.isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-9 w-72" />
        <Skeleton className="h-[760px] w-full" />
      </div>
    );
  }
  if (detail.isError || !detail.data?.rack) return <p className="text-sm text-muted-foreground">Failed to load rack.</p>;

  const r = detail.data.rack;
  const assets = detail.data.assets ?? [];
  const occupied = new Set<number>();
  for (const a of assets) {
    if (a.rack_position_u && a.rack_position_u >= 1) {
      const span = Math.max(1, a.rack_units || 1);
      for (let u = a.rack_position_u; u < a.rack_position_u + span; u++) occupied.add(u);
    }
  }

  return (
    <div className="space-y-6">
      <div>
        <Button variant="ghost" size="sm" onClick={() => nav('/racks')} className="-ml-2">
          <ArrowLeft className="h-4 w-4" /> All racks
        </Button>
      </div>

      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{r.code} · {r.name}</h1>
          <p className="text-sm text-muted-foreground">
            {r.u_height}U · {r.max_kw ? `${r.max_kw} kW max` : 'unrated'} · {assets.length} devices
            {r.serial && <> · SN {r.serial}</>}
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Tabs value={mode} onValueChange={(v) => setMode(v as 'stencil' | 'block')}>
            <TabsList>
              <TabsTrigger value="stencil">Stencil</TabsTrigger>
              <TabsTrigger value="block">Block</TabsTrigger>
            </TabsList>
          </Tabs>
          {hasCapability('inventory:write') && (
            <>
              <Dialog open={addOpen} onOpenChange={setAddOpen}>
                <DialogTrigger asChild>
                  <Button><Plus className="h-4 w-4" /> Add device</Button>
                </DialogTrigger>
                <DialogContent>
                  <DialogHeader><DialogTitle>Add device to rack</DialogTitle></DialogHeader>
                  <NewAssetForm
                    siteId={r.site_id}
                    rackId={r.id}
                    uHeight={r.u_height}
                    occupiedSlots={occupied}
                    onCreated={async () => {
                      setAddOpen(false);
                      toast.success('Device added');
                      await qc.invalidateQueries({ queryKey: ['rack-detail', id] });
                    }}
                  />
                </DialogContent>
              </Dialog>
              <Dialog open={editOpen} onOpenChange={setEditOpen}>
                <DialogTrigger asChild>
                  <Button variant="outline"><Pencil className="h-4 w-4" /> Edit rack</Button>
                </DialogTrigger>
                <DialogContent>
                  <DialogHeader><DialogTitle>Edit rack</DialogTitle></DialogHeader>
                  <EditRackForm
                    rack={r}
                    assets={assets}
                    onSaved={async () => {
                      setEditOpen(false);
                      await qc.invalidateQueries({ queryKey: ['rack-detail', id] });
                    }}
                  />
                </DialogContent>
              </Dialog>
            </>
          )}
        </div>
      </div>

      <Card>
        <CardContent className="p-6">
          <RackVisualization rackId={id} uHeight={r.u_height} assets={assets} mode={mode} />
        </CardContent>
      </Card>

      <Card>
        <CardHeader><CardTitle className="text-base">Devices</CardTitle></CardHeader>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-12">U</TableHead>
                <TableHead>Name</TableHead>
                <TableHead>Hostname</TableHead>
                <TableHead>Kind</TableHead>
                <TableHead>Manufacturer</TableHead>
                <TableHead>Model</TableHead>
                <TableHead>Serial</TableHead>
                <TableHead className="w-20">Alerts</TableHead>
                <TableHead className="w-24">State</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {assets.length === 0 && (
                <TableRow><TableCell colSpan={9} className="text-muted-foreground">No devices in this rack.</TableCell></TableRow>
              )}
              {assets.map((a) => (
                <TableRow key={a.id} onClick={() => nav(`/assets/${a.id}`)} className="cursor-pointer">
                  <TableCell className="tabular-nums">{a.rack_position_u ?? '—'}</TableCell>
                  <TableCell className="font-medium">{a.name}</TableCell>
                  <TableCell className="text-muted-foreground">{a.hostname ?? '—'}</TableCell>
                  <TableCell><Badge variant="secondary">{a.kind}</Badge></TableCell>
                  <TableCell>{a.manufacturer ?? '—'}</TableCell>
                  <TableCell>{a.model ?? '—'}</TableCell>
                  <TableCell className="font-mono text-xs">{a.serial ?? '—'}</TableCell>
                  <TableCell>{a.open_alerts > 0 ? <Badge variant="critical">{a.open_alerts}</Badge> : <span className="text-muted-foreground">0</span>}</TableCell>
                  <TableCell><Badge variant={a.lifecycle_state === 'active' ? 'success' : 'warning'}>{a.lifecycle_state}</Badge></TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </div>
  );
}

// ----- Edit Rack form -----
const editSchema = z.object({
  name: z.string().min(1),
  u_height: z.coerce.number().min(1).max(60),
  max_kw: z.string().optional(),
  serial: z.string().optional(),
});

function EditRackForm({
  rack, assets, onSaved,
}: {
  rack: RackDetail['rack'];
  assets: RackDetail['assets'];
  onSaved: () => void;
}) {
  const updateMutation = useUpdate();
  const isPending = (updateMutation as any).isPending ?? (updateMutation as any).isLoading ?? false;
  const update = updateMutation.mutate;
  const form = useForm<z.infer<typeof editSchema>>({
    resolver: zodResolver(editSchema),
    defaultValues: {
      name: rack.name,
      u_height: rack.u_height,
      max_kw: rack.max_kw?.toString() ?? '',
      serial: rack.serial ?? '',
    },
  });

  // Live preview: which placed devices would fall outside the new envelope?
  const candidate = Number(form.watch('u_height')) || rack.u_height;
  const orphans = assets.filter(
    (a) => a.rack_position_u && (a.rack_position_u + Math.max(1, a.rack_units || 1) - 1) > candidate,
  );

  function onSubmit(v: z.infer<typeof editSchema>) {
    if (orphans.length > 0) {
      toast.error(
        `${orphans.length} device(s) would be orphaned at U${candidate}: ${orphans.slice(0, 3).map((a) => a.name).join(', ')}${orphans.length > 3 ? '…' : ''}`,
      );
      return;
    }
    update(
      {
        resource: 'inventory/racks',
        id: rack.id,
        values: {
          name: v.name,
          u_height: v.u_height,
          max_kw: v.max_kw ? Number(v.max_kw) : null,
          serial: v.serial || null,
        },
        successNotification: false,
      },
      {
        onSuccess: () => { toast.success('Rack updated'); onSaved(); },
        onError: (err: any) => toast.error(err?.message ?? 'Update failed'),
      },
    );
  }

  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="name" render={({ field }) => (
          <FormItem><FormLabel>Name</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <FormField control={form.control} name="u_height" render={({ field }) => (
          <FormItem>
            <FormLabel>Rack height</FormLabel>
            <FormControl>
              <RackHeightPicker value={Number(field.value) || 42} onChange={field.onChange} />
            </FormControl>
            <FormMessage />
          </FormItem>
        )} />
        {orphans.length > 0 && (
          <div className="rounded-md border border-destructive/40 bg-destructive/10 p-3 text-xs">
            <p className="font-medium text-destructive">
              {orphans.length} device(s) would be orphaned at {candidate}U
            </p>
            <ul className="mt-1 list-disc pl-5 text-muted-foreground">
              {orphans.slice(0, 5).map((a) => (
                <li key={a.id}>
                  {a.name} (U{a.rack_position_u}{(a.rack_units || 1) > 1 ? `–U${(a.rack_position_u || 0) + (a.rack_units || 1) - 1}` : ''})
                </li>
              ))}
              {orphans.length > 5 && <li>…and {orphans.length - 5} more</li>}
            </ul>
            <p className="mt-2 text-muted-foreground">Move them to lower U positions before shrinking the rack.</p>
          </div>
        )}
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="max_kw" render={({ field }) => (
            <FormItem><FormLabel>Max kW</FormLabel><FormControl><Input type="number" step="0.1" {...field} /></FormControl><FormMessage /></FormItem>
          )} />
          <FormField control={form.control} name="serial" render={({ field }) => (
            <FormItem><FormLabel>Serial</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
          )} />
        </div>
        <Button type="submit" disabled={isPending || orphans.length > 0}>
          {isPending ? 'Saving…' : 'Save'}
        </Button>
      </form>
    </Form>
  );
}

// ----- New Asset form (rack-scoped) -----
const KINDS = ['server', 'switch', 'router', 'pdu', 'ups', 'crac', 'sensor', 'storage', 'chassis', 'blade', 'other'] as const;

const newAssetSchema = z.object({
  name: z.string().min(1, 'Name required'),
  hostname: z.string().optional(),
  kind: z.enum(KINDS),
  manufacturer: z.string().optional(),
  model: z.string().optional(),
  serial: z.string().optional(),
  rack_position_u: z.string().optional(),
  rack_units: z.coerce.number().min(1).max(60).default(1),
});

function NewAssetForm({
  siteId, rackId, uHeight, occupiedSlots, onCreated,
}: {
  siteId: string;
  rackId: string;
  uHeight: number;
  occupiedSlots: Set<number>;
  onCreated: () => void;
}) {
  const catalog = useStencilCatalog();
  const form = useForm<z.infer<typeof newAssetSchema>>({
    resolver: zodResolver(newAssetSchema),
    defaultValues: { name: '', kind: 'server', rack_units: 1 },
  });

  const manufacturer = form.watch('manufacturer') ?? '';
  const model = form.watch('model') ?? '';
  const positionUStr = form.watch('rack_position_u') ?? '';
  const units = form.watch('rack_units') ?? 1;
  const positionU = positionUStr ? Number(positionUStr) : null;
  const collisions: number[] = [];
  if (positionU) {
    for (let u = positionU; u < positionU + units; u++) {
      if (occupiedSlots.has(u)) collisions.push(u);
    }
  }
  const vendorMatches = (catalog.data?.stencils ?? []).filter(
    (s) => manufacturer && s.manufacturer.toLowerCase().includes(manufacturer.toLowerCase()),
  );

  function applyStencil(s: { manufacturer: string; model: string; u: number; kind_hint?: string }) {
    form.setValue('manufacturer', s.manufacturer);
    form.setValue('model', s.model);
    if (s.kind_hint) form.setValue('kind', s.kind_hint as any);
    if (s.u > 0) form.setValue('rack_units', s.u);
  }

  async function onSubmit(v: z.infer<typeof newAssetSchema>) {
    if (collisions.length) {
      toast.error(`Slots already occupied: U${collisions.join(', U')}`);
      return;
    }
    try {
      await http.post('/inventory/assets', {
        site_id: siteId,
        rack_id: rackId,
        name: v.name,
        hostname: v.hostname || null,
        kind: v.kind,
        manufacturer: v.manufacturer || null,
        model: v.model || null,
        serial: v.serial || null,
        rack_position_u: positionU,
        rack_units: v.rack_units,
        lifecycle_state: 'active',
        metadata_json: {},
      });
      onCreated();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to create asset');
    }
  }

  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="name" render={({ field }) => (
          <FormItem>
            <FormLabel>Name</FormLabel>
            <FormControl><Input placeholder="e.g. R01-server9" {...field} /></FormControl>
            <FormMessage />
          </FormItem>
        )} />
        <FormField control={form.control} name="hostname" render={({ field }) => (
          <FormItem>
            <FormLabel>Hostname (optional)</FormLabel>
            <FormControl><Input {...field} /></FormControl>
            <FormMessage />
          </FormItem>
        )} />
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="manufacturer" render={({ field }) => (
            <FormItem>
              <FormLabel>Manufacturer</FormLabel>
              <FormControl>
                <Input list="vendor-list" placeholder="Dell, HPE, Cisco…" {...field} />
              </FormControl>
              <datalist id="vendor-list">
                {Array.from(new Set((catalog.data?.stencils ?? []).map((s) => s.manufacturer))).sort().map((v) => (
                  <option key={v} value={v} />
                ))}
              </datalist>
              <FormMessage />
            </FormItem>
          )} />
          <FormField control={form.control} name="model" render={({ field }) => (
            <FormItem><FormLabel>Model</FormLabel><FormControl><Input placeholder="PowerEdge R750…" {...field} /></FormControl><FormMessage /></FormItem>
          )} />
        </div>
        {vendorMatches.length > 0 && model.length === 0 && (
          <div className="flex flex-wrap gap-1.5 text-xs text-muted-foreground">
            <span className="self-center mr-1">Stencils for {manufacturer}:</span>
            {vendorMatches.slice(0, 6).map((s) => (
              <Button
                key={`${s.manufacturer}-${s.model}`}
                type="button" variant="outline" size="sm"
                onClick={() => applyStencil(s)}
              >
                <ChevronsUpDown className="h-3 w-3" /> {s.model} ({s.u}U)
              </Button>
            ))}
          </div>
        )}
        <div className="grid grid-cols-3 gap-3">
          <FormField control={form.control} name="kind" render={({ field }) => (
            <FormItem>
              <FormLabel>Kind</FormLabel>
              <Select value={field.value} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
                <SelectContent>{KINDS.map((k) => <SelectItem key={k} value={k}>{k}</SelectItem>)}</SelectContent>
              </Select>
              <FormMessage />
            </FormItem>
          )} />
          <FormField control={form.control} name="rack_position_u" render={({ field }) => (
            <FormItem>
              <FormLabel>Position U (1–{uHeight})</FormLabel>
              <FormControl><Input type="number" min={1} max={uHeight} placeholder="leave blank if unplaced" {...field} /></FormControl>
              <FormMessage />
            </FormItem>
          )} />
          <FormField control={form.control} name="rack_units" render={({ field }) => (
            <FormItem><FormLabel>Size (U)</FormLabel><FormControl><Input type="number" min={1} max={uHeight} {...field} /></FormControl><FormMessage /></FormItem>
          )} />
        </div>
        <FormField control={form.control} name="serial" render={({ field }) => (
          <FormItem><FormLabel>Serial (optional)</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        {collisions.length > 0 && (
          <p className="text-xs font-medium text-warning">Conflict: U{collisions.join(', U')} already occupied.</p>
        )}
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Adding…' : 'Add device'}
        </Button>
      </form>
    </Form>
  );
}
