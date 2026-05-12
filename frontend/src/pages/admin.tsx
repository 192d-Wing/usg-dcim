// Admin — Users + Roles management with assignments manager.
// Cloudscape Tabs + Table + Modal + Form everywhere.

import { useEffect, useMemo, useState } from 'react';
import { useTable, useGetIdentity, useList } from '@refinedev/core';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import ExpandableSection from '@cloudscape-design/components/expandable-section';
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

import { hasCap } from '@/lib/caps';
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
type OidcMapping = {
  id: string;
  idp_role: string;
  claim_source: string;
  dcim_role_id: string;
  dcim_role_name: string;
  description: string | null;
  scope_dimension: string | null;
  scope_target: string | null;
  created_at: string;
};

const SCOPE_DIMENSION_OPTIONS: SelectProps.Option[] = [
  { value: 'global', label: 'global (no restriction)' },
  { value: 'region', label: 'region (target = Region.code)' },
  { value: 'site', label: 'site (target = Site.code)' },
  { value: 'site_group', label: 'site_group (target = SiteGroup.name)' },
  { value: 'enclave', label: 'enclave (target = literal string)' },
  { value: 'organization', label: 'organization (target = literal string)' },
  { value: 'fabric', label: 'fabric (target = Fabric.slug; gates DNS + IPAM)' },
];

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
  const canUsers = hasCap(identity?.capabilities, 'admin:users:read');
  const canRoles = hasCap(identity?.capabilities, 'admin:roles:read');
  const myCaps = identity?.capabilities ?? [];

  if (!canUsers && !canRoles) {
    return (
      <ContentLayout header={<Header variant="h1">Admin</Header>}>
        <Box color="text-status-inactive">
          You don't have <code style={{ fontFamily: 'ui-monospace, monospace' }}>admin:users:read</code> or{' '}
          <code style={{ fontFamily: 'ui-monospace, monospace' }}>admin:roles:read</code>.
        </Box>
      </ContentLayout>
    );
  }

  const tabs: { id: string; label: string; content: React.ReactNode }[] = [];
  if (canUsers) tabs.push({ id: 'users', label: 'Users', content: <UsersTab /> });
  if (canRoles) tabs.push({ id: 'roles', label: 'Roles', content: <RolesTab myCapabilities={myCaps} /> });
  if (canRoles) tabs.push({ id: 'oidc', label: 'OIDC mappings', content: <OidcMappingsTab /> });

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

type CapabilityCatalog = {
  catalog: Record<string, Record<string, string[]>>;
  specialties: Record<string, string>;
};

/** Mirror of backend find_matching_capability: exact, then
 *  progressively shorter `<prefix>:*`, then `*`. */
function canGrantCode(myCaps: string[], code: string): boolean {
  if (myCaps.includes(code)) return true;
  const parts = code.split(':');
  for (let i = parts.length - 1; i > 0; i--) {
    if (myCaps.includes(parts.slice(0, i).join(':') + ':*')) return true;
  }
  return myCaps.includes('*');
}

type CapabilityRowProps = Readonly<{
  domain: string;
  resource: string;
  actions: string[];
  myCapabilities: string[];
  selectedSet: Set<string>;
  search: string;
  onToggle: (code: string, checked: boolean) => void;
}>;

function CapabilityRow({ domain, resource, actions, myCapabilities, selectedSet, search, onToggle }: CapabilityRowProps) {
  const matchesSearch = (c: string) => !search || c.toLowerCase().includes(search.toLowerCase());
  const visibleActions = actions.filter((a) => matchesSearch(`${domain}:${resource}:${a}`));
  if (visibleActions.length === 0) return null;
  return (
    <div style={{ display: 'grid', gridTemplateColumns: '140px 1fr', alignItems: 'center', gap: 12 }}>
      <Box variant="awsui-key-label">{resource}</Box>
      <SpaceBetween size="xs" direction="horizontal">
        {visibleActions.map((action) => {
          const code = `${domain}:${resource}:${action}`;
          return (
            <Checkbox
              key={code}
              checked={selectedSet.has(code)}
              disabled={!canGrantCode(myCapabilities, code)}
              onChange={({ detail }) => onToggle(code, detail.checked)}
            >
              {action}
            </Checkbox>
          );
        })}
      </SpaceBetween>
    </div>
  );
}

type SpecialtyRowProps = Readonly<{
  codes: string[];
  myCapabilities: string[];
  selectedSet: Set<string>;
  search: string;
  onToggle: (code: string, checked: boolean) => void;
}>;

