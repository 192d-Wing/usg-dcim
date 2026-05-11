// Site detail — Cloudscape ContentLayout + KPI tiles + capacity +
// hierarchy. The hierarchy tree (buildings → rooms → rows → racks)
// keeps its existing Tailwind-styled inner blocks; only the outer
// chrome migrates so the dense layout doesn't lose information.

import { useNavigate, useParams } from 'react-router';
import { useQuery } from '@tanstack/react-query';
import { Server, ChevronRight } from 'lucide-react';

import Box from '@cloudscape-design/components/box';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import ExpandableSection from '@cloudscape-design/components/expandable-section';
import Header from '@cloudscape-design/components/header';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Spinner from '@cloudscape-design/components/spinner';
import StatusIndicator from '@cloudscape-design/components/status-indicator';

import { http } from '@/lib/http';
import { CapacityBar } from '@/components/capacity-bar';

type RackNode = {
  id: string; name: string; code: string;
  u_height: number; u_used: number; u_pct: number;
  kw_max: number | null; kw_current: number | null;
  asset_count: number;
};
type RowNode = { id: string; name: string; code: string; racks: RackNode[] };
type RoomNode = {
  id: string; name: string; code: string;
  design_kw: number | null; rows: RowNode[];
};
type BuildingNode = { id: string; name: string; code: string; rooms: RoomNode[] };

type SiteDetail = {
  site: {
    id: string; name: string; code: string; address: string | null;
    timezone: string | null; region_id: string;
    majcom: string | null; organization: string | null; mission_owner: string | null;
    enclave: string | null; classification: string | null; lifecycle_state: string;
  };
  region: { id: string; name: string; code: string } | null;
  kpis: {
    buildings: number; rooms: number; rows: number; racks: number;
    assets: { total: number } & Record<string, number>;
    alerts_firing: { total: number; critical: number; major: number; minor: number; warning: number; info: number };
    collectors: { total: number; healthy: number; stale: number } & Record<string, number>;
  };
  capacity: {
    u_used: number; u_total: number; u_free: number; u_pct: number;
    kw_max_sum: number | null; kw_current: number | null; kw_pct: number | null;
    racks_total: number; racks_with_kw_rating: number;
  };
  hierarchy: BuildingNode[];
  orphan_racks: RackNode[];
};

