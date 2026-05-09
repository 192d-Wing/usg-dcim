import { useState } from 'react';
import { useTable, useGetIdentity } from '@refinedev/core';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import {
  Plus, Pencil, Trash2, Webhook, MessageSquare, Mail, Send, Power,
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
type Kind = 'webhook' | 'slack' | 'email';
type Channel = {
  id: string;
  name: string;
  kind: Kind;
  config_json: Record<string, unknown>;
  min_severity: Severity;
  notify_on_fire: boolean;
  notify_on_resolve: boolean;
  enabled: boolean;
  description: string | null;
};

const SEVERITIES: Severity[] = ['info', 'warning', 'minor', 'major', 'critical'];

const KIND_ICON: Record<Kind, React.ComponentType<{ className?: string }>> = {
  webhook: Webhook,
  slack: MessageSquare,
  email: Mail,
};

function configSummary(c: Channel): string {
  if (c.kind === 'webhook') return (c.config_json.url as string) ?? '(no URL)';
  if (c.kind === 'slack') return (c.config_json.webhook_url as string) ?? '(no URL)';
  if (c.kind === 'email') {
    const r = c.config_json.recipients as string[] | undefined;
    return r && r.length ? r.join(', ') : '(no recipients)';
  }
  return '';
}

export function NotificationsPage() {
  const { tableQuery, result, currentPage, pageCount, setCurrentPage } = useTable<Channel>({
    resource: 'notifications/channels',
    pagination: { pageSize: 50 },
    sorters: { initial: [{ field: 'name', order: 'asc' }] },
  });
  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const canConfigure = identity?.capabilities.includes('alerts:configure');
  const data = result.data ?? [];

  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<Channel | null>(null);

  async function refresh() { await tableQuery.refetch(); }

  async function toggle(c: Channel) {
    try {
      await http.patch(`/notifications/channels/${c.id}`, { enabled: !c.enabled });
      toast.success(c.enabled ? 'Channel disabled' : 'Channel enabled');
      await refresh();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  async function remove(c: Channel) {
    if (!window.confirm(`Delete channel "${c.name}"?`)) return;
    try {
      await http.delete(`/notifications/channels/${c.id}`);
      toast.success('Channel deleted');
      await refresh();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  async function sendTest(c: Channel) {
    try {
      const r = await http.post<{ delivered: boolean; error: string | null }>(
        `/notifications/channels/${c.id}/test`, {},
      );
      if (r.data.delivered) toast.success(`Test delivered to ${c.name}`);
      else toast.error(`Test failed: ${r.data.error ?? 'unknown error'}`);
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  if (!canConfigure) {
    return (
      <div className="space-y-2">
        <h1 className="text-2xl font-semibold tracking-tight">Notifications</h1>
        <p className="text-sm text-muted-foreground">
          You don't have <code className="font-mono">alerts:configure</code>. Ask an admin for a
          role that includes it.
        </p>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex items-end justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Notification channels</h1>
          <p className="text-sm text-muted-foreground">
            Outbound delivery for alert.fire and alert.resolve events
          </p>
        </div>
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button><Plus className="h-4 w-4" /> New channel</Button>
          </DialogTrigger>
          <DialogContent>
            <DialogHeader><DialogTitle>New notification channel</DialogTitle></DialogHeader>
            <ChannelForm onSaved={async () => { setCreateOpen(false); await refresh(); }} />
          </DialogContent>
        </Dialog>
      </div>

      <Card>
        <CardContent className="p-0">
          {tableQuery.isLoading ? (
            <div className="space-y-2 p-4">
              {Array.from({ length: 4 }).map((_, i) => <Skeleton key={`s-${i}`} className="h-9 w-full" />)}
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead className="w-28">Kind</TableHead>
                  <TableHead>Name</TableHead>
                  <TableHead>Target</TableHead>
                  <TableHead>Routing</TableHead>
                  <TableHead className="w-24">Status</TableHead>
                  <TableHead className="w-44" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={6} className="text-muted-foreground">
                      No channels configured. Alerts will fire silently until you add one.
                    </TableCell>
                  </TableRow>
                )}
                {data.map((c) => {
                  const Icon = KIND_ICON[c.kind];
                  return (
                    <TableRow key={c.id}>
                      <TableCell>
                        <Badge variant="secondary" className="flex w-fit items-center gap-1">
                          <Icon className="h-3 w-3" /> {c.kind}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        <div className="font-medium">{c.name}</div>
                        {c.description && (
                          <div className="text-xs text-muted-foreground">{c.description}</div>
                        )}
                      </TableCell>
                      <TableCell className="max-w-xs truncate font-mono text-xs">
                        {configSummary(c)}
                      </TableCell>
                      <TableCell className="text-xs">
                        <div>≥ <span className="font-medium">{c.min_severity}</span></div>
                        <div className="text-muted-foreground">
                          {c.notify_on_fire && 'fire'}
                          {c.notify_on_fire && c.notify_on_resolve && ' + '}
                          {c.notify_on_resolve && 'resolve'}
                          {!c.notify_on_fire && !c.notify_on_resolve && '(no events)'}
                        </div>
                      </TableCell>
                      <TableCell>
                        <Badge variant={c.enabled ? 'success' : 'secondary'}>
                          {c.enabled ? 'enabled' : 'disabled'}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        <div className="flex gap-1">
                          <Button size="sm" variant="ghost" onClick={() => sendTest(c)} title="Send test">
                            <Send className="h-3.5 w-3.5" />
                          </Button>
                          <Button size="sm" variant="ghost" onClick={() => toggle(c)} title={c.enabled ? 'Disable' : 'Enable'}>
                            <Power className="h-3.5 w-3.5" />
                          </Button>
                          <Button size="sm" variant="ghost" onClick={() => setEditing(c)}>
                            <Pencil className="h-3.5 w-3.5" />
                          </Button>
                          <Button size="sm" variant="ghost" onClick={() => remove(c)}>
                            <Trash2 className="h-3.5 w-3.5" />
                          </Button>
                        </div>
                      </TableCell>
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
          <DialogHeader><DialogTitle>Edit notification channel</DialogTitle></DialogHeader>
          {editing && (
            <ChannelForm
              channel={editing}
              onSaved={async () => { setEditing(null); await refresh(); }}
            />
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}

const baseSchema = z.object({
  name: z.string().min(1, 'Name required'),
  kind: z.enum(['webhook', 'slack', 'email']),
  description: z.string().optional(),
  min_severity: z.enum(['info', 'warning', 'minor', 'major', 'critical']),
  notify_on_fire: z.boolean(),
  notify_on_resolve: z.boolean(),
  enabled: z.boolean(),
  // Per-kind config fields, all optional in the schema and validated below.
  webhook_url: z.string().optional(),
  slack_url: z.string().optional(),
  recipients: z.string().optional(), // comma-separated in the UI
});

function ChannelForm({
  channel, onSaved,
}: {
  channel?: Channel;
  onSaved: () => void;
}) {
  const editing = !!channel;
  const initialKind: Kind = channel?.kind ?? 'webhook';
  const initialConfig = channel?.config_json ?? {};
  const form = useForm<z.infer<typeof baseSchema>>({
    resolver: zodResolver(baseSchema),
    defaultValues: {
      name: channel?.name ?? '',
      kind: initialKind,
      description: channel?.description ?? '',
      min_severity: channel?.min_severity ?? 'warning',
      notify_on_fire: channel?.notify_on_fire ?? true,
      notify_on_resolve: channel?.notify_on_resolve ?? true,
      enabled: channel?.enabled ?? true,
      webhook_url: (initialConfig.url as string) ?? '',
      slack_url: (initialConfig.webhook_url as string) ?? '',
      recipients: ((initialConfig.recipients as string[]) ?? []).join(', '),
    },
  });
  const kind = form.watch('kind');

  async function onSubmit(v: z.infer<typeof baseSchema>) {
    let config_json: Record<string, unknown> = {};
    if (v.kind === 'webhook') {
      if (!v.webhook_url) {
        form.setError('webhook_url', { message: 'URL required for webhook channels' });
        return;
      }
      config_json = { url: v.webhook_url };
    } else if (v.kind === 'slack') {
      if (!v.slack_url) {
        form.setError('slack_url', { message: 'Slack webhook URL required' });
        return;
      }
      config_json = { webhook_url: v.slack_url };
    } else if (v.kind === 'email') {
      const recipients = (v.recipients ?? '')
        .split(',').map((s) => s.trim()).filter(Boolean);
      if (recipients.length === 0) {
        form.setError('recipients', { message: 'At least one recipient required' });
        return;
      }
      config_json = { recipients };
    }
    const body: Record<string, unknown> = {
      name: v.name,
      description: v.description || null,
      min_severity: v.min_severity,
      notify_on_fire: v.notify_on_fire,
      notify_on_resolve: v.notify_on_resolve,
      enabled: v.enabled,
      config_json,
    };
    if (!editing) body.kind = v.kind;
    try {
      if (editing && channel) {
        await http.patch(`/notifications/channels/${channel.id}`, body);
        toast.success('Channel updated');
      } else {
        await http.post('/notifications/channels', body);
        toast.success('Channel created');
      }
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'save failed'); }
  }

  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="name" render={({ field }) => (
          <FormItem>
            <FormLabel>Name</FormLabel>
            <FormControl><Input placeholder="e.g. ops-slack" {...field} /></FormControl>
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
        <FormField control={form.control} name="kind" render={({ field }) => (
          <FormItem>
            <FormLabel>Kind</FormLabel>
            <Select value={field.value} onValueChange={field.onChange} disabled={editing}>
              <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
              <SelectContent>
                <SelectItem value="webhook">Generic webhook</SelectItem>
                <SelectItem value="slack">Slack incoming webhook</SelectItem>
                <SelectItem value="email">Email (SMTP)</SelectItem>
              </SelectContent>
            </Select>
            {editing && <p className="text-xs text-muted-foreground">Kind cannot be changed after creation.</p>}
            <FormMessage />
          </FormItem>
        )} />

        {kind === 'webhook' && (
          <FormField control={form.control} name="webhook_url" render={({ field }) => (
            <FormItem>
              <FormLabel>Webhook URL</FormLabel>
              <FormControl><Input type="url" placeholder="https://hook.example/dcim" {...field} /></FormControl>
              <FormMessage />
            </FormItem>
          )} />
        )}
        {kind === 'slack' && (
          <FormField control={form.control} name="slack_url" render={({ field }) => (
            <FormItem>
              <FormLabel>Slack webhook URL</FormLabel>
              <FormControl><Input type="url" placeholder="https://hooks.slack.com/services/…" {...field} /></FormControl>
              <FormMessage />
            </FormItem>
          )} />
        )}
        {kind === 'email' && (
          <FormField control={form.control} name="recipients" render={({ field }) => (
            <FormItem>
              <FormLabel>Recipients (comma-separated)</FormLabel>
              <FormControl><Input placeholder="ops@example.org, oncall@example.org" {...field} /></FormControl>
              <FormMessage />
            </FormItem>
          )} />
        )}

        <div className="grid grid-cols-2 gap-3">
          <FormField control={form.control} name="min_severity" render={({ field }) => (
            <FormItem>
              <FormLabel>Min severity</FormLabel>
              <Select value={field.value} onValueChange={field.onChange}>
                <FormControl><SelectTrigger><SelectValue /></SelectTrigger></FormControl>
                <SelectContent>
                  {SEVERITIES.map((s) => <SelectItem key={s} value={s}>{s}</SelectItem>)}
                </SelectContent>
              </Select>
              <FormMessage />
            </FormItem>
          )} />
          <FormField control={form.control} name="enabled" render={({ field }) => (
            <FormItem className="flex items-center gap-3 space-y-0 pt-6">
              <FormControl>
                <input
                  type="checkbox"
                  className="h-4 w-4"
                  checked={field.value}
                  onChange={(e) => field.onChange(e.target.checked)}
                />
              </FormControl>
              <FormLabel className="!mt-0 text-sm font-normal">Enabled</FormLabel>
            </FormItem>
          )} />
        </div>

        <div className="flex gap-6">
          <FormField control={form.control} name="notify_on_fire" render={({ field }) => (
            <FormItem className="flex items-center gap-2 space-y-0">
              <FormControl>
                <input
                  type="checkbox"
                  className="h-4 w-4"
                  checked={field.value}
                  onChange={(e) => field.onChange(e.target.checked)}
                />
              </FormControl>
              <FormLabel className="!mt-0 text-sm font-normal">Notify on fire</FormLabel>
            </FormItem>
          )} />
          <FormField control={form.control} name="notify_on_resolve" render={({ field }) => (
            <FormItem className="flex items-center gap-2 space-y-0">
              <FormControl>
                <input
                  type="checkbox"
                  className="h-4 w-4"
                  checked={field.value}
                  onChange={(e) => field.onChange(e.target.checked)}
                />
              </FormControl>
              <FormLabel className="!mt-0 text-sm font-normal">Notify on resolve</FormLabel>
            </FormItem>
          )} />
        </div>

        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : editing ? 'Save' : 'Create'}
        </Button>
      </form>
    </Form>
  );
}
