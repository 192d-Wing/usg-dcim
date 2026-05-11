// Sites list — Cloudscape Table.
// Read-only list; no create/edit UI yet (the backend supports POST but
// the operator workflow uses CSV import for bulk site bring-up).

import { useState } from 'react';
import { useTable } from '@refinedev/core';
import { useNavigate } from 'react-router';

import Box from '@cloudscape-design/components/box';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Header from '@cloudscape-design/components/header';
import Link from '@cloudscape-design/components/link';
import Pagination from '@cloudscape-design/components/pagination';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';

type Site = {
  id: string;
  name: string;
  code: string;
  region_id: string;
  majcom?: string | null;
  organization?: string | null;
  enclave?: string | null;
  lifecycle_state: string;
};

export function SitesListPage() {
  const navigate = useNavigate();
  const { tableQuery, result, currentPage, pageCount, setCurrentPage } = useTable<Site>({
    resource: 'inventory/sites',
    pagination: { pageSize: 50 },
    sorters: { initial: [{ field: 'code', order: 'asc' }] },
  });
  const [selected, setSelected] = useState<Site[]>([]);
  const data = result.data ?? [];
  const total = result.total ?? 0;

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          counter={`(${total})`}
          description={`Page ${currentPage} of ${Math.max(pageCount, 1)}`}
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
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            No sites yet.
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
    </ContentLayout>
  );
}
