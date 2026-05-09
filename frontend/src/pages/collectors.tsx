import { useState } from 'react';
import { useTable, useGetIdentity, useList } from '@refinedev/core';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { Plus, Copy, Check, KeyRound } from 'lucide-react';
import { http } from '@/lib/http';
import { formatDate } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { Badge } from '@/components/ui/badge';
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table';
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogTrigger,
} from '@/components/ui/dialog';
import {
  Form, FormControl, FormField, FormItem, FormLabel, FormMessage,
} from '@/components/ui/form';
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select';
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { Input } from '@/components/ui/input';
import { FreshnessBadge } from '@/components/freshness-badge';
import { toast } from 'sonner';

type Site = { id: string; code: string; name: string };
type Collector = {
  id: string; name: string; site_id: string; status: string;
  last_seen_at: string | null; buffered_samples: number; capabilities: string[];
  version: string | null;
};
type Enrollment = {
  collector_id: string;
  enrollment_token: string;
  expires_in_seconds: number;
};

function mapStatus(s: string): string {
  if (s === 'healthy') return 'current';
  if (s === 'degraded') return 'estimated';
  if (s === 'stale' || s === 'unreachable') return 'stale';
  return 'unknown';
}

const CAPABILITIES = ['snmp', 'redfish', 'modbus', 'rest', 'ipmi'] as const;

