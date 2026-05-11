// Admin — Users + Roles management with assignments manager.
// Cloudscape Tabs + Table + Modal + Form everywhere.

import { useState } from 'react';
import { useTable, useGetIdentity, useList } from '@refinedev/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import Pagination from '@cloudscape-design/components/pagination';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import Tabs from '@cloudscape-design/components/tabs';

import { http } from '@/lib/http';
import { formatDate } from '@/lib/utils';

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
type Site = { id: string; code: string; name: string };

const SCOPE_TYPES: SelectProps.Option[] = [
  { value: 'global', label: 'global' },
  { value: 'region', label: 'region' },
  { value: 'site', label: 'site' },
  { value: 'site_group', label: 'site_group' },
  { value: 'enclave', label: 'enclave' },
  { value: 'organization', label: 'organization' },
];

export function AdminPage() {
  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const canUsers = identity?.capabilities.includes('users:manage');
  const canRoles = identity?.capabilities.includes('roles:manage');
  const myCaps = identity?.capabilities ?? [];

  if (!canUsers && !canRoles) {
    return (
      <ContentLayout header={<Header variant="h1">Admin</Header>}>
        <Box color="text-status-inactive">
          You don't have <code style={{ fontFamily: 'ui-monospace, monospace' }}>users:manage</code> or{' '}
          <code style={{ fontFamily: 'ui-monospace, monospace' }}>roles:manage</code>.
        </Box>
      </ContentLayout>
    );
  }

  const tabs: { id: string; label: string; content: React.ReactNode }[] = [];
  if (canUsers) tabs.push({ id: 'users', label: 'Users', content: <UsersTab /> });
  if (canRoles) tabs.push({ id: 'roles', label: 'Roles', content: <RolesTab myCapabilities={myCaps} /> });

  return (
    <ContentLayout
      header={
        <Header variant="h1" description="User accounts, roles, and role assignments.">
          Admin
        </Header>
      }
    >
      <Tabs tabs={tabs} />
    </ContentLayout>
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
    <SpaceBetween size="l">
      <Table<User>
        variant="container"
        loading={tableQuery.isLoading}
        loadingText="Loading users…"
        items={data}
        trackBy="id"
        header={
          <Header
            counter={`(${data.length})`}
            actions={
              <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                New user
              </Button>
            }
          >
            Users
          </Header>
        }
        columnDefinitions={[
          { id: 'email', header: 'Email', cell: (u) => <span style={{ fontWeight: 500 }}>{u.email}</span> },
          { id: 'display_name', header: 'Display name', cell: (u) => u.display_name ?? '—' },
          {
            id: 'sso', header: 'SSO',
            cell: (u) => <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{u.sso_subject ?? '—'}</span>,
          },
          {
            id: 'last_login', header: 'Last login',
            cell: (u) => <Box variant="span" color="text-status-inactive" fontSize="body-s">{formatDate(u.last_login_at)}</Box>,
            width: 200,
          },
          {
            id: 'status', header: 'Status',
            cell: (u) => u.is_active
              ? <StatusIndicator type="success">active</StatusIndicator>
              : <StatusIndicator type="stopped">inactive</StatusIndicator>,
            width: 120,
          },
          {
            id: 'actions', header: '',
            cell: (u) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button onClick={() => setAssigningTo(u)}>Roles</Button>
                <Button iconName="edit" variant="inline-icon" onClick={() => setEditing(u)} ariaLabel={`Edit ${u.email}`} />
                <Button iconName={u.is_active ? 'status-stopped' : 'status-positive'}
                  variant="inline-icon" onClick={() => toggleActive(u)}
                  ariaLabel={u.is_active ? `Deactivate ${u.email}` : `Activate ${u.email}`} />
              </SpaceBetween>
            ),
            width: 200,
          },
        ]}
        empty={<Box textAlign="center" color="inherit" padding="m">No users.</Box>}
        pagination={
          pageCount > 1 ? (
            <Pagination
              currentPageIndex={currentPage}
              pagesCount={pageCount}
              onChange={({ detail }) => setCurrentPage(detail.currentPageIndex)}
            />
          ) : undefined
        }
      />

      <Modal visible={createOpen} onDismiss={() => setCreateOpen(false)} header="New user" size="medium">
        <UserForm onSaved={async () => { setCreateOpen(false); await refresh(); }} />
      </Modal>
      <Modal visible={editing !== null} onDismiss={() => setEditing(null)} header="Edit user" size="medium">
        {editing && <UserForm user={editing} onSaved={async () => { setEditing(null); await refresh(); }} />}
      </Modal>
      <Modal
        visible={assigningTo !== null}
        onDismiss={() => setAssigningTo(null)}
        header={`Roles for ${assigningTo?.email ?? ''}`}
        size="large"
      >
        {assigningTo && <AssignmentsManager user={assigningTo} />}
      </Modal>
    </SpaceBetween>
  );
}

