import { useTable } from '@refinedev/core';
import { Link } from 'react-router';
import { Card, CardContent } from '@/components/ui/card';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import { Button } from '@/components/ui/button';

type Site = {
  id: string;
  name: string;
  code: string;
  region_id: string;
  majcom?: string | null;
  organization?: string | null;
  enclave?: string | null;
  lifecycle_state: string;
};

export function SitesListPage() {
  const { tableQuery, result, currentPage, pageCount, setCurrentPage } = useTable<Site>({
    resource: 'inventory/sites',
    pagination: { pageSize: 50 },
    sorters: { initial: [{ field: 'code', order: 'asc' }] },
  });
  const data = result.data ?? [];
  const total = result.total ?? 0;

  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Sites</h1>
        <p className="text-sm text-muted-foreground">
          {total} total · page {currentPage} of {pageCount}
        </p>
      </div>
      <Card>
        <CardContent className="p-0">
          {tableQuery.isLoading ? (
            <div className="space-y-2 p-4">
              {Array.from({ length: 6 }).map((_, i) => <Skeleton key={`s-${i}`} className="h-9 w-full" />)}
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Code</TableHead>
                  <TableHead>Name</TableHead>
                  <TableHead>MAJCOM</TableHead>
                  <TableHead>Org</TableHead>
                  <TableHead>Enclave</TableHead>
                  <TableHead>State</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.map((s) => (
                  <TableRow key={s.id}>
                    <TableCell><Link to={`/sites/${s.id}`} className="font-mono text-xs hover:underline">{s.code}</Link></TableCell>
                    <TableCell>{s.name}</TableCell>
                    <TableCell>{s.majcom ?? '—'}</TableCell>
                    <TableCell>{s.organization ?? '—'}</TableCell>
                    <TableCell>{s.enclave ?? '—'}</TableCell>
                    <TableCell>
                      <Badge variant={s.lifecycle_state === 'active' ? 'success' : 'warning'}>
                        {s.lifecycle_state}
                      </Badge>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>
      {pageCount > 1 && (
        <div className="flex items-center justify-between text-sm text-muted-foreground">
          <span>Showing {data.length} of {total}</span>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" onClick={() => setCurrentPage(currentPage - 1)} disabled={currentPage <= 1}>Prev</Button>
            <Button variant="outline" size="sm" onClick={() => setCurrentPage(currentPage + 1)} disabled={currentPage >= pageCount}>Next</Button>
          </div>
        </div>
      )}
    </div>
  );
}
