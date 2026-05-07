import { useMemo, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { Zap, Plug, Plus, Trash2, AlertTriangle, ShieldCheck, ShieldAlert, ShieldOff } from 'lucide-react';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger,
} from '@/components/ui/dialog';
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select';
import { Label } from '@/components/ui/label';
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table';
import { http } from '@/lib/http';
import { hasCapability } from '@/lib/access-control-provider';
import { cn } from '@/lib/utils';
import { toast } from 'sonner';

export type PowerChainAsset = {
  id: string;
  name: string;
  kind: string;
  pdu_side?: 'A' | 'B' | 'C' | null;
  psu_count?: number | null;
  redundancy?: 'redundant' | 'single' | 'unpowered' | 'n/a';
};

export type PduSummary = {
  id: string;
  name: string;
  side: 'A' | 'B' | 'C' | null;
  mount: string;
  face: string;
  total_outlets: number;
  used_outlets: number;
};

export type PerAsset = {
  sides_covered: string[];
  connections: {
    pdu_id: string; pdu_name: string; pdu_side: string | null;
    outlet_id: string; outlet_position: number; outlet_label: string | null;
    psu_index: number;
  }[];
  redundancy: 'redundant' | 'single' | 'unpowered' | 'n/a';
};

type Props = {
  rackId: string;
  pdus: PduSummary[];
  perAsset: Record<string, PerAsset>;
  assets: PowerChainAsset[];
};