export function CollectorsPage() {
  const { tableQuery, result } = useTable<Collector>({
    resource: 'collectors',
    pagination: { pageSize: 200 },
  });
  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const sites = sitesRes.result.data ?? [];
  const sitesById = new Map(sites.map((s) => [s.id, s]));
  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const canEnroll = identity?.capabilities.includes('collector:enroll');
  const data = result.data ?? [];

  const [enrollOpen, setEnrollOpen] = useState(false);
  const [enrolled, setEnrolled] = useState<{ enrollment: Enrollment; siteCode: string; name: string } | null>(null);

  return (
    <div className="space-y-4">
      <div className="flex items-end justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Site collectors</h1>
          <p className="text-sm text-muted-foreground">{result.total ?? 0} registered</p>
        </div>
        {canEnroll && (
          <Dialog open={enrollOpen} onOpenChange={setEnrollOpen}>
            <DialogTrigger asChild>
              <Button><Plus className="h-4 w-4" /> Enroll collector</Button>
            </DialogTrigger>
            <DialogContent>
              <DialogHeader><DialogTitle>Enroll a new collector</DialogTitle></DialogHeader>
              <EnrollForm
                sites={sites}
                onEnrolled={(enrollment, siteCode, name) => {
                  setEnrollOpen(false);
                  setEnrolled({ enrollment, siteCode, name });
                  tableQuery.refetch();
                }}
              />
            </DialogContent>
          </Dialog>
        )}
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
                  <TableHead>Version</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Last seen</TableHead>
                  <TableHead className="w-24">Buffered</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={7} className="text-muted-foreground">
                      No collectors enrolled. Use Enroll collector to bootstrap one.
                    </TableCell>
                  </TableRow>
                )}
                {data.map((c) => {
                  const site = sitesById.get(c.site_id);
                  return (
                    <TableRow key={c.id}>
                      <TableCell className="font-medium">{c.name}</TableCell>
                      <TableCell className="text-sm">
                        {site ? `${site.code} · ${site.name}` : (
                          <span className="font-mono text-xs">{c.site_id.slice(0, 8)}…</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <div className="flex flex-wrap gap-1">
                          {c.capabilities.map((cap) => (
                            <Badge key={cap} variant="secondary" className="font-mono text-[10px]">{cap}</Badge>
                          ))}
                          {c.capabilities.length === 0 && <span className="text-xs text-muted-foreground">—</span>}
                        </div>
                      </TableCell>
                      <TableCell className="font-mono text-xs">{c.version ?? '—'}</TableCell>
                      <TableCell><FreshnessBadge state={mapStatus(c.status)} /></TableCell>
                      <TableCell className="text-muted-foreground">{formatDate(c.last_seen_at)}</TableCell>
                      <TableCell className="tabular-nums">{c.buffered_samples?.toLocaleString() ?? 0}</TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <Dialog open={enrolled !== null} onOpenChange={(o) => { if (!o) setEnrolled(null); }}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <KeyRound className="h-4 w-4" /> Bootstrap the collector
            </DialogTitle>
          </DialogHeader>
          {enrolled && (
            <Bootstrap
              token={enrolled.enrollment.enrollment_token}
              collectorId={enrolled.enrollment.collector_id}
              expiresInSeconds={enrolled.enrollment.expires_in_seconds}
              siteCode={enrolled.siteCode}
              name={enrolled.name}
            />
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}

const formSchema = z.object({
  site_id: z.string().min(1, 'Site required'),
  name: z.string().min(1, 'Name required'),
  capabilities: z.array(z.enum(CAPABILITIES)).min(1, 'Pick at least one capability'),
});

function EnrollForm({
  sites, onEnrolled,
}: {
  sites: Site[];
  onEnrolled: (enrollment: Enrollment, siteCode: string, name: string) => void;
}) {
  const form = useForm<z.infer<typeof formSchema>>({
    resolver: zodResolver(formSchema),
    defaultValues: { site_id: '', name: '', capabilities: ['snmp'] },
  });
  const selected = form.watch('capabilities');

  function toggleCap(cap: typeof CAPABILITIES[number], checked: boolean) {
    const cur = form.getValues('capabilities');
    if (checked) form.setValue('capabilities', [...cur, cap], { shouldValidate: true });
    else form.setValue('capabilities', cur.filter((c) => c !== cap), { shouldValidate: true });
  }

  async function onSubmit(v: z.infer<typeof formSchema>) {
    try {
      const r = await http.post<Enrollment>('/collectors/enroll', {
        site_id: v.site_id,
        name: v.name,
        capabilities: v.capabilities,
      });
      const site = sites.find((s) => s.id === v.site_id);
      toast.success('Collector enrolled — bootstrap token issued');
      onEnrolled(r.data, site?.code ?? 'site', v.name);
    } catch (err: any) {
      toast.error(err?.message ?? 'enrollment failed');
    }
  }

  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="site_id" render={({ field }) => (
          <FormItem>
            <FormLabel>Site</FormLabel>
            <Select value={field.value} onValueChange={field.onChange}>
              <FormControl>
                <SelectTrigger><SelectValue placeholder="Pick a site" /></SelectTrigger>
              </FormControl>
              <SelectContent>
                {sites.map((s) => (
                  <SelectItem key={s.id} value={s.id}>{s.code} · {s.name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <FormMessage />
          </FormItem>
        )} />
        <FormField control={form.control} name="name" render={({ field }) => (
          <FormItem>
            <FormLabel>Collector name</FormLabel>
            <FormControl>
              <Input placeholder="e.g. ops-collector-1" {...field} />
            </FormControl>
            <FormMessage />
          </FormItem>
        )} />
        <FormField control={form.control} name="capabilities" render={() => (
          <FormItem>
            <FormLabel>Capabilities</FormLabel>
            <div className="flex flex-wrap gap-2 rounded-md border bg-muted/30 p-3">
              {CAPABILITIES.map((cap) => (
                <label key={cap} className="flex items-center gap-1.5 text-xs">
                  <input
                    type="checkbox"
                    className="h-3.5 w-3.5"
                    checked={selected.includes(cap)}
                    onChange={(e) => toggleCap(cap, e.target.checked)}
                  />
                  <span className="font-mono">{cap}</span>
                </label>
              ))}
            </div>
            <FormMessage />
          </FormItem>
        )} />
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Enrolling…' : 'Enroll & issue token'}
        </Button>
      </form>
    </Form>
  );
}

function Bootstrap({
  token, collectorId, expiresInSeconds, siteCode, name,
}: {
  token: string; collectorId: string; expiresInSeconds: number;
  siteCode: string; name: string;
}) {
  const apiBase =
    typeof window !== 'undefined' && window.location.origin
      ? window.location.origin
      : 'https://your-dcim';
  const dockerCmd = `docker run -d --name dcim-collector-${siteCode.toLowerCase()} \\
  -e DCIM_API_URL=${apiBase} \\
  -e DCIM_COLLECTOR_ID=${collectorId} \\
  -e DCIM_ENROLLMENT_TOKEN=${token} \\
  -v /var/lib/dcim-collector:/data \\
  ghcr.io/192d-wing/usg-dcim-collector:latest`;
  const systemdEnv = `# /etc/dcim-collector.env
DCIM_API_URL=${apiBase}
DCIM_COLLECTOR_ID=${collectorId}
DCIM_ENROLLMENT_TOKEN=${token}
DCIM_DATA_DIR=/var/lib/dcim-collector`;
  const expiresIn = expiresInSeconds < 3600
    ? `${Math.round(expiresInSeconds / 60)} minutes`
    : `${(expiresInSeconds / 3600).toFixed(0)} hour${expiresInSeconds === 3600 ? '' : 's'}`;

  return (
    <div className="space-y-4">
      <p className="text-sm">
        Collector <span className="font-mono">{name}</span> registered for <span className="font-mono">{siteCode}</span>.
        Run one of the snippets below on the site jump host. The token expires in <strong>{expiresIn}</strong>.
      </p>
      <div className="rounded-md border bg-muted/30 p-2 text-xs">
        <div className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
          Enrollment token (one-time)
        </div>
        <CopyBlock value={token} />
      </div>

      <Tabs defaultValue="docker">
        <TabsList>
          <TabsTrigger value="docker">Docker</TabsTrigger>
          <TabsTrigger value="systemd">systemd</TabsTrigger>
        </TabsList>
        <TabsContent value="docker" className="pt-3">
          <CopyBlock value={dockerCmd} multiline />
        </TabsContent>
        <TabsContent value="systemd" className="pt-3 space-y-3">
          <div>
            <div className="text-xs font-medium text-muted-foreground mb-1">1. Drop the env file</div>
            <CopyBlock value={systemdEnv} multiline />
          </div>
          <div>
            <div className="text-xs font-medium text-muted-foreground mb-1">2. Enable + start the service</div>
            <CopyBlock
              value="systemctl enable --now dcim-collector"
              multiline={false}
            />
          </div>
        </TabsContent>
      </Tabs>

      <Card>
        <CardHeader className="pb-2"><CardTitle className="text-xs">After bootstrap</CardTitle></CardHeader>
        <CardContent className="pt-0 text-xs text-muted-foreground space-y-1">
          <p>The collector exchanges this token for an mTLS cert + service token on first contact.</p>
          <p>It will appear here as <strong>healthy</strong> once heartbeats start arriving.</p>
        </CardContent>
      </Card>
    </div>
  );
}

function CopyBlock({ value, multiline }: { value: string; multiline?: boolean }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    await navigator.clipboard.writeText(value);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }
  return (
    <div className="flex items-start gap-2">
      <pre
        className={`flex-1 overflow-x-auto rounded-md bg-background p-2 font-mono text-[11px] ${
          multiline ? 'whitespace-pre' : 'whitespace-pre-wrap break-all'
        }`}
      >{value}</pre>
      <Button size="sm" variant="outline" onClick={copy} className="shrink-0">
        {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
        {copied ? 'Copied' : 'Copy'}
      </Button>
    </div>
  );
}
