import { useState } from 'react';
import { useNavigate } from 'react-router';
import { useTable, useGetIdentity, useList } from '@refinedev/core';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import {
  ArrowLeft, Plus, Pencil, Trash2, Power, ExternalLink,
} from 'lucide-react';
import { http } from '@/lib/http';
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

type Severity = 'info' | 'warning' | 'minor' | 'major' | 'critical';
type Operator = '>' | '<' | '>=' | '<=' | '==' | '!=';

const SEVERITIES: Severity[] = ['info', 'warning', 'minor', 'major', 'critical'];
const OPERATORS: Operator[] = ['>', '<', '>=', '<=', '==', '!='];

type Site = { id: string; code: string; name: string };
type Rule = {
  id: string;
  name: string;
  description: string | null;
  metric: string;
  operator: string;
  threshold: number;
  duration_seconds: number;
  severity: Severity;
  site_scope_id: string | null;
  enabled: boolean;
  runbook_url: string | null;
  asset_filter_json: Record<string, unknown>;
};

function sevVariant(s: Severity): 'critical' | 'warning' | 'success' | 'secondary' {
  if (s === 'critical' || s === 'major') return 'critical';
  if (s === 'minor' || s === 'warning') return 'warning';
  if (s === 'info') return 'secondary';
  return 'success';
}

