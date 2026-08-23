// Buildings list — Cloudscape Table with site filter and
// create/edit/delete modals. Row click opens the building floor view.

import { useState } from 'react';
import { useInvalidate, useList, useTable } from '@refinedev/core';
import { useNavigate } from 'react-router';
import { toast } from 'sonner';

import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ContentLayout from '@cloudscape-design/components/content-layout';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Link from '@cloudscape-design/components/link';
import Modal from '@cloudscape-design/components/modal';
import Pagination from '@cloudscape-design/components/pagination';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';

import { hasCapability } from '@/lib/access-control-provider';
import { http } from '@/lib/http';

type Site = { id: string; code: string; name: string };
type Building = {
  id: string;
  site_id: string;
  name: string;
  code: string;
  created_at: string;
  updated_at: string;
};

type ModalState =
  | { mode: 'create' }
  | { mode: 'edit'; building: Building }
  | { mode: 'delete'; building: Building }
  | null;

export function BuildingsListPage() {
  const nav = useNavigate();
  const invalidate = useInvalidate();
  const [siteOpt, setSiteOpt] = useState<SelectProps.Option | null>(null);
  const [modal, setModal] = useState<ModalState>(null);

  const sites = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const siteById = new Map((sites.result.data ?? []).map((s) => [s.id, s]));

  const siteId = siteOpt?.value ?? '';
  const { tableQuery, result, currentPage, pageCount, setCurrentPage } = useTable<Building>({
    resource: 'inventory/buildings',
    pagination: { pageSize: 50 },
    filters: {
      permanent: siteId ? [{ field: 'site_id', operator: 'eq', value: siteId }] : [],
    },
  });
  const data = result.data ?? [];
  const total = result.total ?? 0;

  const canCreate = hasCapability('inventory:buildings:create');
  const canUpdate = hasCapability('inventory:buildings:update');
  const canDelete = hasCapability('inventory:buildings:delete');

  async function refresh() {
    // Refine's own invalidation — hand-built react-query keys never
    // matched the library's key shape, leaving the table stale.
    await invalidate({ resource: 'inventory/buildings', invalidates: ['list'] });
  }

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          counter={`(${total})`}
          description="Buildings and their datacenter floors. Open a building to see racks and power per floor."
          actions={
            canCreate ? (
              <Button variant="primary" onClick={() => setModal({ mode: 'create' })}>
                New building
              </Button>
            ) : undefined
          }
        >
          Buildings
        </Header>
      }
    >
      <Table<Building>
        variant="container"
        loading={tableQuery.isLoading}
        loadingText="Loading buildings…"
        items={data}
        trackBy="id"
        onRowClick={({ detail }) => nav(`/buildings/${detail.item.id}`)}
        filter={
          <div style={{ maxWidth: 320 }}>
            <Select
              placeholder="All sites"
              selectedOption={siteOpt}
              onChange={({ detail }) =>
                setSiteOpt(detail.selectedOption.value ? detail.selectedOption : null)}
              options={[
                { value: '', label: 'All sites' },
                ...(sites.result.data ?? []).map((s) => ({ value: s.id, label: `${s.code} · ${s.name}` })),
              ]}
              expandToViewport
            />
          </div>
        }
        columnDefinitions={[
          {
            id: 'code',
            header: 'Code',
            cell: (b) => (
              <Link
                href={`/buildings/${b.id}`}
                onFollow={(e) => { e.preventDefault(); nav(`/buildings/${b.id}`); }}
              >
                <span style={{ fontFamily: 'ui-monospace, monospace' }}>{b.code}</span>
              </Link>
            ),
            width: 160,
          },
          { id: 'name', header: 'Name', cell: (b) => b.name },
          {
            id: 'site',
            header: 'Site',
            cell: (b) => {
              const s = siteById.get(b.site_id);
              return s ? (
                <Link
                  href={`/sites/${s.id}`}
                  onFollow={(e) => { e.preventDefault(); nav(`/sites/${s.id}`); }}
                >
                  {s.code} · {s.name}
                </Link>
              ) : '—';
            },
          },
          {
            id: 'updated',
            header: 'Updated',
            cell: (b) => (b.updated_at ? new Date(b.updated_at).toLocaleDateString() : '—'),
            width: 130,
          },
          {
            id: 'actions',
            header: 'Actions',
            cell: (b) => (
              // Contain native clicks so row navigation doesn't fire.
              <div onClick={(e) => e.stopPropagation()}>
                <SpaceBetween size="xs" direction="horizontal">
                  {canUpdate && (
                    <Button variant="inline-link" onClick={() => setModal({ mode: 'edit', building: b })}>
                      Edit
                    </Button>
                  )}
                  {canDelete && (
                    <Button variant="inline-link" onClick={() => setModal({ mode: 'delete', building: b })}>
                      Delete
                    </Button>
                  )}
                </SpaceBetween>
              </div>
            ),
            width: 160,
          },
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            {siteId ? 'No buildings at this site yet.' : 'No buildings yet.'}
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

      {(modal?.mode === 'create' || modal?.mode === 'edit') && (
        <BuildingFormModal
          building={modal.mode === 'edit' ? modal.building : null}
          sites={sites.result.data ?? []}
          defaultSiteId={siteId}
          onDismiss={() => setModal(null)}
          onSaved={async (id) => {
            setModal(null);
            await refresh();
            if (id) nav(`/buildings/${id}`);
          }}
        />
      )}
      {modal?.mode === 'delete' && (
        <DeleteBuildingModal
          building={modal.building}
          onDismiss={() => setModal(null)}
          onDeleted={async () => { setModal(null); await refresh(); }}
        />
      )}
    </ContentLayout>
  );
}

