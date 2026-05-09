import { useState } from 'react';
import { useTable, useGetIdentity, useList } from '@refinedev/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import {
  Plus, Pencil, Power, Users as UsersIcon, ShieldCheck, Trash2, X,
} from 'lucide-react';
import { http } from '@/lib/http';
import { formatDate } from '@/lib/utils';
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
import { Tabs, TabsList, TabsTrigger, TabsContent } from '@/components/ui/tabs';
import { toast } from 'sonner';

type User = {
  id: string; email: string; display_name: string | null;
  is_active: boolean; sso_subject: string | null;
  last_login_at: string | null; created_at: string;
};
type Role = {
  id: string; name: string; description: string | null;
  permission_codes: string[]; is_system: boolean;
};
type ScopeRow = { id: string; scope_type: string; target_id: string | null };
type Assignment = {
  id: string; user_id: string; role_id: string;
  role_name: string; scopes: ScopeRow[];
};

const SCOPE_TYPES = ['global', 'region', 'site', 'site_group', 'enclave', 'organization'];

export function AdminPage() {
  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const canUsers = identity?.capabilities.includes('users:manage');
  const canRoles = identity?.capabilities.includes('roles:manage');
  const myCaps = identity?.capabilities ?? [];

  if (!canUsers && !canRoles) {
    return (
      <div className="space-y-2">
        <h1 className="text-2xl font-semibold tracking-tight">Admin</h1>
        <p className="text-sm text-muted-foreground">
          You don't have <code className="font-mono">users:manage</code> or{' '}
          <code className="font-mono">roles:manage</code>.
        </p>
      </div>
    );
  }

  const initial = canUsers ? 'users' : 'roles';
  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Admin</h1>
        <p className="text-sm text-muted-foreground">User accounts, roles, and role assignments</p>
      </div>
      <Tabs defaultValue={initial}>
        <TabsList>
          {canUsers && <TabsTrigger value="users"><UsersIcon className="h-3.5 w-3.5" /> Users</TabsTrigger>}
          {canRoles && <TabsTrigger value="roles"><ShieldCheck className="h-3.5 w-3.5" /> Roles</TabsTrigger>}
        </TabsList>
        {canUsers && <TabsContent value="users" className="pt-3"><UsersTab /></TabsContent>}
        {canRoles && <TabsContent value="roles" className="pt-3"><RolesTab myCapabilities={myCaps} /></TabsContent>}
      </Tabs>
    </div>
  );
}

// ----------------------- Users -----------------------

function UsersTab() {
  const { tableQuery, result, currentPage, pageCount, setCurrentPage } = useTable<User>({
    resource: 'admin/users',
    pagination: { pageSize: 50 },
    sorters: { initial: [{ field: 'email', order: 'asc' }] },
  });
  const data = result.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<User | null>(null);
  const [assigningTo, setAssigningTo] = useState<User | null>(null);

  async function refresh() { await tableQuery.refetch(); }

  async function toggleActive(u: User) {
    try {
      await http.patch(`/admin/users/${u.id}`, { is_active: !u.is_active });
      toast.success(u.is_active ? 'User deactivated' : 'User activated');
      await refresh();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button><Plus className="h-4 w-4" /> New user</Button>
          </DialogTrigger>
          <DialogContent>
            <DialogHeader><DialogTitle>New user</DialogTitle></DialogHeader>
            <UserForm onSaved={async () => { setCreateOpen(false); await refresh(); }} />
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
                  <TableHead>Email</TableHead>
                  <TableHead>Display name</TableHead>
                  <TableHead>SSO</TableHead>
                  <TableHead>Last login</TableHead>
                  <TableHead className="w-24">Status</TableHead>
                  <TableHead className="w-44" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={6} className="text-muted-foreground">No users.</TableCell>
                  </TableRow>
                )}
                {data.map((u) => (
                  <TableRow key={u.id}>
                    <TableCell className="font-medium">{u.email}</TableCell>
                    <TableCell>{u.display_name ?? '—'}</TableCell>
                    <TableCell className="font-mono text-xs">{u.sso_subject ?? '—'}</TableCell>
                    <TableCell className="text-xs text-muted-foreground">{formatDate(u.last_login_at)}</TableCell>
                    <TableCell>
                      <Badge variant={u.is_active ? 'success' : 'secondary'}>
                        {u.is_active ? 'active' : 'inactive'}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <div className="flex gap-1">
                        <Button size="sm" variant="ghost" onClick={() => setAssigningTo(u)} title="Manage role assignments">
                          <ShieldCheck className="h-3.5 w-3.5" /> Roles
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => setEditing(u)} title="Edit">
                          <Pencil className="h-3.5 w-3.5" />
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => toggleActive(u)} title={u.is_active ? 'Deactivate' : 'Activate'}>
                          <Power className="h-3.5 w-3.5" />
                        </Button>
                      </div>
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

      <Dialog open={editing !== null} onOpenChange={(o) => { if (!o) setEditing(null); }}>
        <DialogContent>
          <DialogHeader><DialogTitle>Edit user</DialogTitle></DialogHeader>
          {editing && <UserForm user={editing} onSaved={async () => { setEditing(null); await refresh(); }} />}
        </DialogContent>
      </Dialog>

      <Dialog open={assigningTo !== null} onOpenChange={(o) => { if (!o) setAssigningTo(null); }}>
        <DialogContent className="max-w-2xl">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2">
              <ShieldCheck className="h-4 w-4" /> Roles for {assigningTo?.email}
            </DialogTitle>
          </DialogHeader>
          {assigningTo && <AssignmentsManager user={assigningTo} />}
        </DialogContent>
      </Dialog>
    </div>
  );
}

