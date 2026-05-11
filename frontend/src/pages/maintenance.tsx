// Maintenance windows — Cloudscape table + create/edit Modals.

import { useState } from 'react';
import { useTable, useGetIdentity, useList } from '@refinedev/core';
import { useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import Pagination from '@cloudscape-design/components/pagination';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator, { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';

import { http } from '@/lib/http';
import { formatDate, relativeTime } from '@/lib/utils';

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

const STATUS_TYPE: Record<WindowStatus, StatusIndicatorProps.Type> = {
  active: 'error',
  upcoming: 'warning',
  past: 'stopped',
};

const ALL_SITES_OPT: SelectProps.Option = { value: '__all__', label: 'All sites' };

function toLocalInput(iso: string | undefined): string {
  if (!iso) return '';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  const pad = (n: number) => n.toString().padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function fromLocalInput(local: string): string {
  return new Date(local).toISOString();
}

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
    <ContentLayout
      header={
        <Header
          variant="h1"
          counter={`(${total})`}
          description="Suppress alerts during planned work."
        >
          Maintenance windows
        </Header>
      }
    >
      <Table<Window>
        variant="container"
        loading={tableQuery.isLoading}
        loadingText="Loading windows…"
        items={data}
        trackBy="id"
        header={
          <Header
            counter={`(${data.length})`}
            actions={canConfigure && (
              <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                New window
              </Button>
            )}
          >
            Windows
          </Header>
        }
        columnDefinitions={[
          {
            id: 'status', header: 'Status',
            cell: (w) => {
              const s = statusOf(w);
              return <StatusIndicator type={STATUS_TYPE[s]}>{s}</StatusIndicator>;
            },
            width: 120,
          },
          { id: 'name', header: 'Name', cell: (w) => <span style={{ fontWeight: 500 }}>{w.name}</span> },
          {
            id: 'scope', header: 'Scope',
            cell: (w) => {
              const site = w.site_id ? sitesById.get(w.site_id) : null;
              if (site) return `${site.code} · ${site.name}`;
              if (w.site_id) return `site ${w.site_id.slice(0, 8)}…`;
              return <Box variant="span" color="text-status-inactive">all sites</Box>;
            },
          },
          {
            id: 'starts', header: 'Starts',
            cell: (w) => (
              <SpaceBetween size="xxxs">
                <span>{formatDate(w.starts_at)}</span>
                <Box variant="span" color="text-status-inactive" fontSize="body-s">
                  {relativeTime(w.starts_at)}
                </Box>
              </SpaceBetween>
            ),
          },
          {
            id: 'ends', header: 'Ends',
            cell: (w) => (
              <SpaceBetween size="xxxs">
                <span>{formatDate(w.ends_at)}</span>
                <Box variant="span" color="text-status-inactive" fontSize="body-s">
                  {relativeTime(w.ends_at)}
                </Box>
              </SpaceBetween>
            ),
          },
          {
            id: 'reason', header: 'Reason',
            cell: (w) => (
              <Box variant="span" color="text-status-inactive" fontSize="body-s">
                {w.reason ?? '—'}
              </Box>
            ),
          },
          {
            id: 'createdBy', header: 'Created by',
            cell: (w) => (
              <Box variant="span" color="text-status-inactive" fontSize="body-s">
                {w.created_by ?? '—'}
              </Box>
            ),
          },
          ...(canConfigure ? [{
            id: 'actions', header: '',
            cell: (w: Window) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button iconName="edit" variant="inline-icon" onClick={() => setEditing(w)} ariaLabel={`Edit ${w.name}`} />
                <Button iconName="remove" variant="inline-icon" onClick={() => remove(w)} ariaLabel={`Delete ${w.name}`} />
              </SpaceBetween>
            ),
            width: 120,
          }] : []),
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            No maintenance windows configured.
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

      {canConfigure && (
        <Modal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header="New maintenance window"
          size="medium"
        >
          <WindowForm
            sites={sites}
            onSaved={async () => { setCreateOpen(false); await refresh(); }}
          />
        </Modal>
      )}

      <Modal
        visible={editing !== null}
        onDismiss={() => setEditing(null)}
        header="Edit maintenance window"
        size="medium"
      >
        {editing && (
          <WindowForm
            sites={sites}
            window={editing}
            onSaved={async () => { setEditing(null); await refresh(); }}
          />
        )}
      </Modal>
    </ContentLayout>
  );
}

function WindowForm({
  sites, window: existing, onSaved,
}: Readonly<{
  sites: Site[];
  window?: Window;
  onSaved: () => void;
}>) {
  const editing = !!existing;
  const siteOptions: SelectProps.Option[] = [
    ALL_SITES_OPT,
    ...sites.map((s) => ({ value: s.id, label: `${s.code} · ${s.name}` })),
  ];

  const [name, setName] = useState(existing?.name ?? '');
  const [siteOpt, setSiteOpt] = useState<SelectProps.Option>(
    siteOptions.find((o) => o.value === existing?.site_id) ?? ALL_SITES_OPT,
  );
  const [startsAt, setStartsAt] = useState(toLocalInput(existing?.starts_at));
  const [endsAt, setEndsAt] = useState(toLocalInput(existing?.ends_at));
  const [reason, setReason] = useState(existing?.reason ?? '');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Name required';
    if (!startsAt) errs.starts = 'Start required';
    if (!endsAt) errs.ends = 'End required';
    if (startsAt && endsAt && new Date(endsAt) <= new Date(startsAt)) {
      errs.ends = 'End must be after start';
    }
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    const body = {
      name,
      site_id: siteOpt.value === ALL_SITES_OPT.value ? null : siteOpt.value,
      starts_at: fromLocalInput(startsAt),
      ends_at: fromLocalInput(endsAt),
      reason: reason || null,
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
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form onSubmit={onSubmit}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={submitting}>
            {submitting ? 'Saving…' : editing ? 'Save changes' : 'Create window'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField label="Name" errorText={errors.name}>
            <Input value={name} onChange={({ detail }) => setName(detail.value)} placeholder="e.g. Q3 power maintenance" />
          </FormField>
          <FormField label="Scope">
            <Select
              selectedOption={siteOpt}
              onChange={({ detail }) => setSiteOpt(detail.selectedOption)}
              options={siteOptions}
              expandToViewport
            />
          </FormField>
          <FormField label="Starts at" errorText={errors.starts}>
            {/* datetime-local is a vanilla HTML input; Cloudscape's
                Input doesn't expose all the variants and a native input
                is fine here. */}
            <input
              type="datetime-local"
              value={startsAt}
              onChange={(e) => setStartsAt(e.target.value)}
              style={{
                width: '100%', padding: '6px 10px', borderRadius: 8,
                border: '1px solid var(--color-border-input-default, #aab)',
                background: 'inherit', color: 'inherit',
              }}
            />
          </FormField>
          <FormField label="Ends at" errorText={errors.ends}>
            <input
              type="datetime-local"
              value={endsAt}
              onChange={(e) => setEndsAt(e.target.value)}
              style={{
                width: '100%', padding: '6px 10px', borderRadius: 8,
                border: '1px solid var(--color-border-input-default, #aab)',
                background: 'inherit', color: 'inherit',
              }}
            />
          </FormField>
          <FormField label="Reason (optional)">
            <Input value={reason ?? ''} onChange={({ detail }) => setReason(detail.value)} placeholder="e.g. PDU firmware upgrade" />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}
