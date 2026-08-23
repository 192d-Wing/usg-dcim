// Sites list — Cloudscape Table with create/edit modals. Region is
// picked (or quick-created) in the create modal; code and region are
// immutable after creation (the PATCH surface covers name, lifecycle,
// address, majcom), so the edit modal shows them read-only. CSV import
// remains the bulk bring-up path; this covers the one-off site.

import { useState } from 'react';
import { useInvalidate, useList, useTable } from '@refinedev/core';
import { useNavigate } from 'react-router';
import { toast } from 'sonner';

import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import ContentLayout from '@cloudscape-design/components/content-layout';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Link from '@cloudscape-design/components/link';
import Modal from '@cloudscape-design/components/modal';
import Pagination from '@cloudscape-design/components/pagination';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';

import { hasCapability } from '@/lib/access-control-provider';
import { http } from '@/lib/http';

type Site = {
  id: string;
  name: string;
  code: string;
  region_id: string;
  address?: string | null;
  timezone?: string | null;
  majcom?: string | null;
  organization?: string | null;
  enclave?: string | null;
  lifecycle_state: string;
};

type Region = { id: string; code: string; name: string };

const LIFECYCLE_STATES = [
  'planned', 'staged', 'active', 'maintenance', 'decommissioned', 'retired',
] as const;

type ModalState = { mode: 'create' } | { mode: 'edit'; site: Site } | null;

export function SitesListPage() {
  const navigate = useNavigate();
  const invalidate = useInvalidate();
  const { tableQuery, result, currentPage, pageCount, setCurrentPage } = useTable<Site>({
    resource: 'inventory/sites',
    pagination: { pageSize: 50 },
    sorters: { initial: [{ field: 'code', order: 'asc' }] },
  });
  const [selected, setSelected] = useState<Site[]>([]);
  const [modal, setModal] = useState<ModalState>(null);
  const data = result.data ?? [];
  const total = result.total ?? 0;

  const canCreate = hasCapability('inventory:sites:create');
  const canUpdate = hasCapability('inventory:sites:update');

  async function refresh() {
    // Refine's own invalidation — hand-built react-query keys never
    // matched the library's key shape, leaving the table stale.
    await invalidate({ resource: 'inventory/sites', invalidates: ['list'] });
  }

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          counter={`(${total})`}
          description={`Page ${currentPage} of ${Math.max(pageCount, 1)}`}
          actions={
            canCreate ? (
              <Button variant="primary" onClick={() => setModal({ mode: 'create' })}>
                New site
              </Button>
            ) : undefined
          }
        >
          Sites
        </Header>
      }
    >
      <Table<Site>
        variant="container"
        loading={tableQuery.isLoading}
        loadingText="Loading sites…"
        items={data}
        trackBy="id"
        selectionType="single"
        selectedItems={selected}
        onSelectionChange={({ detail }) => setSelected(detail.selectedItems)}
        onRowClick={({ detail }) => navigate(`/sites/${detail.item.id}`)}
        ariaLabels={{
          selectionGroupLabel: 'Site selection',
          itemSelectionLabel: (_d, item) => `Select ${item.code}`,
          allItemsSelectionLabel: () => 'Select all sites',
        }}
        columnDefinitions={[
          {
            id: 'code',
            header: 'Code',
            cell: (s) => (
              <Link
                href={`/sites/${s.id}`}
                onFollow={(e) => { e.preventDefault(); navigate(`/sites/${s.id}`); }}
              >
                <span style={{ fontFamily: 'ui-monospace, monospace' }}>{s.code}</span>
              </Link>
            ),
            width: 140,
          },
          { id: 'name', header: 'Name', cell: (s) => s.name },
          { id: 'majcom', header: 'MAJCOM', cell: (s) => s.majcom ?? '—' },
          { id: 'organization', header: 'Org', cell: (s) => s.organization ?? '—' },
          { id: 'enclave', header: 'Enclave', cell: (s) => s.enclave ?? '—' },
          {
            id: 'lifecycle_state',
            header: 'State',
            cell: (s) => s.lifecycle_state === 'active'
              ? <StatusIndicator type="success">{s.lifecycle_state}</StatusIndicator>
              : <StatusIndicator type="warning">{s.lifecycle_state}</StatusIndicator>,
            width: 120,
          },
          ...(canUpdate ? [{
            id: 'actions',
            header: 'Actions',
            cell: (s: Site) => (
              // Contain native clicks so row navigation doesn't fire.
              <div onClick={(e) => e.stopPropagation()}>
                <Button variant="inline-link" onClick={() => setModal({ mode: 'edit', site: s })}>
                  Edit
                </Button>
              </div>
            ),
            width: 100,
          }] : []),
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            {canCreate ? 'No sites yet — use "New site" to create the first one.' : 'No sites yet.'}
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

      {modal && (
        <SiteFormModal
          site={modal.mode === 'edit' ? modal.site : null}
          onDismiss={() => setModal(null)}
          onSaved={async (id) => {
            setModal(null);
            await refresh();
            if (id) navigate(`/sites/${id}`);
          }}
        />
      )}
    </ContentLayout>
  );
}

