// DNS tab for the IPAM page. Three sub-panels under a fabric selector:
//   1. Zones      — apex + per-site zones, with a render preview.
//   2. Records    — drill from a zone, type-specific forms, ipam-projected
//                   rows are read-only with a badge.
//   3. Servers + anycast — register CoreDNS deployments, bind recursive
//                   servers to BGP peers + an anycast group.
//
// Visual conventions mirror the existing OverlaysTab pattern in
// frontend/src/pages/ipam.tsx: fabric selector at the top, then panels.

import { useEffect, useMemo, useState } from 'react';
import { useList } from '@refinedev/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { Plus, Trash2, RefreshCw, Globe, FileText } from 'lucide-react';
import { http } from '@/lib/http';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Badge } from '@/components/ui/badge';
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
import { toast } from 'sonner';

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

export function DnsTab({ canWrite }: { canWrite: boolean }) {
  const fabricsRes = useList<Fabric>({ resource: 'ipam/fabrics', pagination: { pageSize: 200 } });
  const fabrics = fabricsRes.result.data ?? [];
  const [fabricId, setFabricId] = useState<string>('');
  const [zoneId, setZoneId] = useState<string | null>(null);

  // First fabric becomes the default once fabrics arrive.
  useEffect(() => {
    if (!fabricId && fabrics.length > 0) setFabricId(fabrics[0].id);
  }, [fabricId, fabrics]);

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end gap-3">
        <div className="space-y-1">
          <p className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Fabric</p>
          <Select value={fabricId} onValueChange={(v) => { setFabricId(v); setZoneId(null); }}>
            <SelectTrigger className="w-[260px]"><SelectValue placeholder="Pick a fabric" /></SelectTrigger>
            <SelectContent>
              {fabrics.map((f) => <SelectItem key={f.id} value={f.id}>{f.name}</SelectItem>)}
            </SelectContent>
          </Select>
        </div>
      </div>

      {fabricId && (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          <ZonesPanel
            fabricId={fabricId}
            selectedZoneId={zoneId}
            onSelectZone={setZoneId}
            canWrite={canWrite}
          />
          {zoneId
            ? <RecordsPanel zoneId={zoneId} canWrite={canWrite} />
            : (
              <Card><CardContent className="p-6 text-sm text-muted-foreground">
                Pick a zone to see its records.
              </CardContent></Card>
            )}
        </div>
      )}

      {fabricId && (
        <ServersPanel fabricId={fabricId} canWrite={canWrite} />
      )}
    </div>
  );
}

// ----------------------- Zones -----------------------