const userSchema = z.object({
  email: z.string().email('Email required'),
  display_name: z.string().optional(),
  is_active: z.boolean(),
});

function UserForm({ user, onSaved }: { user?: User; onSaved: () => void }) {
  const editing = !!user;
  const form = useForm<z.infer<typeof userSchema>>({
    resolver: zodResolver(userSchema),
    defaultValues: {
      email: user?.email ?? '',
      display_name: user?.display_name ?? '',
      is_active: user?.is_active ?? true,
    },
  });

  async function onSubmit(v: z.infer<typeof userSchema>) {
    try {
      if (editing && user) {
        await http.patch(`/admin/users/${user.id}`, {
          display_name: v.display_name || null,
          is_active: v.is_active,
        });
        toast.success('User updated');
      } else {
        await http.post('/admin/users', {
          email: v.email,
          display_name: v.display_name || null,
          is_active: v.is_active,
        });
        toast.success('User created');
      }
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'save failed'); }
  }

  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
        <FormField control={form.control} name="email" render={({ field }) => (
          <FormItem>
            <FormLabel>Email</FormLabel>
            <FormControl><Input type="email" disabled={editing} {...field} /></FormControl>
            <FormMessage />
          </FormItem>
        )} />
        <FormField control={form.control} name="display_name" render={({ field }) => (
          <FormItem>
            <FormLabel>Display name (optional)</FormLabel>
            <FormControl><Input {...field} /></FormControl>
            <FormMessage />
          </FormItem>
        )} />
        <FormField control={form.control} name="is_active" render={({ field }) => (
          <FormItem className="flex items-center gap-3 space-y-0">
            <FormControl>
              <input
                type="checkbox"
                className="h-4 w-4"
                checked={field.value}
                onChange={(e) => field.onChange(e.target.checked)}
              />
            </FormControl>
            <FormLabel className="!mt-0 text-sm font-normal">Active</FormLabel>
          </FormItem>
        )} />
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : editing ? 'Save' : 'Create'}
        </Button>
      </form>
    </Form>
  );
}

// ----------------------- Assignments manager -----------------------

type Site = { id: string; code: string; name: string };