function SiteFormModal({
  site, onDismiss, onSaved,
}: Readonly<{
  site: Site | null;
  onDismiss: () => void;
  onSaved: (newId: string | null) => void;
}>) {
  const isEdit = site !== null;
  const invalidate = useInvalidate();

  const regions = useList<Region>({
    resource: 'inventory/regions',
    pagination: { pageSize: 200 },
    queryOptions: { enabled: !isEdit },
  });
  const regionOpts: SelectProps.Option[] = (regions.result.data ?? [])
    .map((r) => ({ value: r.id, label: `${r.code} · ${r.name}` }));

  const [regionOpt, setRegionOpt] = useState<SelectProps.Option | null>(null);
  const [name, setName] = useState(site?.name ?? '');
  const [code, setCode] = useState(site?.code ?? '');
  const [lifecycle, setLifecycle] = useState<SelectProps.Option>({
    value: site?.lifecycle_state ?? 'active',
    label: site?.lifecycle_state ?? 'active',
  });
  const [address, setAddress] = useState(site?.address ?? '');
  const [timezone, setTimezone] = useState(site?.timezone ?? '');
  const [majcom, setMajcom] = useState(site?.majcom ?? '');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  async function quickCreateRegion() {
    const v = window.prompt('New region (used for both name and code):');
    if (!v?.trim()) return;
    try {
      const r = await http.post('/inventory/regions', { name: v.trim(), code: v.trim() });
      toast.success('Region created');
      await invalidate({ resource: 'inventory/regions', invalidates: ['list'] });
      setRegionOpt({ value: r.data.id, label: `${v.trim()} · ${v.trim()}` });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  async function onSubmit() {
    const errs: Record<string, string> = {};
    if (!isEdit && !regionOpt?.value) errs.region = 'Region required';
    if (!name.trim()) errs.name = 'Name required';
    if (!isEdit && !code.trim()) errs.code = 'Code required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      if (isEdit) {
        await http.patch(`/inventory/sites/${site.id}`, {
          name,
          lifecycle_state: lifecycle.value,
          address: address.trim() || null,
          majcom: majcom.trim() || null,
        });
        toast.success('Site updated');
        onSaved(null);
      } else {
        const r = await http.post('/inventory/sites', {
          region_id: regionOpt!.value,
          name,
          code,
          lifecycle_state: lifecycle.value,
          address: address.trim() || null,
          timezone: timezone.trim() || null,
          majcom: majcom.trim() || null,
          metadata_json: {},
        });
        toast.success('Site created');
        onSaved(r.data.id);
      }
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
      setSubmitting(false);
    }
  }

  return (
    <Modal
      visible
      onDismiss={onDismiss}
      header={isEdit ? `Edit ${site.code}` : 'New site'}
      footer={
        <Box float="right">
          <SpaceBetween size="xs" direction="horizontal">
            <Button onClick={onDismiss}>Cancel</Button>
            <Button variant="primary" loading={submitting} onClick={onSubmit}>
              {isEdit ? 'Save' : 'Create'}
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <SpaceBetween size="m">
        {!isEdit && (
          <FormField
            label="Region"
            errorText={errors.region}
            description={regionOpts.length === 0 ? 'None yet — use + New to create one.' : undefined}
          >
            <SpaceBetween size="xs" direction="horizontal">
              <Select
                placeholder="Pick a region…"
                selectedOption={regionOpt}
                onChange={({ detail }) => setRegionOpt(detail.selectedOption)}
                options={regionOpts}
                loadingText="Loading regions…"
                statusType={regions.query.isLoading ? 'loading' : 'finished'}
                expandToViewport
              />
              <Button iconName="add-plus" onClick={quickCreateRegion}>New</Button>
            </SpaceBetween>
          </FormField>
        )}
        <ColumnLayout columns={2}>
          <FormField label="Name" errorText={errors.name}>
            <Input value={name} onChange={({ detail }) => setName(detail.value)} />
          </FormField>
          <FormField
            label="Code"
            errorText={errors.code}
            description={isEdit ? 'Immutable after creation' : undefined}
          >
            <Input
              value={code}
              disabled={isEdit}
              onChange={({ detail }) => setCode(detail.value)}
            />
          </FormField>
        </ColumnLayout>
        <FormField label="Lifecycle state">
          <Select
            selectedOption={lifecycle}
            onChange={({ detail }) => setLifecycle(detail.selectedOption)}
            options={LIFECYCLE_STATES.map((s) => ({ value: s, label: s }))}
            expandToViewport
          />
        </FormField>
        <FormField label="Address">
          <Input value={address} onChange={({ detail }) => setAddress(detail.value)} />
        </FormField>
        <ColumnLayout columns={2}>
          <FormField label="Timezone" description={isEdit ? 'Immutable after creation' : 'e.g. America/Chicago'}>
            <Input
              value={timezone}
              disabled={isEdit}
              onChange={({ detail }) => setTimezone(detail.value)}
            />
          </FormField>
          <FormField label="MAJCOM">
            <Input value={majcom} onChange={({ detail }) => setMajcom(detail.value)} />
          </FormField>
        </ColumnLayout>
      </SpaceBetween>
    </Modal>
  );
}
