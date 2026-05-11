// Enterprise overview — Cloudscape ContentLayout with KPI tiles and a
// "sites at risk" table. Refetches every 30s.

import { useMemo } from 'react';
import { useList } from '@refinedev/core';
import { useQuery } from '@tanstack/react-query';
import { useNavigate } from 'react-router';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Cards from '@cloudscape-design/components/cards';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Header from '@cloudscape-design/components/header';
import Link from '@cloudscape-design/components/link';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Spinner from '@cloudscape-design/components/spinner';

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
  // Look up site name + code so the table doesn't show raw UUID prefixes.
  // Same lookup pattern as audit.tsx.
  const sitesRes = useList<{ id: string; code: string; name: string }>({
    resource: 'inventory/sites', pagination: { pageSize: 500 },
  });
  const sitesById = useMemo(
    () => new Map((sitesRes.result.data ?? []).map((s) => [s.id, s])),
    [sitesRes.result.data],
  );
  const atRiskRows = atRisk.data ?? [];

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

        {/* Cards (not Table) — a 2-field at-a-glance list looked
            stretched in a wide Table because the columns spread to the
            container's edges with empty middle. Cards renders one tile
            per site and degrades cleanly when the list is empty. */}
        <Cards<AtRiskSite>
          loading={atRisk.isLoading}
          loadingText="Loading at-risk sites…"
          items={atRiskRows}
          trackBy="site_id"
          cardsPerRow={[
            { cards: 1 },
            { minWidth: 500, cards: 2 },
            { minWidth: 900, cards: 3 },
          ]}
          header={
            <Header
              counter={`(${atRiskRows.length})`}
              description="Sites with major or worse alerts firing"
            >
              Sites at risk
            </Header>
          }
          cardDefinition={{
            header: (s) => {
              const site = sitesById.get(s.site_id);
              return (
                <Link
                  href={`/sites/${s.site_id}`}
                  onFollow={(e) => { e.preventDefault(); navigate(`/sites/${s.site_id}`); }}
                >
                  {site
                    ? `${site.code} · ${site.name}`
                    : `${s.site_id.slice(0, 8)}…`}
                </Link>
              );
            },
            sections: [
              {
                id: 'alerts',
                header: 'Open alerts',
                content: (s) => <Badge color="red">{String(s.alert_count)}</Badge>,
              },
            ],
          }}
          empty={
            <Box textAlign="center" color="inherit" padding="l">
              <SpaceBetween size="xs">
                <b>No sites currently at risk</b>
                <Box variant="p" color="inherit">All clear — nothing major or worse is firing.</Box>
              </SpaceBetween>
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
