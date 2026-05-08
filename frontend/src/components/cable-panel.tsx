import { useMemo, useState } from 'react';
import { useList, useUpdate, useDelete } from '@refinedev/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { Cable as CableIcon, Plus, Pencil, Trash2 } from 'lucide-react';
import { toast } from 'sonner';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { Badge } from '@/components/ui/badge';
import {
  Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog';
import {
  Form, FormControl, FormField, FormItem, FormLabel, FormMessage,
} from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select';
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table';
import { http } from '@/lib/http';
import { hasCapability } from '@/lib/access-control-provider';

type Cable = {
  id: string;
  site_id: string;
  a_asset_id: string;
  a_port: string | null;
  b_asset_id: string;
  b_port: string | null;
  medium: string | null;
  color: string | null;
  length_m: number | null;
  label: string | null;
};
type Asset = { id: string; name: string; kind: string; site_id: string; rack_id: string | null };

const COMMON_MEDIA = ['cat6', 'cat6a', 'smf', 'mmf', 'dac', 'aoc', 'power-c13', 'power-c19'];
const COMMON_COLORS = ['blue', 'yellow', 'red', 'green', 'orange', 'white', 'black', 'gray'];

const cableSchema = z.object({
  a_asset_id: z.string().uuid('Pick an A-end asset'),
  a_port: z.string().optional(),
  b_asset_id: z.string().uuid('Pick a B-end asset'),
  b_port: z.string().optional(),
  medium: z.string().optional(),
  color: z.string().optional(),
  length_m: z.string().optional(),
  label: z.string().optional(),
}).refine((v) => v.a_asset_id !== v.b_asset_id, {
  path: ['b_asset_id'], message: 'A-end and B-end must differ',
});
type CableForm = z.infer<typeof cableSchema>;

type Props = {
  rackId: string;
  siteId: string;
  rackAssets: { id: string; name: string; kind: string }[];
};

export function CablePanel({ rackId, siteId, rackAssets }: Props) {
  const canWrite = hasCapability('inventory:write');
  const qc = useQueryClient();
  const [dialogOpen, setDialogOpen] = useState(false);
  const [editing, setEditing] = useState<Cable | null>(null);

  const cablesRes = useQuery({
    queryKey: ['rack-cables', rackId],
    queryFn: async () => {
      const r = await http.get<{ items: Cable[] }>('/inventory/cables', {
        params: { rack_id: rackId, page_size: 500 },
      });
      return r.data.items ?? [];
    },
    enabled: !!rackId,
  });

  const cables = cablesRes.data ?? [];
  const otherEndIds = useMemo(() => {
    const local = new Set(rackAssets.map((a) => a.id));
    const ids = new Set<string>();
    for (const c of cables) {
      if (!local.has(c.a_asset_id)) ids.add(c.a_asset_id);
      if (!local.has(c.b_asset_id)) ids.add(c.b_asset_id);
    }
    return Array.from(ids);
  }, [cables, rackAssets]);

  // Hydrate names for endpoints that live outside this rack.
  const remoteRes = useQuery({
    queryKey: ['cable-remote-endpoints', otherEndIds.sort().join(',')],
    queryFn: async () => {
      if (otherEndIds.length === 0) return [] as Asset[];
      const results = await Promise.all(
        otherEndIds.map((id) => http.get<Asset>(`/inventory/assets/${id}`).then((r) => r.data)),
      );
      return results;
    },
    enabled: otherEndIds.length > 0,
  });

  const assetById = useMemo(() => {
    const m = new Map<string, { id: string; name: string; kind: string }>();
    for (const a of rackAssets) m.set(a.id, a);
    for (const a of remoteRes.data ?? []) m.set(a.id, { id: a.id, name: a.name, kind: a.kind });
    return m;
  }, [rackAssets, remoteRes.data]);

  const deleteMutation = useDelete();
  function onDelete(c: Cable) {
    if (!confirm(`Delete cable${c.label ? ` "${c.label}"` : ''}?`)) return;
    deleteMutation.mutate(
      { resource: 'inventory/cables', id: c.id, successNotification: false },
      {
        onSuccess: () => {
          toast.success('Cable removed');
          qc.invalidateQueries({ queryKey: ['rack-cables', rackId] });
        },
        onError: (err: any) => toast.error(err?.message ?? 'Delete failed'),
      },
    );
  }

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between">
        <CardTitle className="flex items-center gap-2 text-base">
          <CableIcon className="h-4 w-4" /> Cables ({cables.length})
        </CardTitle>
        {canWrite && (
          <Button size="sm" onClick={() => { setEditing(null); setDialogOpen(true); }}>
            <Plus className="h-4 w-4" /> Add cable
          </Button>
        )}
      </CardHeader>
      <CardContent className="p-0">
        {cablesRes.isLoading ? (
          <div className="space-y-2 p-6">
            <Skeleton className="h-5 w-full" />
            <Skeleton className="h-5 w-full" />
          </div>
        ) : cables.length === 0 ? (
          <p className="p-6 text-sm text-muted-foreground">No cables logged for this rack yet.</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>A-end</TableHead>
                <TableHead>A port</TableHead>
                <TableHead>B-end</TableHead>
                <TableHead>B port</TableHead>
                <TableHead>Medium</TableHead>
                <TableHead>Color</TableHead>
                <TableHead className="w-16 text-right">Len (m)</TableHead>
                <TableHead>Label</TableHead>
                {canWrite && <TableHead className="w-20" />}
              </TableRow>
            </TableHeader>
            <TableBody>
              {cables.map((c) => {
                const a = assetById.get(c.a_asset_id);
                const b = assetById.get(c.b_asset_id);
                return (
                  <TableRow key={c.id}>
                    <TableCell className="font-medium">{a?.name ?? c.a_asset_id.slice(0, 8)}</TableCell>
                    <TableCell className="font-mono text-xs">{c.a_port ?? '—'}</TableCell>
                    <TableCell className="font-medium">{b?.name ?? c.b_asset_id.slice(0, 8)}</TableCell>
                    <TableCell className="font-mono text-xs">{c.b_port ?? '—'}</TableCell>
                    <TableCell>{c.medium ? <Badge variant="secondary">{c.medium}</Badge> : '—'}</TableCell>
                    <TableCell>
                      {c.color ? (
                        <span className="flex items-center gap-1.5 text-xs">
                          <span
                            className="h-3 w-3 rounded-sm border border-border"
                            style={{ background: c.color }}
                          />
                          {c.color}
                        </span>
                      ) : '—'}
                    </TableCell>
                    <TableCell className="text-right tabular-nums">{c.length_m ?? '—'}</TableCell>
                    <TableCell className="text-muted-foreground">{c.label ?? '—'}</TableCell>
                    {canWrite && (
                      <TableCell className="text-right">
                        <Button size="sm" variant="ghost" onClick={() => { setEditing(c); setDialogOpen(true); }}>
                          <Pencil className="h-3.5 w-3.5" />
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => onDelete(c)}>
                          <Trash2 className="h-3.5 w-3.5" />
                        </Button>
                      </TableCell>
                    )}
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>
      <CableDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        siteId={siteId}
        rackAssets={rackAssets}
        editing={editing}
        onSaved={() => qc.invalidateQueries({ queryKey: ['rack-cables', rackId] })}
      />
    </Card>
  );
}

function CableDialog({
  open, onOpenChange, siteId, rackAssets, editing, onSaved,
}: {
  open: boolean;
  onOpenChange: (o: boolean) => void;
  siteId: string;
  rackAssets: { id: string; name: string; kind: string }[];
  editing: Cable | null;
  onSaved: () => void;
}) {
  const form = useForm<CableForm>({
    resolver: zodResolver(cableSchema),
    defaultValues: {
      a_asset_id: editing?.a_asset_id ?? rackAssets[0]?.id ?? '',
      a_port: editing?.a_port ?? '',
      b_asset_id: editing?.b_asset_id ?? rackAssets[1]?.id ?? '',
      b_port: editing?.b_port ?? '',
      medium: editing?.medium ?? '',
      color: editing?.color ?? '',
      length_m: editing?.length_m != null ? String(editing.length_m) : '',
      label: editing?.label ?? '',
    },
    values: editing
      ? {
        a_asset_id: editing.a_asset_id,
        a_port: editing.a_port ?? '',
        b_asset_id: editing.b_asset_id,
        b_port: editing.b_port ?? '',
        medium: editing.medium ?? '',
        color: editing.color ?? '',
        length_m: editing.length_m != null ? String(editing.length_m) : '',
        label: editing.label ?? '',
      }
      : undefined,
  });

  // Site-wide asset list for cross-rack cabling. The form is opened from a
  // specific rack, so we default endpoint pickers to local-rack assets but let
  // either side reach to any asset in the same site.
  const siteAssetsRes = useList<Asset>({
    resource: 'inventory/assets',
    pagination: { pageSize: 500 },
    filters: [{ field: 'site_id', operator: 'eq', value: siteId }],
    queryOptions: { enabled: open && !!siteId },
  });
  const siteAssets = siteAssetsRes.result.data ?? [];

  const updateMutation = useUpdate();

  async function onSubmit(v: CableForm) {
    const payload = {
      a_asset_id: v.a_asset_id,
      a_port: v.a_port || null,
      b_asset_id: v.b_asset_id,
      b_port: v.b_port || null,
      medium: v.medium || null,
      color: v.color || null,
      length_m: v.length_m ? Number(v.length_m) : null,
      label: v.label || null,
    };
    try {
      if (editing) {
        await new Promise<void>((resolve, reject) => {
          updateMutation.mutate(
            { resource: 'inventory/cables', id: editing.id, values: payload, successNotification: false },
            { onSuccess: () => resolve(), onError: (e) => reject(e) },
          );
        });
        toast.success('Cable updated');
      } else {
        await http.post('/inventory/cables', { ...payload, site_id: siteId });
        toast.success('Cable added');
      }
      onOpenChange(false);
      onSaved();
    } catch (err: any) {
      toast.error(err?.message ?? 'Save failed');
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>{editing ? 'Edit cable' : 'Add cable'}</DialogTitle>
        </DialogHeader>
        <Form {...form}>
          <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
            <div className="grid grid-cols-2 gap-3">
              <FormField control={form.control} name="a_asset_id" render={({ field }) => (
                <FormItem>
                  <FormLabel>A-end asset</FormLabel>
                  <Select value={field.value} onValueChange={field.onChange}>
                    <FormControl><SelectTrigger><SelectValue placeholder="Pick A-end" /></SelectTrigger></FormControl>
                    <SelectContent>
                      <AssetGroup label="In this rack" assets={rackAssets} />
                      <AssetGroup
                        label="Other in site"
                        assets={siteAssets.filter((a) => !rackAssets.some((r) => r.id === a.id))}
                      />
                    </SelectContent>
                  </Select>
                  <FormMessage />
                </FormItem>
              )} />
              <FormField control={form.control} name="a_port" render={({ field }) => (
                <FormItem>
                  <FormLabel>A port</FormLabel>
                  <FormControl><Input placeholder="e.g. eth0, Gi0/24, port-1" {...field} /></FormControl>
                  <FormMessage />
                </FormItem>
              )} />
              <FormField control={form.control} name="b_asset_id" render={({ field }) => (
                <FormItem>
                  <FormLabel>B-end asset</FormLabel>
                  <Select value={field.value} onValueChange={field.onChange}>
                    <FormControl><SelectTrigger><SelectValue placeholder="Pick B-end" /></SelectTrigger></FormControl>
                    <SelectContent>
                      <AssetGroup label="In this rack" assets={rackAssets} />
                      <AssetGroup
                        label="Other in site"
                        assets={siteAssets.filter((a) => !rackAssets.some((r) => r.id === a.id))}
                      />
                    </SelectContent>
                  </Select>
                  <FormMessage />
                </FormItem>
              )} />
              <FormField control={form.control} name="b_port" render={({ field }) => (
                <FormItem>
                  <FormLabel>B port</FormLabel>
                  <FormControl><Input placeholder="e.g. Gi1/0/12" {...field} /></FormControl>
                  <FormMessage />
                </FormItem>
              )} />
            </div>
            <div className="grid grid-cols-3 gap-3">
              <FormField control={form.control} name="medium" render={({ field }) => (
                <FormItem>
                  <FormLabel>Medium</FormLabel>
                  <FormControl>
                    <Input list="cable-media" placeholder="cat6, smf…" {...field} />
                  </FormControl>
                  <datalist id="cable-media">
                    {COMMON_MEDIA.map((m) => <option key={m} value={m} />)}
                  </datalist>
                  <FormMessage />
                </FormItem>
              )} />
              <FormField control={form.control} name="color" render={({ field }) => (
                <FormItem>
                  <FormLabel>Color</FormLabel>
                  <FormControl>
                    <Input list="cable-colors" placeholder="blue, yellow…" {...field} />
                  </FormControl>
                  <datalist id="cable-colors">
                    {COMMON_COLORS.map((c) => <option key={c} value={c} />)}
                  </datalist>
                  <FormMessage />
                </FormItem>
              )} />
              <FormField control={form.control} name="length_m" render={({ field }) => (
                <FormItem>
                  <FormLabel>Length (m)</FormLabel>
                  <FormControl><Input type="number" step="0.1" min={0} {...field} /></FormControl>
                  <FormMessage />
                </FormItem>
              )} />
            </div>
            <FormField control={form.control} name="label" render={({ field }) => (
              <FormItem>
                <FormLabel>Label (optional)</FormLabel>
                <FormControl><Input placeholder="e.g. CAB-001" {...field} /></FormControl>
                <FormMessage />
              </FormItem>
            )} />
            <DialogFooter>
              <Button type="button" variant="ghost" onClick={() => onOpenChange(false)}>Cancel</Button>
              <Button type="submit" disabled={form.formState.isSubmitting}>
                {form.formState.isSubmitting ? 'Saving…' : editing ? 'Save' : 'Add cable'}
              </Button>
            </DialogFooter>
          </form>
        </Form>
      </DialogContent>
    </Dialog>
  );
}

function AssetGroup({ label, assets }: { label: string; assets: { id: string; name: string; kind: string }[] }) {
  if (assets.length === 0) return null;
  return (
    <>
      <div className="px-2 py-1 text-[10px] uppercase tracking-wider text-muted-foreground">{label}</div>
      {assets.map((a) => (
        <SelectItem key={a.id} value={a.id}>
          {a.name} <span className="text-muted-foreground">· {a.kind}</span>
        </SelectItem>
      ))}
    </>
  );
}
