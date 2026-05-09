import { useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useGetIdentity } from '@refinedev/core';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { Plus, Trash2, Copy, Check, KeyRound } from 'lucide-react';
import { http } from '@/lib/http';
import { formatDate } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
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
import { toast } from 'sonner';

type Token = {
  id: string;
  name: string;
  permission_codes: string[];
  scope_json: Record<string, unknown>;
  created_at: string;
  expires_at: string | null;
  last_used_at: string | null;
  revoked: boolean;
  plaintext?: string | null;
};

export function TokensPage() {
  const qc = useQueryClient();
  const { data: identity } = useGetIdentity<{ email: string | null; capabilities: string[] }>();
  const canManage = identity?.capabilities.includes('tokens:manage');
  const myCaps = identity?.capabilities ?? [];

  const tokens = useQuery({
    queryKey: ['tokens'],
    queryFn: async () => (await http.get<Token[]>('/auth/tokens')).data,
    enabled: canManage,
  });

  const [createOpen, setCreateOpen] = useState(false);
  const [issued, setIssued] = useState<Token | null>(null);

  async function revoke(t: Token) {
    if (!window.confirm(`Revoke token "${t.name}"? Existing clients using this token will fail.`)) {
      return;
    }
    try {
      await http.delete(`/auth/tokens/${t.id}`);
      toast.success('Token revoked');
      await qc.invalidateQueries({ queryKey: ['tokens'] });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to revoke');
    }
  }

  if (!canManage) {
    return (
      <div className="space-y-2">
        <h1 className="text-2xl font-semibold tracking-tight">API tokens</h1>
        <p className="text-sm text-muted-foreground">
          You don't have the <code className="font-mono">tokens:manage</code> capability. Ask an admin
          to grant a role that includes it (e.g. RegionalAdmin, EnterpriseAdmin).
        </p>
      </div>
    );
  }

  const data = tokens.data ?? [];

  return (
    <div className="space-y-4">
      <div className="flex items-end justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">API tokens</h1>
          <p className="text-sm text-muted-foreground">
            Tokens issued to <span className="font-mono">{identity?.email}</span> · plaintext shown once at creation
          </p>
        </div>
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button><Plus className="h-4 w-4" /> Issue token</Button>
          </DialogTrigger>
          <DialogContent>
            <DialogHeader><DialogTitle>Issue API token</DialogTitle></DialogHeader>
            <IssueForm
              myCapabilities={myCaps}
              onIssued={async (t) => {
                setCreateOpen(false);
                setIssued(t);
                await qc.invalidateQueries({ queryKey: ['tokens'] });
              }}
            />
          </DialogContent>
        </Dialog>
      </div>

      <Card>
        <CardContent className="p-0">
          {tokens.isLoading ? (
            <div className="space-y-2 p-4">
              {Array.from({ length: 3 }).map((_, i) => <Skeleton key={`s-${i}`} className="h-9 w-full" />)}
            </div>
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Capabilities</TableHead>
                  <TableHead>Created</TableHead>
                  <TableHead>Expires</TableHead>
                  <TableHead>Last used</TableHead>
                  <TableHead className="w-24">Status</TableHead>
                  <TableHead className="w-12" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={7} className="text-muted-foreground">
                      No tokens issued yet. Create one to authenticate scripts or integrations.
                    </TableCell>
                  </TableRow>
                )}
                {data.map((t) => (
                  <TableRow key={t.id}>
                    <TableCell className="font-medium">{t.name}</TableCell>
                    <TableCell>
                      <div className="flex flex-wrap gap-1">
                        {t.permission_codes.length === 0 && (
                          <span className="text-xs text-muted-foreground">none</span>
                        )}
                        {t.permission_codes.map((c) => (
                          <Badge key={c} variant="secondary" className="font-mono text-[10px]">{c}</Badge>
                        ))}
                      </div>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">{formatDate(t.created_at)}</TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {t.expires_at ? formatDate(t.expires_at) : 'never'}
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {t.last_used_at ? formatDate(t.last_used_at) : 'never'}
                    </TableCell>
                    <TableCell>
                      <Badge variant={t.revoked ? 'secondary' : 'success'}>
                        {t.revoked ? 'revoked' : 'active'}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      {!t.revoked && (
                        <Button size="sm" variant="ghost" onClick={() => revoke(t)} title="Revoke">
                          <Trash2 className="h-3.5 w-3.5" />
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

      <Dialog open={issued !== null} onOpenChange={(o) => { if (!o) setIssued(null); }}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <KeyRound className="h-4 w-4" /> Token created — copy it now
            </DialogTitle>
          </DialogHeader>
          {issued && <PlaintextReveal token={issued} />}
        </DialogContent>
      </Dialog>
    </div>
  );
}

const issueSchema = z.object({
  name: z.string().min(1, 'Name required'),
  permission_codes: z.array(z.string()).min(1, 'Pick at least one capability'),
  expires_at: z.string().optional(),
});

function IssueForm({
  myCapabilities, onIssued,
}: {
  myCapabilities: string[];
  onIssued: (token: Token) => void;
}) {
  const form = useForm<z.infer<typeof issueSchema>>({
    resolver: zodResolver(issueSchema),
    defaultValues: { name: '', permission_codes: [], expires_at: '' },
  });

  function toggleCap(code: string, checked: boolean) {
    const cur = form.getValues('permission_codes');
    if (checked) form.setValue('permission_codes', [...cur, code], { shouldValidate: true });
    else form.setValue('permission_codes', cur.filter((c) => c !== code), { shouldValidate: true });
  }
  const selected = form.watch('permission_codes');

  async function onSubmit(v: z.infer<typeof issueSchema>) {
    const body = {
      name: v.name,
      permission_codes: v.permission_codes,
      scope_json: {},
      expires_at: v.expires_at ? new Date(v.expires_at).toISOString() : null,
    };
    try {
      const r = await http.post<Token>('/auth/tokens', body);
      toast.success('Token issued');
      onIssued(r.data);
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to issue token');
    }
  }

  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="name" render={({ field }) => (
          <FormItem>
            <FormLabel>Name</FormLabel>
            <FormControl><Input placeholder="e.g. ansible-collector-bootstrap" {...field} /></FormControl>
            <FormMessage />
          </FormItem>
        )} />
        <FormField control={form.control} name="permission_codes" render={() => (
          <FormItem>
            <FormLabel>Capabilities</FormLabel>
            <p className="text-xs text-muted-foreground">
              You can only grant capabilities you hold. Pick the smallest set the token needs.
            </p>
            <div className="grid max-h-48 grid-cols-2 gap-1.5 overflow-y-auto rounded-md border bg-muted/30 p-3">
              {myCapabilities.length === 0 && (
                <p className="col-span-2 text-xs text-muted-foreground">
                  Your account has no capabilities to delegate.
                </p>
              )}
              {myCapabilities.map((c) => (
                <label key={c} className="flex items-center gap-2 text-xs">
                  <input
                    type="checkbox"
                    className="h-3.5 w-3.5"
                    checked={selected.includes(c)}
                    onChange={(e) => toggleCap(c, e.target.checked)}
                  />
                  <span className="font-mono">{c}</span>
                </label>
              ))}
            </div>
            <FormMessage />
          </FormItem>
        )} />
        <FormField control={form.control} name="expires_at" render={({ field }) => (
          <FormItem>
            <FormLabel>Expires (optional)</FormLabel>
            <FormControl><Input type="datetime-local" {...field} /></FormControl>
            <FormMessage />
          </FormItem>
        )} />
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Issuing…' : 'Issue token'}
        </Button>
      </form>
    </Form>
  );
}

function PlaintextReveal({ token }: { token: Token }) {
  const [copied, setCopied] = useState(false);
  if (!token.plaintext) return <p className="text-sm text-muted-foreground">Plaintext not available.</p>;
  async function copy() {
    if (!token.plaintext) return;
    await navigator.clipboard.writeText(token.plaintext);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }
  return (
    <div className="space-y-3">
      <p className="text-sm">
        Copy the token now — it cannot be displayed again.
      </p>
      <div className="flex items-center gap-2 rounded-md border bg-muted/30 p-2">
        <code className="flex-1 break-all font-mono text-xs">{token.plaintext}</code>
        <Button size="sm" variant="outline" onClick={copy}>
          {copied ? <Check className="h-3.5 w-3.5" /> : <Copy className="h-3.5 w-3.5" />}
          {copied ? 'Copied' : 'Copy'}
        </Button>
      </div>
      <Card>
        <CardHeader className="pb-2"><CardTitle className="text-xs">Use it</CardTitle></CardHeader>
        <CardContent className="pt-0">
          <code className="block whitespace-pre-wrap break-all font-mono text-[11px] text-muted-foreground">
            curl -H "Authorization: Bearer {token.plaintext}" \
            {'\n  '}https://your-dcim/api/v1/auth/me
          </code>
        </CardContent>
      </Card>
    </div>
  );
}
