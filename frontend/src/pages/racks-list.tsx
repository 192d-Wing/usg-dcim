import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router';
import { useList } from '@refinedev/core';
import { useQuery } from '@tanstack/react-query';
import { Plus, Server } from 'lucide-react';
import { Card, CardContent } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select';
import { CapacityBar } from '@/components/capacity-bar';
import { Badge } from '@/components/ui/badge';
import { http } from '@/lib/http';

type Site = { id: string; code: string; name: string };
type Rack = { id: string; name: string; code: string; u_height: number; max_kw: number | null; serial: string | null; site_id: string };
type CapacityRow = {
  rack_id: string;
  u_used: number; u_total: number; u_pct: number;
  kw_current: number | null; kw_max: number | null; kw_pct: number | null;
  biggest_contiguous_free: number;
};
type ForecastRow = {
  rack_id: string;
  slope_u_per_day: number | null;
  days_until_full: number | null;
  runway_band: 'critical' | 'warning' | 'healthy' | 'unknown';
};
const BAND_VARIANT: Record<ForecastRow['runway_band'], 'critical' | 'warning' | 'success' | 'secondary'> = {
  critical: 'critical', warning: 'warning', healthy: 'success', unknown: 'secondary',
};
function formatDays(d: number | null): string {
  if (d === null) return '—';
  if (d < 1) return '<1d';
  if (d > 730) return `${Math.round(d / 365)}y`;
  if (d > 60) return `${Math.round(d / 30)}mo`;
  return `${Math.round(d)}d`;
}

export function RacksListPage() {
  const nav = useNavigate();
  const [siteId, setSiteId] = useState<string>('all');

  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const racksRes = useList<Rack>({
    resource: 'inventory/racks',
    pagination: { pageSize: 200 },
    filters: siteId === 'all' ? [] : [{ field: 'site_id', operator: 'eq', value: siteId }],
  });

  // Pull capacity for the same filter set in one round-trip.
  const capacityRes = useQuery({
    queryKey: ['racks-capacity', siteId],
    queryFn: async () => {
      const params: Record<string, string | number> = { u: 0, limit: 500 };
      if (siteId !== 'all') params.site_id = siteId;
      const r = await http.get<{ racks: CapacityRow[] }>('/dashboards/free-space', { params });
      return r.data.racks;
    },
    refetchInterval: 30_000,
  });

  const capById = useMemo(() => {
    const m = new Map<string, CapacityRow>();
    for (const c of capacityRes.data ?? []) m.set(c.rack_id, c);
    return m;
  }, [capacityRes.data]);

  const forecastRes = useQuery({
    queryKey: ['racks-forecast', siteId],
    queryFn: async () => {
      const params: Record<string, string | number> = { limit: 500 };
      if (siteId !== 'all') params.site_id = siteId;
      const r = await http.get<{ racks: ForecastRow[] }>('/dashboards/forecast/racks', { params });
      return r.data.racks;
    },
    refetchInterval: 60_000,
  });
  const forecastById = useMemo(() => {
    const m = new Map<string, ForecastRow>();
    for (const f of forecastRes.data ?? []) m.set(f.rack_id, f);
    return m;
  }, [forecastRes.data]);

  const sites = sitesRes.result.data ?? [];
  const racks = racksRes.result.data ?? [];
  const racksTotal = racksRes.result.total ?? racks.length;

  return (
    <div className="space-y-4">
      <div className="flex items-end justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Racks</h1>
          <p className="text-sm text-muted-foreground">{racksTotal} racks</p>
        </div>
        <div className="flex gap-2">
          <Select value={siteId} onValueChange={setSiteId}>
            <SelectTrigger className="w-[260px]">
              <SelectValue placeholder="Filter by site" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All sites</SelectItem>
              {sites.map((s) => (
                <SelectItem key={s.id} value={s.id}>{s.code} · {s.name}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button onClick={() => nav('/racks/new')}>
            <Plus className="h-4 w-4" /> New rack
          </Button>
        </div>
      </div>

      {racksRes.query.isLoading ? (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
          {Array.from({ length: 8 }).map((_, i) => <Skeleton key={`s-${i}`} className="h-44 rounded-lg" />)}
        </div>
      ) : (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
          {racks.map((r) => {
            const cap = capById.get(r.id);
            return (
              <Card
                key={r.id}
                role="button"
                onClick={() => nav(`/racks/${r.id}`)}
                className="cursor-pointer transition-colors hover:bg-accent/40"
              >
                <CardContent className="space-y-3 p-4">
                  <div>
                    <div className="flex items-center gap-2 text-xs text-muted-foreground">
                      <Server className="h-3.5 w-3.5" /> {r.code}
                    </div>
                    <div className="mt-1 truncate text-base font-semibold">{r.name}</div>
                    <div className="mt-1 text-xs text-muted-foreground">
                      {r.u_height}U · {r.max_kw ? `${r.max_kw} kW` : 'unrated'}
                    </div>
                  </div>

                  {cap ? (
                    <div className="space-y-2">
                      <CapacityBar
                        used={cap.u_used} total={cap.u_total}
                        leftLabel={`${cap.u_used}/${cap.u_total} U`}
                        compact
                      />
                      {cap.kw_max !== null ? (
                        <CapacityBar
                          used={cap.kw_current ?? 0}
                          total={cap.kw_max}
                          unknown={cap.kw_current === null}
                          leftLabel={
                            cap.kw_current === null
                              ? `—/${cap.kw_max} kW`
                              : `${cap.kw_current.toFixed(1)}/${cap.kw_max} kW`
                          }
                          compact
                        />
                      ) : (
                        <div className="text-[11px] text-muted-foreground">No kW rating</div>
                      )}
                      <div className="flex items-center justify-between text-[11px] text-muted-foreground">
                        <span>Largest gap: <span className="font-mono">{cap.biggest_contiguous_free}U</span></span>
                        {(() => {
                          const fc = forecastById.get(r.id);
                          if (!fc) return null;
                          if (fc.slope_u_per_day === null) {
                            return <Badge variant="secondary" className="text-[10px]">no trend</Badge>;
                          }
                          return (
                            <Badge variant={BAND_VARIANT[fc.runway_band]} className="text-[10px]">
                              {formatDays(fc.days_until_full)} runway
                            </Badge>
                          );
                        })()}
                      </div>
                    </div>
                  ) : (
                    <Skeleton className="h-16 w-full" />
                  )}
                </CardContent>
              </Card>
            );
          })}
          {racks.length === 0 && (
            <p className="col-span-full text-sm text-muted-foreground">No racks for this filter.</p>
          )}
        </div>
      )}
    </div>
  );
}