function SpecialtyRow({ codes, myCapabilities, selectedSet, search, onToggle }: SpecialtyRowProps) {
  const matchesSearch = (c: string) => !search || c.toLowerCase().includes(search.toLowerCase());
  const visible = codes.filter(matchesSearch);
  if (visible.length === 0) return null;
  return (
    <div style={{ display: 'grid', gridTemplateColumns: '140px 1fr', alignItems: 'flex-start', gap: 12, marginTop: 4 }}>
      <Box variant="awsui-key-label" color="text-status-inactive">specialty</Box>
      <SpaceBetween size="xs" direction="horizontal">
        {visible.map((code) => (
          <Checkbox
            key={code}
            checked={selectedSet.has(code)}
            disabled={!canGrantCode(myCapabilities, code)}
            onChange={({ detail }) => onToggle(code, detail.checked)}
          >
            <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{code}</span>
          </Checkbox>
        ))}
      </SpaceBetween>
    </div>
  );
}

type DomainSectionProps = Readonly<{
  domain: string;
  catalog: CapabilityCatalog;
  domainCodes: string[];
  myCapabilities: string[];
  selectedSet: Set<string>;
  search: string;
  onToggle: (code: string, checked: boolean) => void;
  onSetMany: (codes: string[], on: boolean) => void;
}>;

