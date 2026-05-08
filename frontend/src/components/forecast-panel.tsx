import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { TrendingUp } from 'lucide-react';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { http } from '@/lib/http';

type Band = 'critical' | 'warning' | 'healthy' | 'unknown';
type KwForecast = {
  max_kw: number | null;
  days: number;
  samples: number;
  slope_kw_per_day: number | null;
  current_kw: number | null;
  days_until_max: number | null;
  projected_max_date: string | null;
  runway_band: Band;
};
type Forecast = {
  u_used: number;
  u_total: number;
  u_free: number;
  slope_u_per_day: number | null;
  days_until_full: number | null;
  projected_fill_date: string | null;
  runway_band: Band;
  kw_forecast: KwForecast | null;
  what_if_add_units?: number;
  what_if_u_used?: number;
  what_if_u_free?: number;
  what_if_days_until_full?: number | null;
  what_if_runway_band?: Band;
};

const BAND_VARIANT: Record<string, 'critical' | 'warning' | 'success' | 'secondary'> = {
  critical: 'critical', warning: 'warning', healthy: 'success', unknown: 'secondary',
};

function formatDays(d: number | null | undefined): string {
  if (d === null || d === undefined) return '—';
  if (d < 1) return '<1 day';
  if (d > 730) return `${Math.round(d / 365)}y`;
  if (d > 60) return `${Math.round(d / 30)}mo`;
  return `${Math.round(d)}d`;
}

function formatDate(iso: string | null): string {
  if (!iso) return '—';
  return new Date(iso).toLocaleDateString();
}

export function ForecastPanel({ rackId }: { rackId: string }) {
  const [whatIfU, setWhatIfU] = useState<string>('');
  const addUnits = whatIfU ? Math.max(0, Math.min(60, Number(whatIfU))) : 0;

  const forecastRes = useQuery({
    queryKey: ['rack-forecast', rackId, addUnits],
    queryFn: async () => {
      const url = addUnits > 0
        ? `/dashboards/forecast/racks/${rackId}?add_units=${addUnits}`
        : `/dashboards/forecast/racks/${rackId}`;
      return (await http.get<Forecast>(url)).data;
    },
    enabled: !!rackId,
  });

  const f = forecastRes.data;

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <TrendingUp className="h-4 w-4" /> Capacity forecast
          {f && (
            <Badge variant={BAND_VARIANT[f.runway_band]} className="ml-2 capitalize">
              {f.runway_band}
            </Badge>
          )}
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4">
        {forecastRes.isLoading ? (
          <Skeleton className="h-24 w-full" />
        ) : !f ? (
          <p className="text-sm text-muted-foreground">No forecast available.</p>
        ) : (
          <>
            <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
              <Field label="Used / total" value={`${f.u_used} / ${f.u_total} U`} />
              <Field label="Free" value={`${f.u_free} U`} />
              <Field
                label="Growth"
                value={
                  f.slope_u_per_day === null
                    ? 'no trend yet'
                    : `${f.slope_u_per_day.toFixed(2)} U/day`
                }
              />
              <Field
                label="Runway"
                value={
                  f.slope_u_per_day === null
                    ? '—'
                    : `${formatDays(f.days_until_full)} (${formatDate(f.projected_fill_date)})`
                }
              />
            </div>
            {f.slope_u_per_day === null && (
              <p className="text-xs text-muted-foreground">
                Need at least two placements with distinct timestamps before a slope can be inferred.
              </p>
            )}
            {f.kw_forecast && <KwSection kw={f.kw_forecast} />}
            <div className="rounded-md border bg-muted/30 p-3 space-y-2">
              <Label className="text-xs uppercase tracking-wider text-muted-foreground">
                What-if: add U to this rack
              </Label>
              <div className="flex items-center gap-3">
                <Input
                  type="number" min={0} max={60} className="w-24"
                  value={whatIfU} placeholder="0"
                  onChange={(e) => setWhatIfU(e.target.value)}
                />
                {addUnits > 0 && (
                  <div className="flex flex-1 items-center gap-3 text-sm">
                    <span className="text-muted-foreground">→</span>
                    <span>
                      {f.what_if_u_used} / {f.u_total} U used
                      <span className="text-muted-foreground"> · {f.what_if_u_free} U free</span>
                    </span>
                    {f.what_if_runway_band && (
                      <Badge variant={BAND_VARIANT[f.what_if_runway_band]} className="capitalize">
                        {formatDays(f.what_if_days_until_full)} runway · {f.what_if_runway_band}
                      </Badge>
                    )}
                  </div>
                )}
              </div>
            </div>
          </>
        )}
      </CardContent>
    </Card>
  );
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">{label}</div>
      <div className="mt-1 text-sm font-medium tabular-nums">{value}</div>
    </div>
  );
}

function KwSection({ kw }: { kw: KwForecast }) {
  const slope = kw.slope_kw_per_day;
  return (
    <div className="rounded-md border bg-muted/20 p-3 space-y-2">
      <div className="flex items-center justify-between">
        <Label className="text-xs uppercase tracking-wider text-muted-foreground">
          kW trend ({kw.days}d window · {kw.samples} samples)
        </Label>
        <Badge variant={BAND_VARIANT[kw.runway_band]} className="capitalize">
          {kw.runway_band}
        </Badge>
      </div>
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
        <Field
          label="Current"
          value={kw.current_kw === null ? '—' : `${kw.current_kw.toFixed(2)} kW`}
        />
        <Field
          label="Max rated"
          value={kw.max_kw === null ? '—' : `${kw.max_kw.toFixed(1)} kW`}
        />
        <Field
          label="Growth"
          value={slope === null ? 'no trend yet' : `${(slope * 1000).toFixed(0)} W/day`}
        />
        <Field
          label="Runway to max"
          value={
            kw.days_until_max === null
              ? '—'
              : `${formatDays(kw.days_until_max)} (${formatDate(kw.projected_max_date)})`
          }
        />
      </div>
      {kw.samples < 2 && (
        <p className="text-xs text-muted-foreground">
          Need at least two daily kW samples from PDU telemetry before a slope can be inferred.
        </p>
      )}
    </div>
  );
}