function fmtDuration(s: number): string {
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.round(s / 60)}m`;
  return `${(s / 3600).toFixed(1)}h`;
}

export function AlertRulesPage() {
  const nav = useNavigate();
  const { tableQuery, result, currentPage, pageCount, setCurrentPage } = useTable<Rule>({
    resource: 'alerts/rules',
    pagination: { pageSize: 50 },
    sorters: { initial: [{ field: 'name', order: 'asc' }] },
  });
  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const sites = sitesRes.result.data ?? [];
  const sitesById = new Map(sites.map((s) => [s.id, s]));
  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const canConfigure = identity?.capabilities.includes('alerts:configure');
  const data = result.data ?? [];
  const total = result.total ?? 0;

  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<Rule | null>(null);

  async function refresh() { await tableQuery.refetch(); }

  async function toggle(r: Rule) {
    try {
      await http.patch(`/alerts/rules/${r.id}`, { enabled: !r.enabled });
      toast.success(r.enabled ? 'Rule disabled' : 'Rule enabled');
      await refresh();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  async function remove(r: Rule) {
    if (!window.confirm(`Delete alert rule "${r.name}"?`)) return;
    try {
      await http.delete(`/alerts/rules/${r.id}`);
      toast.success('Alert rule deleted');
      await refresh();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to delete');
    }
  }

  return (
    <div className="space-y-4">
      <Button variant="ghost" size="sm" onClick={() => nav('/alerts')} className="-ml-2">
        <ArrowLeft className="h-4 w-4" /> Back to alerts
      </Button>

      <div className="flex items-end justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Alert rules</h1>
          <p className="text-sm text-muted-foreground">
            {total} configured · evaluated against telemetry every cycle
          </p>
        </div>
        {canConfigure && (
          <Dialog open={createOpen} onOpenChange={setCreateOpen}>
            <DialogTrigger asChild>
              <Button><Plus className="h-4 w-4" /> New rule</Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader><DialogTitle>New alert rule</DialogTitle></DialogHeader>
              <RuleForm
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
                  <TableHead className="w-24">Severity</TableHead>
                  <TableHead>Name</TableHead>
                  <TableHead>Trigger</TableHead>
                  <TableHead className="w-24">For</TableHead>
                  <TableHead>Scope</TableHead>
                  <TableHead className="w-24">Status</TableHead>
                  <TableHead className="w-12">Doc</TableHead>
                  {canConfigure && <TableHead className="w-32" />}
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={canConfigure ? 8 : 7} className="text-muted-foreground">
                      No alert rules configured.
                    </TableCell>
                  </TableRow>
                )}
                {data.map((r) => {
                  const site = r.site_scope_id ? sitesById.get(r.site_scope_id) : null;
                  const scope = site
                    ? `${site.code} · ${site.name}`
                    : r.site_scope_id
                      ? `site ${r.site_scope_id.slice(0, 8)}…`
                      : 'enterprise default';
                  return (
                    <TableRow key={r.id}>
                      <TableCell>
                        <Badge variant={sevVariant(r.severity)}>{r.severity}</Badge>
                      </TableCell>
                      <TableCell>
                        <div className="font-medium">{r.name}</div>
                        {r.description && (
                          <div className="text-xs text-muted-foreground">{r.description}</div>
                        )}
                      </TableCell>
                      <TableCell className="font-mono text-xs">
                        {r.metric} {r.operator} {r.threshold}
                      </TableCell>
                      <TableCell className="tabular-nums text-xs">
                        {fmtDuration(r.duration_seconds)}
                      </TableCell>
                      <TableCell className={r.site_scope_id ? '' : 'text-muted-foreground'}>
                        {scope}
                      </TableCell>
                      <TableCell>
                        <Badge variant={r.enabled ? 'success' : 'secondary'}>
                          {r.enabled ? 'enabled' : 'disabled'}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        {r.runbook_url ? (
                          <a
                            href={r.runbook_url}
                            target="_blank" rel="noreferrer"
                            className="text-muted-foreground hover:text-foreground"
                            title="Runbook"
                          >
                            <ExternalLink className="h-3.5 w-3.5" />
                          </a>
                        ) : (
                          <span className="text-muted-foreground">—</span>
                        )}
                      </TableCell>
                      {canConfigure && (
                        <TableCell>
                          <div className="flex gap-1">
                            <Button
                              size="sm" variant="ghost"
                              onClick={() => toggle(r)}
                              title={r.enabled ? 'Disable' : 'Enable'}
                            >
                              <Power className="h-3.5 w-3.5" />
                            </Button>
                            <Button size="sm" variant="ghost" onClick={() => setEditing(r)}>
                              <Pencil className="h-3.5 w-3.5" />
                            </Button>
                            <Button size="sm" variant="ghost" onClick={() => remove(r)}>
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
          <DialogHeader><DialogTitle>Edit alert rule</DialogTitle></DialogHeader>
          {editing && (
            <RuleForm
              sites={sites}
              rule={editing}
              onSaved={async () => { setEditing(null); await refresh(); }}
            />
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}

const ALL_SITES = '__all__';

const formSchema = z.object({
  name: z.string().min(1, 'Name required'),
  description: z.string().optional(),
  metric: z.string().min(1, 'Metric required'),
  operator: z.enum(OPERATORS),
  threshold: z.coerce.number(),
  duration_seconds: z.coerce.number().min(0).max(86400),
  severity: z.enum(SEVERITIES),
  site_scope_id: z.string(),
  runbook_url: z.string().optional(),
  enabled: z.boolean(),
});

function RuleForm({
  sites, rule, onSaved,
}: {
  sites: Site[];
  rule?: Rule;
  onSaved: () => void;
}) {
  const editing = !!rule;
  const form = useForm<z.infer<typeof formSchema>>({
    resolver: zodResolver(formSchema),
    defaultValues: {
      name: rule?.name ?? '',
      description: rule?.description ?? '',
      metric: rule?.metric ?? '',
      operator: (rule?.operator as Operator) ?? '>',
      threshold: rule?.threshold ?? 0,
      duration_seconds: rule?.duration_seconds ?? 60,
      severity: rule?.severity ?? 'major',
      site_scope_id: rule?.site_scope_id ?? ALL_SITES,
      runbook_url: rule?.runbook_url ?? '',
      enabled: rule?.enabled ?? true,
    },
  });

  async function onSubmit(v: z.infer<typeof formSchema>) {
    const body = {
      name: v.name,
      description: v.description || null,
      metric: v.metric,
      operator: v.operator,
      threshold: v.threshold,
      duration_seconds: v.duration_seconds,
      severity: v.severity,
      site_scope_id: v.site_scope_id === ALL_SITES ? null : v.site_scope_id,
      runbook_url: v.runbook_url || null,
      enabled: v.enabled,
      asset_filter_json: rule?.asset_filter_json ?? {},
    };
    try {
      if (editing && rule) {
        await http.patch(`/alerts/rules/${rule.id}`, body);
        toast.success('Alert rule updated');
      } else {
        await http.post('/alerts/rules', body);
        toast.success('Alert rule created');
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
            <FormControl><Input placeholder="e.g. PDU input kW above 80%" {...field} /></FormControl>
            <FormMessage />
          </FormItem>
        )} />
        <FormField control={form.control} name="description" render={({ field }) => (
          <FormItem>
            <FormLabel>Description (optional)</FormLabel>
            <FormControl><Input {...field} /></FormControl>
            <FormMessage />
          </FormItem>
        )} />
        <FormField control={form.control} name="metric" render={({ field }) => (
          <FormItem>
            <FormLabel>Metric</FormLabel>
            <FormControl>
              <Input placeholder="e.g. pdu.input.kw, sensor.temp.c" {...field} />
            </FormControl>
            <FormMessage />
          </FormItem>
        )} />
        <div className="grid grid-cols-3 gap-3">
          <FormField control={form.control} name="operator" render={({ field }) => (
            <FormItem>
              <FormLabel>Operator</FormLabel>
              <Select value={field.value} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
                <SelectContent>
                  {OPERATORS.map((o) => <SelectItem key={o} value={o}>{o}</SelectItem>)}
                </SelectContent>
              </Select>
              <FormMessage />
            </FormItem>
          )} />
          <FormField control={form.control} name="threshold" render={({ field }) => (
            <FormItem>
              <FormLabel>Threshold</FormLabel>
              <FormControl><Input type="number" step="any" {...field} /></FormControl>
              <FormMessage />
            </FormItem>
          )} />
          <FormField control={form.control} name="duration_seconds" render={({ field }) => (
            <FormItem>
              <FormLabel>Duration (s)</FormLabel>
              <FormControl><Input type="number" min={0} max={86400} {...field} /></FormControl>
              <FormMessage />
            </FormItem>
          )} />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="severity" render={({ field }) => (
            <FormItem>
              <FormLabel>Severity</FormLabel>
              <Select value={field.value} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
                <SelectContent>
                  {SEVERITIES.map((s) => <SelectItem key={s} value={s}>{s}</SelectItem>)}
                </SelectContent>
              </Select>
              <FormMessage />
            </FormItem>
          )} />
          <FormField control={form.control} name="site_scope_id" render={({ field }) => (
            <FormItem>
              <FormLabel>Scope</FormLabel>
              <Select value={field.value} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
                <SelectContent>
                  <SelectItem value={ALL_SITES}>Enterprise default</SelectItem>
                  {sites.map((s) => (
                    <SelectItem key={s.id} value={s.id}>{s.code} · {s.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <FormMessage />
            </FormItem>
          )} />
        </div>
        <FormField control={form.control} name="runbook_url" render={({ field }) => (
          <FormItem>
            <FormLabel>Runbook URL (optional)</FormLabel>
            <FormControl>
              <Input type="url" placeholder="https://runbooks.example/pdu-overload" {...field} />
            </FormControl>
            <FormMessage />
          </FormItem>
        )} />
        <FormField control={form.control} name="enabled" render={({ field }) => (
          <FormItem className="flex items-center gap-3 space-y-0">
            <FormControl>
              <input
                type="checkbox"
                checked={field.value}
                onChange={(e) => field.onChange(e.target.checked)}
                className="h-4 w-4"
              />
            </FormControl>
            <FormLabel className="!mt-0">Enabled</FormLabel>
          </FormItem>
        )} />
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : editing ? 'Save changes' : 'Create rule'}
        </Button>
      </form>
    </Form>
  );
}