function DomainSection({
  domain, catalog, domainCodes, myCapabilities, selectedSet, search, onToggle, onSetMany,
}: DomainSectionProps) {
  const grantable = domainCodes.filter((c) => canGrantCode(myCapabilities, c));
  const matchesSearch = (c: string) => !search || c.toLowerCase().includes(search.toLowerCase());
  const visible = grantable.filter(matchesSearch);
  if (visible.length === 0 && search) return null;
  const selectedInDomain = grantable.filter((c) => selectedSet.has(c)).length;
  const allOn = grantable.length > 0 && selectedInDomain === grantable.length;
  const resources = catalog.catalog[domain] ?? {};
  const specialtyCodes = Object.keys(catalog.specialties).filter((s) => s.startsWith(domain + ':'));

  return (
    <ExpandableSection
      variant="container"
      defaultExpanded={!!search || selectedInDomain > 0}
      headerText={`${domain} — ${selectedInDomain} / ${grantable.length} selected`}
      headerActions={
        <Checkbox
          checked={allOn}
          disabled={grantable.length === 0}
          onChange={({ detail }) => onSetMany(grantable, detail.checked)}
        >
          all
        </Checkbox>
      }
    >
      <SpaceBetween size="xs">
        {Object.entries(resources).map(([resource, actions]) => (
          <CapabilityRow
            key={resource}
            domain={domain}
            resource={resource}
            actions={actions}
            myCapabilities={myCapabilities}
            selectedSet={selectedSet}
            search={search}
            onToggle={onToggle}
          />
        ))}
        {specialtyCodes.length > 0 && (
          <SpecialtyRow
            codes={specialtyCodes}
            myCapabilities={myCapabilities}
            selectedSet={selectedSet}
            search={search}
            onToggle={onToggle}
          />
        )}
      </SpaceBetween>
    </ExpandableSection>
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
  const [search, setSearch] = useState('');
  const [catalog, setCatalog] = useState<CapabilityCatalog | null>(null);

  useEffect(() => {
    http.get<CapabilityCatalog>('/admin/capabilities/catalog')
      .then((r) => setCatalog(r.data))
      .catch((err) => toast.error(err?.message ?? 'failed to load capability catalog'));
  }, []);

  // Pre-compute the universe of grantable codes per domain so per-domain
  // counters and "grant all" actions don't reflow on every search edit.
  const codesByDomain = useMemo(() => {
    if (!catalog) return new Map<string, string[]>();
    const out = new Map<string, string[]>();
    for (const [domain, resources] of Object.entries(catalog.catalog)) {
      const codes: string[] = [];
      for (const [resource, actions] of Object.entries(resources)) {
        for (const action of actions) codes.push(`${domain}:${resource}:${action}`);
      }
      for (const specialty of Object.keys(catalog.specialties)) {
        if (specialty.startsWith(domain + ':')) codes.push(specialty);
      }
      if (codes.length > 0) out.set(domain, codes);
    }
    // Domains that only have specialty codes (none in CAPABILITY_CATALOG).
    for (const specialty of Object.keys(catalog.specialties)) {
      const domain = specialty.split(':')[0];
      if (!out.has(domain)) out.set(domain, [specialty]);
    }
    return out;
  }, [catalog]);

  const selectedSet = useMemo(() => new Set(selected), [selected]);

  function toggle(cap: string, checked: boolean) {
    setSelected((cur) => checked ? [...cur, cap] : cur.filter((c) => c !== cap));
  }
  function setMany(codes: string[], on: boolean) {
    setSelected((cur) => {
      const s = new Set(cur);
      for (const c of codes) on ? s.add(c) : s.delete(c);
      return Array.from(s);
    });
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
            label={`Capabilities (${selected.length} selected)`}
            description="Grouped by domain. You can only grant capabilities you hold yourself; disabled rows are outside your reach."
            errorText={errors.caps}
          >
            <SpaceBetween size="xs">
              <Input
                value={search}
                onChange={({ detail }) => setSearch(detail.value)}
                placeholder="Filter codes (e.g. dns:zones, audit, *:read)"
                type="search"
              />
              {catalog === null && <Box color="text-status-inactive">Loading catalog…</Box>}
              {catalog !== null && Array.from(codesByDomain.entries())
                .sort(([a], [b]) => a.localeCompare(b))
                .map(([domain, allCodes]) => (
                  <DomainSection
                    key={domain}
                    domain={domain}
                    catalog={catalog}
                    domainCodes={allCodes}
                    myCapabilities={myCapabilities}
                    selectedSet={selectedSet}
                    search={search}
                    onToggle={toggle}
                    onSetMany={setMany}
                  />
                ))}
            </SpaceBetween>
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}

// ----------------------- OIDC role mappings -----------------------

function OidcMappingsTab() {
  const { tableQuery, result, currentPage, pageCount, setCurrentPage } = useTable<OidcMapping>({
    resource: 'admin/oidc-role-mappings',
    pagination: { pageSize: 50 },
  });
  const rolesRes = useList<Role>({ resource: 'admin/roles', pagination: { pageSize: 200 } });
  const data = result.data ?? [];
  const roles = rolesRes.result.data ?? [];
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<OidcMapping | null>(null);

  async function refresh() { await tableQuery.refetch(); }

  async function remove(m: OidcMapping) {
    if (!globalThis.confirm(`Delete mapping for IdP role "${m.idp_role}"?`)) return;
    try {
      await http.delete(`/admin/oidc-role-mappings/${m.id}`);
      toast.success('Mapping deleted');
      await refresh();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  return (
    <SpaceBetween size="l">
      <Table<OidcMapping>
        variant="container"
        loading={tableQuery.isLoading}
        loadingText="Loading mappings…"
        items={data}
        trackBy="id"
        header={
          <Header
            counter={`(${data.length})`}
            description="Map an IdP-asserted role (Keycloak realm role, Okta/ADFS group) to a DCIM role. Applied on every OIDC sign-in."
            actions={
              <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                New mapping
              </Button>
            }
          >
            OIDC role mappings
          </Header>
        }
        columnDefinitions={[
          {
            id: 'idp_role', header: 'IdP role',
            cell: (m) => <span style={{ fontFamily: 'ui-monospace, monospace', fontWeight: 500 }}>{m.idp_role}</span>,
          },
          {
            id: 'claim_source', header: 'Source',
            cell: (m) => <Badge color="grey">{m.claim_source}</Badge>,
            width: 130,
          },
          {
            id: 'dcim_role', header: 'DCIM role',
            cell: (m) => <Badge color="blue">{m.dcim_role_name}</Badge>,
          },
          {
            id: 'scope', header: 'Scope',
            cell: (m) => m.scope_dimension
              ? <Badge color="green">{`${m.scope_dimension}=${m.scope_target ?? '?'}`}</Badge>
              : <Box variant="span" color="text-status-inactive" fontSize="body-s">global</Box>,
            width: 200,
          },
          {
            id: 'description', header: 'Description',
            cell: (m) => <Box variant="span" color="text-status-inactive" fontSize="body-s">{m.description ?? '—'}</Box>,
          },
          {
            id: 'actions', header: '',
            cell: (m) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button iconName="edit" variant="inline-icon" onClick={() => setEditing(m)} ariaLabel={`Edit ${m.idp_role}`} />
                <Button iconName="remove" variant="inline-icon" onClick={() => remove(m)} ariaLabel={`Delete ${m.idp_role}`} />
              </SpaceBetween>
            ),
            width: 110,
          },
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            No mappings yet. OIDC users sign in without any DCIM role attached until you add at least one mapping.
          </Box>
        }
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

      <Modal visible={createOpen} onDismiss={() => setCreateOpen(false)} header="New OIDC mapping" size="medium">
        <MappingForm
          roles={roles}
          onSaved={async () => { setCreateOpen(false); await refresh(); }}
        />
      </Modal>
      <Modal visible={editing !== null} onDismiss={() => setEditing(null)} header="Edit OIDC mapping" size="medium">
        {editing && (
          <MappingForm
            roles={roles}
            mapping={editing}
            onSaved={async () => { setEditing(null); await refresh(); }}
          />
        )}
      </Modal>
    </SpaceBetween>
  );
}

const CLAIM_SOURCE_OPTIONS: SelectProps.Option[] = [
  { value: 'keycloak', label: 'Keycloak (realm_access.roles)' },
  { value: 'keycloak-client', label: 'Keycloak (resource_access.<client>.roles)' },
  { value: 'okta', label: 'Okta (groups)' },
  { value: 'adfs', label: 'ADFS (groups / roles)' },
  { value: 'other', label: 'Other' },
];

function MappingForm({
  roles, mapping, onSaved,
}: Readonly<{
  roles: Role[];
  mapping?: OidcMapping;
  onSaved: () => void;
}>) {
  const editing = !!mapping;
  const [idpRole, setIdpRole] = useState(mapping?.idp_role ?? '');
  const [claimSource, setClaimSource] = useState<SelectProps.Option>(
    CLAIM_SOURCE_OPTIONS.find((o) => o.value === mapping?.claim_source) ?? CLAIM_SOURCE_OPTIONS[0],
  );
  const [dcimRoleOpt, setDcimRoleOpt] = useState<SelectProps.Option | null>(
    mapping ? { value: mapping.dcim_role_id, label: mapping.dcim_role_name } : null,
  );
  const [description, setDescription] = useState(mapping?.description ?? '');
  const [scopeDim, setScopeDim] = useState<SelectProps.Option>(
    SCOPE_DIMENSION_OPTIONS.find((o) => o.value === (mapping?.scope_dimension ?? 'global'))
    ?? SCOPE_DIMENSION_OPTIONS[0],
  );
  const [scopeTarget, setScopeTarget] = useState(mapping?.scope_target ?? '');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const roleOptions: SelectProps.Option[] = roles.map((r) => ({
    value: r.id,
    label: r.is_system ? `${r.name} (system)` : r.name,
  }));

  const dimIsScoped = scopeDim.value !== 'global';

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!idpRole.trim()) errs.idp_role = 'IdP role name required';
    if (!dcimRoleOpt?.value) errs.dcim_role = 'Pick a DCIM role';
    if (dimIsScoped && !scopeTarget.trim()) errs.scope_target = 'Target required when scope is set';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    const body = {
      claim_source: claimSource.value,
      dcim_role_id: dcimRoleOpt!.value,
      description: description || null,
      scope_dimension: scopeDim.value,
      scope_target: dimIsScoped ? scopeTarget.trim() : null,
    };
    try {
      if (editing && mapping) {
        await http.patch(`/admin/oidc-role-mappings/${mapping.id}`, body);
        toast.success('Mapping updated');
      } else {
        await http.post('/admin/oidc-role-mappings', {
          ...body, idp_role: idpRole.trim(),
        });
        toast.success('Mapping created');
      }
      onSaved();
    } catch (err: any) {
      toast.error(err?.message ?? 'save failed');
    } finally {
      setSubmitting(false);
    }
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
          <FormField
            label="IdP role"
            description="Exact string as it appears in the ID token (case-sensitive). Cannot be changed after creation."
            errorText={errors.idp_role}
          >
            <Input
              value={idpRole}
              disabled={editing}
              placeholder="e.g. dcim-admin"
              onChange={({ detail }) => setIdpRole(detail.value)}
            />
          </FormField>
          <FormField label="Claim source" description="Where to expect this role string. Documentation only — matching is across all known claim paths.">
            <Select
              selectedOption={claimSource}
              onChange={({ detail }) => setClaimSource(detail.selectedOption)}
              options={CLAIM_SOURCE_OPTIONS}
              expandToViewport
            />
          </FormField>
          <FormField label="DCIM role" errorText={errors.dcim_role}>
            <Select
              placeholder="Pick a DCIM role"
              selectedOption={dcimRoleOpt}
              onChange={({ detail }) => setDcimRoleOpt(detail.selectedOption)}
              options={roleOptions}
              expandToViewport
            />
          </FormField>
          <FormField label="Description (optional)">
            <Input value={description ?? ''} onChange={({ detail }) => setDescription(detail.value)} />
          </FormField>
          <FormField
            label="Scope dimension"
            description="Restrict this mapping to a single region / site / etc. Leave as 'global' for fleet-wide grants."
          >
            <Select
              selectedOption={scopeDim}
              onChange={({ detail }) => setScopeDim(detail.selectedOption)}
              options={SCOPE_DIMENSION_OPTIONS}
              expandToViewport
            />
          </FormField>
          {dimIsScoped && (
            <FormField
              label="Scope target"
              description="Resolved at sign-in against the matching column (e.g. 'EUCOM' against Region.code)."
              errorText={errors.scope_target}
            >
              <Input
                value={scopeTarget}
                onChange={({ detail }) => setScopeTarget(detail.value)}
                placeholder={
                  scopeDim.value === 'region' ? 'e.g. EUCOM' :
                  scopeDim.value === 'site' ? 'e.g. EUCOM-001' :
                  scopeDim.value === 'site_group' ? 'site group name' :
                  scopeDim.value === 'fabric' ? 'e.g. prod (Fabric.slug)' :
                  'literal string value'
                }
              />
            </FormField>
          )}
        </SpaceBetween>
      </Form>
    </form>
  );
}