function UserForm({ user, onSaved }: Readonly<{ user?: User; onSaved: () => void }>) {
  const editing = !!user;
  const [email, setEmail] = useState(user?.email ?? '');
  const [displayName, setDisplayName] = useState(user?.display_name ?? '');
  const [isActive, setIsActive] = useState(user?.is_active ?? true);
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!editing && !/^.+@.+\..+$/.test(email)) errs.email = 'Email required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      if (editing && user) {
        await http.patch(`/admin/users/${user.id}`, { display_name: displayName || null, is_active: isActive });
        toast.success('User updated');
      } else {
        await http.post('/admin/users', { email, display_name: displayName || null, is_active: isActive });
        toast.success('User created');
      }
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'save failed'); } finally { setSubmitting(false); }
  }

  return (
    <form onSubmit={onSubmit}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={submitting}>
            {submitting ? 'Saving…' : editing ? 'Save' : 'Create'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField label="Email" errorText={errors.email}>
            <Input type="email" value={email} disabled={editing}
              onChange={({ detail }) => setEmail(detail.value)} />
          </FormField>
          <FormField label="Display name (optional)">
            <Input value={displayName ?? ''} onChange={({ detail }) => setDisplayName(detail.value)} />
          </FormField>
          <Checkbox checked={isActive} onChange={({ detail }) => setIsActive(detail.checked)}>Active</Checkbox>
        </SpaceBetween>
      </Form>
    </form>
  );
}

// ----------------------- Assignments manager -----------------------

