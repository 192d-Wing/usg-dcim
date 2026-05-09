import { useState } from 'react';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { useTable, useGetIdentity, useList } from '@refinedev/core';
import { useQueryClient } from '@tanstack/react-query';
import { Plus, Pencil, Trash2, Wrench } from 'lucide-react';
import { http } from '@/lib/http';
import { cn, formatDate, relativeTime } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Badge } from '@/components/ui/badge';
import { Skeleton } from '@/components/ui/skeleton';
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table';
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger,
} from '@/components/ui/dialog';
import {
  Form, FormControl, FormField, FormItem, FormLabel, FormMessage,
} from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select';
import { toast } from 'sonner';

type Site = { id: string; code: string; name: string };
type Window = {
  id: string; name: string;
  site_id: string | null;
  starts_at: string; ends_at: string;
  reason: string | null;
  created_by: string | null;
  asset_filter_json: Record<string, unknown>;
};

type WindowStatus = 'active' | 'upcoming' | 'past';

function statusOf(w: Window, now = Date.now()): WindowStatus {
  const start = new Date(w.starts_at).getTime();
  const end = new Date(w.ends_at).getTime();
  if (now < start) return 'upcoming';
  if (now > end) return 'past';
  return 'active';
}

const STATUS_VARIANT: Record<WindowStatus, 'critical' | 'warning' | 'secondary'> = {
  active: 'critical',
  upcoming: 'warning',
  past: 'secondary',
};

