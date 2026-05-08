import { useMemo } from 'react';
import { Link } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { Cable as CableIcon } from 'lucide-react';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { Badge } from '@/components/ui/badge';
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table';
import { cn } from '@/lib/utils';
import { http } from '@/lib/http';

type Cable = {
  id: string;
  a_asset_id: string; a_port: string | null;
  b_asset_id: string; b_port: string | null;
  medium: string | null; color: string | null;
  length_m: number | null; label: string | null; face: string | null;
};
type RemoteAsset = { id: string; name: string; kind: string; rack_id: string | null };

export function AssetCablesPanel({
  assetId, portCount,
}: {
  assetId: string;
  portCount?: number | null;
}) {
  const cablesRes = useQuery({
    queryKey: ['asset-cables', assetId],
    queryFn: async () => {
      const r = await http.get<{ items: Cable[] }>('/inventory/cables', {
        params: { asset_id: assetId, page_size: 500 },
      });
      return r.data.items ?? [];
    },
    enabled: !!assetId,
  });
  const cables = cablesRes.data ?? [];

  const remoteIds = useMemo(() => {
    const ids = new Set<string>();
    for (const c of cables) {
      if (c.a_asset_id !== assetId) ids.add(c.a_asset_id);
      if (c.b_asset_id !== assetId) ids.add(c.b_asset_id);
    }
    return Array.from(ids);
  }, [cables, assetId]);

  const remoteRes = useQuery({
    queryKey: ['asset-cables-remotes', remoteIds.sort().join(',')],
    queryFn: async () => {
      if (remoteIds.length === 0) return [] as RemoteAsset[];
      return Promise.all(
        remoteIds.map((id) => http.get<RemoteAsset>(`/inventory/assets/${id}`).then((r) => r.data)),
      );
    },
    enabled: remoteIds.length > 0,
  });
  const remoteById = useMemo(() => {
    const m = new Map<string, RemoteAsset>();
    for (const a of remoteRes.data ?? []) m.set(a.id, a);
    return m;
  }, [remoteRes.data]);

  // For port-bearing assets (patch panels), build a port → connected cable map
  // so the grid can show used vs free at a glance and tooltip the remote end.
  const portUse = useMemo(() => {
    const m = new Map<number, { remoteName: string; remotePort: string | null }>();
    if (!portCount || portCount <= 0) return m;
    for (const c of cables) {
      const localIsA = c.a_asset_id === assetId;
      const localPort = localIsA ? c.a_port : c.b_port;
      if (!localPort) continue;
      const n = Number(localPort);
      if (!Number.isInteger(n) || n < 1 || n > portCount) continue;
      const remoteId = localIsA ? c.b_asset_id : c.a_asset_id;
      const remote = remoteById.get(remoteId);
      m.set(n, {
        remoteName: remote?.name ?? remoteId.slice(0, 8),
        remotePort: localIsA ? c.b_port : c.a_port,
      });
    }
    return m;
  }, [cables, portCount, assetId, remoteById]);

  const ports = portCount ?? 0;

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2 text-base">
          <CableIcon className="h-4 w-4" /> Cables ({cables.length})
          {ports > 0 && (
            <span className="ml-2 text-xs font-normal text-muted-foreground">
              {portUse.size} / {ports} ports in use
            </span>
          )}
        </CardTitle>
      </CardHeader>
      {ports > 0 && (
        <CardContent className="border-b py-3">
          <div className="flex flex-wrap gap-1">
            {Array.from({ length: ports }, (_, i) => i + 1).map((n) => {
              const use = portUse.get(n);
              return (
                <div
                  key={n}
                  className={cn(
                    'flex h-6 w-7 items-center justify-center rounded-sm border text-[10px] font-mono tabular-nums',
                    use
                      ? 'border-success/40 bg-success/15 text-success'
                      : 'border-border bg-muted/30 text-muted-foreground',
                  )}
                  title={use ? `→ ${use.remoteName}${use.remotePort ? ` port ${use.remotePort}` : ''}` : 'free'}
                >
                  {n}
                </div>
              );
            })}
          </div>
        </CardContent>
      )}
      <CardContent className="p-0">
        {cablesRes.isLoading ? (
          <div className="space-y-2 p-6">
            <Skeleton className="h-5 w-full" />
            <Skeleton className="h-5 w-full" />
          </div>
        ) : cables.length === 0 ? (
          <p className="p-4 text-sm text-muted-foreground">No cables connect to this asset.</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-20">Local port</TableHead>
                <TableHead>Connected to</TableHead>
                <TableHead>Remote port</TableHead>
                <TableHead className="w-16">Face</TableHead>
                <TableHead>Medium</TableHead>
                <TableHead>Color</TableHead>
                <TableHead className="w-16 text-right">Len (m)</TableHead>
                <TableHead>Label</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {cables.map((c) => {
                const localIsA = c.a_asset_id === assetId;
                const localPort = localIsA ? c.a_port : c.b_port;
                const remoteId = localIsA ? c.b_asset_id : c.a_asset_id;
                const remotePort = localIsA ? c.b_port : c.a_port;
                const remote = remoteById.get(remoteId);
                return (
                  <TableRow key={c.id}>
                    <TableCell className="font-mono text-xs">{localPort ?? '—'}</TableCell>
                    <TableCell className="font-medium">
                      {remote ? (
                        <Link to={`/assets/${remote.id}`} className="text-primary hover:underline">
                          {remote.name} <span className="text-muted-foreground">· {remote.kind}</span>
                        </Link>
                      ) : remoteId.slice(0, 8)}
                    </TableCell>
                    <TableCell className="font-mono text-xs">{remotePort ?? '—'}</TableCell>
                    <TableCell>
                      {c.face
                        ? <Badge variant="outline" className="capitalize">{c.face}</Badge>
                        : <span className="text-muted-foreground">—</span>}
                    </TableCell>
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
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </CardContent>
    </Card>
  );
}