function BuildingFormModal({
  building, sites, defaultSiteId, onDismiss, onSaved,
}: Readonly<{
  building: Building | null;
  sites: Site[];
  defaultSiteId: string;
  onDismiss: () => void;
  onSaved: (newId: string | null) => void;
}>) {
  const isEdit = building !== null;
  const initialSite = sites.find((s) => s.id === (building?.site_id ?? defaultSiteId));
  const [siteOpt, setSiteOpt] = useState<SelectProps.Option | null>(
    initialSite ? { value: initialSite.id, label: `${initialSite.code} · ${initialSite.name}` } : null,
  );
  const [name, setName] = useState(building?.name ?? '');
  const [code, setCode] = useState(building?.code ?? '');
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  async function onSubmit() {
    const errs: Record<string, string> = {};
    if (!isEdit && !siteOpt?.value) errs.site = 'Site required';
    if (!name.trim()) errs.name = 'Name required';
    if (!code.trim()) errs.code = 'Code required';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      if (isEdit) {
        await http.patch(`/inventory/buildings/${building.id}`, { name, code });
        toast.success('Building updated');
        onSaved(null);
      } else {
        const r = await http.post('/inventory/buildings', {
          site_id: siteOpt!.value, name, code,
        });
        toast.success('Building created');
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
      header={isEdit ? `Edit ${building.code}` : 'New building'}
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
          <FormField label="Site" errorText={errors.site}>
            <Select
              placeholder="Pick a site…"
              selectedOption={siteOpt}
              onChange={({ detail }) => setSiteOpt(detail.selectedOption)}
              options={sites.map((s) => ({ value: s.id, label: `${s.code} · ${s.name}` }))}
              expandToViewport
            />
          </FormField>
        )}
        <FormField label="Name" errorText={errors.name}>
          <Input value={name} onChange={({ detail }) => setName(detail.value)} />
        </FormField>
        <FormField label="Code" errorText={errors.code}>
          <Input value={code} onChange={({ detail }) => setCode(detail.value)} />
        </FormField>
      </SpaceBetween>
    </Modal>
  );
}

function DeleteBuildingModal({
  building, onDismiss, onDeleted,
}: Readonly<{
  building: Building;
  onDismiss: () => void;
  onDeleted: () => void;
}>) {
  const [submitting, setSubmitting] = useState(false);

  async function onConfirm() {
    setSubmitting(true);
    try {
      await http.delete(`/inventory/buildings/${building.id}`);
      toast.success('Building deleted');
      onDeleted();
    } catch (err: any) {
      // FK 409 from the backend when rooms still exist.
      toast.error(
        err?.response?.status === 409
          ? 'Building still has rooms — delete or move them first.'
          : err?.message ?? 'failed',
      );
      setSubmitting(false);
    }
  }

  return (
    <Modal
      visible
      onDismiss={onDismiss}
      header={`Delete ${building.code}?`}
      footer={
        <Box float="right">
          <SpaceBetween size="xs" direction="horizontal">
            <Button onClick={onDismiss}>Cancel</Button>
            <Button variant="primary" loading={submitting} onClick={onConfirm}>
              Delete
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <Box>
        This removes <b>{building.code} · {building.name}</b>. Buildings with
        rooms can't be deleted until the rooms are removed.
      </Box>
    </Modal>
  );
}
