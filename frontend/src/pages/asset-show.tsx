import { useParams, useNavigate } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import {
  LineChart, Line, ResponsiveContainer, XAxis, YAxis, Tooltip, CartesianGrid,
} from 'recharts';
import { ArrowLeft } from 'lucide-react';
import { http } from '@/lib/http';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table';
import { FreshnessBadge } from '@/components/freshness-badge';
import { AssetCablesPanel } from '@/components/asset-cables-panel';
import { formatDate } from '@/lib/utils';

type AssetDetail = {
  asset: {
    id: string; site_id: string; rack_id: string | null; name: string; hostname: string | null;
    kind: string; manufacturer: string | null; model: string | null; serial: string | null;
    firmware: string | null; mgmt_ip: string | null; mgmt_protocol: string | null; mgmt_port: number | null;
    rack_position_u: number | null; rack_units: number | null;
    port_count: number | null; lifecycle_state: string;
  };
  telemetry_sources: {
    metric: string; unit: string | null; source_system: string | null;
    freshness: string; last_value: number | null; last_reading_at: string | null;
    last_success_at: string | null; poll_interval_seconds: number;
  }[];
  recent_alerts: {
    id: string; severity: string; state: string; summary: string;
    first_seen_at: string; last_seen_at: string;
  }[];
};