function AssignmentsManager({ user }: Readonly<{ user: User }>) {
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

  const roleOptions: SelectProps.Option[] = roles.map((r) => ({
    value: r.id,
    label: r.is_system ? `${r.name} (system)` : r.name,
  }));
  const siteOptions: SelectProps.Option[] = sites.map((s) => ({
    value: s.id, label: `${s.code} · ${s.name}`,
  }));

  const [roleOpt, setRoleOpt] = useState<SelectProps.Option | null>(null);
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
    if (!roleOpt?.value) { toast.error('Pick a role'); return; }
    try {
      await http.post('/admin/assignments', {
        user_id: user.id,
        role_id: roleOpt.value,
        scopes: scopeRows
          .filter((s) => s.scope_type === 'global' || s.target_id)
          .map((s) => ({
            scope_type: s.scope_type,
            target_id: s.scope_type === 'global' ? null : s.target_id,
          })),
      });
      toast.success('Role assigned');
      setRoleOpt(null);
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
    <SpaceBetween size="l">
      <Container header={<Header variant="h3">Current roles</Header>}>
        {assignments.isLoading && <Box color="text-status-inactive">Loading…</Box>}
        {!assignments.isLoading && (assignments.data ?? []).length === 0 && (
          <Box color="text-status-inactive">No role assignments.</Box>
        )}
        <SpaceBetween size="s">
          {(assignments.data ?? []).map((a) => (
            <Container key={a.id}>
              <SpaceBetween size="xs" direction="horizontal">
                <Box>
                  <Box variant="awsui-key-label">{a.role_name}</Box>
                  <SpaceBetween size="xxs" direction="horizontal">
                    {a.scopes.length === 0
                      ? <Badge>no scope (role default)</Badge>
                      : a.scopes.map((s) => <Badge key={s.id}>{describeScope(s)}</Badge>)}
                  </SpaceBetween>
                </Box>
                <Button iconName="remove" variant="inline-icon" onClick={() => remove(a)} ariaLabel="Remove assignment" />
              </SpaceBetween>
            </Container>
          ))}
        </SpaceBetween>
      </Container>

      <Container header={<Header variant="h3">Assign a role</Header>}>
        <SpaceBetween size="m">
          <FormField label="Role">
            <Select
              placeholder="Pick a role"
              selectedOption={roleOpt}
              onChange={({ detail }) => setRoleOpt(detail.selectedOption)}
              options={roleOptions}
              expandToViewport
            />
          </FormField>
          <Box>
            <SpaceBetween size="xs" direction="horizontal">
              <Box variant="awsui-key-label">Scope rows (empty = role default)</Box>
              <Button iconName="add-plus" onClick={addScopeRow}>Row</Button>
            </SpaceBetween>
            <SpaceBetween size="xs">
              {scopeRows.map((row, idx) => (
                <SpaceBetween key={`scope-${idx}`} size="xs" direction="horizontal">
                  <Select
                    selectedOption={SCOPE_TYPES.find((s) => s.value === row.scope_type) ?? SCOPE_TYPES[0]}
                    onChange={({ detail }) => updateScopeRow(idx, { scope_type: detail.selectedOption.value!, target_id: '' })}
                    options={SCOPE_TYPES}
                    expandToViewport
                  />
                  {row.scope_type === 'global' && (
                    <Box variant="span" color="text-status-inactive" fontSize="body-s">unrestricted</Box>
                  )}
                  {row.scope_type === 'site' && (
                    <Select
                      placeholder="Pick a site"
                      selectedOption={siteOptions.find((s) => s.value === row.target_id) ?? null}
                      onChange={({ detail }) => updateScopeRow(idx, { target_id: detail.selectedOption.value ?? '' })}
                      options={siteOptions}
                      expandToViewport
                    />
                  )}
                  {row.scope_type !== 'global' && row.scope_type !== 'site' && (
                    <Input
                      value={row.target_id}
                      onChange={({ detail }) => updateScopeRow(idx, { target_id: detail.value })}
                      placeholder={
                        row.scope_type === 'enclave' ? 'enclave name'
                        : row.scope_type === 'organization' ? 'organization label'
                        : 'uuid'
                      }
                    />
                  )}
                  <Button iconName="close" variant="inline-icon" onClick={() => removeScopeRow(idx)} ariaLabel="Remove row" />
                </SpaceBetween>
              ))}
            </SpaceBetween>
          </Box>
          <Button variant="primary" onClick={assign} disabled={!roleOpt?.value}>Assign role</Button>
        </SpaceBetween>
      </Container>
    </SpaceBetween>
  );
}

// ----------------------- Roles -----------------------