export function SiteShowPage() {
  const { id = '' } = useParams<{ id: string }>();
  const nav = useNavigate();
  const detail = useQuery({
    queryKey: ['site-detail', id],
    queryFn: async () => (await http.get<SiteDetail>(`/dashboards/sites/${id}`)).data,
    enabled: !!id,
    refetchInterval: 30_000,
  });

  if (detail.isLoading) {
    return (
      <ContentLayout header={<Header variant="h1">Loading…</Header>}>
        <Box textAlign="center" padding="xl"><Spinner size="large" /></Box>
      </ContentLayout>
    );
  }
  if (detail.isError || !detail.data?.site) {
    return (
      <ContentLayout header={<Header variant="h1">Site</Header>}>
        <Box color="text-status-error">Failed to load site.</Box>
      </ContentLayout>
    );
  }

  const { site, region, kpis, capacity, hierarchy, orphan_racks } = detail.data;
  const alerts = kpis.alerts_firing;
  const collectors = kpis.collectors;
  const power = formatPowerKpi(capacity);
  const alertSummary = formatAlertSummary(alerts);
  const collectorSummary = formatCollectorSummary(collectors);

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description={[
            region?.code,
            site.majcom,
            site.organization,
            site.address ?? 'no address on file',
          ].filter(Boolean).join(' · ')}
          actions={
            <SpaceBetween size="xs" direction="horizontal">
              {site.classification && (
                <StatusIndicator type="warning">{site.classification}</StatusIndicator>
              )}
              {site.enclave && (
                <Box variant="span" color="text-status-info">
                  <StatusIndicator type="info">{site.enclave}</StatusIndicator>
                </Box>
              )}
              {site.mission_owner && (
                <Box variant="span" color="text-status-inactive">{site.mission_owner}</Box>
              )}
              {site.lifecycle_state === 'active'
                ? <StatusIndicator type="success">{site.lifecycle_state}</StatusIndicator>
                : <StatusIndicator type="warning">{site.lifecycle_state}</StatusIndicator>}
            </SpaceBetween>
          }
        >
          {site.code} · {site.name}
        </Header>
      }
    >
      <SpaceBetween size="l">
        <Container>
          <ColumnLayout columns={4} variant="text-grid">
            <KpiTile
              label="Footprint"
              primary={`${kpis.racks} racks`}
              secondary={`${kpis.buildings} bldg · ${kpis.rooms} rm · ${kpis.rows} rows · ${kpis.assets.total} assets`}
            />
            <KpiTile label="Power" primary={power.primary} secondary={power.secondary} />
            <KpiTile
              label="Open alerts"
              primary={alertSummary.primary}
              secondary={alertSummary.secondary}
              tone={alertSummary.tone}
            />
            <KpiTile
              label="Collectors"
              primary={collectorSummary.primary}
              secondary={collectorSummary.secondary}
              tone={collectorSummary.tone}
            />
          </ColumnLayout>
        </Container>

        <Container header={<Header variant="h2">Site capacity</Header>}>
          <ColumnLayout columns={2}>
            <SpaceBetween size="xs">
              <Box variant="awsui-key-label">Rack space</Box>
              <CapacityBar
                used={capacity.u_used}
                total={capacity.u_total}
                leftLabel={`${capacity.u_used} / ${capacity.u_total} U used`}
              />
              <Box color="text-status-inactive" fontSize="body-s">
                {capacity.u_free} U free across {capacity.racks_total} racks
              </Box>
            </SpaceBetween>
            <SpaceBetween size="xs">
              <Box variant="awsui-key-label">Power</Box>
              {capacity.kw_max_sum === null ? (
                <Box color="text-status-inactive" fontSize="body-s">
                  No racks at this site have a kW rating configured.
                </Box>
              ) : (
                <>
                  <CapacityBar
                    used={capacity.kw_current ?? 0}
                    total={capacity.kw_max_sum}
                    unknown={capacity.kw_current === null}
                    leftLabel={
                      capacity.kw_current === null
                        ? `— / ${capacity.kw_max_sum.toFixed(0)} kW`
                        : `${capacity.kw_current.toFixed(2)} / ${capacity.kw_max_sum.toFixed(0)} kW`
                    }
                  />
                  <Box color="text-status-inactive" fontSize="body-s">
                    {capacity.kw_current === null
                      ? 'awaiting current PDU telemetry'
                      : `${(capacity.kw_max_sum - capacity.kw_current).toFixed(2)} kW headroom`}
                  </Box>
                </>
              )}
            </SpaceBetween>
          </ColumnLayout>
        </Container>

        <Container header={<Header variant="h2">Hierarchy</Header>}>
          {hierarchy.length === 0 && orphan_racks.length === 0 && (
            <Box color="text-status-inactive">
              No buildings yet. Create a building → room → row → rack to populate this site.
            </Box>
          )}
          <SpaceBetween size="s">
            {hierarchy.map((b) => (
              <BuildingSection key={b.id} building={b} onRackClick={(rackId) => nav(`/racks/${rackId}`)} />
            ))}
            {orphan_racks.length > 0 && (
              <Container
                header={<Header variant="h3">Unassigned racks</Header>}
              >
                <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
                  {orphan_racks.map((rk) => (
                    <RackTile key={rk.id} rack={rk} onClick={() => nav(`/racks/${rk.id}`)} />
                  ))}
                </div>
              </Container>
            )}
          </SpaceBetween>
        </Container>
      </SpaceBetween>
    </ContentLayout>
  );
}

type Tone = 'ok' | 'warn' | 'danger';

function KpiTile({
  label, primary, secondary, tone = 'ok',
}: Readonly<{
  label: string;
  primary: string;
  secondary: string;
  tone?: Tone;
}>) {
  const colorByTone = {
    ok: 'inherit',
    warn: 'text-status-warning',
    danger: 'text-status-error',
  } as const;
  return (
    <Box>
      <Box variant="awsui-key-label">{label}</Box>
      <Box variant="h3" color={colorByTone[tone] as any}>{primary}</Box>
      <Box color="text-status-inactive" fontSize="body-s">{secondary || '—'}</Box>
    </Box>
  );
}

function formatPowerKpi(c: SiteDetail['capacity']): { primary: string; secondary: string } {
  if (c.kw_max_sum === null) {
    return { primary: 'no kW data', secondary: 'no rack kW ratings configured' };
  }
  const primary = c.kw_current === null
    ? `— / ${c.kw_max_sum.toFixed(0)} kW`
    : `${c.kw_current.toFixed(1)} kW`;
  const secondary = `${c.racks_with_kw_rating}/${c.racks_total} racks rated · ${c.kw_max_sum.toFixed(0)} kW max`;
  return { primary, secondary };
}

function formatAlertSummary(
  a: SiteDetail['kpis']['alerts_firing'],
): { primary: string; secondary: string; tone: Tone } {
  if (a.total === 0) return { primary: 'None', secondary: 'all clear', tone: 'ok' };
  const parts = [
    a.critical && `${a.critical} critical`,
    a.major && `${a.major} major`,
    a.minor && `${a.minor} minor`,
    a.warning && `${a.warning} warning`,
  ].filter(Boolean) as string[];
  let tone: Tone = 'ok';
  if (a.critical > 0 || a.major > 0) tone = 'danger';
  else if (a.minor > 0 || a.warning > 0) tone = 'warn';
  return { primary: a.total.toString(), secondary: parts.join(' · '), tone };
}

