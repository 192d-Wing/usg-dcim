import { useQuery } from '@tanstack/react-query';
import { Link } from 'react-router';
import { Activity, AlertTriangle, Building2, Cpu, Server, Wifi } from 'lucide-react';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { Badge } from '@/components/ui/badge';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { http } from '@/lib/http';
import { cn, relativeTime } from '@/lib/utils';

type Overview = {
  sites: { total: number; active: number };
  racks: { total: number };
  alerts: { sites_with_critical: number };
  collectors: { healthy: number; stale: number };
  telemetry: { stale_sources: number };
  generated_at: string;
};

export function DashboardPage() {
  const overview = useQuery({
    queryKey: ['enterprise'],
    queryFn: async () => (await http.get<Overview>('/dashboards/enterprise')).data,
    refetchInterval: 30_000,
  });
  const atRisk = useQuery({
    queryKey: ['sites-at-risk'],
    queryFn: async () => (await http.get('/dashboards/sites/at-risk')).data.sites as { site_id: string; alert_count: number }[],
    refetchInterval: 30_000,
  });

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Enterprise overview</h1>
        <p className="text-sm text-muted-foreground">
          Updated {overview.data ? relativeTime(overview.data.generated_at) : '…'}
        </p>
      </div>

      <div className="grid gap-4 md:grid-cols-2 lg:grid-cols-3 xl:grid-cols-5">
        <Kpi
          title="Sites" icon={Building2}
          value={overview.data ? `${overview.data.sites.active} / ${overview.data.sites.total}` : null}
          hint="active / total"
        />
        <Kpi
          title="Racks" icon={Server}
          value={overview.data ? overview.data.racks.total.toLocaleString() : null}
          hint="across enterprise"
        />
        <Kpi
          title="Critical alerts" icon={AlertTriangle}
          value={overview.data ? overview.data.alerts.sites_with_critical : null}
          hint="sites firing"
          tone={overview.data?.alerts.sites_with_critical ? 'critical' : 'success'}
        />
        <Kpi
          title="Collectors" icon={Cpu}
          value={overview.data ? `${overview.data.collectors.healthy} / ${overview.data.collectors.healthy + overview.data.collectors.stale}` : null}
          hint="healthy"
          tone={overview.data?.collectors.stale ? 'warning' : 'success'}
        />
        <Kpi
          title="Stale telemetry" icon={Wifi}
          value={overview.data ? overview.data.telemetry.stale_sources : null}
          hint="sources behind"
          tone={overview.data?.telemetry.stale_sources ? 'warning' : 'success'}
        />
      </div>

      <Card>
        <CardHeader className="flex flex-row items-center justify-between">
          <div>
            <CardTitle className="flex items-center gap-2"><Activity className="h-4 w-4" /> Sites at risk</CardTitle>
            <p className="text-xs text-muted-foreground">Sites with major or worse alerts firing</p>
          </div>
        </CardHeader>
        <CardContent>
          {atRisk.isLoading && <Skeleton className="h-24 w-full" />}
          {atRisk.data && (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Site</TableHead>
                  <TableHead className="w-24">Open alerts</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {atRisk.data.length === 0 && (
                  <TableRow><TableCell colSpan={2} className="text-muted-foreground">No sites currently at risk.</TableCell></TableRow>
                )}
                {atRisk.data.map((s) => (
                  <TableRow key={s.site_id}>
                    <TableCell><Link to={`/sites/${s.site_id}`} className="font-mono text-xs hover:underline">{s.site_id.slice(0, 8)}…</Link></TableCell>
                    <TableCell><Badge variant="critical">{s.alert_count}</Badge></TableCell>
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

function Kpi({
  title, value, hint, icon: Icon, tone,
}: {
  title: string;
  value: string | number | null;
  hint?: string;
  icon: React.ComponentType<{ className?: string }>;
  tone?: 'success' | 'warning' | 'critical';
}) {
  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-2">
        <CardTitle className="text-xs font-medium text-muted-foreground uppercase tracking-wider">{title}</CardTitle>
        <Icon className="h-4 w-4 text-muted-foreground" />
      </CardHeader>
      <CardContent>
        <div className={cn(
          'text-3xl font-semibold tabular-nums',
          tone === 'critical' && 'text-destructive',
          tone === 'warning' && 'text-warning',
          tone === 'success' && 'text-success',
        )}>
          {value === null ? <Skeleton className="h-8 w-20" /> : value}
        </div>
        {hint && <p className="mt-1 text-xs text-muted-foreground">{hint}</p>}
      </CardContent>
    </Card>
  );
}