function AssignmentsManager({ user }: { user: User }) {
  const qc = useQueryClient();
  const assignments = useQuery({
    queryKey: ['admin-assignments', user.id],
    queryFn: async () => (await http.get<Assignment[]>(`/admin/users/${user.id}/assignments`)).data,
  });
  const rolesRes = useList<Role>({ resource: 'admin/roles', pagination: { pageSize: 200 } });
  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const roles = rolesRes.result.data ?? [];
  const sites = sitesRes.result.data ?? [];
  const sitesById = new Map(sites.map((s) => [s.id, s]));

  const [roleId, setRoleId] = useState('');
  const [scopeRows, setScopeRows] = useState<{ scope_type: string; target_id: string }[]>([]);

  function addScopeRow() {
    setScopeRows([...scopeRows, { scope_type: 'global', target_id: '' }]);
  }
  function updateScopeRow(idx: number, patch: Partial<{ scope_type: string; target_id: string }>) {
    setScopeRows(scopeRows.map((r, i) => (i === idx ? { ...r, ...patch } : r)));
  }
  function removeScopeRow(idx: number) {
    setScopeRows(scopeRows.filter((_, i) => i !== idx));
  }

  async function assign() {
    if (!roleId) return toast.error('Pick a role');
    try {
      await http.post('/admin/assignments', {
        user_id: user.id,
        role_id: roleId,
        scopes: scopeRows
          .filter((s) => s.scope_type === 'global' || s.target_id)
          .map((s) => ({
            scope_type: s.scope_type,
            target_id: s.scope_type === 'global' ? null : s.target_id,
          })),
      });
      toast.success('Role assigned');
      setRoleId('');
      setScopeRows([]);
      await qc.invalidateQueries({ queryKey: ['admin-assignments', user.id] });
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  async function remove(a: Assignment) {
    if (!window.confirm(`Remove role "${a.role_name}" from ${user.email}?`)) return;
    try {
      await http.delete(`/admin/assignments/${a.id}`);
      toast.success('Assignment removed');
      await qc.invalidateQueries({ queryKey: ['admin-assignments', user.id] });
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  function describeScope(s: ScopeRow): string {
    if (s.scope_type === 'global') return 'global';
    if (s.scope_type === 'site' && s.target_id) {
      const site = sitesById.get(s.target_id);
      if (site) return `site:${site.code}`;
    }
    return `${s.scope_type}:${s.target_id ?? '*'}`;
  }

  return (
    <div className="space-y-4">
      <div>
        <div className="text-xs font-semibold uppercase tracking-wider text-muted-foreground mb-2">
          Current roles
        </div>
        {assignments.isLoading ? (
          <Skeleton className="h-16 w-full" />
        ) : (assignments.data ?? []).length === 0 ? (
          <p className="text-xs text-muted-foreground">No role assignments.</p>
        ) : (
          <div className="space-y-2">
            {(assignments.data ?? []).map((a) => (
              <div key={a.id} className="flex items-start justify-between rounded-md border bg-muted/30 p-2 text-sm">
                <div>
                  <div className="font-medium">{a.role_name}</div>
                  <div className="mt-1 flex flex-wrap gap-1">
                    {a.scopes.length === 0
                      ? <Badge variant="secondary" className="text-[10px]">no scope (role default)</Badge>
                      : a.scopes.map((s) => (
                        <Badge key={s.id} variant="outline" className="font-mono text-[10px]">{describeScope(s)}</Badge>
                      ))}
                  </div>
                </div>
                <Button size="sm" variant="ghost" onClick={() => remove(a)}>
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="space-y-3 rounded-md border bg-muted/20 p-3">
        <div className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          Assign a role
        </div>
        <div className="space-y-1">
          <label className="text-xs font-medium text-muted-foreground">Role</label>
          <Select value={roleId} onValueChange={setRoleId}>
            <SelectTrigger><SelectValue placeholder="Pick a role" /></SelectTrigger>
            <SelectContent>
              {roles.map((r) => (
                <SelectItem key={r.id} value={r.id}>
                  {r.name}{r.is_system && ' (system)'}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <div className="space-y-2">
          <div className="flex items-center justify-between">
            <label className="text-xs font-medium text-muted-foreground">Scope rows (empty = role default)</label>
            <Button type="button" size="sm" variant="outline" onClick={addScopeRow}>
              <Plus className="h-3.5 w-3.5" /> Row
            </Button>
          </div>
          {scopeRows.map((row, idx) => (
            <div key={`scope-${idx}`} className="flex items-center gap-2">
              <Select value={row.scope_type} onValueChange={(v) => updateScopeRow(idx, { scope_type: v, target_id: '' })}>
                <SelectTrigger className="w-[170px]"><SelectValue /></SelectTrigger>
                <SelectContent>
                  {SCOPE_TYPES.map((t) => <SelectItem key={t} value={t}>{t}</SelectItem>)}
                </SelectContent>
              </Select>
              {row.scope_type === 'global' ? (
                <span className="text-xs text-muted-foreground">unrestricted</span>
              ) : row.scope_type === 'site' ? (
                <Select value={row.target_id} onValueChange={(v) => updateScopeRow(idx, { target_id: v })}>
                  <SelectTrigger className="flex-1"><SelectValue placeholder="Pick a site" /></SelectTrigger>
                  <SelectContent>
                    {sites.map((s) => <SelectItem key={s.id} value={s.id}>{s.code} · {s.name}</SelectItem>)}
                  </SelectContent>
                </Select>
              ) : (
                <Input
                  className="flex-1 font-mono text-xs"
                  placeholder={
                    row.scope_type === 'enclave' ? 'enclave name'
                    : row.scope_type === 'organization' ? 'organization label'
                    : 'uuid'
                  }
                  value={row.target_id}
                  onChange={(e) => updateScopeRow(idx, { target_id: e.target.value })}
                />
              )}
              <Button type="button" size="sm" variant="ghost" onClick={() => removeScopeRow(idx)}>
                <X className="h-3.5 w-3.5" />
              </Button>
            </div>
          ))}
        </div>

        <Button onClick={assign} disabled={!roleId}>Assign role</Button>
      </div>
    </div>
  );
}

// ----------------------- Roles -----------------------

const roleSchema = z.object({
  name: z.string().min(1, 'Name required'),
  description: z.string().optional(),
  permission_codes: z.array(z.string()).min(1, 'Pick at least one capability'),
});

function RolesTab({ myCapabilities }: { myCapabilities: string[] }) {
  const { tableQuery, result, currentPage, pageCount, setCurrentPage } = useTable<Role>({
    resource: 'admin/roles',
    pagination: { pageSize: 50 },
    sorters: { initial: [{ field: 'name', order: 'asc' }] },
  });
  const data = result.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<Role | null>(null);

  async function refresh() { await tableQuery.refetch(); }

  async function remove(r: Role) {
    if (r.is_system) return toast.error('system roles cannot be deleted');
    if (!window.confirm(`Delete role "${r.name}"?`)) return;
    try {
      await http.delete(`/admin/roles/${r.id}`);
      toast.success('Role deleted');
      await refresh();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Dialog open={createOpen} onOpenChange={setCreateOpen}>
          <DialogTrigger asChild>
            <Button><Plus className="h-4 w-4" /> New role</Button>
          </DialogTrigger>
          <DialogContent>
            <DialogHeader><DialogTitle>New role</DialogTitle></DialogHeader>
            <RoleForm
              myCapabilities={myCapabilities}
              onSaved={async () => { setCreateOpen(false); await refresh(); }}
            />
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
                  <TableHead>Name</TableHead>
                  <TableHead>Description</TableHead>
                  <TableHead>Capabilities</TableHead>
                  <TableHead className="w-24">Type</TableHead>
                  <TableHead className="w-28" />
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.length === 0 && (
                  <TableRow>
                    <TableCell colSpan={5} className="text-muted-foreground">No roles defined.</TableCell>
                  </TableRow>
                )}
                {data.map((r) => (
                  <TableRow key={r.id}>
                    <TableCell className="font-medium">{r.name}</TableCell>
                    <TableCell className="text-sm text-muted-foreground">{r.description ?? '—'}</TableCell>
                    <TableCell>
                      <div className="flex flex-wrap gap-1 max-w-md">
                        {r.permission_codes.slice(0, 6).map((c) => (
                          <Badge key={c} variant="secondary" className="font-mono text-[10px]">{c}</Badge>
                        ))}
                        {r.permission_codes.length > 6 && (
                          <Badge variant="outline" className="text-[10px]">+{r.permission_codes.length - 6}</Badge>
                        )}
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge variant={r.is_system ? 'secondary' : 'success'}>
                        {r.is_system ? 'system' : 'custom'}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <div className="flex gap-1">
                        <Button size="sm" variant="ghost" onClick={() => setEditing(r)} disabled={r.is_system}>
                          <Pencil className="h-3.5 w-3.5" />
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => remove(r)} disabled={r.is_system}>
                          <Trash2 className="h-3.5 w-3.5" />
                        </Button>
                      </div>
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

      <Dialog open={editing !== null} onOpenChange={(o) => { if (!o) setEditing(null); }}>
        <DialogContent>
          <DialogHeader><DialogTitle>Edit role</DialogTitle></DialogHeader>
          {editing && (
            <RoleForm
              myCapabilities={myCapabilities}
              role={editing}
              onSaved={async () => { setEditing(null); await refresh(); }}
            />
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}

function RoleForm({
  myCapabilities, role, onSaved,
}: {
  myCapabilities: string[];
  role?: Role;
  onSaved: () => void;
}) {
  const editing = !!role;
  const form = useForm<z.infer<typeof roleSchema>>({
    resolver: zodResolver(roleSchema),
    defaultValues: {
      name: role?.name ?? '',
      description: role?.description ?? '',
      permission_codes: role?.permission_codes ?? [],
    },
  });
  const selected = form.watch('permission_codes');

  function toggleCap(cap: string, checked: boolean) {
    const cur = form.getValues('permission_codes');
    if (checked) form.setValue('permission_codes', [...cur, cap], { shouldValidate: true });
    else form.setValue('permission_codes', cur.filter((c) => c !== cap), { shouldValidate: true });
  }

  async function onSubmit(v: z.infer<typeof roleSchema>) {
    try {
      if (editing && role) {
        await http.patch(`/admin/roles/${role.id}`, {
          name: v.name,
          description: v.description || null,
          permission_codes: v.permission_codes,
        });
        toast.success('Role updated');
      } else {
        await http.post('/admin/roles', {
          name: v.name,
          description: v.description || null,
          permission_codes: v.permission_codes,
        });
        toast.success('Role created');
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
            <FormControl><Input {...field} /></FormControl>
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
        <FormField control={form.control} name="permission_codes" render={() => (
          <FormItem>
            <FormLabel>Capabilities</FormLabel>
            <p className="text-xs text-muted-foreground">
              You can only grant capabilities you hold yourself.
            </p>
            <div className="grid max-h-48 grid-cols-2 gap-1.5 overflow-y-auto rounded-md border bg-muted/30 p-3">
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
        <Button type="submit" disabled={form.formState.isSubmitting}>
          {form.formState.isSubmitting ? 'Saving…' : editing ? 'Save' : 'Create'}
        </Button>
      </form>
    </Form>
  );
}
