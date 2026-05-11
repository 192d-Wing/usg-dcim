// Enterprise overview — Cloudscape ContentLayout with KPI tiles and a
// "sites at risk" table. Refetches every 30s.

import { useQuery } from '@tanstack/react-query';
import { useNavigate } from 'react-router';

import Box from '@cloudscape-design/components/box';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Header from '@cloudscape-design/components/header';
import Link from '@cloudscape-design/components/link';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Spinner from '@cloudscape-design/components/spinner';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';

import { http } from '@/lib/http';
import { relativeTime } from '@/lib/utils';

type Overview = {
  sites: { total: number; active: number };
  racks: { total: number };
  alerts: { sites_with_critical: number };
  collectors: { healthy: number; stale: number };
  telemetry: { stale_sources: number };
  generated_at: string;
};

type AtRiskSite = { site_id: string; alert_count: number };

type Tone = 'success' | 'warning' | 'critical';

export function DashboardPage() {
  const navigate = useNavigate();
  const overview = useQuery({
    queryKey: ['enterprise'],
    queryFn: async () => (await http.get<Overview>('/dashboards/enterprise')).data,
    refetchInterval: 30_000,
  });
  const atRisk = useQuery({
    queryKey: ['sites-at-risk'],
    queryFn: async () => (await http.get('/dashboards/sites/at-risk')).data.sites as AtRiskSite[],
    refetchInterval: 30_000,
  });

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description={overview.data ? `Updated ${relativeTime(overview.data.generated_at)}` : 'Loading…'}
        >
          Enterprise overview
        </Header>
      }
    >
      <SpaceBetween size="l">
        <Container>
          <ColumnLayout columns={5} variant="text-grid">
            <Kpi
              title="Sites"
              value={overview.data ? `${overview.data.sites.active} / ${overview.data.sites.total}` : null}
              hint="active / total"
            />
            <Kpi
              title="Racks"
              value={overview.data ? overview.data.racks.total.toLocaleString() : null}
              hint="across enterprise"
            />
            <Kpi
              title="Critical alerts"
              value={overview.data ? overview.data.alerts.sites_with_critical : null}
              hint="sites firing"
              tone={overview.data?.alerts.sites_with_critical ? 'critical' : 'success'}
            />
            <Kpi
              title="Collectors"
              value={overview.data ? `${overview.data.collectors.healthy} / ${overview.data.collectors.healthy + overview.data.collectors.stale}` : null}
              hint="healthy"
              tone={overview.data?.collectors.stale ? 'warning' : 'success'}
            />
            <Kpi
              title="Stale telemetry"
              value={overview.data ? overview.data.telemetry.stale_sources : null}
              hint="sources behind"
              tone={overview.data?.telemetry.stale_sources ? 'warning' : 'success'}
            />
          </ColumnLayout>
        </Container>

        <Table<AtRiskSite>
          variant="container"
          loading={atRisk.isLoading}
          loadingText="Loading at-risk sites…"
          items={atRisk.data ?? []}
          trackBy="site_id"
          header={
            <Header
              variant="h2"
              description="Sites with major or worse alerts firing"
            >
              Sites at risk
            </Header>
          }
          columnDefinitions={[
            {
              id: 'site',
              header: 'Site',
              cell: (s) => (
                <Link
                  href={`/sites/${s.site_id}`}
                  onFollow={(e) => { e.preventDefault(); navigate(`/sites/${s.site_id}`); }}
                >
                  <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>
                    {s.site_id.slice(0, 8)}…
                  </span>
                </Link>
              ),
            },
            {
              id: 'alerts',
              header: 'Open alerts',
              cell: (s) => <StatusIndicator type="error">{s.alert_count}</StatusIndicator>,
              width: 140,
            },
          ]}
          empty={
            <Box textAlign="center" color="inherit" padding="m">
              No sites currently at risk.
            </Box>
          }
        />
      </SpaceBetween>
    </ContentLayout>
  );
}

function Kpi({
  title, value, hint, tone,
}: Readonly<{
  title: string;
  value: string | number | null;
  hint?: string;
  tone?: Tone;
}>) {
  const colorByTone = {
    success: 'text-status-success',
    warning: 'text-status-warning',
    critical: 'text-status-error',
  } as const;
  const valueColor = tone ? (colorByTone[tone] as any) : 'inherit';
  return (
    <Box>
      <Box variant="awsui-key-label">{title}</Box>
      {value === null
        ? <Spinner />
        : <Box variant="h2" color={valueColor}>{value}</Box>}
      {hint && <Box color="text-status-inactive" fontSize="body-s">{hint}</Box>}
    </Box>
  );
}
