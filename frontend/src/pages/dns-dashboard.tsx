// DNS Overview — one-page service dashboard. Cloudscape ContentLayout
// with KPI tiles, a global QPS/latency timeline, per-site rollup table,
// server health table, and a placeholder for the top-queried-names tile
// that lands once the collector grows per-name counters.

import { useMemo } from 'react';
import { useNavigate } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import {
  LineChart, Line, ResponsiveContainer, XAxis, YAxis, Tooltip, CartesianGrid,
} from 'recharts';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Header from '@cloudscape-design/components/header';
import Link from '@cloudscape-design/components/link';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Spinner from '@cloudscape-design/components/spinner';
import StatusIndicator, {
  StatusIndicatorProps,
} from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';

import { http } from '@/lib/http';
import { relativeTime } from '@/lib/utils';

type DashGlobal = {
  qps_now: number;
  qps_avg: number;
  queries_total: number;
  nxdomain_pct: number;
  servfail_pct: number;
  p50_ms: number | null;
  p95_ms: number | null;
  sites_active: number;
  servers_total: number;
  zones_total: number;
  zones_signed: number;
  zones_nsec3: number;
  anycast_groups: number;
  engines: { coredns: number; hickory: number };
};

type DashSeriesPoint = {
  observed_at: string;
  qps: number;
  nxdomain_pct: number;
  servfail_pct: number;
  p50_ms: number | null;
  p95_ms: number | null;
};

type DashSitePanel = {
  site_id: string;
  site_name: string;
  qps_now: number;
  queries_total: number;
  nxdomain_pct: number;
  servfail_pct: number;
  p95_ms: number | null;
  server_count: number;
};

type DashServerHealth = {
  server_id: string;
  name: string;
  role: 'auth' | 'recursive';
  engine: 'coredns' | 'hickory';
  site_id: string | null;
  site_name: string | null;
  last_render_status: string | null;
  last_render_at: string | null;
  last_render_etag: string | null;
  qps_now: number | null;
};

type Dashboard = {
  generated_at: string;
  window_minutes: number;
  overall: DashGlobal;
  series: DashSeriesPoint[];
  by_site: DashSitePanel[];
  server_health: DashServerHealth[];
  top_names: unknown | null;
};

// Render-status → Cloudscape status chip. Matches the chip pattern on
// the existing Servers tab so operators get consistent visual cues
// across the DNS area.
function renderStatusChip(
  status: string | null,
): StatusIndicatorProps.Type | undefined {
  if (status === 'ok') return 'success';
  if (status === 'error') return 'error';
  if (status === 'rendering') return 'in-progress';
  return undefined;
}

function Kpi({
  title, value, hint,
}: {
  title: string;
  value: string | null;
  hint?: string;
}) {
  return (
    <div>
      <Box variant="awsui-key-label">{title}</Box>
      <Box variant="awsui-value-large">
        {value ?? <Spinner size="normal" />}
      </Box>
      {hint && <Box color="text-status-inactive" fontSize="body-s">{hint}</Box>}
    </div>
  );
}

