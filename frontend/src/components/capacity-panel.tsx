import { Server, Zap, Ruler } from 'lucide-react';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Badge } from '@/components/ui/badge';
import { CapacityBar } from './capacity-bar';

export type Capacity = {
  u_used: number;
  u_total: number;
  u_pct: number;
  u_free: number;
  kw_current: number | null;
  kw_max: number | null;
  kw_pct: number | null;
  biggest_contiguous_free: number;
  free_runs: { start_u: number; length: number }[];
};

export function CapacityPanel({ capacity }: { capacity: Capacity }) {
  const c = capacity;
  return (
    <Card>
      <CardHeader className="pb-3">
        <CardTitle className="flex items-center gap-2 text-base">
          <Server className="h-4 w-4" /> Capacity
        </CardTitle>
      </CardHeader>
      <CardContent className="grid gap-5 md:grid-cols-3">
        <div className="space-y-2">
          <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
            <Ruler className="h-3.5 w-3.5" /> Rack space
          </div>
          <CapacityBar
            used={c.u_used}
            total={c.u_total}
            leftLabel={`${c.u_used} / ${c.u_total} U used`}
          />
          <p className="text-xs text-muted-foreground">{c.u_free} U free</p>
        </div>

        <div className="space-y-2">
          <div className="flex items-center gap-1.5 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
            <Zap className="h-3.5 w-3.5" /> Power
          </div>
          {c.kw_max === null ? (
            <p className="text-xs text-muted-foreground">No max kW configured for this rack.</p>
          ) : (
            <>
              <CapacityBar
                used={c.kw_current ?? 0}
                total={c.kw_max}
                unknown={c.kw_current === null}
                leftLabel={
                  c.kw_current === null
                    ? `— / ${c.kw_max} kW`
                    : `${c.kw_current.toFixed(2)} / ${c.kw_max} kW`
                }
              />
              <p className="text-xs text-muted-foreground">
                {c.kw_current === null
                  ? 'Awaiting current PDU telemetry'
                  : `${(c.kw_max - c.kw_current).toFixed(2)} kW headroom`}
              </p>
            </>
          )}
        </div>

        <div className="space-y-2">
          <div className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
            Free contiguous space
          </div>
          {c.free_runs.length === 0 ? (
            <p className="text-xs text-muted-foreground">Rack is full.</p>
          ) : (
            <>
              <div className="flex flex-wrap gap-1.5">
                {c.free_runs.slice(0, 6).map((r) => (
                  <Badge
                    key={`${r.start_u}-${r.length}`}
                    variant={r.length >= 4 ? 'success' : r.length >= 2 ? 'secondary' : 'outline'}
                    className="font-mono"
                  >
                    {r.length}U @ U{r.start_u}
                  </Badge>
                ))}
              </div>
              <p className="text-xs text-muted-foreground">
                Largest gap: <span className="font-mono">{c.biggest_contiguous_free}U</span>
              </p>
            </>
          )}
        </div>
      </CardContent>
    </Card>
  );
}