function formatCollectorSummary(
  c: SiteDetail['kpis']['collectors'],
): { primary: string; secondary: string; tone: Tone } {
  if (c.total === 0) {
    return {
      primary: 'None enrolled',
      secondary: 'no collector reports for this site',
      tone: 'warn',
    };
  }
  return {
    primary: `${c.healthy ?? 0}/${c.total} healthy`,
    secondary: `${c.stale} stale · ${c.degraded ?? 0} degraded · ${c.unreachable ?? 0} unreachable`,
    tone: c.stale > 0 ? 'warn' : 'ok',
  };
}

function BuildingSection({
  building, onRackClick,
}: Readonly<{
  building: BuildingNode;
  onRackClick: (rackId: string) => void;
}>) {
  const rackCount = building.rooms.reduce(
    (n, rm) => n + rm.rows.reduce((m, rw) => m + rw.racks.length, 0),
    0,
  );
  return (
    <ExpandableSection
      defaultExpanded
      variant="container"
      headerText={`${building.code} · ${building.name}`}
      headerCounter={`(${building.rooms.length} room${building.rooms.length === 1 ? '' : 's'} · ${rackCount} rack${rackCount === 1 ? '' : 's'})`}
    >
      <SpaceBetween size="s">
        {building.rooms.length === 0 && (
          <Box color="text-status-inactive" fontSize="body-s">No rooms in this building.</Box>
        )}
        {building.rooms.map((rm) => (
          <RoomBlock key={rm.id} room={rm} onRackClick={onRackClick} />
        ))}
      </SpaceBetween>
    </ExpandableSection>
  );
}

function RoomBlock({
  room, onRackClick,
}: Readonly<{
  room: RoomNode;
  onRackClick: (rackId: string) => void;
}>) {
  const rackCount = room.rows.reduce((n, rw) => n + rw.racks.length, 0);
  return (
    <div className="space-y-2 rounded-md border bg-card p-3">
      <div className="flex items-baseline justify-between gap-2 text-sm">
        <div className="flex items-baseline gap-2">
          <ChevronRight className="h-3.5 w-3.5 self-center text-muted-foreground" />
          <span className="font-medium">{room.code}</span>
          <span className="text-muted-foreground">{room.name}</span>
        </div>
        <span className="text-xs text-muted-foreground">
          {room.design_kw ? `${room.design_kw} kW design · ` : ''}{rackCount} rack{rackCount === 1 ? '' : 's'}
        </span>
      </div>
      {room.rows.length === 0 && (
        <p className="text-xs text-muted-foreground">No rows in this room.</p>
      )}
      {room.rows.map((rw) => (
        <div key={rw.id} className="space-y-1.5 pl-4">
          <div className="text-xs text-muted-foreground">
            Row <span className="font-mono">{rw.code}</span> · {rw.name} · {rw.racks.length} rack{rw.racks.length === 1 ? '' : 's'}
          </div>
          {rw.racks.length > 0 && (
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
              {rw.racks.map((rk) => (
                <RackTile key={rk.id} rack={rk} onClick={() => onRackClick(rk.id)} />
              ))}
            </div>
          )}
        </div>
      ))}
    </div>
  );
}

function RackTile({
  rack, onClick,
}: Readonly<{ rack: RackNode; onClick: () => void }>) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="space-y-1.5 rounded-md border bg-background p-2.5 text-left transition-colors hover:bg-accent/40"
    >
      <div className="flex items-baseline justify-between gap-2">
        <div className="flex items-baseline gap-2">
          <Server className="h-3.5 w-3.5 self-center text-muted-foreground" />
          <span className="font-mono text-xs font-medium">{rack.code}</span>
          <span className="truncate text-xs text-muted-foreground">{rack.name}</span>
        </div>
        <span className="text-[11px] text-muted-foreground">{rack.asset_count}d</span>
      </div>
      <CapacityBar
        used={rack.u_used} total={rack.u_height}
        leftLabel={`${rack.u_used}/${rack.u_height} U`}
        compact
      />
      {rack.kw_max !== null && (
        <CapacityBar
          used={rack.kw_current ?? 0}
          total={rack.kw_max}
          unknown={rack.kw_current === null}
          leftLabel={
            rack.kw_current === null
              ? `—/${rack.kw_max} kW`
              : `${rack.kw_current.toFixed(1)}/${rack.kw_max} kW`
          }
          compact
        />
      )}
    </button>
  );
}

