// Asset detail — Cloudscape ContentLayout with field grid, telemetry
// sources Table, recharts series cards, IP allocations, recent alerts.

import { useState } from 'react';
import { useParams, useNavigate } from 'react-router';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  LineChart, Line, ResponsiveContainer, XAxis, YAxis, Tooltip, CartesianGrid,
} from 'recharts';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Header from '@cloudscape-design/components/header';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Spinner from '@cloudscape-design/components/spinner';
import StatusIndicator, { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';

import { http } from '@/lib/http';
import { hasCapability } from '@/lib/access-control-provider';
import { AssetCablesPanel } from '@/components/asset-cables-panel';
import { DecommissionDialog } from '@/components/decommission-dialog';
import { formatDate } from '@/lib/utils';

type AssetDetail = {
  asset: {
    id: string; site_id: string; rack_id: string | null; name: string; hostname: string | null;
    kind: string; manufacturer: string | null; model: string | null; serial: string | null;
    firmware: string | null; mgmt_ip: string | null; mgmt_protocol: string | null; mgmt_port: number | null;
    rack_position_u: number | null; rack_units: number | null;
    port_count: number | null; lifecycle_state: string;
  };
  telemetry_sources: {
    metric: string; unit: string | null; source_system: string | null;
    freshness: string; last_value: number | null; last_reading_at: string | null;
    last_success_at: string | null; poll_interval_seconds: number;
  }[];
  ip_addresses: {
    id: string; subnet_id: string; address: string;
    role: string; status: string; source: string;
    dns_name: string | null; description: string | null;
    dhcp_lease_expires_at: string | null;
  }[];
  recent_alerts: {
    id: string; severity: string; state: string; summary: string;
    first_seen_at: string; last_seen_at: string;
  }[];
};

function freshnessType(s: string): StatusIndicatorProps.Type {
  if (s === 'current') return 'success';
  if (s === 'estimated') return 'warning';
  if (s === 'stale') return 'error';
  return 'pending';
}
function sevType(s: string): StatusIndicatorProps.Type {
  if (s === 'critical' || s === 'major') return 'error';
  if (s === 'minor' || s === 'warning') return 'warning';
  return 'success';
}

export function AssetShowPage() {
  const { id = '' } = useParams<{ id: string }>();
  const nav = useNavigate();
  const qc = useQueryClient();
  const [decomOpen, setDecomOpen] = useState(false);
  const canWrite = hasCapability('inventory:assets:update');
  const detail = useQuery({
    queryKey: ['asset-detail', id],
    queryFn: async () => (await http.get<AssetDetail>(`/dashboards/assets/${id}`)).data,
    enabled: !!id,
    refetchInterval: 15_000,
  });

  if (detail.isLoading) {
    return (
      <ContentLayout header={<Header variant="h1">Loading…</Header>}>
        <Box textAlign="center" padding="xl"><Spinner size="large" /></Box>
      </ContentLayout>
    );
  }
  if (!detail.data?.asset) {
    return (
      <ContentLayout header={<Header variant="h1">Asset</Header>}>
        <Box color="text-status-error">Failed to load asset.</Box>
      </ContentLayout>
    );
  }

  const a = detail.data.asset;
  const sources = detail.data.telemetry_sources ?? [];
  const alerts = detail.data.recent_alerts ?? [];
  const ips = detail.data.ip_addresses ?? [];
  const canDecommission = canWrite && a.lifecycle_state !== 'decommissioned' && a.lifecycle_state !== 'retired';

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description={[
            a.kind,
            a.hostname ?? 'no hostname',
            a.mgmt_ip,
          ].filter(Boolean).join(' · ')}
          actions={
            <SpaceBetween size="xs" direction="horizontal">
              <Button onClick={() => (a.rack_id ? nav(`/racks/${a.rack_id}`) : nav('/racks'))} iconName="angle-left">
                Back to rack
              </Button>
              {canDecommission && (
                <Button onClick={() => setDecomOpen(true)} iconName="remove">
                  Decommission
                </Button>
              )}
              {a.lifecycle_state === 'active'
                ? <StatusIndicator type="success">{a.lifecycle_state}</StatusIndicator>
                : <StatusIndicator type="warning">{a.lifecycle_state}</StatusIndicator>}
            </SpaceBetween>
          }
        >
          {a.name}
        </Header>
      }
    >
      <SpaceBetween size="l">
        <DecommissionDialog
          asset={{
            id: a.id, name: a.name, kind: a.kind,
            serial: a.serial, lifecycle_state: a.lifecycle_state,
          }}
          open={decomOpen}
          onOpenChange={setDecomOpen}
          onDecommissioned={() => qc.invalidateQueries({ queryKey: ['asset-detail', id] })}
        />

        <Container header={<Header variant="h2">Details</Header>}>
          <ColumnLayout columns={6} variant="text-grid">
            <Field label="Manufacturer" value={a.manufacturer ?? '—'} />
            <Field label="Model" value={a.model ?? '—'} />
            <Field label="Serial" value={a.serial ?? '—'} mono />
            <Field label="Firmware" value={a.firmware ?? '—'} />
            <Field
              label="Mgmt"
              value={`${a.mgmt_protocol ?? '—'} ${a.mgmt_ip ?? ''}${a.mgmt_port ? ':' + a.mgmt_port : ''}`}
            />
            <Field
              label="Rack position"
              value={a.rack_position_u ? `U${a.rack_position_u} (${a.rack_units}U)` : '—'}
            />
          </ColumnLayout>
        </Container>

        <Table
          variant="container"
          header={<Header variant="h2">Telemetry sources</Header>}
          items={sources}
          trackBy="metric"
          columnDefinitions={[
            { id: 'metric', header: 'Metric', cell: (s) => <span style={{ fontWeight: 500 }}>{s.metric}</span> },
            { id: 'value', header: 'Last value', cell: (s) => <span style={{ fontVariantNumeric: 'tabular-nums' }}>{s.last_value ?? '—'}</span> },
            { id: 'unit', header: 'Unit', cell: (s) => s.unit ?? '' },
            {
              id: 'reading', header: 'Last reading',
              cell: (s) => <Box variant="span" color="text-status-inactive" fontSize="body-s">{formatDate(s.last_reading_at)}</Box>,
            },
            { id: 'source', header: 'Source', cell: (s) => s.source_system ?? '—' },
            {
              id: 'freshness', header: 'Freshness',
              cell: (s) => <StatusIndicator type={freshnessType(s.freshness)}>{s.freshness}</StatusIndicator>,
              width: 130,
            },
          ]}
          empty={<Box textAlign="center" color="inherit" padding="m">No telemetry has been ingested for this asset yet.</Box>}
        />

        {sources.slice(0, 4).map((s) => (
          <SeriesChart key={s.metric} siteId={a.site_id} assetId={a.id} metric={s.metric} unit={s.unit} />
        ))}

        <Table
          variant="container"
          header={<Header variant="h2">IP addresses</Header>}
          items={ips}
          trackBy="id"
          columnDefinitions={[
            {
              id: 'address', header: 'Address',
              cell: (ip) => <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{ip.address}</span>,
            },
            { id: 'role', header: 'Role', cell: (ip) => <Badge>{ip.role}</Badge>, width: 110 },
            {
              id: 'source', header: 'Source',
              cell: (ip) => <Badge color={ip.source === 'dhcp' ? 'severity-medium' : 'grey'}>{ip.source}</Badge>,
              width: 110,
            },
            {
              id: 'status', header: 'Status',
              cell: (ip) => ip.status === 'active'
                ? <StatusIndicator type="success">{ip.status}</StatusIndicator>
                : <StatusIndicator type="stopped">{ip.status}</StatusIndicator>,
              width: 120,
            },
            { id: 'dns', header: 'DNS', cell: (ip) => <Box variant="span" color="text-status-inactive">{ip.dns_name ?? '—'}</Box> },
            {
              id: 'lease', header: 'DHCP lease ends',
              cell: (ip) => <Box variant="span" color="text-status-inactive" fontSize="body-s">{ip.dhcp_lease_expires_at ? formatDate(ip.dhcp_lease_expires_at) : '—'}</Box>,
            },
          ]}
          empty={<Box textAlign="center" color="inherit" padding="m">No IP allocations bound to this asset.</Box>}
        />

        <AssetCablesPanel assetId={a.id} portCount={a.port_count} />

        <Table
          variant="container"
          header={<Header variant="h2">Recent alerts</Header>}
          items={alerts}
          trackBy="id"
          columnDefinitions={[
            {
              id: 'severity', header: 'Severity',
              cell: (al) => <StatusIndicator type={sevType(al.severity)}>{al.severity}</StatusIndicator>,
              width: 120,
            },
            { id: 'state', header: 'State', cell: (al) => al.state, width: 120 },
            { id: 'summary', header: 'Summary', cell: (al) => al.summary },
            {
              id: 'first', header: 'First seen',
              cell: (al) => <Box variant="span" color="text-status-inactive" fontSize="body-s">{formatDate(al.first_seen_at)}</Box>,
            },
            {
              id: 'last', header: 'Last seen',
              cell: (al) => <Box variant="span" color="text-status-inactive" fontSize="body-s">{formatDate(al.last_seen_at)}</Box>,
            },
          ]}
          empty={<Box textAlign="center" color="inherit" padding="m">No alerts have fired for this asset.</Box>}
        />
      </SpaceBetween>
    </ContentLayout>
  );
}