export function AssetShowPage() {
  const { id = '' } = useParams<{ id: string }>();
  const nav = useNavigate();
  const detail = useQuery({
    queryKey: ['asset-detail', id],
    queryFn: async () => (await http.get<AssetDetail>(`/dashboards/assets/${id}`)).data,
    enabled: !!id,
    refetchInterval: 15_000,
  });

  if (detail.isLoading) {
    return (
      <div className="space-y-4">
        <Skeleton className="h-8 w-72" />
        <Skeleton className="h-40 w-full" />
        <Skeleton className="h-60 w-full" />
      </div>
    );
  }
  if (!detail.data?.asset) return <p className="text-sm text-muted-foreground">Failed to load asset.</p>;

  const a = detail.data.asset;
  const sources = detail.data.telemetry_sources ?? [];
  const alerts = detail.data.recent_alerts ?? [];

  return (
    <div className="space-y-6">
      <Button
        variant="ghost" size="sm"
        onClick={() => (a.rack_id ? nav(`/racks/${a.rack_id}`) : nav('/racks'))}
        className="-ml-2"
      >
        <ArrowLeft className="h-4 w-4" /> Back to rack
      </Button>

      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{a.name}</h1>
          <p className="text-sm text-muted-foreground">
            {a.kind}
            {a.hostname ? ` · ${a.hostname}` : ' · no hostname'}
            {a.mgmt_ip ? ` · ${a.mgmt_ip}` : ''}
          </p>
        </div>
        <Badge variant={a.lifecycle_state === 'active' ? 'success' : 'warning'}>{a.lifecycle_state}</Badge>
      </div>

      <div className="grid gap-3 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-6">
        <FieldCard label="Manufacturer" value={a.manufacturer ?? '—'} />
        <FieldCard label="Model" value={a.model ?? '—'} />
        <FieldCard label="Serial" value={a.serial ?? '—'} mono />
        <FieldCard label="Firmware" value={a.firmware ?? '—'} />
        <FieldCard label="Mgmt" value={`${a.mgmt_protocol ?? '—'} ${a.mgmt_ip ?? ''}${a.mgmt_port ? ':' + a.mgmt_port : ''}`} />
        <FieldCard label="Rack position" value={a.rack_position_u ? `U${a.rack_position_u} (${a.rack_units}U)` : '—'} />
      </div>

      <Card>
        <CardHeader><CardTitle className="text-base">Telemetry sources</CardTitle></CardHeader>
        <CardContent className="p-0">
          {sources.length === 0 ? (
            <p className="p-4 text-sm text-muted-foreground">No telemetry has been ingested for this asset yet.</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Metric</TableHead>
                  <TableHead>Last value</TableHead>
                  <TableHead>Unit</TableHead>
                  <TableHead>Last reading</TableHead>
                  <TableHead>Source</TableHead>
                  <TableHead>Freshness</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {sources.map((s) => (
                  <TableRow key={s.metric}>
                    <TableCell className="font-medium">{s.metric}</TableCell>
                    <TableCell className="tabular-nums">{s.last_value ?? '—'}</TableCell>
                    <TableCell>{s.unit ?? ''}</TableCell>
                    <TableCell className="text-muted-foreground">{formatDate(s.last_reading_at)}</TableCell>
                    <TableCell>{s.source_system ?? '—'}</TableCell>
                    <TableCell><FreshnessBadge state={s.freshness} /></TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {sources.slice(0, 4).map((s) => (
        <SeriesChart key={s.metric} siteId={a.site_id} assetId={a.id} metric={s.metric} unit={s.unit} />
      ))}

      <AssetCablesPanel assetId={a.id} portCount={a.port_count} />

      <Card>
        <CardHeader><CardTitle className="text-base">Recent alerts</CardTitle></CardHeader>
        <CardContent className="p-0">
          {alerts.length === 0 ? (
            <p className="p-4 text-sm text-muted-foreground">No alerts have fired for this asset.</p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Severity</TableHead>
                  <TableHead>State</TableHead>
                  <TableHead>Summary</TableHead>
                  <TableHead>First seen</TableHead>
                  <TableHead>Last seen</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {alerts.map((al) => (
                  <TableRow key={al.id}>
                    <TableCell><Badge variant={sevVariant(al.severity)}>{al.severity}</Badge></TableCell>
                    <TableCell>{al.state}</TableCell>
                    <TableCell>{al.summary}</TableCell>
                    <TableCell className="text-muted-foreground">{formatDate(al.first_seen_at)}</TableCell>
                    <TableCell className="text-muted-foreground">{formatDate(al.last_seen_at)}</TableCell>
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

function FieldCard({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <Card>
      <CardContent className="space-y-1 p-4">
        <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">{label}</div>
        <div className={mono ? 'font-mono text-sm' : 'text-sm'}>{value}</div>
      </CardContent>
    </Card>
  );
}

function sevVariant(s: string): 'critical' | 'warning' | 'success' {
  if (s === 'critical' || s === 'major') return 'critical';
  if (s === 'minor' || s === 'warning') return 'warning';
  return 'success';
}

function SeriesChart({ siteId, assetId, metric, unit }: { siteId: string; assetId: string; metric: string; unit: string | null }) {
  const series = useQuery({
    queryKey: ['series', siteId, assetId, metric],
    queryFn: async () => {
      const end = new Date();
      const start = new Date(end.getTime() - 3_600_000);
      const r = await http.get('/telemetry/series', {
        params: { site_id: siteId, asset_id: assetId, metric, start: start.toISOString(), end: end.toISOString() },
      });
      return (r.data.points ?? []) as { ts: string; value: number }[];
    },
    refetchInterval: 15_000,
  });
  const points = series.data ?? [];
  if (points.length === 0) return null;

  const data = points.map((p) => ({ ts: new Date(p.ts).getTime(), value: p.value }));
  const last = data[data.length - 1];

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-sm">{metric}</CardTitle>
        <span className="text-xs text-muted-foreground">
          last: <span className="font-mono">{last.value}{unit ? ` ${unit}` : ''}</span> · {data.length} pts (1h)
        </span>
      </CardHeader>
      <CardContent className="h-40 px-2 pt-0 pb-2">
        <ResponsiveContainer width="100%" height="100%">
          <LineChart data={data} margin={{ top: 5, right: 16, left: 0, bottom: 0 }}>
            <CartesianGrid stroke="hsl(var(--border))" strokeDasharray="3 3" />
            <XAxis
              dataKey="ts" tickFormatter={(v) => new Date(v).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
              fontSize={11} stroke="hsl(var(--muted-foreground))"
            />
            <YAxis fontSize={11} stroke="hsl(var(--muted-foreground))" width={40} />
            <Tooltip
              labelFormatter={(v) => new Date(v as number).toLocaleString()}
              contentStyle={{ background: 'hsl(var(--popover))', border: '1px solid hsl(var(--border))', borderRadius: 6 }}
            />
            <Line type="monotone" dataKey="value" stroke="hsl(var(--primary))" strokeWidth={1.5} dot={false} />
          </LineChart>
        </ResponsiveContainer>
      </CardContent>
    </Card>
  );
}