const zoneSchema = z.object({
  name: z.string().min(1),
  kind: z.enum(['apex', 'site']),
  site_id: z.string().optional(),
  default_ttl: z.string().min(1),
});

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
    <Card>
      <CardContent className="p-0">
        <div className="flex items-center justify-between border-b p-3">
          <h3 className="text-sm font-semibold flex items-center gap-2">
            <Globe className="h-4 w-4" /> Zones
          </h3>
          {canWrite && (
            <Dialog open={createOpen} onOpenChange={setCreateOpen}>
              <DialogTrigger asChild>
                <Button size="sm" variant="outline"><Plus className="h-3.5 w-3.5" /> Add zone</Button>
              </DialogTrigger>
              <DialogContent>
                <DialogHeader><DialogTitle>New DNS zone</DialogTitle></DialogHeader>
                <ZoneForm fabricId={fabricId} onSaved={async () => { setCreateOpen(false); await refresh(); }} />
              </DialogContent>
            </Dialog>
          )}
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Kind</TableHead>
              <TableHead>TTL</TableHead>
              <TableHead className="w-32" />
            </TableRow>
          </TableHeader>
          <TableBody>
            {zones.length === 0 && !zonesQ.isLoading && (
              <TableRow><TableCell colSpan={4} className="text-muted-foreground">
                No zones in this fabric yet.
              </TableCell></TableRow>
            )}
            {zones.map((z) => (
              <TableRow
                key={z.id}
                className={'cursor-pointer hover:bg-accent/40 ' + (selectedZoneId === z.id ? 'bg-accent/30' : '')}
                onClick={() => onSelectZone(z.id === selectedZoneId ? null : z.id)}
              >
                <TableCell className="font-mono">{z.name}</TableCell>
                <TableCell><Badge variant={z.kind === 'apex' ? 'default' : 'secondary'}>{z.kind}</Badge></TableCell>
                <TableCell className="font-mono">{z.default_ttl}</TableCell>
                <TableCell onClick={(e) => e.stopPropagation()}>
                  <Button size="sm" variant="ghost" onClick={() => setPreviewZone(z)} title="Preview rendered zone">
                    <FileText className="h-3.5 w-3.5" />
                  </Button>
                  {canWrite && z.kind === 'site' && (
                    <Button size="sm" variant="ghost" onClick={() => syncFromIpam(z)} title="Sync records from IPAM">
                      <RefreshCw className="h-3.5 w-3.5" />
                    </Button>
                  )}
                  {canWrite && (
                    <Button size="sm" variant="ghost" onClick={() => remove(z)} title="Delete zone">
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </CardContent>
      <Dialog
        open={previewZone !== null}
        onOpenChange={(o) => { if (!o) setPreviewZone(null); }}
      >
        <DialogContent className="max-w-3xl">
          <DialogHeader>
            <DialogTitle>Zone preview: <span className="font-mono">{previewZone?.name}</span></DialogTitle>
          </DialogHeader>
          {previewZone && <ZonePreview zoneId={previewZone.id} />}
        </DialogContent>
      </Dialog>
    </Card>
  );
}

function ZonePreview({ zoneId }: { zoneId: string }) {
  const { data, isLoading } = useQuery({
    queryKey: ['dns-zone-preview', zoneId],
    queryFn: async () => (await http.get<{ text: string; record_count: number }>(`/dns/zones/${zoneId}/preview`)).data,
  });
  if (isLoading) return <p className="text-sm text-muted-foreground">Rendering…</p>;
  return (
    <div className="space-y-2">
      <p className="text-xs text-muted-foreground">{data?.record_count} records</p>
      <pre className="max-h-[60vh] overflow-auto rounded-md border bg-muted/30 p-3 text-xs">
        {data?.text}
      </pre>
    </div>
  );
}

function ZoneForm({ fabricId, onSaved }: { fabricId: string; onSaved: () => void }) {
  const NONE = '__none__';
  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 500 } });
  const sites = sitesRes.result.data ?? [];
  const form = useForm<z.infer<typeof zoneSchema>>({
    resolver: zodResolver(zoneSchema),
    defaultValues: { name: '', kind: 'site', site_id: NONE, default_ttl: '300' },
  });
  const kind = form.watch('kind');
  async function onSubmit(v: z.infer<typeof zoneSchema>) {
    if (v.kind === 'site' && (!v.site_id || v.site_id === NONE)) {
      toast.error('Site zones require a site');
      return;
    }
    try {
      await http.post('/dns/zones', {
        name: v.name,
        kind: v.kind,
        fabric_id: fabricId,
        site_id: v.kind === 'site' && v.site_id !== NONE ? v.site_id : null,
        default_ttl: Number(v.default_ttl),
      });
      toast.success('Zone created');
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="name" render={({ field }) => (
          <FormItem>
            <FormLabel>Zone FQDN</FormLabel>
            <FormControl><Input placeholder="e.g. site42.prod.dcim.mil" className="font-mono" {...field} /></FormControl>
            <FormMessage />
          </FormItem>
        )} />
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="kind" render={({ field }) => (
            <FormItem>
              <FormLabel>Kind</FormLabel>
              <Select value={field.value} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
                <SelectContent>
                  <SelectItem value="apex">Apex (per-fabric)</SelectItem>
                  <SelectItem value="site">Site (per-site)</SelectItem>
                </SelectContent>
              </Select>
            </FormItem>
          )} />
          <FormField control={form.control} name="default_ttl" render={({ field }) => (
            <FormItem><FormLabel>Default TTL (s)</FormLabel><FormControl><Input type="number" {...field} /></FormControl><FormMessage /></FormItem>
          )} />
        </div>
        {kind === 'site' && (
          <FormField control={form.control} name="site_id" render={({ field }) => (
            <FormItem>
              <FormLabel>Site</FormLabel>
              <Select value={field.value ?? NONE} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue placeholder="Pick a site" /></SelectTrigger></FormControl>
                <SelectContent>
                  <SelectItem value={NONE}>(unassigned)</SelectItem>
                  {sites.map((s) => <SelectItem key={s.id} value={s.id}>{s.code} · {s.name}</SelectItem>)}
                </SelectContent>
              </Select>
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
    <Card>
      <CardContent className="p-0">
        <div className="flex items-center justify-between border-b p-3">
          <h3 className="text-sm font-semibold">Records</h3>
          {canWrite && (
            <Dialog open={createOpen} onOpenChange={setCreateOpen}>
              <DialogTrigger asChild>
                <Button size="sm" variant="outline"><Plus className="h-3.5 w-3.5" /> Add record</Button>
              </DialogTrigger>
              <DialogContent>
                <DialogHeader><DialogTitle>New DNS record</DialogTitle></DialogHeader>
                <RecordForm zoneId={zoneId} onSaved={async () => { setCreateOpen(false); await refresh(); }} />
              </DialogContent>
            </Dialog>
          )}
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Type</TableHead>
              <TableHead>Data</TableHead>
              <TableHead>Source</TableHead>
              {canWrite && <TableHead className="w-12" />}
            </TableRow>
          </TableHeader>
          <TableBody>
            {records.length === 0 && !recordsQ.isLoading && (
              <TableRow><TableCell colSpan={canWrite ? 5 : 4} className="text-muted-foreground">
                No records yet.
              </TableCell></TableRow>
            )}
            {records.map((r) => (
              <TableRow key={r.id}>
                <TableCell className="font-mono">{r.name || '@'}</TableCell>
                <TableCell><Badge variant="outline">{r.type}</Badge></TableCell>
                <TableCell className="font-mono text-xs">{formatRdata(r)}</TableCell>
                <TableCell>
                  {r.source === 'ipam'
                    ? <Badge variant="secondary" className="text-[10px]">from IPAM</Badge>
                    : <span className="text-xs text-muted-foreground">manual</span>}
                </TableCell>
                {canWrite && (
                  <TableCell>
                    <Button size="sm" variant="ghost" onClick={() => remove(r)} title="Delete record"
                      disabled={r.source === 'ipam'}>
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

const recordSchema = z.object({
  name: z.string().min(1),
  type: z.enum(RECORD_TYPES),
  ttl: z.string().optional(),
  // The data shape is type-dependent; we collect the union of fields and
  // pick the right ones based on `type` at submit time.
  target: z.string().optional(),
  priority: z.string().optional(),
  weight: z.string().optional(),
  port: z.string().optional(),
  text: z.string().optional(),
  flags: z.string().optional(),
  tag: z.string().optional(),
  value: z.string().optional(),
});

function RecordForm({ zoneId, onSaved }: { zoneId: string; onSaved: () => void }) {
  const form = useForm<z.infer<typeof recordSchema>>({
    resolver: zodResolver(recordSchema),
    defaultValues: { name: '@', type: 'A' },
  });
  const type = form.watch('type');

  function buildData(v: z.infer<typeof recordSchema>): Record<string, any> {
    switch (v.type) {
      case 'A':
      case 'AAAA':
      case 'CNAME':
      case 'NS':
      case 'PTR':
        return { target: v.target ?? '' };
      case 'MX':
        return { priority: Number(v.priority ?? 10), target: v.target ?? '' };
      case 'TXT':
        return { text: v.text ?? '' };
      case 'SRV':
        return {
          priority: Number(v.priority ?? 0),
          weight: Number(v.weight ?? 0),
          port: Number(v.port ?? 0),
          target: v.target ?? '',
        };
      case 'CAA':
        return {
          flags: Number(v.flags ?? 0),
          tag: v.tag ?? 'issue',
          value: v.value ?? '',
        };
    }
  }

  async function onSubmit(v: z.infer<typeof recordSchema>) {
    try {
      await http.post('/dns/records', {
        zone_id: zoneId,
        name: v.name,
        type: v.type,
        ttl: v.ttl ? Number(v.ttl) : null,
        data: buildData(v),
      });
      toast.success('Record created');
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <div className="grid grid-cols-3 gap-3">
          <FormField control={form.control} name="name" render={({ field }) => (
            <FormItem><FormLabel>Name</FormLabel><FormControl><Input placeholder="@ or leaf-01" className="font-mono" {...field} /></FormControl><FormMessage /></FormItem>
          )} />
          <FormField control={form.control} name="type" render={({ field }) => (
            <FormItem>
              <FormLabel>Type</FormLabel>
              <Select value={field.value} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
                <SelectContent>
                  {RECORD_TYPES.map((t) => <SelectItem key={t} value={t}>{t}</SelectItem>)}
                </SelectContent>
              </Select>
            </FormItem>
          )} />
          <FormField control={form.control} name="ttl" render={({ field }) => (
            <FormItem><FormLabel>TTL (s, optional)</FormLabel><FormControl><Input type="number" {...field} /></FormControl><FormMessage /></FormItem>
          )} />
        </div>
        {/* Type-specific fields. We render only what the chosen record type needs. */}
        {(['A', 'AAAA', 'CNAME', 'NS', 'PTR'] as const).includes(type as any) && (
          <FormField control={form.control} name="target" render={({ field }) => (
            <FormItem><FormLabel>{type === 'A' || type === 'AAAA' ? 'IP address' : 'Target FQDN'}</FormLabel><FormControl><Input className="font-mono" {...field} /></FormControl><FormMessage /></FormItem>
          )} />
        )}
        {type === 'MX' && (
          <div className="grid grid-cols-2 gap-3">
            <FormField control={form.control} name="priority" render={({ field }) => (
              <FormItem><FormLabel>Priority</FormLabel><FormControl><Input type="number" placeholder="10" {...field} /></FormControl><FormMessage /></FormItem>
            )} />
            <FormField control={form.control} name="target" render={({ field }) => (
              <FormItem><FormLabel>Mail server FQDN</FormLabel><FormControl><Input className="font-mono" {...field} /></FormControl><FormMessage /></FormItem>
            )} />
          </div>
        )}
        {type === 'TXT' && (
          <FormField control={form.control} name="text" render={({ field }) => (
            <FormItem><FormLabel>Text (no surrounding quotes)</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
          )} />
        )}
        {type === 'SRV' && (
          <div className="grid grid-cols-4 gap-3">
            <FormField control={form.control} name="priority" render={({ field }) => (
              <FormItem><FormLabel>Priority</FormLabel><FormControl><Input type="number" {...field} /></FormControl><FormMessage /></FormItem>
            )} />
            <FormField control={form.control} name="weight" render={({ field }) => (
              <FormItem><FormLabel>Weight</FormLabel><FormControl><Input type="number" {...field} /></FormControl><FormMessage /></FormItem>
            )} />
            <FormField control={form.control} name="port" render={({ field }) => (
              <FormItem><FormLabel>Port</FormLabel><FormControl><Input type="number" {...field} /></FormControl><FormMessage /></FormItem>
            )} />
            <FormField control={form.control} name="target" render={({ field }) => (
              <FormItem><FormLabel>Target</FormLabel><FormControl><Input className="font-mono" {...field} /></FormControl><FormMessage /></FormItem>
            )} />
          </div>
        )}
        {type === 'CAA' && (
          <div className="grid grid-cols-3 gap-3">
            <FormField control={form.control} name="flags" render={({ field }) => (
              <FormItem><FormLabel>Flags</FormLabel><FormControl><Input type="number" placeholder="0" {...field} /></FormControl><FormMessage /></FormItem>
            )} />
            <FormField control={form.control} name="tag" render={({ field }) => (
              <FormItem><FormLabel>Tag</FormLabel><FormControl><Input placeholder="issue" {...field} /></FormControl><FormMessage /></FormItem>
            )} />
            <FormField control={form.control} name="value" render={({ field }) => (
              <FormItem><FormLabel>Value</FormLabel><FormControl><Input className="font-mono" {...field} /></FormControl><FormMessage /></FormItem>
            )} />
          </div>
        )}
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : 'Create'}
        </Button>
      </form>
    </Form>
  );
}

// ----------------------- Servers + anycast -----------------------

const serverSchema = z.object({
  name: z.string().min(1),
  site_id: z.string().min(1),
  role: z.enum(['auth', 'recursive']),
  unicast_ip: z.string().min(1),
  anycast_group_id: z.string().optional(),
});

const anycastSchema = z.object({
  name: z.string().min(1),
  service: z.enum(['dns_recursive', 'ntp', 'log']),
  anycast_ipv4: z.string().optional(),
  anycast_ipv6: z.string().optional(),
});

const bgpSchema = z.object({
  name: z.string().min(1),
  site_id: z.string().min(1),
  local_asn: z.string().min(1),
  peer_asn: z.string().min(1),
  peer_ip: z.string().min(1),
  md5_password: z.string().optional(),
});

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
  const [anycastOpen, setAnycastOpen] = useState(false);
  const [bgpOpen, setBgpOpen] = useState(false);

  return (
    <Card>
      <CardContent className="p-0">
        <div className="flex items-center justify-between border-b p-3">
          <h3 className="text-sm font-semibold">DNS servers + anycast</h3>
          {canWrite && (
            <div className="flex gap-2">
              <Dialog open={anycastOpen} onOpenChange={setAnycastOpen}>
                <DialogTrigger asChild>
                  <Button size="sm" variant="outline"><Plus className="h-3.5 w-3.5" /> Anycast group</Button>
                </DialogTrigger>
                <DialogContent>
                  <DialogHeader><DialogTitle>New anycast group</DialogTitle></DialogHeader>
                  <AnycastForm fabricId={fabricId} onSaved={async () => { setAnycastOpen(false); await qc.invalidateQueries({ queryKey: ['anycast-groups', fabricId] }); }} />
                </DialogContent>
              </Dialog>
              <Dialog open={bgpOpen} onOpenChange={setBgpOpen}>
                <DialogTrigger asChild>
                  <Button size="sm" variant="outline"><Plus className="h-3.5 w-3.5" /> BGP peer</Button>
                </DialogTrigger>
                <DialogContent>
                  <DialogHeader><DialogTitle>New BGP peer</DialogTitle></DialogHeader>
                  <BgpPeerForm sites={sites} onSaved={async () => { setBgpOpen(false); await qc.invalidateQueries({ queryKey: ['bgp-peers'] }); }} />
                </DialogContent>
              </Dialog>
              <Dialog open={serverOpen} onOpenChange={setServerOpen}>
                <DialogTrigger asChild>
                  <Button size="sm"><Plus className="h-3.5 w-3.5" /> DNS server</Button>
                </DialogTrigger>
                <DialogContent>
                  <DialogHeader><DialogTitle>New DNS server</DialogTitle></DialogHeader>
                  <ServerForm fabricId={fabricId} sites={sites} anycast={anycast} onSaved={async () => { setServerOpen(false); await qc.invalidateQueries({ queryKey: ['dns-servers', fabricId] }); }} />
                </DialogContent>
              </Dialog>
            </div>
          )}
        </div>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Name</TableHead>
              <TableHead>Site</TableHead>
              <TableHead>Role</TableHead>
              <TableHead>Unicast IP</TableHead>
              <TableHead>Anycast</TableHead>
              <TableHead>Last render</TableHead>
              <TableHead>BGP peers</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {servers.length === 0 && !serversQ.isLoading && (
              <TableRow><TableCell colSpan={7} className="text-muted-foreground">No DNS servers yet.</TableCell></TableRow>
            )}
            {servers.map((s) => {
              const ag = anycast.find((a) => a.id === s.anycast_group_id);
              return (
                <TableRow key={s.id}>
                  <TableCell className="font-medium">{s.name}</TableCell>
                  <TableCell className="text-xs">{sitesById.get(s.site_id)?.code ?? s.site_id.slice(0, 8) + '…'}</TableCell>
                  <TableCell><Badge variant={s.role === 'recursive' ? 'default' : 'secondary'}>{s.role}</Badge></TableCell>
                  <TableCell className="font-mono text-xs">{s.unicast_ip}</TableCell>
                  <TableCell className="font-mono text-xs">
                    {ag ? `${ag.anycast_ipv4 ?? ''}${ag.anycast_ipv6 ? ` / ${ag.anycast_ipv6}` : ''}` : '—'}
                  </TableCell>
                  <TableCell>
                    <RenderStatusBadge server={s} />
                  </TableCell>
                  <TableCell>
                    {s.role === 'recursive'
                      ? <BindingsCell server={s} peers={peers} canWrite={canWrite} />
                      : <span className="text-xs text-muted-foreground">—</span>}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  );
}

function RenderStatusBadge({ server }: { server: DnsServer }) {
  if (!server.last_render_at) return <span className="text-xs text-muted-foreground">never</span>;
  const ok = server.last_render_status === 'ok';
  return (
    <span className="inline-flex items-center gap-2">
      <Badge variant={ok ? 'success' as any : 'destructive'} className="text-[10px]">
        {server.last_render_status}
      </Badge>
      <span className="text-[10px] text-muted-foreground" title={server.last_render_error ?? ''}>
        {new Date(server.last_render_at).toLocaleString()}
      </span>
    </span>
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

  return (
    <div className="space-y-1">
      {bindings.map((b) => {
        const p = peers.find((x) => x.id === b.bgp_peer_id);
        return (
          <div key={b.id} className="flex items-center gap-1 text-xs">
            <span className="font-mono">{p?.peer_ip ?? b.bgp_peer_id.slice(0, 8) + '…'}</span>
            {p && <span className="text-muted-foreground">AS{p.peer_asn}</span>}
            {canWrite && (
              <Button size="sm" variant="ghost" className="h-5 w-5 p-0" onClick={() => remove(b.id)}>
                <Trash2 className="h-3 w-3" />
              </Button>
            )}
          </div>
        );
      })}
      {canWrite && sitePeers.length > bindings.length && (
        <Select value="" onValueChange={(v) => add(v)}>
          <SelectTrigger className="h-7 text-xs"><SelectValue placeholder="+ add peer" /></SelectTrigger>
          <SelectContent>
            {sitePeers
              .filter((p) => !bindings.some((b) => b.bgp_peer_id === p.id))
              .map((p) => <SelectItem key={p.id} value={p.id}>{p.peer_ip} (AS{p.peer_asn})</SelectItem>)}
          </SelectContent>
        </Select>
      )}
    </div>
  );
}

function ServerForm({
  fabricId, sites, anycast, onSaved,
}: { fabricId: string; sites: Site[]; anycast: AnycastGroup[]; onSaved: () => void }) {
  const NONE = '__none__';
  const form = useForm<z.infer<typeof serverSchema>>({
    resolver: zodResolver(serverSchema),
    defaultValues: { name: '', site_id: '', role: 'auth', unicast_ip: '', anycast_group_id: NONE },
  });
  const role = form.watch('role');
  async function onSubmit(v: z.infer<typeof serverSchema>) {
    try {
      await http.post('/dns/servers', {
        name: v.name,
        site_id: v.site_id,
        fabric_id: fabricId,
        role: v.role,
        unicast_ip: v.unicast_ip,
        anycast_group_id: v.role === 'recursive' && v.anycast_group_id !== NONE ? v.anycast_group_id : null,
      });
      toast.success('DNS server created');
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="name" render={({ field }) => (
          <FormItem><FormLabel>Name</FormLabel><FormControl><Input placeholder="e.g. site42-coredns-auth" {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="site_id" render={({ field }) => (
            <FormItem>
              <FormLabel>Site</FormLabel>
              <Select value={field.value} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue placeholder="Pick a site" /></SelectTrigger></FormControl>
                <SelectContent>
                  {sites.map((s) => <SelectItem key={s.id} value={s.id}>{s.code} · {s.name}</SelectItem>)}
                </SelectContent>
              </Select>
              <FormMessage />
            </FormItem>
          )} />
          <FormField control={form.control} name="role" render={({ field }) => (
            <FormItem>
              <FormLabel>Role</FormLabel>
              <Select value={field.value} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
                <SelectContent>
                  <SelectItem value="auth">Authoritative</SelectItem>
                  <SelectItem value="recursive">Recursive</SelectItem>
                </SelectContent>
              </Select>
            </FormItem>
          )} />
        </div>
        <FormField control={form.control} name="unicast_ip" render={({ field }) => (
          <FormItem><FormLabel>Unicast (mgmt) IP</FormLabel><FormControl><Input className="font-mono" placeholder="10.42.0.53" {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        {role === 'recursive' && (
          <FormField control={form.control} name="anycast_group_id" render={({ field }) => (
            <FormItem>
              <FormLabel>Anycast group</FormLabel>
              <Select value={field.value ?? NONE} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue placeholder="Pick an anycast group" /></SelectTrigger></FormControl>
                <SelectContent>
                  <SelectItem value={NONE}>(none)</SelectItem>
                  {anycast.map((a) => <SelectItem key={a.id} value={a.id}>{a.name} ({a.anycast_ipv4 ?? a.anycast_ipv6})</SelectItem>)}
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground">Recursive servers must bind an anycast group.</p>
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

function AnycastForm({ fabricId, onSaved }: { fabricId: string; onSaved: () => void }) {
  const form = useForm<z.infer<typeof anycastSchema>>({
    resolver: zodResolver(anycastSchema),
    defaultValues: { name: '', service: 'dns_recursive', anycast_ipv4: '', anycast_ipv6: '' },
  });
  async function onSubmit(v: z.infer<typeof anycastSchema>) {
    try {
      await http.post('/dns/anycast-groups', {
        name: v.name, fabric_id: fabricId, service: v.service,
        anycast_ipv4: v.anycast_ipv4 || null,
        anycast_ipv6: v.anycast_ipv6 || null,
      });
      toast.success('Anycast group created');
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="name" render={({ field }) => (
          <FormItem><FormLabel>Name</FormLabel><FormControl><Input placeholder="e.g. prod-dns-recursive" {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <FormField control={form.control} name="service" render={({ field }) => (
          <FormItem>
            <FormLabel>Service</FormLabel>
            <Select value={field.value} onValueChange={field.onChange}>
              <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
              <SelectContent>
                <SelectItem value="dns_recursive">DNS recursive</SelectItem>
                <SelectItem value="ntp">NTP (reserved)</SelectItem>
                <SelectItem value="log">Log (reserved)</SelectItem>
              </SelectContent>
            </Select>
          </FormItem>
        )} />
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="anycast_ipv4" render={({ field }) => (
            <FormItem><FormLabel>Anycast IPv4 (optional)</FormLabel><FormControl><Input className="font-mono" placeholder="10.255.0.53" {...field} /></FormControl><FormMessage /></FormItem>
          )} />
          <FormField control={form.control} name="anycast_ipv6" render={({ field }) => (
            <FormItem><FormLabel>Anycast IPv6 (optional)</FormLabel><FormControl><Input className="font-mono" placeholder="2001:db8::53" {...field} /></FormControl><FormMessage /></FormItem>
          )} />
        </div>
        <p className="text-xs text-muted-foreground">At least one of v4 / v6 must be set.</p>
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : 'Create'}
        </Button>
      </form>
    </Form>
  );
}

function BgpPeerForm({ sites, onSaved }: { sites: Site[]; onSaved: () => void }) {
  const form = useForm<z.infer<typeof bgpSchema>>({
    resolver: zodResolver(bgpSchema),
    defaultValues: { name: '', site_id: '', local_asn: '65000', peer_asn: '65001', peer_ip: '', md5_password: '' },
  });
  async function onSubmit(v: z.infer<typeof bgpSchema>) {
    try {
      await http.post('/dns/bgp-peers', {
        name: v.name,
        site_id: v.site_id,
        local_asn: Number(v.local_asn),
        peer_asn: Number(v.peer_asn),
        peer_ip: v.peer_ip,
        md5_password: v.md5_password || null,
      });
      toast.success('BGP peer created');
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="name" render={({ field }) => (
          <FormItem><FormLabel>Name</FormLabel><FormControl><Input placeholder="e.g. site42-leaf-01" {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <FormField control={form.control} name="site_id" render={({ field }) => (
          <FormItem>
            <FormLabel>Site</FormLabel>
            <Select value={field.value} onValueChange={field.onChange}>
              <FormControl><SelectTrigger><SelectValue placeholder="Pick a site" /></SelectTrigger></FormControl>
              <SelectContent>
                {sites.map((s) => <SelectItem key={s.id} value={s.id}>{s.code} · {s.name}</SelectItem>)}
              </SelectContent>
            </Select>
            <FormMessage />
          </FormItem>
        )} />
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="local_asn" render={({ field }) => (
            <FormItem><FormLabel>Local AS</FormLabel><FormControl><Input type="number" {...field} /></FormControl><FormMessage /></FormItem>
          )} />
          <FormField control={form.control} name="peer_asn" render={({ field }) => (
            <FormItem><FormLabel>Peer AS</FormLabel><FormControl><Input type="number" {...field} /></FormControl><FormMessage /></FormItem>
          )} />
        </div>
        <FormField control={form.control} name="peer_ip" render={({ field }) => (
          <FormItem><FormLabel>Peer IP</FormLabel><FormControl><Input className="font-mono" placeholder="10.42.255.1" {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <FormField control={form.control} name="md5_password" render={({ field }) => (
          <FormItem><FormLabel>MD5 password (optional)</FormLabel><FormControl><Input type="password" {...field} /></FormControl><FormMessage /></FormItem>
        )} />
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : 'Create'}
        </Button>
      </form>
    </Form>
  );
}