function Field({ label, value, mono }: Readonly<{ label: string; value: string; mono?: boolean }>) {
  return (
    <Box>
      <Box variant="awsui-key-label">{label}</Box>
      <Box variant="span">
        {mono
          ? <span style={{ fontFamily: 'ui-monospace, monospace' }}>{value}</span>
          : value}
      </Box>
    </Box>
  );
}

function SeriesChart({ siteId, assetId, metric, unit }: Readonly<{ siteId: string; assetId: string; metric: string; unit: string | null }>) {
  const series = useQuery({
    queryKey: ['series', siteId, assetId, metric],
    queryFn: async () => {
      const end = new Date();
      const start = new Date(end.getTime() - 3_600_000);
      const r = await http.get('/telemetry/series', {
        params: { site_id: siteId, asset_id: assetId, metric, start: start.toISOString(), end: end.toISOString() },
      });
      return (r.data.points ?? []) as { ts: string; value: number }[];
    },
    refetchInterval: 15_000,
  });
  const points = series.data ?? [];
  if (points.length === 0) return null;
  const data = points.map((p) => ({ ts: new Date(p.ts).getTime(), value: p.value }));
  const last = data[data.length - 1];

  return (
    <Container
      header={
        <Header
          variant="h3"
          description={`last: ${last.value}${unit ? ` ${unit}` : ''} · ${data.length} pts (1h)`}
        >
          <span style={{ fontFamily: 'ui-monospace, monospace' }}>{metric}</span>
        </Header>
      }
    >
      <div style={{ height: 160 }}>
        <ResponsiveContainer width="100%" height="100%">
          <LineChart data={data} margin={{ top: 5, right: 16, left: 0, bottom: 0 }}>
            <CartesianGrid stroke="#444" strokeDasharray="3 3" />
            <XAxis
              dataKey="ts" tickFormatter={(v) => new Date(v).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
              fontSize={11} stroke="#888"
            />
            <YAxis fontSize={11} stroke="#888" width={40} />
            <Tooltip
              labelFormatter={(v) => new Date(v as number).toLocaleString()}
              contentStyle={{ background: '#1b1b1b', border: '1px solid #444', borderRadius: 6 }}
            />
            <Line type="monotone" dataKey="value" stroke="#5dade2" strokeWidth={1.5} dot={false} />
          </LineChart>
        </ResponsiveContainer>
      </div>
    </Container>
  );
}
