// DNS Overview — one-page service dashboard. Cloudscape ContentLayout
// with KPI tiles, a global QPS/latency timeline, per-site rollup table,
// server health table, and a placeholder for the top-queried-names tile
// that lands once the collector grows per-name counters.

import { useMemo, useState } from 'react';
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
import SegmentedControl from '@cloudscape-design/components/segmented-control';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Spinner from '@cloudscape-design/components/spinner';
import StatusIndicator, {
  StatusIndicatorProps,
} from '@cloudscape-design/components/status-indicator';
import CopyToClipboard from '@cloudscape-design/components/copy-to-clipboard';
import Table, { TableProps } from '@cloudscape-design/components/table';

import { http } from '@/lib/http';
import { relativeTime } from '@/lib/utils';
import { useFabricScope } from '@/contexts/fabric-scope';

// Window options for the dashboard's time-range selector. Values are
// the `minutes` query-string the backend accepts; labels are what
// operators see on the segmented control.
const WINDOW_OPTIONS = [
  { id: '60',   text: '1h'  },
  { id: '360',  text: '6h'  },
  { id: '1440', text: '24h' },
] as const;
type WindowId = typeof WINDOW_OPTIONS[number]['id'];

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

type DashTopName = {
  name: string;
  type: string;
  count: number;
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

type DashStorage = {
  sample_count: number;
  samples_with_top_names: number;
  top_names_bytes_avg: number | null;
  top_names_bytes_total: number;
};

type Dashboard = {
  generated_at: string;
  window_minutes: number;
  overall: DashGlobal;
  series: DashSeriesPoint[];
  by_site: DashSitePanel[];
  server_health: DashServerHealth[];
  // Null when no server in the deployment has dnstap wired (the
  // collector is the only thing that ships this). Empty list means
  // dnstap is wired but the window saw zero queries.
  top_names: DashTopName[] | null;
  storage: DashStorage;
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

// Cell renderers extracted from the top-names Table so they don't
// re-define a component on every row render. Each gets the row item
// as a positional argument — Cloudscape's column API hands the cell
// function the whole row.
function TopNameCell(item: Readonly<DashTopName>) {
  // The CopyToClipboard control gives operators a one-click way to
  // pivot from the dashboard into their existing DNS tooling (`dig`,
  // record search, log greps) without retyping the name. Keeping the
  // monospaced label inline alongside so the row still reads as a
  // single thing visually.
  return (
    <span style={{
      display: 'inline-flex', alignItems: 'center', gap: 4,
      fontFamily: 'ui-monospace, monospace',
    }}>
      {item.name}
      <CopyToClipboard
        variant="icon"
        copyButtonAriaLabel={`Copy ${item.name}`}
        copyErrorText="Copy failed"
        copySuccessText="Copied"
        textToCopy={item.name.replace(/\.$/, '')}
      />
    </span>
  );
}

function TopTypeCell(item: Readonly<DashTopName>) {
  const color = item.type === 'A' || item.type === 'AAAA' ? 'blue' : 'grey';
  return <Badge color={color}>{item.type}</Badge>;
}

function TopCountCell(item: Readonly<DashTopName>) {
  return item.count.toLocaleString();
}

type TopNamesPanelProps = Readonly<{
  topNames: DashTopName[] | null | undefined;
  windowMinutes: number;
  isLoading: boolean;
}>;

const _TOP_NAME_COLUMNS: TableProps.ColumnDefinition<DashTopName>[] = [
  {
    id: 'name', header: 'Name', cell: TopNameCell,
    isRowHeader: true, sortingField: 'name',
  },
  { id: 'type', header: 'Type', cell: TopTypeCell, sortingField: 'type' },
  {
    id: 'count', header: 'Count', cell: TopCountCell,
    sortingField: 'count',
  },
];


function TopNamesPanel({
  topNames,
  windowMinutes,
  isLoading,
}: TopNamesPanelProps) {
  // Default sort: count descending — same order central already ships
  // but explicit so the column-header click toggles work intuitively.
  const [sorting, setSorting] = useState<{
    sortingColumn: TableProps.SortingColumn<DashTopName>;
    sortingDescending: boolean;
  }>({
    sortingColumn: _TOP_NAME_COLUMNS[2],
    sortingDescending: true,
  });
  const sortedItems = useMemo(() => {
    const field = sorting.sortingColumn.sortingField as keyof DashTopName | undefined;
    if (!field || !topNames || topNames.length === 0) return topNames ?? [];
    const copy = [...topNames];
    copy.sort((a, b) => {
      const av = a[field];
      const bv = b[field];
      if (av === bv) return 0;
      const less = av < bv ? -1 : 1;
      return sorting.sortingDescending ? -less : less;
    });
    return copy;
  }, [topNames, sorting]);
  // Null `top_names` means no server in the deployment is shipping a
  // dnstap reservoir yet — render a hint card instead of an empty
  // table so operators know the gap is "not wired" vs "no traffic".
  if (topNames === null) {
    return (
      <Container
        header={
          <Header
            variant="h2"
            description="dnstap not wired on any DnsServer"
          >
            Top queried names
          </Header>
        }
      >
        <Box color="text-status-inactive" padding="l" textAlign="center">
          <SpaceBetween size="s">
            <Box variant="strong">No per-name data yet</Box>
            <Box>
              The collector ships a top-K reservoir of query names
              when a server has{' '}
              <code>dnstap_socket</code>
              {' '}set. Hickory recursives can&apos;t supply this (no
              dnstap support upstream); CoreDNS auth pods do once{' '}
              <code>DCIM_DNS_DNSTAP_ENABLED</code>
              {' '}is true.
            </Box>
          </SpaceBetween>
        </Box>
      </Container>
    );
  }
  return (
    <Table
      header={
        <Header
          variant="h2"
          counter={`(${topNames?.length ?? 0})`}
          description={`Last ${windowMinutes}-minute window`}
        >
          Top queried names
        </Header>
      }
      variant="container"
      items={sortedItems}
      loading={isLoading}
      loadingText="Loading…"
      empty={
        <Box color="text-status-inactive" padding="m">
          No queries observed in the window.
        </Box>
      }
      columnDefinitions={_TOP_NAME_COLUMNS}
      sortingColumn={sorting.sortingColumn}
      sortingDescending={sorting.sortingDescending}
      onSortingChange={({ detail }) => setSorting({
        sortingColumn: detail.sortingColumn,
        sortingDescending: detail.isDescending ?? false,
      })}
    />
  );
}

function GlobalKpiStrip({ overall }: Readonly<{ overall: DashGlobal | undefined }>) {
  return (
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
  );
}

export function DnsDashboardPage() {
  const navigate = useNavigate();
  const { fabricId, fabrics } = useFabricScope();
  const [windowId, setWindowId] = useState<WindowId>('60');
  const minutes = Number(windowId);

  // Dashboard endpoint accepts `fabric_id` to scope every aggregate
  // to one fabric — server-health, by-site, the bucketed series, and
  // global KPIs all narrow consistently. When the top-nav fabric
  // picker hasn't resolved yet (null), we skip the query string and
  // get the global view.
  const fabricQS = fabricId ? `&fabric_id=${fabricId}` : '';
  const { data, isLoading } = useQuery({
    queryKey: ['dns-dashboard', minutes, fabricId],
    queryFn: async () => (
      (await http.get<Dashboard>(
        `/dns/dashboard?minutes=${minutes}${fabricQS}`,
      )).data
    ),
    refetchInterval: 30_000,
    staleTime: 25_000,
  });

  const activeFabric = fabrics.find((f) => f.id === fabricId);

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

  const scopeLabel = activeFabric
    ? `fabric: ${activeFabric.name}`
    : 'all fabrics';
  const timelineLabel = WINDOW_OPTIONS.find((o) => o.id === windowId)?.text ?? `${minutes}m`;
  // Storage footer note. Shows the rough disk envelope of the
  // metrics-samples table so operators can decide whether to tighten
  // `dns_metrics_retention_days` before the cron sweeps. Suppressed
  // until at least one row has a top_names payload — until then the
  // avg is null and the total is 0, which isn't a useful signal.
  const storageNote = (() => {
    const s = data?.storage;
    if (!s?.samples_with_top_names) return null;
    const mb = (s.top_names_bytes_total / 1_048_576).toFixed(1);
    return `top_names: ${s.samples_with_top_names.toLocaleString()} rows · ` +
      `${s.top_names_bytes_avg ?? 0}B avg · ${mb}MB total`;
  })();
  let headerDescription = 'Loading…';
  if (data) {
    const base = `Window: last ${data.window_minutes} min · ${scopeLabel} · updated ${relativeTime(data.generated_at)}`;
    headerDescription = storageNote ? `${base} · ${storageNote}` : base;
  }

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description={headerDescription}
          actions={
            <SegmentedControl
              selectedId={windowId}
              onChange={({ detail }) => setWindowId(detail.selectedId as WindowId)}
              options={WINDOW_OPTIONS.map((o) => ({ id: o.id, text: o.text }))}
              label="Time window"
            />
          }
        >
          DNS overview
        </Header>
      }
    >
      <SpaceBetween size="l">
        {/* ---- KPI strip ---- */}
        <GlobalKpiStrip overall={overall} />

        {/* ---- Timeline ---- */}
        <Container header={<Header variant="h2">{`QPS · last ${timelineLabel}`}</Header>}>
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

          <TopNamesPanel
            topNames={data?.top_names}
            windowMinutes={data?.window_minutes ?? 60}
            isLoading={isLoading}
          />
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
