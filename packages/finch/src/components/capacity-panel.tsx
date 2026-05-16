import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import Header from '@cloudscape-design/components/header';
import SpaceBetween from '@cloudscape-design/components/space-between';

import { CapacityBar } from './capacity-bar';

export type Capacity = {
  u_used: number;
  u_total: number;
  u_pct: number;
  u_free: number;
  kw_current: number | null;
  kw_max: number | null;
  kw_pct: number | null;
  biggest_contiguous_free: number;
  free_runs: { start_u: number; length: number }[];
};

const MONO = { fontFamily: 'ui-monospace, monospace' } as const;

function badgeColorForRun(length: number): 'green' | 'grey' {
  if (length >= 4) return 'green';
  return 'grey';
}

export function CapacityPanel({ capacity }: Readonly<{ capacity: Capacity }>) {
  const c = capacity;
  return (
    <Container header={<Header variant="h2">Capacity</Header>}>
      <ColumnLayout columns={3}>
        <SpaceBetween size="xs">
          <Box variant="awsui-key-label">Rack space</Box>
          <CapacityBar
            used={c.u_used}
            total={c.u_total}
            leftLabel={`${c.u_used} / ${c.u_total} U used`}
          />
          <Box color="text-status-inactive" fontSize="body-s">{c.u_free} U free</Box>
        </SpaceBetween>

        <SpaceBetween size="xs">
          <Box variant="awsui-key-label">Power</Box>
          {c.kw_max === null ? (
            <Box color="text-status-inactive" fontSize="body-s">
              No max kW configured for this rack.
            </Box>
          ) : (
            <>
              <CapacityBar
                used={c.kw_current ?? 0}
                total={c.kw_max}
                unknown={c.kw_current === null}
                leftLabel={
                  c.kw_current === null
                    ? `— / ${c.kw_max} kW`
                    : `${c.kw_current.toFixed(2)} / ${c.kw_max} kW`
                }
              />
              <Box color="text-status-inactive" fontSize="body-s">
                {c.kw_current === null
                  ? 'Awaiting current PDU telemetry'
                  : `${(c.kw_max - c.kw_current).toFixed(2)} kW headroom`}
              </Box>
            </>
          )}
        </SpaceBetween>

        <SpaceBetween size="xs">
          <Box variant="awsui-key-label">Free contiguous space</Box>
          {c.free_runs.length === 0 ? (
            <Box color="text-status-inactive" fontSize="body-s">Rack is full.</Box>
          ) : (
            <>
              <SpaceBetween size="xxs" direction="horizontal">
                {c.free_runs.slice(0, 6).map((r) => (
                  <Badge key={`${r.start_u}-${r.length}`} color={badgeColorForRun(r.length)}>
                    {r.length}U @ U{r.start_u}
                  </Badge>
                ))}
              </SpaceBetween>
              <Box color="text-status-inactive" fontSize="body-s">
                Largest gap: <span style={MONO}>{c.biggest_contiguous_free}U</span>
              </Box>
            </>
          )}
        </SpaceBetween>
      </ColumnLayout>
    </Container>
  );
}
