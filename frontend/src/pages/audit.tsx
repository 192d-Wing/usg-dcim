import { useMemo, useState } from 'react';
import { useTable, useList } from '@refinedev/core';
import { useQuery } from '@tanstack/react-query';
import { ChevronDown, ChevronRight, ScrollText } from 'lucide-react';
import { http } from '@/lib/http';
import { formatDate } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import { Input } from '@/components/ui/input';
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table';
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select';

type AuditEntry = {
  id: string;
  occurred_at: string;
  actor_user_id: string | null;
  actor_token_id: string | null;
  actor_label: string | null;
  actor_ip: string | null;
  action: string;
  target_type: string | null;
  target_id: string | null;
  site_id: string | null;
  request_id: string | null;
  success: boolean;
  diff_json: Record<string, unknown>;
  metadata_json: Record<string, unknown>;
};
type Site = { id: string; code: string; name: string };

const ALL = '__all__';

export function AuditPage() {
  const [action, setAction] = useState(ALL);
  const [siteId, setSiteId] = useState(ALL);
  const [targetType, setTargetType] = useState('');
  const [targetId, setTargetId] = useState('');
  const [actorLabel, setActorLabel] = useState('');

  const filters = useMemo(() => {
    const f: { field: string; operator: 'eq' | 'contains'; value: string }[] = [];
    if (action !== ALL) f.push({ field: 'action', operator: 'eq', value: action });
    if (siteId !== ALL) f.push({ field: 'site_id', operator: 'eq', value: siteId });
    if (targetType) f.push({ field: 'target_type', operator: 'eq', value: targetType });
    if (targetId) f.push({ field: 'target_id', operator: 'eq', value: targetId });
    return f;
  }, [action, siteId, targetType, targetId]);

  const { tableQuery, result, currentPage, pageCount, setCurrentPage } = useTable<AuditEntry>({
    resource: 'audit/log',
    pagination: { pageSize: 50 },
    filters: { permanent: filters },
  });
  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const sites = sitesRes.result.data ?? [];
  const sitesById = new Map(sites.map((s) => [s.id, s]));

  const actions = useQuery({
    queryKey: ['audit-actions'],
    queryFn: async () => (await http.get<string[]>('/audit/actions')).data,
    staleTime: 60_000,
  });

  let data = result.data ?? [];
  const total = result.total ?? 0;
  // actor_label is a free-text needle filtered client-side — saves another endpoint param.
  if (actorLabel) {
    const needle = actorLabel.toLowerCase();
    data = data.filter((a) => (a.actor_label ?? '').toLowerCase().includes(needle));
  }

  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight flex items-center gap-2">
          <ScrollText className="h-5 w-5" /> Audit log
        </h1>
        <p className="text-sm text-muted-foreground">
          {total.toLocaleString()} entries match the current filter
        </p>
      </div>

      <Card>
        <CardContent className="grid gap-3 p-4 md:grid-cols-5">
          <div className="space-y-1">
            <label className="text-xs font-medium text-muted-foreground">Action</label>
            <Select value={action} onValueChange={(v) => { setAction(v); setCurrentPage(1); }}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>All actions</SelectItem>
                {(actions.data ?? []).map((a) => (
                  <SelectItem key={a} value={a}>{a}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1">
            <label className="text-xs font-medium text-muted-foreground">Site</label>
            <Select value={siteId} onValueChange={(v) => { setSiteId(v); setCurrentPage(1); }}>
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem value={ALL}>All sites</SelectItem>
                {sites.map((s) => (
                  <SelectItem key={s.id} value={s.id}>{s.code} · {s.name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1">
            <label className="text-xs font-medium text-muted-foreground">Target type</label>
            <Input
              value={targetType}
              onChange={(e) => { setTargetType(e.target.value); setCurrentPage(1); }}
              placeholder="asset, rack, site…"
            />
          </div>
          <div className="space-y-1">
            <label className="text-xs font-medium text-muted-foreground">Target id</label>
            <Input
              value={targetId}
              onChange={(e) => { setTargetId(e.target.value); setCurrentPage(1); }}
              placeholder="exact uuid"
              className="font-mono text-xs"
            />
          </div>
          <div className="space-y-1">
            <label className="text-xs font-medium text-muted-foreground">Actor (contains)</label>
            <Input
              value={actorLabel}
              onChange={(e) => setActorLabel(e.target.value)}
              placeholder="email or label"
            />
          </div>
        </CardContent>
      </Card>

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
                  <TableHead className="w-44">When</TableHead>
                  <TableHead>Actor</TableHead>
                  <TableHead>Action</TableHead>
                  <TableHead>Target</TableHead>
                  <TableHead>Site</TableHead>
                  <TableHead className="w-20">Result</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={6} className="text-muted-foreground">
                      No entries match this filter.
                    </TableCell>
                  </TableRow>
                )}
                {data.map((e) => <AuditRow key={e.id} entry={e} sitesById={sitesById} />)}
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

function AuditRow({ entry, sitesById }: { entry: AuditEntry; sitesById: Map<string, Site> }) {
  const [open, setOpen] = useState(false);
  const hasDetail = (
    Object.keys(entry.diff_json ?? {}).length > 0
    || Object.keys(entry.metadata_json ?? {}).length > 0
    || !!entry.actor_ip
    || !!entry.request_id
  );
  const site = entry.site_id ? sitesById.get(entry.site_id) : null;

  return (
    <>
      <TableRow
        className={hasDetail ? 'cursor-pointer hover:bg-accent/40' : ''}
        onClick={() => hasDetail && setOpen(!open)}
      >
        <TableCell className="text-xs">
          <div>{formatDate(entry.occurred_at)}</div>
        </TableCell>
        <TableCell className="text-sm">
          {entry.actor_label ?? <span className="text-muted-foreground">—</span>}
          {entry.actor_token_id && (
            <Badge variant="outline" className="ml-1.5 text-[10px]">token</Badge>
          )}
        </TableCell>
        <TableCell>
          <code className="font-mono text-xs">{entry.action}</code>
        </TableCell>
        <TableCell className="text-xs">
          {entry.target_type ? (
            <span className="font-mono">
              {entry.target_type}{entry.target_id ? `:${entry.target_id.slice(0, 8)}…` : ''}
            </span>
          ) : <span className="text-muted-foreground">—</span>}
        </TableCell>
        <TableCell className="text-xs">
          {site ? `${site.code}` : entry.site_id ? `${entry.site_id.slice(0, 8)}…` : <span className="text-muted-foreground">—</span>}
        </TableCell>
        <TableCell>
          <div className="flex items-center gap-1">
            <Badge variant={entry.success ? 'success' : 'critical'}>
              {entry.success ? 'ok' : 'fail'}
            </Badge>
            {hasDetail && (
              open ? <ChevronDown className="h-3.5 w-3.5 text-muted-foreground" />
                   : <ChevronRight className="h-3.5 w-3.5 text-muted-foreground" />
            )}
          </div>
        </TableCell>
      </TableRow>
      {open && hasDetail && (
        <TableRow>
          <TableCell colSpan={6} className="bg-muted/30">
            <div className="grid gap-3 text-xs md:grid-cols-2">
              {(entry.actor_ip || entry.request_id) && (
                <div className="space-y-1">
                  <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                    Request
                  </div>
                  {entry.actor_ip && <div>IP: <span className="font-mono">{entry.actor_ip}</span></div>}
                  {entry.request_id && <div>Request id: <span className="font-mono">{entry.request_id}</span></div>}
                </div>
              )}
              {Object.keys(entry.diff_json ?? {}).length > 0 && (
                <div className="space-y-1">
                  <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                    Diff
                  </div>
                  <pre className="overflow-x-auto rounded-md bg-background p-2 font-mono text-[11px]">
                    {JSON.stringify(entry.diff_json, null, 2)}
                  </pre>
                </div>
              )}
              {Object.keys(entry.metadata_json ?? {}).length > 0 && (
                <div className="space-y-1 md:col-span-2">
                  <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                    Metadata
                  </div>
                  <pre className="overflow-x-auto rounded-md bg-background p-2 font-mono text-[11px]">
                    {JSON.stringify(entry.metadata_json, null, 2)}
                  </pre>
                </div>
              )}
            </div>
          </TableCell>
        </TableRow>
      )}
    </>
  );
}
