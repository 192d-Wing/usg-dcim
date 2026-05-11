// Alerts list — Cloudscape Table with state filter (firing /
// acknowledged / resolved / suppressed) and per-row Ack action when
// the operator has alerts:ack capability.

import { useState } from 'react';
import { useTable, useGetIdentity } from '@refinedev/core';
import { useNavigate } from 'react-router';
import { toast } from 'sonner';

import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Header from '@cloudscape-design/components/header';
import Pagination from '@cloudscape-design/components/pagination';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator, {
  StatusIndicatorProps,
} from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';

import { http } from '@/lib/http';
import { formatDate } from '@/lib/utils';

type Alert = {
  id: string; site_id: string; severity: string; state: string;
  summary: string; first_seen_at: string;
};

const STATE_OPTIONS: SelectProps.Option[] = [
  { value: 'firing', label: 'Firing' },
  { value: 'acknowledged', label: 'Acknowledged' },
  { value: 'resolved', label: 'Resolved' },
  { value: 'suppressed', label: 'Suppressed' },
];

/** Map alert severity to Cloudscape's status palette. critical/major →
 *  error (red), minor/warning → warning (orange), info → info (blue),
 *  anything else (cleared, unknown) → success. */
function sevType(s: string): StatusIndicatorProps.Type {
  if (s === 'critical' || s === 'major') return 'error';
  if (s === 'minor' || s === 'warning') return 'warning';
  if (s === 'info') return 'info';
  return 'success';
}

export function AlertsPage() {
  const navigate = useNavigate();
  const [stateOpt, setStateOpt] = useState<SelectProps.Option>(STATE_OPTIONS[0]);
  const { tableQuery, result, currentPage, pageCount, setCurrentPage } = useTable<Alert>({
    resource: 'alerts',
    pagination: { pageSize: 50 },
    filters: { permanent: [{ field: 'state', operator: 'eq', value: stateOpt.value! }] },
  });
  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const canAck = identity?.capabilities.includes('alerts:ack');
  const data = result.data ?? [];
  const total = result.total ?? 0;

  async function ack(id: string) {
    try {
      await http.post(`/alerts/${id}/ack`, { note: null });
      toast.success('Alert acknowledged');
      tableQuery.refetch();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          counter={`(${total})`}
          actions={
            <SpaceBetween size="xs" direction="horizontal">
              <Button onClick={() => navigate('/alerts/rules')} iconName="settings">
                Manage rules
              </Button>
              <Select
                selectedOption={stateOpt}
                onChange={({ detail }) => { setStateOpt(detail.selectedOption); setCurrentPage(1); }}
                options={STATE_OPTIONS}
                expandToViewport
              />
            </SpaceBetween>
          }
          description="Open and historical alerts. Rules + suppression managed under Manage rules."
        >
          Alerts
        </Header>
      }
    >
      <Table<Alert>
        variant="container"
        loading={tableQuery.isLoading}
        loadingText="Loading alerts…"
        items={data}
        trackBy="id"
        columnDefinitions={[
          {
            id: 'severity',
            header: 'Severity',
            cell: (a) => <StatusIndicator type={sevType(a.severity)}>{a.severity}</StatusIndicator>,
            width: 120,
          },
          {
            id: 'site',
            header: 'Site',
            cell: (a) => (
              <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>
                {a.site_id.slice(0, 8)}…
              </span>
            ),
            width: 140,
          },
          {
            id: 'summary',
            header: 'Summary',
            cell: (a) => a.summary,
          },
          {
            id: 'first_seen',
            header: 'First seen',
            cell: (a) => (
              <Box variant="span" color="text-status-inactive" fontSize="body-s">
                {formatDate(a.first_seen_at)}
              </Box>
            ),
            width: 200,
          },
          {
            id: 'actions',
            header: '',
            cell: (a) => stateOpt.value === 'firing' && canAck ? (
              <Button onClick={() => ack(a.id)} iconName="status-positive">
                Ack
              </Button>
            ) : null,
            width: 100,
          },
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            All clear.
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