function RolesTab({ myCapabilities }: Readonly<{ myCapabilities: string[] }>) {
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
    if (r.is_system) { toast.error('system roles cannot be deleted'); return; }
    if (!window.confirm(`Delete role "${r.name}"?`)) return;
    try {
      await http.delete(`/admin/roles/${r.id}`);
      toast.success('Role deleted');
      await refresh();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <SpaceBetween size="l">
      <Table<Role>
        variant="container"
        loading={tableQuery.isLoading}
        loadingText="Loading roles…"
        items={data}
        trackBy="id"
        header={
          <Header
            counter={`(${data.length})`}
            actions={
              <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                New role
              </Button>
            }
          >
            Roles
          </Header>
        }
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (r) => <span style={{ fontWeight: 500 }}>{r.name}</span> },
          {
            id: 'description', header: 'Description',
            cell: (r) => <Box variant="span" color="text-status-inactive" fontSize="body-s">{r.description ?? '—'}</Box>,
          },
          {
            id: 'capabilities', header: 'Capabilities',
            cell: (r) => (
              <SpaceBetween size="xxs" direction="horizontal">
                {r.permission_codes.slice(0, 6).map((c) => <Badge key={c}>{c}</Badge>)}
                {r.permission_codes.length > 6 && <Badge>{`+${r.permission_codes.length - 6}`}</Badge>}
              </SpaceBetween>
            ),
          },
          {
            id: 'type', header: 'Type',
            cell: (r) => r.is_system
              ? <Badge color="grey">system</Badge>
              : <Badge color="green">custom</Badge>,
            width: 110,
          },
          {
            id: 'actions', header: '',
            cell: (r) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button iconName="edit" variant="inline-icon" disabled={r.is_system} onClick={() => setEditing(r)} ariaLabel={`Edit ${r.name}`} />
                <Button iconName="remove" variant="inline-icon" disabled={r.is_system} onClick={() => remove(r)} ariaLabel={`Delete ${r.name}`} />
              </SpaceBetween>
            ),
            width: 120,
          },
        ]}
        empty={<Box textAlign="center" color="inherit" padding="m">No roles defined.</Box>}
        pagination={
          pageCount > 1 ? (
            <Pagination
              currentPageIndex={currentPage}
              pagesCount={pageCount}
              onChange={({ detail }) => setCurrentPage(detail.currentPageIndex)}
            />
          ) : undefined
        }
      />

      <Modal visible={createOpen} onDismiss={() => setCreateOpen(false)} header="New role" size="medium">
        <RoleForm myCapabilities={myCapabilities} onSaved={async () => { setCreateOpen(false); await refresh(); }} />
      </Modal>
      <Modal visible={editing !== null} onDismiss={() => setEditing(null)} header="Edit role" size="medium">
        {editing && (
          <RoleForm
            myCapabilities={myCapabilities}
            role={editing}
            onSaved={async () => { setEditing(null); await refresh(); }}
          />
        )}
      </Modal>
    </SpaceBetween>
  );
}

function RoleForm({
  myCapabilities, role, onSaved,
}: Readonly<{
  myCapabilities: string[];
  role?: Role;
  onSaved: () => void;
}>) {
  const editing = !!role;
  const [name, setName] = useState(role?.name ?? '');
  const [description, setDescription] = useState(role?.description ?? '');
  const [selected, setSelected] = useState<string[]>(role?.permission_codes ?? []);
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  function toggle(cap: string, checked: boolean) {
    setSelected((cur) => checked ? [...cur, cap] : cur.filter((c) => c !== cap));
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Name required';
    if (selected.length === 0) errs.caps = 'Pick at least one capability';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      const body = { name, description: description || null, permission_codes: selected };
      if (editing && role) {
        await http.patch(`/admin/roles/${role.id}`, body);
        toast.success('Role updated');
      } else {
        await http.post('/admin/roles', body);
        toast.success('Role created');
      }
      onSaved();
    } catch (err: any) { toast.error(err?.message ?? 'save failed'); } finally { setSubmitting(false); }
  }

  return (
    <form onSubmit={onSubmit}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={submitting}>
            {submitting ? 'Saving…' : editing ? 'Save' : 'Create'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField label="Name" errorText={errors.name}>
            <Input value={name} onChange={({ detail }) => setName(detail.value)} />
          </FormField>
          <FormField label="Description (optional)">
            <Input value={description ?? ''} onChange={({ detail }) => setDescription(detail.value)} />
          </FormField>
          <FormField
            label="Capabilities"
            description="You can only grant capabilities you hold yourself."
            errorText={errors.caps}
          >
            <div style={{
              display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 6,
              maxHeight: 200, overflowY: 'auto',
            }}>
              {myCapabilities.map((c) => (
                <Checkbox
                  key={c}
                  checked={selected.includes(c)}
                  onChange={({ detail }) => toggle(c, detail.checked)}
                >
                  <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{c}</span>
                </Checkbox>
              ))}
            </div>
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}
