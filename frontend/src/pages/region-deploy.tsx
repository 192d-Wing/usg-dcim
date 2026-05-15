// Region Deploy — list view skeleton (PR 2).
//
// Read-only listing of bare-metal region deployments per site. Wizard
// (PR 13) and detail view (PR 14) live in separate files and replace
// the placeholder rows below.

import { useTable } from '@refinedev/core';

import Box from '@cloudscape-design/components/box';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Header from '@cloudscape-design/components/header';
import StatusIndicator, {
  StatusIndicatorProps,
} from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';

import { formatDate } from '@/lib/utils';

type RegionDeploymentStatus =
  | 'pending'
  | 'preflight'
  | 'provisioning'
  | 'joining'
  | 'cni'
  | 'apps'
  | 'verify'
  | 'ready'
  | 'failed'
  | 'aborted';

type RegionDeploymentSummary = {
  id: string;
  site_id: string;
  name: string;
  status: RegionDeploymentStatus;
  current_stage: string | null;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
};

// Map deploy status → Cloudscape StatusIndicator type. Terminal states
// (ready/failed/aborted) get explicit colors; everything else is
// "in-progress" so the table stays visually quiet until something
// actually goes wrong.
function statusType(s: RegionDeploymentStatus): StatusIndicatorProps.Type {
  if (s === 'ready') return 'success';
  if (s === 'failed') return 'error';
  if (s === 'aborted') return 'stopped';
  if (s === 'pending') return 'pending';
  return 'in-progress';
}

export function RegionDeployPage() {
  const { tableQuery, result } = useTable<RegionDeploymentSummary>({
    resource: 'region-deployments',
    pagination: { pageSize: 50 },
  });
  const rows = result?.data ?? [];

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description="Bare-metal Kubernetes cluster bring-up per site. See docs/dev/region-deploy.md."
        >
          Region deployments
        </Header>
      }
    >
      <Table
        loading={tableQuery?.isLoading}
        items={rows}
        trackBy="id"
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (r) => r.name },
          {
            id: 'status',
            header: 'Status',
            cell: (r) => (
              <StatusIndicator type={statusType(r.status)}>
                {r.status}
              </StatusIndicator>
            ),
          },
          {
            id: 'stage',
            header: 'Current stage',
            cell: (r) => r.current_stage ?? '—',
          },
          {
            id: 'created',
            header: 'Created',
            cell: (r) => formatDate(r.created_at),
          },
          {
            id: 'finished',
            header: 'Finished',
            cell: (r) => (r.finished_at ? formatDate(r.finished_at) : '—'),
          },
        ]}
        empty={
          <Box textAlign="center" color="inherit">
            <b>No region deployments yet.</b>
            <Box variant="p" color="inherit">
              The deploy wizard lands in a follow-up PR.
            </Box>
          </Box>
        }
      />
    </ContentLayout>
  );
}
