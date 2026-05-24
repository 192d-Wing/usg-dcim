import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Spinner from '@cloudscape-design/components/spinner';
import { http } from '@/lib/http';

type Band = 'critical' | 'warning' | 'healthy' | 'unknown';
type KwForecast = {
  max_kw: number | null;
  days: number;
  samples: number;
  slope_kw_per_day: number | null;
  current_kw: number | null;
  days_until_max: number | null;
  projected_max_date: string | null;
  runway_band: Band;
};
type Forecast = {
  u_used: number;
  u_total: number;
  u_free: number;
  slope_u_per_day: number | null;
  days_until_full: number | null;
  projected_fill_date: string | null;
  runway_band: Band;
  kw_forecast: KwForecast | null;
  what_if_add_units?: number;
  what_if_u_used?: number;
  what_if_u_free?: number;
  what_if_days_until_full?: number | null;
  what_if_runway_band?: Band;
};

const BAND_COLOR: Record<Band, 'red' | 'severity-medium' | 'green' | 'grey'> = {
  critical: 'red',
  warning: 'severity-medium',
  healthy: 'green',
  unknown: 'grey',
};

function formatDays(d: number | null | undefined): string {
  if (d === null || d === undefined) return '—';
  if (d < 1) return '<1 day';
  if (d > 730) return `${Math.round(d / 365)}y`;
  if (d > 60) return `${Math.round(d / 30)}mo`;
  return `${Math.round(d)}d`;
}

function formatDate(iso: string | null): string {
  if (!iso) return '—';
  return new Date(iso).toLocaleDateString();
}

export function ForecastPanel({ rackId }: Readonly<{ rackId: string }>) {
  const [whatIfU, setWhatIfU] = useState<string>('');
  const addUnits = whatIfU ? Math.max(0, Math.min(60, Number(whatIfU))) : 0;

  const forecastRes = useQuery({
    queryKey: ['rack-forecast', rackId, addUnits],
    queryFn: async () => {
      const url = addUnits > 0
        ? `/dashboards/forecast/racks/${rackId}?add_units=${addUnits}`
        : `/dashboards/forecast/racks/${rackId}`;
      return (await http.get<Forecast>(url)).data;
    },
    enabled: !!rackId,
  });

  const f = forecastRes.data;

  return (
    <Container
      header={
        <Header
          variant="h2"
          actions={f && (
            <Badge color={BAND_COLOR[f.runway_band]}>
              {f.runway_band}
            </Badge>
          )}
        >
          Capacity forecast
        </Header>
      }
    >
      {forecastRes.isLoading && (
        <Box color="text-status-inactive"><Spinner /> Loading…</Box>
      )}
      {!forecastRes.isLoading && !f && (
        <Box color="text-status-inactive">No forecast available.</Box>
      )}
      {f && (
        <SpaceBetween size="m">
          <ColumnLayout columns={4}>
            <Field label="Used / total" value={`${f.u_used} / ${f.u_total} U`} />
            <Field label="Free" value={`${f.u_free} U`} />
            <Field
              label="Growth"
              value={
                f.slope_u_per_day === null
                  ? 'no trend yet'
                  : `${f.slope_u_per_day.toFixed(2)} U/day`
              }
            />
            <Field
              label="Runway"
              value={
                f.slope_u_per_day === null
                  ? '—'
                  : `${formatDays(f.days_until_full)} (${formatDate(f.projected_fill_date)})`
              }
            />
          </ColumnLayout>
          {f.slope_u_per_day === null && (
            <Box color="text-status-inactive" fontSize="body-s">
              Need at least two placements with distinct timestamps before a slope can be inferred.
            </Box>
          )}
          {f.kw_forecast && <KwSection kw={f.kw_forecast} />}
          <Container header={<Header variant="h3">What-if: add U to this rack</Header>}>
            <SpaceBetween size="xs">
              <div style={{ width: 140 }}>
                <FormField label="Units to add">
                  <Input
                    type="number"
                    value={whatIfU}
                    placeholder="0"
                    onChange={({ detail }) => setWhatIfU(detail.value)}
                  />
                </FormField>
              </div>
              {addUnits > 0 && (
                <SpaceBetween size="xs" direction="horizontal">
                  <Box color="text-status-inactive">→</Box>
                  <Box>
                    {f.what_if_u_used} / {f.u_total} U used
                    <Box variant="span" color="text-status-inactive">
                      {' '}· {f.what_if_u_free} U free
                    </Box>
                  </Box>
                  {f.what_if_runway_band && (
                    <Badge color={BAND_COLOR[f.what_if_runway_band]}>
                      {formatDays(f.what_if_days_until_full)} runway · {f.what_if_runway_band}
                    </Badge>
                  )}
                </SpaceBetween>
              )}
            </SpaceBetween>
          </Container>
        </SpaceBetween>
      )}
    </Container>
  );
}

function Field({ label, value }: Readonly<{ label: string; value: string }>) {
  return (
    <div>
      <Box variant="awsui-key-label">{label}</Box>
      <Box fontSize="body-m" fontWeight="bold">{value}</Box>
    </div>
  );
}

function KwSection({ kw }: Readonly<{ kw: KwForecast }>) {
  const slope = kw.slope_kw_per_day;
  return (
    <Container
      header={
        <Header
          variant="h3"
          actions={<Badge color={BAND_COLOR[kw.runway_band]}>{kw.runway_band}</Badge>}
          description={`${kw.days}d window · ${kw.samples} samples`}
        >
          kW trend
        </Header>
      }
    >
      <SpaceBetween size="xs">
        <ColumnLayout columns={4}>
          <Field
            label="Current"
            value={kw.current_kw === null ? '—' : `${kw.current_kw.toFixed(2)} kW`}
          />
          <Field
            label="Max rated"
            value={kw.max_kw === null ? '—' : `${kw.max_kw.toFixed(1)} kW`}
          />
          <Field
            label="Growth"
            value={slope === null ? 'no trend yet' : `${(slope * 1000).toFixed(0)} W/day`}
          />
          <Field
            label="Runway to max"
            value={
              kw.days_until_max === null
                ? '—'
                : `${formatDays(kw.days_until_max)} (${formatDate(kw.projected_max_date)})`
            }
          />
        </ColumnLayout>
        {kw.samples < 2 && (
          <Box color="text-status-inactive" fontSize="body-s">
            Need at least two daily kW samples from PDU telemetry before a slope can be inferred.
          </Box>
        )}
      </SpaceBetween>
    </Container>
  );
}
