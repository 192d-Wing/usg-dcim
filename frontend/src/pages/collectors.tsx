import { useTable } from '@refinedev/core';
import { Card, CardContent } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table';
import { FreshnessBadge } from '@/components/freshness-badge';
import { formatDate } from '@/lib/utils';

type Collector = {
  id: string; name: string; site_id: string; status: string;
  last_seen_at: string | null; buffered_samples: number; capabilities: string[];
};

function mapStatus(s: string): string {
  if (s === 'healthy') return 'current';
  if (s === 'degraded') return 'estimated';
  if (s === 'stale' || s === 'unreachable') return 'stale';
  return 'unknown';
}

export function CollectorsPage() {
  const { tableQuery, result } = useTable<Collector>({
    resource: 'collectors',
    pagination: { pageSize: 200 },
  });
  const data = result.data ?? [];

  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Site collectors</h1>
        <p className="text-sm text-muted-foreground">{result.total ?? 0} registered</p>
      </div>
      <Card>
        <CardContent className="p-0">
          {tableQuery.isLoading ? (
            <div className="p-4 space-y-2">{Array.from({ length: 6 }).map((_, i) => <Skeleton key={`s-${i}`} className="h-9 w-full" />)}</div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Site</TableHead>
                  <TableHead>Capabilities</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Last seen</TableHead>
                  <TableHead className="w-24">Buffered</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.map((c) => (
                  <TableRow key={c.id}>
                    <TableCell className="font-medium">{c.name}</TableCell>
                    <TableCell className="font-mono text-xs">{c.site_id.slice(0, 8)}…</TableCell>
                    <TableCell className="text-xs text-muted-foreground">{c.capabilities.join(', ')}</TableCell>
                    <TableCell><FreshnessBadge state={mapStatus(c.status)} /></TableCell>
                    <TableCell className="text-muted-foreground">{formatDate(c.last_seen_at)}</TableCell>
                    <TableCell className="tabular-nums">{c.buffered_samples?.toLocaleString() ?? 0}</TableCell>
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
