// Racks list — Cloudscape Cards collection (one tile per rack).

import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router';
import { useList } from '@refinedev/core';
import { useQuery } from '@tanstack/react-query';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Cards from '@cloudscape-design/components/cards';
import ContentLayout from '@cloudscape-design/components/content-layout';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Link from '@cloudscape-design/components/link';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';

import { CapacityBar } from '@/components/capacity-bar';
import { http } from '@/lib/http';

type Site = { id: string; code: string; name: string };
type Rack = { id: string; name: string; code: string; u_height: number; max_kw: number | null; serial: string | null; site_id: string };
type CapacityRow = {
  rack_id: string;
  u_used: number; u_total: number; u_pct: number;
  kw_current: number | null; kw_max: number | null; kw_pct: number | null;
  biggest_contiguous_free: number;
};
type ForecastRow = {
  rack_id: string;
  slope_u_per_day: number | null;
  days_until_full: number | null;
  runway_band: 'critical' | 'warning' | 'healthy' | 'unknown';
};

const BAND_COLOR: Record<ForecastRow['runway_band'], 'red' | 'severity-medium' | 'green' | 'grey'> = {
  critical: 'red',
  warning: 'severity-medium',
  healthy: 'green',
  unknown: 'grey',
};

function formatDays(d: number | null): string {
  if (d === null) return '—';
  if (d < 1) return '<1d';
  if (d > 730) return `${Math.round(d / 365)}y`;
  if (d > 60) return `${Math.round(d / 30)}mo`;
  return `${Math.round(d)}d`;
}

const ALL_SITES_OPT: SelectProps.Option = { value: 'all', label: 'All sites' };

export function RacksListPage() {
  const nav = useNavigate();
  const [siteOpt, setSiteOpt] = useState<SelectProps.Option>(ALL_SITES_OPT);
  const siteId = siteOpt.value!;

  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const racksRes = useList<Rack>({
    resource: 'inventory/racks',
    pagination: { pageSize: 200 },
    filters: siteId === 'all' ? [] : [{ field: 'site_id', operator: 'eq', value: siteId }],
  });

  const capacityRes = useQuery({
    queryKey: ['racks-capacity', siteId],
    queryFn: async () => {
      const params: Record<string, string | number> = { u: 0, limit: 500 };
      if (siteId !== 'all') params.site_id = siteId;
      const r = await http.get<{ racks: CapacityRow[] }>('/dashboards/free-space', { params });
      return r.data.racks;
    },
    refetchInterval: 30_000,
  });
  const capById = useMemo(() => {
    const m = new Map<string, CapacityRow>();
    for (const c of capacityRes.data ?? []) m.set(c.rack_id, c);
    return m;
  }, [capacityRes.data]);

  const forecastRes = useQuery({
    queryKey: ['racks-forecast', siteId],
    queryFn: async () => {
      const params: Record<string, string | number> = { limit: 500 };
      if (siteId !== 'all') params.site_id = siteId;
      const r = await http.get<{ racks: ForecastRow[] }>('/dashboards/forecast/racks', { params });
      return r.data.racks;
    },
    refetchInterval: 60_000,
  });
  const forecastById = useMemo(() => {
    const m = new Map<string, ForecastRow>();
    for (const f of forecastRes.data ?? []) m.set(f.rack_id, f);
    return m;
  }, [forecastRes.data]);

  const sites = sitesRes.result.data ?? [];
  const racks = racksRes.result.data ?? [];
  const racksTotal = racksRes.result.total ?? racks.length;

  const siteOptions: SelectProps.Option[] = [
    ALL_SITES_OPT,
    ...sites.map((s) => ({ value: s.id, label: `${s.code} · ${s.name}` })),
  ];

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          counter={`(${racksTotal})`}
          actions={
            <SpaceBetween size="xs" direction="horizontal">
              <FormField label="Site">
                <Select
                  selectedOption={siteOpt}
                  onChange={({ detail }) => setSiteOpt(detail.selectedOption)}
                  options={siteOptions}
                  expandToViewport
                />
              </FormField>
              <Button variant="primary" iconName="add-plus" onClick={() => nav('/racks/new')}>
                New rack
              </Button>
            </SpaceBetween>
          }
        >
          Racks
        </Header>
      }
    >
      <Cards<Rack>
        loading={racksRes.query.isLoading}
        loadingText="Loading racks…"
        items={racks}
        trackBy="id"
        cardsPerRow={[
          { cards: 1 },
          { minWidth: 500, cards: 2 },
          { minWidth: 900, cards: 3 },
          { minWidth: 1300, cards: 4 },
        ]}
        cardDefinition={{
          header: (r) => (
            <Link
              href={`/racks/${r.id}`}
              onFollow={(e) => { e.preventDefault(); nav(`/racks/${r.id}`); }}
            >
              {r.code} · {r.name}
            </Link>
          ),
          sections: [
            {
              id: 'spec',
              content: (r) => (
                <Box color="text-status-inactive" fontSize="body-s">
                  {r.u_height}U · {r.max_kw ? `${r.max_kw} kW` : 'unrated'}
                </Box>
              ),
            },
            {
              id: 'capacity',
              content: (r) => {
                const cap = capById.get(r.id);
                if (!cap) return <Box color="text-status-inactive">Loading…</Box>;
                return (
                  <SpaceBetween size="xxs">
                    <CapacityBar
                      used={cap.u_used} total={cap.u_total}
                      leftLabel={`${cap.u_used}/${cap.u_total} U`}
                      compact
                    />
                    {cap.kw_max !== null ? (
                      <CapacityBar
                        used={cap.kw_current ?? 0}
                        total={cap.kw_max}
                        unknown={cap.kw_current === null}
                        leftLabel={
                          cap.kw_current === null
                            ? `—/${cap.kw_max} kW`
                            : `${cap.kw_current.toFixed(1)}/${cap.kw_max} kW`
                        }
                        compact
                      />
                    ) : (
                      <Box color="text-status-inactive" fontSize="body-s">No kW rating</Box>
                    )}
                  </SpaceBetween>
                );
              },
            },
            {
              id: 'runway',
              content: (r) => {
                const cap = capById.get(r.id);
                const fc = forecastById.get(r.id);
                return (
                  <SpaceBetween size="xxs" direction="horizontal">
                    {cap && (
                      <Box variant="span" color="text-status-inactive" fontSize="body-s">
                        Largest gap: <span style={{ fontFamily: 'ui-monospace, monospace' }}>{cap.biggest_contiguous_free}U</span>
                      </Box>
                    )}
                    {fc && fc.slope_u_per_day !== null && (
                      <Badge color={BAND_COLOR[fc.runway_band]}>
                        {`${formatDays(fc.days_until_full)} runway`}
                      </Badge>
                    )}
                    {fc && fc.slope_u_per_day === null && <Badge>no trend</Badge>}
                  </SpaceBetween>
                );
              },
            },
          ],
        }}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            No racks for this filter.
          </Box>
        }
      />
    </ContentLayout>
  );
}
