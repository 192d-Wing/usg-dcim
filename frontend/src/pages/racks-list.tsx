import { useState } from 'react';
import { useNavigate } from 'react-router';
import { useList } from '@refinedev/core';
import { Plus, Server } from 'lucide-react';
import { Card, CardContent } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select';

type Site = { id: string; code: string; name: string };
type Rack = { id: string; name: string; code: string; u_height: number; max_kw: number | null; serial: string | null; site_id: string };

export function RacksListPage() {
  const nav = useNavigate();
  const [siteId, setSiteId] = useState<string>('all');

  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const racksRes = useList<Rack>({
    resource: 'inventory/racks',
    pagination: { pageSize: 200 },
    filters: siteId === 'all' ? [] : [{ field: 'site_id', operator: 'eq', value: siteId }],
  });

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
          {Array.from({ length: 8 }).map((_, i) => <Skeleton key={`s-${i}`} className="h-28 rounded-lg" />)}
        </div>
      ) : (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
          {racks.map((r) => (
            <Card
              key={r.id}
              role="button"
              onClick={() => nav(`/racks/${r.id}`)}
              className="cursor-pointer transition-colors hover:bg-accent/40"
            >
              <CardContent className="p-4">
                <div className="flex items-center gap-2 text-xs text-muted-foreground">
                  <Server className="h-3.5 w-3.5" /> {r.code}
                </div>
                <div className="mt-1 truncate text-base font-semibold">{r.name}</div>
                <div className="mt-2 text-xs text-muted-foreground">
                  {r.u_height}U · {r.max_kw ? `${r.max_kw} kW` : 'unrated'}
                </div>
                {r.serial && <div className="mt-1 truncate text-[11px] text-muted-foreground">SN {r.serial}</div>}
              </CardContent>
            </Card>
          ))}
          {racks.length === 0 && (
            <p className="col-span-full text-sm text-muted-foreground">No racks for this filter.</p>
          )}
        </div>
      )}
    </div>
  );
}