export function MaintenancePage() {
  const { tableQuery, result, currentPage, pageCount, setCurrentPage } = useTable<Window>({
    resource: 'alerts/maintenance-windows',
    pagination: { pageSize: 50 },
    sorters: { initial: [{ field: 'starts_at', order: 'desc' }] },
  });
  const sitesRes = useList<Site>({
    resource: 'inventory/sites', pagination: { pageSize: 200 },
  });
  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const canConfigure = identity?.capabilities.includes('alerts:configure');
  const sites = sitesRes.result.data ?? [];
  const sitesById = new Map(sites.map((s) => [s.id, s]));
  const data = result.data ?? [];
  const total = result.total ?? 0;
  const qc = useQueryClient();
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<Window | null>(null);

  async function refresh() {
    await tableQuery.refetch();
    await qc.invalidateQueries({ queryKey: ['site-detail'] });
  }

  async function remove(w: Window) {
    if (!window.confirm(`Delete maintenance window "${w.name}"?`)) return;
    try {
      await http.delete(`/alerts/maintenance-windows/${w.id}`);
      toast.success('Maintenance window deleted');
      await refresh();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to delete');
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-end justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Maintenance windows</h1>
          <p className="text-sm text-muted-foreground">
            {total} total · suppress alerts during planned work
          </p>
        </div>
        {canConfigure && (
          <Dialog open={createOpen} onOpenChange={setCreateOpen}>
            <DialogTrigger asChild>
              <Button><Plus className="h-4 w-4" /> New window</Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader><DialogTitle>New maintenance window</DialogTitle></DialogHeader>
              <WindowForm
                sites={sites}
                onSaved={async () => { setCreateOpen(false); await refresh(); }}
              />
            </DialogContent>
          </Dialog>
        )}
      </div>

      <Card>
        <CardContent className="p-0">
          {tableQuery.isLoading ? (
            <div className="space-y-2 p-4">
              {Array.from({ length: 5 }).map((_, i) => <Skeleton key={`s-${i}`} className="h-9 w-full" />)}
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-24">Status</TableHead>
                  <TableHead>Name</TableHead>
                  <TableHead>Scope</TableHead>
                  <TableHead>Starts</TableHead>
                  <TableHead>Ends</TableHead>
                  <TableHead>Reason</TableHead>
                  <TableHead>Created by</TableHead>
                  {canConfigure && <TableHead className="w-28" />}
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={canConfigure ? 8 : 7} className="text-muted-foreground">
                      No maintenance windows configured.
                    </TableCell>
                  </TableRow>
                )}
                {data.map((w) => {
                  const status = statusOf(w);
                  const site = w.site_id ? sitesById.get(w.site_id) : null;
                  const scope = site
                    ? `${site.code} · ${site.name}`
                    : w.site_id
                      ? `site ${w.site_id.slice(0, 8)}…`
                      : 'all sites';
                  return (
                    <TableRow key={w.id}>
                      <TableCell>
                        <Badge variant={STATUS_VARIANT[status]}>{status}</Badge>
                      </TableCell>
                      <TableCell className="font-medium">{w.name}</TableCell>
                      <TableCell className={cn('text-sm', !w.site_id && 'text-muted-foreground')}>
                        {scope}
                      </TableCell>
                      <TableCell className="text-sm">
                        <div>{formatDate(w.starts_at)}</div>
                        <div className="text-xs text-muted-foreground">{relativeTime(w.starts_at)}</div>
                      </TableCell>
                      <TableCell className="text-sm">
                        <div>{formatDate(w.ends_at)}</div>
                        <div className="text-xs text-muted-foreground">{relativeTime(w.ends_at)}</div>
                      </TableCell>
                      <TableCell className="max-w-xs truncate text-sm text-muted-foreground">
                        {w.reason ?? '—'}
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground">
                        {w.created_by ?? '—'}
                      </TableCell>
                      {canConfigure && (
                        <TableCell>
                          <div className="flex gap-1">
                            <Button size="sm" variant="ghost" onClick={() => setEditing(w)}>
                              <Pencil className="h-3.5 w-3.5" />
                            </Button>
                            <Button size="sm" variant="ghost" onClick={() => remove(w)}>
                              <Trash2 className="h-3.5 w-3.5" />
                            </Button>
                          </div>
                        </TableCell>
                      )}
                    </TableRow>
                  );
                })}
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

      <Dialog open={editing !== null} onOpenChange={(o) => { if (!o) setEditing(null); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <Wrench className="h-4 w-4" /> Edit maintenance window
            </DialogTitle>
          </DialogHeader>
          {editing && (
            <WindowForm
              sites={sites}
              window={editing}
              onSaved={async () => { setEditing(null); await refresh(); }}
            />
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}

const formSchema = z.object({
  name: z.string().min(1, 'Name required'),
  site_id: z.string(),
  starts_at: z.string().min(1, 'Start required'),
  ends_at: z.string().min(1, 'End required'),
  reason: z.string().optional(),
}).refine(
  (v) => new Date(v.ends_at) > new Date(v.starts_at),
  { message: 'End must be after start', path: ['ends_at'] },
);

const ALL_SITES = '__all__';

function toLocalInput(iso: string | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  // datetime-local needs YYYY-MM-DDTHH:mm in the browser's local TZ
  const pad = (n: number) => n.toString().padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function fromLocalInput(local: string): string {
  // The browser parses "YYYY-MM-DDTHH:mm" as local time; toISOString shifts to UTC for the API.
  return new Date(local).toISOString();
}

function WindowForm({
  sites, window: existing, onSaved,
}: {
  sites: Site[];
  window?: Window;
  onSaved: () => void;
}) {
  const editing = !!existing;
  const form = useForm<z.infer<typeof formSchema>>({
    resolver: zodResolver(formSchema),
    defaultValues: {
      name: existing?.name ?? '',
      site_id: existing?.site_id ?? ALL_SITES,
      starts_at: toLocalInput(existing?.starts_at),
      ends_at: toLocalInput(existing?.ends_at),
      reason: existing?.reason ?? '',
    },
  });

  async function onSubmit(v: z.infer<typeof formSchema>) {
    const body = {
      name: v.name,
      site_id: v.site_id === ALL_SITES ? null : v.site_id,
      starts_at: fromLocalInput(v.starts_at),
      ends_at: fromLocalInput(v.ends_at),
      reason: v.reason || null,
      asset_filter_json: existing?.asset_filter_json ?? {},
    };
    try {
      if (editing && existing) {
        await http.patch(`/alerts/maintenance-windows/${existing.id}`, body);
        toast.success('Maintenance window updated');
      } else {
        await http.post('/alerts/maintenance-windows', body);
        toast.success('Maintenance window created');
      }
      onSaved();
    } catch (err: any) {
      toast.error(err?.message ?? 'save failed');
    }
  }

  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="name" render={({ field }) => (
          <FormItem>
            <FormLabel>Name</FormLabel>
            <FormControl><Input placeholder="e.g. Q3 power maintenance" {...field} /></FormControl>
            <FormMessage />
          </FormItem>
        )} />
        <FormField control={form.control} name="site_id" render={({ field }) => (
          <FormItem>
            <FormLabel>Scope</FormLabel>
            <Select value={field.value} onValueChange={field.onChange}>
              <FormControl>
                <SelectTrigger><SelectValue placeholder="All sites" /></SelectTrigger>
              </FormControl>
              <SelectContent>
                <SelectItem value={ALL_SITES}>All sites</SelectItem>
                {sites.map((s) => (
                  <SelectItem key={s.id} value={s.id}>{s.code} · {s.name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <FormMessage />
          </FormItem>
        )} />
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="starts_at" render={({ field }) => (
            <FormItem>
              <FormLabel>Starts at</FormLabel>
              <FormControl><Input type="datetime-local" {...field} /></FormControl>
              <FormMessage />
            </FormItem>
          )} />
          <FormField control={form.control} name="ends_at" render={({ field }) => (
            <FormItem>
              <FormLabel>Ends at</FormLabel>
              <FormControl><Input type="datetime-local" {...field} /></FormControl>
              <FormMessage />
            </FormItem>
          )} />
        </div>
        <FormField control={form.control} name="reason" render={({ field }) => (
          <FormItem>
            <FormLabel>Reason (optional)</FormLabel>
            <FormControl><Input placeholder="e.g. PDU firmware upgrade" {...field} /></FormControl>
            <FormMessage />
          </FormItem>
        )} />
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : editing ? 'Save changes' : 'Create window'}
        </Button>
      </form>
    </Form>
  );
}