export function PowerChainPanel({ rackId, pdus, perAsset, assets }: Props) {
  const canWrite = hasCapability('inventory:write');
  const nonPdus = assets.filter((a) => a.kind !== 'pdu');

  // Roll up redundancy counts for the summary header
  const counts = useMemo(() => {
    const c = { redundant: 0, single: 0, unpowered: 0, total: nonPdus.length };
    for (const a of nonPdus) {
      const r = perAsset[a.id]?.redundancy;
      if (r === 'redundant') c.redundant++;
      else if (r === 'single') c.single++;
      else c.unpowered++;
    }
    return c;
  }, [nonPdus, perAsset]);

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-3">
        <div className="flex items-center gap-2">
          <CardTitle className="flex items-center gap-2 text-base">
            <Zap className="h-4 w-4" /> Power chain
          </CardTitle>
        </div>
        <div className="flex items-center gap-3 text-xs">
          <Counter icon={ShieldCheck} tone="success" label="Redundant" n={counts.redundant} />
          <Counter icon={ShieldAlert} tone="warning" label="Single" n={counts.single} />
          <Counter icon={ShieldOff} tone="critical" label="Unpowered" n={counts.unpowered} />
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        <PduStrip pdus={pdus} />

        {nonPdus.length === 0 ? (
          <p className="text-sm text-muted-foreground">No powered devices in this rack yet.</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Device</TableHead>
                <TableHead className="w-32">Redundancy</TableHead>
                <TableHead>Power feeds</TableHead>
                {canWrite && <TableHead className="w-24"></TableHead>}
              </TableRow>
            </TableHeader>
            <TableBody>
              {nonPdus.map((a) => {
                const pa = perAsset[a.id] ?? { sides_covered: [], connections: [], redundancy: 'unpowered' as const };
                return (
                  <DeviceRow
                    key={a.id}
                    rackId={rackId}
                    asset={a}
                    pdus={pdus}
                    chain={pa}
                    canWrite={canWrite}
                  />
                );
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}

function Counter({ icon: Icon, tone, label, n }: {
  icon: React.ComponentType<{ className?: string }>;
  tone: 'success' | 'warning' | 'critical';
  label: string;
  n: number;
}) {
  const cls = tone === 'success' ? 'text-success' : tone === 'warning' ? 'text-warning' : 'text-destructive';
  return (
    <span className={cn('flex items-center gap-1 font-medium', cls)}>
      <Icon className="h-3.5 w-3.5" /> {n} {label}
    </span>
  );
}

function PduStrip({ pdus }: { pdus: PduSummary[] }) {
  if (pdus.length === 0) {
    return <p className="text-xs text-muted-foreground">No PDUs in this rack.</p>;
  }
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
      {pdus.map((p) => {
        const sideColor =
          p.side === 'A' ? 'bg-blue-500/15 text-blue-300 border-blue-500/40' :
          p.side === 'B' ? 'bg-red-500/15 text-red-300 border-red-500/40' :
          p.side === 'C' ? 'bg-purple-500/15 text-purple-300 border-purple-500/40' :
          'bg-muted text-muted-foreground border-border';
        return (
          <div key={p.id} className={cn('rounded-md border p-2', sideColor)}>
            <div className="flex items-center justify-between text-xs">
              <span className="font-mono font-semibold">{p.name}</span>
              {p.side && <Badge variant="outline" className="border-current text-current">Side {p.side}</Badge>}
            </div>
            <div className="mt-1 flex items-center justify-between text-[10px] opacity-90">
              <span>{p.mount === 'rack' ? '1U mount' : p.mount === 'vertical-left' ? '0U vertical (left)' : p.mount === 'vertical-right' ? '0U vertical (right)' : p.mount}</span>
              <span>{p.face}</span>
            </div>
            <div className="mt-2 text-[10px] tabular-nums">
              {p.used_outlets} / {p.total_outlets} outlets used
            </div>
            <div className="mt-1 h-1 overflow-hidden rounded-full bg-black/40">
              <div
                className="h-full bg-current"
                style={{ width: p.total_outlets ? `${(p.used_outlets / p.total_outlets) * 100}%` : '0%' }}
              />
            </div>
          </div>
        );
      })}
    </div>
  );
}

function DeviceRow({
  rackId, asset, pdus, chain, canWrite,
}: {
  rackId: string;
  asset: PowerChainAsset;
  pdus: PduSummary[];
  chain: PerAsset;
  canWrite: boolean;
}) {
  const qc = useQueryClient();
  const [editOpen, setEditOpen] = useState(false);

  const variantFor = chain.redundancy === 'redundant' ? 'success'
    : chain.redundancy === 'single' ? 'warning'
    : chain.redundancy === 'unpowered' ? 'critical'
    : 'secondary';
  const Icon = chain.redundancy === 'redundant' ? ShieldCheck
    : chain.redundancy === 'single' ? ShieldAlert
    : ShieldOff;

  async function disconnect(outletId: string) {
    try {
      await http.delete(`/power/outlets/${outletId}/connect`);
      toast.success('Disconnected');
      await qc.invalidateQueries({ queryKey: ['rack-detail', rackId] });
    } catch (err: any) {
      toast.error(err?.message ?? 'Failed to disconnect');
    }
  }

  return (
    <TableRow>
      <TableCell>
        <div className="font-medium">{asset.name}</div>
        <div className="text-[11px] text-muted-foreground">
          {asset.kind}{asset.psu_count ? ` · ${asset.psu_count} PSU` : ''}
        </div>
      </TableCell>
      <TableCell>
        <Badge variant={variantFor as any} className="gap-1">
          <Icon className="h-3 w-3" /> {chain.redundancy}
        </Badge>
      </TableCell>
      <TableCell>
        {chain.connections.length === 0 ? (
          <span className="text-xs text-muted-foreground">No connections</span>
        ) : (
          <div className="flex flex-wrap gap-1.5">
            {chain.connections
              .sort((a, b) => a.psu_index - b.psu_index)
              .map((c) => {
                const sideColor =
                  c.pdu_side === 'A' ? 'border-blue-500/50 text-blue-300' :
                  c.pdu_side === 'B' ? 'border-red-500/50 text-red-300' :
                  c.pdu_side === 'C' ? 'border-purple-500/50 text-purple-300' :
                  'border-border text-muted-foreground';
                return (
                  <div key={c.outlet_id} className="group flex items-center gap-1">
                    <Badge variant="outline" className={cn('font-mono text-[10px]', sideColor)}>
                      <Plug className="mr-1 h-3 w-3" />
                      PSU{c.psu_index} → {c.pdu_name} · U{String(c.outlet_label ?? c.outlet_position).padStart(2, '0')}
                    </Badge>
                    {canWrite && (
                      <button
                        type="button"
                        onClick={() => disconnect(c.outlet_id)}
                        className="opacity-0 transition-opacity group-hover:opacity-100"
                        title="Disconnect"
                      >
                        <Trash2 className="h-3 w-3 text-muted-foreground hover:text-destructive" />
                      </button>
                    )}
                  </div>
                );
              })}
          </div>
        )}
      </TableCell>
      {canWrite && (
        <TableCell>
          <Dialog open={editOpen} onOpenChange={setEditOpen}>
            <DialogTrigger asChild>
              <Button size="sm" variant="outline">
                <Plus className="h-3 w-3" /> Connect
              </Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader>
                <DialogTitle>Connect {asset.name} to a PDU outlet</DialogTitle>
              </DialogHeader>
              <ConnectForm
                rackId={rackId}
                asset={asset}
                pdus={pdus}
                existingPsuIndices={chain.connections.map((c) => c.psu_index)}
                onDone={() => {
                  setEditOpen(false);
                  qc.invalidateQueries({ queryKey: ['rack-detail', rackId] });
                }}
              />
            </DialogContent>
          </Dialog>
        </TableCell>
      )}
    </TableRow>
  );
}

function ConnectForm({
  rackId, asset, pdus, existingPsuIndices, onDone,
}: {
  rackId: string;
  asset: PowerChainAsset;
  pdus: PduSummary[];
  existingPsuIndices: number[];
  onDone: () => void;
}) {
  const [pduId, setPduId] = useState<string>(pdus[0]?.id ?? '');
  const [outletId, setOutletId] = useState<string>('');
  const psuCount = asset.psu_count ?? 2;
  const nextPsu = Array.from({ length: psuCount }, (_, i) => i + 1).find((i) => !existingPsuIndices.includes(i)) ?? 1;
  const [psuIndex, setPsuIndex] = useState<number>(nextPsu);
  const [busy, setBusy] = useState(false);

  // Fetch outlets for the chosen PDU
  const outletsRes = useQuery({
    queryKey: ['outlets', pduId],
    queryFn: async () => {
      if (!pduId) return [];
      const r = await http.get<any[]>(`/power/pdus/${pduId}/outlets`);
      return r.data;
    },
    enabled: !!pduId,
  });

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!outletId) {
      toast.error('Pick an outlet');
      return;
    }
    setBusy(true);
    try {
      await http.post(`/power/outlets/${outletId}/connect`, {
        asset_id: asset.id,
        psu_index: psuIndex,
      });
      toast.success(`Connected PSU${psuIndex}`);
      onDone();
    } catch (err: any) {
      toast.error(err?.message ?? 'Failed to connect');
    } finally {
      setBusy(false);
    }
  }

  const availableOutlets = (outletsRes.data ?? []).filter((o: any) => !o.connected);

  return (
    <form onSubmit={submit} className="space-y-4">
      <div className="space-y-1.5">
        <Label>PSU</Label>
        <Select value={String(psuIndex)} onValueChange={(v) => setPsuIndex(Number(v))}>
          <SelectTrigger><SelectValue /></SelectTrigger>
          <SelectContent>
            {Array.from({ length: psuCount }, (_, i) => i + 1).map((i) => (
              <SelectItem key={i} value={String(i)} disabled={existingPsuIndices.includes(i)}>
                PSU{i} {existingPsuIndices.includes(i) ? '(already connected)' : ''}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="space-y-1.5">
        <Label>PDU</Label>
        <Select value={pduId} onValueChange={(v) => { setPduId(v); setOutletId(''); }}>
          <SelectTrigger><SelectValue placeholder="Pick a PDU" /></SelectTrigger>
          <SelectContent>
            {pdus.map((p) => (
              <SelectItem key={p.id} value={p.id}>
                {p.name} {p.side ? `(side ${p.side})` : ''}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div className="space-y-1.5">
        <Label>Outlet</Label>
        <Select value={outletId} onValueChange={setOutletId} disabled={!pduId || outletsRes.isLoading}>
          <SelectTrigger>
            <SelectValue placeholder={outletsRes.isLoading ? 'Loading…' : 'Pick an outlet'} />
          </SelectTrigger>
          <SelectContent>
            {availableOutlets.length === 0 && (
              <div className="px-2 py-1.5 text-xs text-muted-foreground">All outlets in use on this PDU.</div>
            )}
            {availableOutlets.map((o: any) => (
              <SelectItem key={o.id} value={o.id}>
                Outlet {String(o.label ?? o.position).padStart(2, '0')}{o.phase ? ` · phase ${o.phase}` : ''}{o.receptacle ? ` · ${o.receptacle}` : ''}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {asset.psu_count && asset.psu_count > 1 && pdus.length >= 2 && (
        <div className="flex items-start gap-2 rounded-md border border-warning/40 bg-warning/10 p-2 text-[11px] text-muted-foreground">
          <AlertTriangle className="mt-0.5 h-3.5 w-3.5 text-warning" />
          For redundancy, connect each PSU to a PDU on a different side (A vs B).
        </div>
      )}
      <Button type="submit" disabled={busy || !outletId}>
        {busy ? 'Connecting…' : 'Connect'}
      </Button>
    </form>
  );
}
