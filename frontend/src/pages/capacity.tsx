import { useState } from 'react';
import { useNavigate } from 'react-router';
import { useList } from '@refinedev/core';
import { useQuery } from '@tanstack/react-query';
import { Search } from 'lucide-react';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table';
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select';
import { CapacityBar } from '@/components/capacity-bar';
import { Badge } from '@/components/ui/badge';
import { http } from '@/lib/http';

type Site = { id: string; code: string; name: string };
type FreeSpaceRow = {
  rack_id: string;
  site_id: string;
  code: string;
  name: string;
  u_height: number;
  u_used: number; u_total: number; u_pct: number;
  kw_current: number | null; kw_max: number | null;
  biggest_contiguous_free: number;
  free_runs: { start_u: number; length: number }[];
};

export function CapacityPage() {
  const nav = useNavigate();
  const [minU, setMinU] = useState('1');
  const [siteId, setSiteId] = useState('all');
  const [minKw, setMinKw] = useState('');

  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const sites = sitesRes.result.data ?? [];

  const params: Record<string, string | number> = { u: Number(minU) || 1, limit: 200 };
  if (siteId !== 'all') params.site_id = siteId;
  if (minKw && Number(minKw) > 0) params.min_kw_headroom = Number(minKw);

  const result = useQuery({
    queryKey: ['free-space', minU, siteId, minKw],
    queryFn: async () => {
      const r = await http.get<{ racks: FreeSpaceRow[]; count: number }>('/dashboards/free-space', { params });
      return r.data;
    },
    refetchInterval: 60_000,
  });

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Capacity & free space</h1>
        <p className="text-sm text-muted-foreground">
          Find racks with enough contiguous U slots — and optional kW headroom — for an upcoming install.
        </p>
      </div>

      <Card>
        <CardHeader className="pb-3">
          <CardTitle className="flex items-center gap-2 text-base">
            <Search className="h-4 w-4" /> Search
          </CardTitle>
        </CardHeader>
        <CardContent className="grid gap-4 md:grid-cols-4">
          <div className="space-y-1.5">
            <Label htmlFor="min-u">Need at least (U)</Label>
            <Input
              id="min-u" type="number" min={1} max={60}
              value={minU} onChange={(e) => setMinU(e.target.value)}
            />
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="site">Site</Label>
            <Select value={siteId} onValueChange={setSiteId}>
              <SelectTrigger id="site"><SelectValue placeholder="Any site" /></SelectTrigger>
              <SelectContent>
                <SelectItem value="all">Any site</SelectItem>
                {sites.map((s) => (
                  <SelectItem key={s.id} value={s.id}>{s.code} · {s.name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="min-kw">Min kW headroom (optional)</Label>
            <Input
              id="min-kw" type="number" step="0.1" placeholder="e.g. 2.5"
              value={minKw} onChange={(e) => setMinKw(e.target.value)}
            />
          </div>
          <div className="flex items-end">
            <Button
              variant="outline"
              onClick={() => { setMinU('1'); setSiteId('all'); setMinKw(''); }}
              className="w-full"
            >
              Reset
            </Button>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader className="flex flex-row items-center justify-between space-y-0 pb-3">
          <CardTitle className="text-base">
            {result.data ? `${result.data.count} matching rack${result.data.count === 1 ? '' : 's'}` : 'Results'}
          </CardTitle>
          <p className="text-xs text-muted-foreground">Sorted by largest contiguous gap</p>
        </CardHeader>
        <CardContent className="p-0">
          {result.isLoading ? (
            <div className="space-y-2 p-4">
              {Array.from({ length: 6 }).map((_, i) => <Skeleton key={`s-${i}`} className="h-9 w-full" />)}
            </div>
          ) : (result.data?.racks.length ?? 0) === 0 ? (
            <p className="p-6 text-sm text-muted-foreground">
              No racks match. Try lowering the U requirement or removing the kW headroom filter.
            </p>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Rack</TableHead>
                  <TableHead className="w-24">Largest gap</TableHead>
                  <TableHead>Free slots</TableHead>
                  <TableHead className="w-48">U utilization</TableHead>
                  <TableHead className="w-48">kW utilization</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {result.data?.racks.map((r) => (
                  <TableRow key={r.rack_id} onClick={() => nav(`/racks/${r.rack_id}`)} className="cursor-pointer">
                    <TableCell>
                      <div className="font-medium">{r.code} · {r.name}</div>
                      <div className="text-[11px] text-muted-foreground">
                        site <span className="font-mono">{r.site_id.slice(0, 8)}…</span>
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge variant={r.biggest_contiguous_free >= 4 ? 'success' : 'secondary'} className="font-mono">
                        {r.biggest_contiguous_free}U
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <div className="flex flex-wrap gap-1">
                        {r.free_runs.slice(0, 4).map((run) => (
                          <Badge key={`${run.start_u}-${run.length}`} variant="outline" className="font-mono text-[10px]">
                            {run.length}U @ U{run.start_u}
                          </Badge>
                        ))}
                        {r.free_runs.length > 4 && (
                          <Badge variant="outline" className="text-[10px]">+{r.free_runs.length - 4}</Badge>
                        )}
                      </div>
                    </TableCell>
                    <TableCell>
                      <CapacityBar
                        used={r.u_used} total={r.u_total}
                        leftLabel={`${r.u_used}/${r.u_total} U`}
                        compact
                      />
                    </TableCell>
                    <TableCell>
                      {r.kw_max === null ? (
                        <span className="text-xs text-muted-foreground">Unrated</span>
                      ) : (
                        <CapacityBar
                          used={r.kw_current ?? 0}
                          total={r.kw_max}
                          unknown={r.kw_current === null}
                          leftLabel={
                            r.kw_current === null
                              ? `—/${r.kw_max} kW`
                              : `${r.kw_current.toFixed(1)}/${r.kw_max} kW`
                          }
                          compact
                        />
                      )}
                    </TableCell>
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