export function DnsDashboardPage() {
  const navigate = useNavigate();

  const { data, isLoading } = useQuery({
    queryKey: ['dns-dashboard', 60],
    queryFn: async () => (
      (await http.get<Dashboard>('/dns/dashboard?minutes=60')).data
    ),
    refetchInterval: 30_000,
    staleTime: 25_000,
  });

  const overall = data?.overall;
  // Recharts wants Date objects (or numeric ms) — Cloudscape ships
  // strings. Convert once into a memoized array rather than per-tick.
  const series = useMemo(() => (
    (data?.series ?? []).map((p) => ({
      ts: new Date(p.observed_at).getTime(),
      qps: p.qps,
      p95: p.p95_ms ?? 0,
      nxdomain_pct: p.nxdomain_pct,
    }))
  ), [data?.series]);

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description={
            data
              ? `Window: last ${data.window_minutes} min · updated ${relativeTime(data.generated_at)}`
              : 'Loading…'
          }
        >
          DNS overview
        </Header>
      }
    >
      <SpaceBetween size="l">
        {/* ---- KPI strip ---- */}
        <Container header={<Header variant="h2">Global</Header>}>
          <ColumnLayout columns={5} variant="text-grid">
            <Kpi
              title="QPS now"
              value={overall ? overall.qps_now.toLocaleString() : null}
              hint={
                overall
                  ? `${overall.qps_avg.toLocaleString()} avg / ${overall.queries_total.toLocaleString()} queries in window`
                  : undefined
              }
            />
            <Kpi
              title="NXDOMAIN"
              value={overall ? `${overall.nxdomain_pct.toFixed(2)}%` : null}
              hint={
                overall ? `SERVFAIL ${overall.servfail_pct.toFixed(2)}%` : undefined
              }
            />
            <Kpi
              title="Latency p95"
              value={overall && overall.p95_ms !== null ? `${overall.p95_ms.toFixed(2)} ms` : '—'}
              hint={
                overall && overall.p50_ms !== null
                  ? `p50 ${overall.p50_ms.toFixed(2)} ms`
                  : 'no samples'
              }
            />
            <Kpi
              title="Zones"
              value={overall ? overall.zones_total.toLocaleString() : null}
              hint={
                overall
                  ? `${overall.zones_signed} signed · ${overall.zones_nsec3} NSEC3`
                  : undefined
              }
            />
            <Kpi
              title="Servers"
              value={overall ? overall.servers_total.toLocaleString() : null}
              hint={
                overall
                  ? `Recursive: ${overall.engines.coredns} CoreDNS / ${overall.engines.hickory} Hickory · ${overall.anycast_groups} anycast`
                  : undefined
              }
            />
          </ColumnLayout>
        </Container>

        {/* ---- Timeline ---- */}
        <Container header={<Header variant="h2">QPS · last 60 minutes</Header>}>
          <div style={{ height: 240 }}>
            {isLoading || series.length === 0 ? (
              <Box color="text-status-inactive" padding="m" textAlign="center">
                {isLoading ? <Spinner /> : 'No samples in window'}
              </Box>
            ) : (
              <ResponsiveContainer width="100%" height="100%">
                <LineChart
                  data={series}
                  margin={{ top: 8, right: 16, left: 0, bottom: 0 }}
                >
                  <CartesianGrid stroke="#444" strokeDasharray="3 3" />
                  <XAxis
                    dataKey="ts"
                    type="number"
                    domain={['dataMin', 'dataMax']}
                    tickFormatter={(v) => new Date(v).toLocaleTimeString([], {
                      hour: '2-digit', minute: '2-digit',
                    })}
                    fontSize={11}
                    stroke="#888"
                  />
                  <YAxis yAxisId="qps" fontSize={11} stroke="#888" width={50} />
                  <YAxis
                    yAxisId="lat"
                    orientation="right"
                    fontSize={11}
                    stroke="#888"
                    width={50}
                    unit=" ms"
                  />
                  <Tooltip
                    labelFormatter={(v) => new Date(v as number).toLocaleString()}
                    contentStyle={{
                      background: '#1b1b1b',
                      border: '1px solid #444',
                      borderRadius: 6,
                    }}
                  />
                  <Line
                    yAxisId="qps"
                    name="QPS"
                    type="monotone"
                    dataKey="qps"
                    stroke="#5dade2"
                    strokeWidth={1.5}
                    dot={false}
                  />
                  <Line
                    yAxisId="lat"
                    name="p95 ms"
                    type="monotone"
                    dataKey="p95"
                    stroke="#f5b041"
                    strokeWidth={1.5}
                    dot={false}
                  />
                </LineChart>
              </ResponsiveContainer>
            )}
          </div>
        </Container>

        {/* ---- Per-site + Top names ---- */}
        <ColumnLayout columns={2}>
          <Table
            header={<Header variant="h2">By site</Header>}
            variant="container"
            items={data?.by_site ?? []}
            loading={isLoading}
            loadingText="Loading…"
            empty={
              <Box color="text-status-inactive" padding="m">
                No DNS servers registered to any site yet.
              </Box>
            }
            columnDefinitions={[
              {
                id: 'site',
                header: 'Site',
                cell: (item: DashSitePanel) => (
                  <Link onFollow={() => navigate(`/sites/${item.site_id}`)}>
                    {item.site_name}
                  </Link>
                ),
                sortingField: 'site_name',
                isRowHeader: true,
              },
              {
                id: 'qps_now',
                header: 'QPS',
                cell: (item: DashSitePanel) => item.qps_now.toLocaleString(),
                sortingField: 'qps_now',
              },
              {
                id: 'nxdomain',
                header: 'NXDOMAIN%',
                cell: (item: DashSitePanel) => `${item.nxdomain_pct.toFixed(2)}%`,
              },
              {
                id: 'servfail',
                header: 'SERVFAIL%',
                cell: (item: DashSitePanel) => `${item.servfail_pct.toFixed(2)}%`,
              },
              {
                id: 'p95',
                header: 'p95',
                cell: (item: DashSitePanel) => (
                  item.p95_ms !== null ? `${item.p95_ms.toFixed(2)} ms` : '—'
                ),
              },
              {
                id: 'servers',
                header: 'Servers',
                cell: (item: DashSitePanel) => item.server_count,
              },
            ]}
          />

          {/* Top-N queried names — placeholder until the collector
              grows per-name instrumentation. Showing the rationale
              inline so operators know what to expect without diffing
              against the dashboard plan. */}
          <Container
            header={
              <Header
                variant="h2"
                description="Per-name counters land in a follow-up commit"
              >
                Top queried names
              </Header>
            }
          >
            <Box color="text-status-inactive" padding="l" textAlign="center">
              <SpaceBetween size="s">
                <Box variant="strong">Coming soon</Box>
                <Box>
                  CoreDNS and Hickory both expose per-name counters in
                  their Prometheus output. The collector currently
                  aggregates by rcode only — extending it to keep a
                  top-K reservoir per server is the next change.
                </Box>
              </SpaceBetween>
            </Box>
          </Container>
        </ColumnLayout>

        {/* ---- Server health ---- */}
        <Table
          header={
            <Header
              variant="h2"
              counter={`(${data?.server_health.length ?? 0})`}
              description="One row per registered DnsServer"
            >
              Server health
            </Header>
          }
          variant="container"
          items={data?.server_health ?? []}
          loading={isLoading}
          loadingText="Loading server health…"
          empty={
            <Box color="text-status-inactive" padding="m">
              No DnsServer rows registered. Visit IPAM → DNS → Servers
              to add one.
            </Box>
          }
          columnDefinitions={[
            {
              id: 'name',
              header: 'Name',
              cell: (item: DashServerHealth) => (
                <Link onFollow={() => navigate('/ipam?tab=dns&sub=servers')}>
                  {item.name}
                </Link>
              ),
              isRowHeader: true,
            },
            {
              id: 'role',
              header: 'Role',
              cell: (item: DashServerHealth) => (
                <Badge color={item.role === 'auth' ? 'blue' : 'green'}>
                  {item.role}
                </Badge>
              ),
            },
            {
              id: 'engine',
              header: 'Engine',
              cell: (item: DashServerHealth) => item.engine,
            },
            {
              id: 'site',
              header: 'Site',
              cell: (item: DashServerHealth) => item.site_name ?? '—',
            },
            {
              id: 'qps',
              header: 'QPS',
              cell: (item: DashServerHealth) => (
                item.qps_now !== null ? item.qps_now.toLocaleString() : '—'
              ),
            },
            {
              id: 'status',
              header: 'Render status',
              cell: (item: DashServerHealth) => {
                const chip = renderStatusChip(item.last_render_status);
                if (!chip) return '—';
                return (
                  <StatusIndicator type={chip}>
                    {item.last_render_status}
                  </StatusIndicator>
                );
              },
            },
            {
              id: 'last_render',
              header: 'Last render',
              cell: (item: DashServerHealth) => (
                item.last_render_at ? relativeTime(item.last_render_at) : 'never'
              ),
            },
          ]}
        />
      </SpaceBetween>
    </ContentLayout>
  );
}
