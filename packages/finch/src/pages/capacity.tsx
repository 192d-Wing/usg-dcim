// Capacity & free space — Cloudscape filter Container + results Table.

import { useState } from 'react';
import { useNavigate } from 'react-router';
import { useList } from '@refinedev/core';
import { useQuery } from '@tanstack/react-query';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';

import { http } from '@/lib/http';
import { CapacityBar } from '@/components/capacity-bar';

type Site = { id: string; code: string; name: string };
type FreeSpaceRow = {
  rack_id: string;
  site_id: string;
  code: string;
  name: string;
  u_height: number;
  u_used: number; u_total: number; u_pct: number;
  kw_current: number | null; kw_max: number | null;
  biggest_contiguous_free: number;
  free_runs: { start_u: number; length: number }[];
};

const ANY_SITE_OPT: SelectProps.Option = { value: 'all', label: 'Any site' };

export function CapacityPage() {
  const nav = useNavigate();
  const [minU, setMinU] = useState('1');
  const [siteOpt, setSiteOpt] = useState<SelectProps.Option>(ANY_SITE_OPT);
  const [minKw, setMinKw] = useState('');

  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const sites = sitesRes.result.data ?? [];
  const siteOptions: SelectProps.Option[] = [
    ANY_SITE_OPT,
    ...sites.map((s) => ({ value: s.id, label: `${s.code} · ${s.name}` })),
  ];

  const params: Record<string, string | number> = { u: Number(minU) || 1, limit: 200 };
  if (siteOpt.value !== ANY_SITE_OPT.value) params.site_id = siteOpt.value!;
  if (minKw && Number(minKw) > 0) params.min_kw_headroom = Number(minKw);

  const result = useQuery({
    queryKey: ['free-space', minU, siteOpt.value, minKw],
    queryFn: async () => {
      const r = await http.get<{ racks: FreeSpaceRow[]; count: number }>('/dashboards/free-space', { params });
      return r.data;
    },
    refetchInterval: 60_000,
  });

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description="Find racks with enough contiguous U slots — and optional kW headroom — for an upcoming install."
        >
          Capacity & free space
        </Header>
      }
    >
      <SpaceBetween size="l">
        <Container header={<Header variant="h2">Search</Header>}>
          <ColumnLayout columns={4}>
            <FormField label="Need at least (U)">
              <Input
                type="number"
                value={minU}
                onChange={({ detail }) => setMinU(detail.value)}
              />
            </FormField>
            <FormField label="Site">
              <Select
                selectedOption={siteOpt}
                onChange={({ detail }) => setSiteOpt(detail.selectedOption)}
                options={siteOptions}
                expandToViewport
              />
            </FormField>
            <FormField label="Min kW headroom (optional)">
              <Input
                type="number"
                value={minKw}
                onChange={({ detail }) => setMinKw(detail.value)}
                placeholder="e.g. 2.5"
              />
            </FormField>
            <FormField label=" ">
              <Button
                onClick={() => { setMinU('1'); setSiteOpt(ANY_SITE_OPT); setMinKw(''); }}
              >
                Reset
              </Button>
            </FormField>
          </ColumnLayout>
        </Container>

        <Table<FreeSpaceRow>
          variant="container"
          loading={result.isLoading}
          loadingText="Searching racks…"
          items={result.data?.racks ?? []}
          trackBy="rack_id"
          onRowClick={({ detail }) => nav(`/racks/${detail.item.rack_id}`)}
          header={
            <Header
              counter={`(${result.data?.count ?? 0})`}
              description="Sorted by largest contiguous gap"
            >
              Matching racks
            </Header>
          }
          columnDefinitions={[
            {
              id: 'rack', header: 'Rack',
              cell: (r) => (
                <SpaceBetween size="xxxs">
                  <span style={{ fontWeight: 500 }}>{r.code} · {r.name}</span>
                  <Box variant="span" color="text-status-inactive" fontSize="body-s">
                    site <span style={{ fontFamily: 'ui-monospace, monospace' }}>{r.site_id.slice(0, 8)}…</span>
                  </Box>
                </SpaceBetween>
              ),
            },
            {
              id: 'gap', header: 'Largest gap',
              cell: (r) => (
                <Badge color={r.biggest_contiguous_free >= 4 ? 'green' : 'grey'}>
                  {`${r.biggest_contiguous_free}U`}
                </Badge>
              ),
              width: 130,
            },
            {
              id: 'free', header: 'Free slots',
              cell: (r) => (
                <SpaceBetween size="xxs" direction="horizontal">
                  {r.free_runs.slice(0, 4).map((run) => (
                    <Badge key={`${run.start_u}-${run.length}`}>
                      {`${run.length}U @ U${run.start_u}`}
                    </Badge>
                  ))}
                  {r.free_runs.length > 4 && <Badge>{`+${r.free_runs.length - 4}`}</Badge>}
                </SpaceBetween>
              ),
            },
            {
              id: 'u', header: 'U utilization',
              cell: (r) => (
                <CapacityBar used={r.u_used} total={r.u_total} leftLabel={`${r.u_used}/${r.u_total} U`} compact />
              ),
              width: 220,
            },
            {
              id: 'kw', header: 'kW utilization',
              cell: (r) => r.kw_max === null
                ? <Box variant="span" color="text-status-inactive" fontSize="body-s">Unrated</Box>
                : (
                  <CapacityBar
                    used={r.kw_current ?? 0}
                    total={r.kw_max}
                    unknown={r.kw_current === null}
                    leftLabel={
                      r.kw_current === null
                        ? `—/${r.kw_max} kW`
                        : `${r.kw_current.toFixed(1)}/${r.kw_max} kW`
                    }
                    compact
                  />
                ),
              width: 220,
            },
          ]}
          empty={
            <Box textAlign="center" color="inherit" padding="m">
              No racks match. Try lowering the U requirement or removing the kW headroom filter.
            </Box>
          }
        />
      </SpaceBetween>
    </ContentLayout>
  );
}
