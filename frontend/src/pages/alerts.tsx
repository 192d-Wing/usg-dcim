import { useTable, useGetIdentity } from '@refinedev/core';
import { useState } from 'react';
import { Link } from 'react-router';
import { Check, Settings } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table';
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select';
import { http } from '@/lib/http';
import { formatDate } from '@/lib/utils';
import { toast } from 'sonner';

type Alert = {
  id: string; site_id: string; severity: string; state: string;
  summary: string; first_seen_at: string;
};

function sevVariant(s: string): 'critical' | 'warning' | 'success' {
  if (s === 'critical' || s === 'major') return 'critical';
  if (s === 'minor' || s === 'warning') return 'warning';
  return 'success';
}

export function AlertsPage() {
  const [state, setState] = useState('firing');
  const { tableQuery, result, currentPage, pageCount, setCurrentPage } = useTable<Alert>({
    resource: 'alerts',
    pagination: { pageSize: 50 },
    filters: { permanent: [{ field: 'state', operator: 'eq', value: state }] },
  });
  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const canAck = identity?.capabilities.includes('alerts:ack');
  const data = result.data ?? [];
  const total = result.total ?? 0;

  async function ack(id: string) {
    try {
      await http.post(`/alerts/${id}/ack`, { note: null });
      toast.success('Alert acknowledged');
      tableQuery.refetch();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-end justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Alerts</h1>
          <p className="text-sm text-muted-foreground">{total} matching</p>
        </div>
        <div className="flex gap-2">
          <Button asChild variant="outline">
            <Link to="/alerts/rules"><Settings className="h-4 w-4" /> Manage rules</Link>
          </Button>
          <Select value={state} onValueChange={(v) => { setState(v); setCurrentPage(1); }}>
            <SelectTrigger className="w-[180px]"><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="firing">Firing</SelectItem>
              <SelectItem value="acknowledged">Acknowledged</SelectItem>
              <SelectItem value="resolved">Resolved</SelectItem>
              <SelectItem value="suppressed">Suppressed</SelectItem>
            </SelectContent>
          </Select>
        </div>
      </div>

      <Card>
        <CardContent className="p-0">
          {tableQuery.isLoading ? (
            <div className="p-4 space-y-2">{Array.from({ length: 5 }).map((_, i) => <Skeleton key={`s-${i}`} className="h-9 w-full" />)}</div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-24">Severity</TableHead>
                  <TableHead>Site</TableHead>
                  <TableHead>Summary</TableHead>
                  <TableHead className="w-44">First seen</TableHead>
                  <TableHead className="w-24"></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.length === 0 && (
                  <TableRow><TableCell colSpan={5} className="text-muted-foreground">All clear.</TableCell></TableRow>
                )}
                {data.map((a) => (
                  <TableRow key={a.id}>
                    <TableCell><Badge variant={sevVariant(a.severity)}>{a.severity}</Badge></TableCell>
                    <TableCell className="font-mono text-xs">{a.site_id.slice(0, 8)}…</TableCell>
                    <TableCell>{a.summary}</TableCell>
                    <TableCell className="text-muted-foreground">{formatDate(a.first_seen_at)}</TableCell>
                    <TableCell>
                      {state === 'firing' && canAck && (
                        <Button size="sm" variant="outline" onClick={() => ack(a.id)}>
                          <Check className="h-4 w-4" /> Ack
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      {pageCount > 1 && (
        <div className="flex items-center justify-end gap-2 text-sm text-muted-foreground">
          <Button variant="outline" size="sm" onClick={() => setCurrentPage(currentPage - 1)} disabled={currentPage <= 1}>Prev</Button>
          <span>page {currentPage} of {pageCount}</span>
          <Button variant="outline" size="sm" onClick={() => setCurrentPage(currentPage + 1)} disabled={currentPage >= pageCount}>Next</Button>
        </div>
      )}
    </div>
  );
}
