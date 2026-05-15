// Audit log page — Cloudscape canary.
//
// Server-side filters: action / site / target_type / target_id / since / until.
// Client-side filter:  actor_label (free-text "contains").
// Selecting one or more rows reveals the diff/metadata/request detail
// panel below the table — same information the prior shadcn version
// surfaced via inline row expansion, modeled with Cloudscape's
// selection API instead of the tree-shaped expandableRows.

import { useMemo, useState } from 'react';
import { useTable, useList } from '@refinedev/core';
import { useQuery } from '@tanstack/react-query';

import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Pagination from '@cloudscape-design/components/pagination';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';

import { http } from '@/lib/http';
import { formatDate } from '@/lib/utils';

type AuditEntry = {
  id: string;
  occurred_at: string;
  actor_user_id: string | null;
  actor_token_id: string | null;
  actor_label: string | null;
  actor_ip: string | null;
  action: string;
  target_type: string | null;
  target_id: string | null;
  site_id: string | null;
  request_id: string | null;
  success: boolean;
  diff_json: Record<string, unknown>;
  metadata_json: Record<string, unknown>;
};
type Site = { id: string; code: string; name: string };

const ALL_OPTION: SelectProps.Option = { value: '__all__', label: 'All' };

export function AuditPage() {
  const [actionOpt, setActionOpt] = useState<SelectProps.Option>(ALL_OPTION);
  const [siteOpt, setSiteOpt] = useState<SelectProps.Option>(ALL_OPTION);
  const [targetType, setTargetType] = useState('');
  const [targetId, setTargetId] = useState('');
  const [actorLabel, setActorLabel] = useState('');
  // datetime-local strings (no timezone). Treated as the operator's local
  // wall-clock time and converted to ISO on the way out.
  const [sinceLocal, setSinceLocal] = useState('');
  const [untilLocal, setUntilLocal] = useState('');
  const [selected, setSelected] = useState<AuditEntry[]>([]);

  const filters = useMemo(() => {
    const f: { field: string; operator: 'eq' | 'contains'; value: string }[] = [];
    if (actionOpt.value !== ALL_OPTION.value) f.push({ field: 'action', operator: 'eq', value: actionOpt.value! });
    if (siteOpt.value !== ALL_OPTION.value) f.push({ field: 'site_id', operator: 'eq', value: siteOpt.value! });
    if (targetType) f.push({ field: 'target_type', operator: 'eq', value: targetType });
    if (targetId) f.push({ field: 'target_id', operator: 'eq', value: targetId });
    const sinceIso = localToIso(sinceLocal);
    const untilIso = localToIso(untilLocal);
    if (sinceIso) f.push({ field: 'since', operator: 'eq', value: sinceIso });
    if (untilIso) f.push({ field: 'until', operator: 'eq', value: untilIso });
    return f;
  }, [actionOpt, siteOpt, targetType, targetId, sinceLocal, untilLocal]);

  const { tableQuery, result, currentPage, pageCount, setCurrentPage } = useTable<AuditEntry>({
    resource: 'audit/log',
    pagination: { pageSize: 50 },
    filters: { permanent: filters },
  });
  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const sites = sitesRes.result.data ?? [];
  const sitesById = useMemo(() => new Map(sites.map((s) => [s.id, s])), [sites]);

  const actions = useQuery({
    queryKey: ['audit-actions'],
    queryFn: async () => (await http.get<string[]>('/audit/actions')).data,
    staleTime: 60_000,
  });

  // Server returns up to pageSize rows; actor_label needle filters
  // client-side to save a backend round-trip on a low-cardinality column.
  let rows = result.data ?? [];
  if (actorLabel) {
    const needle = actorLabel.toLowerCase();
    rows = rows.filter((a) => (a.actor_label ?? '').toLowerCase().includes(needle));
  }
  const total = result.total ?? 0;

  const actionOptions: SelectProps.Option[] = [
    ALL_OPTION,
    ...(actions.data ?? []).map((a) => ({ value: a, label: a })),
  ];
  const siteOptions: SelectProps.Option[] = [
    ALL_OPTION,
    ...sites.map((s) => ({ value: s.id, label: `${s.code} · ${s.name}` })),
  ];

  // Only show detail for selected rows that actually carry detail —
  // otherwise selecting a "fail with no diff" row leaves an empty card.
  const detailRows = selected.filter(hasDetail);

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description={`${total.toLocaleString()} entries match the current filter`}
        >
          Audit log
        </Header>
      }
    >
      <SpaceBetween size="l">
        <Container>
          <ColumnLayout columns={4}>
            <FormField label="Action">
              <Select
                selectedOption={actionOpt}
                onChange={({ detail }) => { setActionOpt(detail.selectedOption); setCurrentPage(1); }}
                options={actionOptions}
                expandToViewport
              />
            </FormField>
            <FormField label="Site">
              <Select
                selectedOption={siteOpt}
                onChange={({ detail }) => { setSiteOpt(detail.selectedOption); setCurrentPage(1); }}
                options={siteOptions}
                expandToViewport
              />
            </FormField>
            <FormField label="Target type">
              <Input
                value={targetType}
                onChange={({ detail }) => { setTargetType(detail.value); setCurrentPage(1); }}
                placeholder="asset, rack, site…"
              />
            </FormField>
            <FormField label="Target id">
              <Input
                value={targetId}
                onChange={({ detail }) => { setTargetId(detail.value); setCurrentPage(1); }}
                placeholder="exact uuid"
              />
            </FormField>
            <FormField label="Actor (contains)">
              <Input
                value={actorLabel}
                onChange={({ detail }) => setActorLabel(detail.value)}
                placeholder="email or label"
              />
            </FormField>
            <FormField
              label="Since"
              description="Local time; inclusive lower bound."
            >
              <input
                type="datetime-local"
                value={sinceLocal}
                onChange={(e) => { setSinceLocal(e.target.value); setCurrentPage(1); }}
                style={{
                  width: '100%', boxSizing: 'border-box',
                  padding: '4px 8px', fontFamily: 'inherit', fontSize: 14,
                }}
              />
            </FormField>
            <FormField
              label="Until"
              description="Local time; inclusive upper bound."
            >
              <input
                type="datetime-local"
                value={untilLocal}
                onChange={(e) => { setUntilLocal(e.target.value); setCurrentPage(1); }}
                style={{
                  width: '100%', boxSizing: 'border-box',
                  padding: '4px 8px', fontFamily: 'inherit', fontSize: 14,
                }}
              />
            </FormField>
            <FormField label=" ">
              <Button
                disabled={!sinceLocal && !untilLocal}
                onClick={() => { setSinceLocal(''); setUntilLocal(''); setCurrentPage(1); }}
              >
                Clear date range
              </Button>
            </FormField>
          </ColumnLayout>
        </Container>

        <Table<AuditEntry>
          variant="container"
          loading={tableQuery.isLoading}
          loadingText="Loading audit entries…"
          items={rows}
          trackBy="id"
          selectionType="multi"
          selectedItems={selected}
          onSelectionChange={({ detail }) => setSelected(detail.selectedItems)}
          ariaLabels={{
            selectionGroupLabel: 'Audit entry selection',
            allItemsSelectionLabel: ({ selectedItems }) =>
              `${selectedItems.length} entries selected`,
            itemSelectionLabel: (_data, item) => `Select ${item.action}`,
          }}
          columnDefinitions={[
            {
              id: 'when',
              header: 'When',
              cell: (e) => <span style={{ fontSize: 12 }}>{formatDate(e.occurred_at)}</span>,
              width: 180,
            },
            {
              id: 'actor',
              header: 'Actor',
              cell: (e) => (
                <Box variant="span">
                  {e.actor_label ?? <Box variant="span" color="text-status-inactive">—</Box>}
                  {e.actor_token_id && (
                    <Box variant="span" margin={{ left: 'xs' }}>
                      <StatusIndicator type="info">token</StatusIndicator>
                    </Box>
                  )}
                </Box>
              ),
            },
            {
              id: 'action',
              header: 'Action',
              cell: (e) => <code style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{e.action}</code>,
            },
            {
              id: 'target',
              header: 'Target',
              cell: (e) => e.target_type
                ? <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>
                    {e.target_type}{e.target_id ? `:${e.target_id.slice(0, 8)}…` : ''}
                  </span>
                : <Box variant="span" color="text-status-inactive">—</Box>,
            },
            {
              id: 'site',
              header: 'Site',
              cell: (e) => {
                if (!e.site_id) return <Box variant="span" color="text-status-inactive">—</Box>;
                const s = sitesById.get(e.site_id);
                return <span style={{ fontSize: 12 }}>{s ? s.code : `${e.site_id.slice(0, 8)}…`}</span>;
              },
            },
            {
              id: 'result',
              header: 'Result',
              cell: (e) => e.success
                ? <StatusIndicator type="success">ok</StatusIndicator>
                : <StatusIndicator type="error">fail</StatusIndicator>,
              width: 100,
            },
          ]}
          empty={
            <Box textAlign="center" color="inherit" padding="m">
              No entries match this filter.
            </Box>
          }
          pagination={
            pageCount > 1 ? (
              <Pagination
                currentPageIndex={currentPage}
                pagesCount={pageCount}
                onChange={({ detail }) => setCurrentPage(detail.currentPageIndex)}
              />
            ) : undefined
          }
        />

        {detailRows.length > 0 && (
          <SpaceBetween size="s">
            <Header
              variant="h3"
              actions={<Button onClick={() => setSelected([])}>Clear</Button>}
            >
              Detail ({detailRows.length})
            </Header>
            {detailRows.map((e) => <DetailPanel key={e.id} entry={e} />)}
          </SpaceBetween>
        )}
      </SpaceBetween>
    </ContentLayout>
  );
}

// `<input type="datetime-local">` gives us "YYYY-MM-DDTHH:mm" with no zone.
// Treat it as the user's local wall-clock time so the picker matches what
// they see in the When column; the Date constructor parses it that way.
function localToIso(local: string): string {
  if (!local) return '';
  const d = new Date(local);
  return Number.isNaN(d.getTime()) ? '' : d.toISOString();
}

function hasDetail(e: AuditEntry): boolean {
  return (
    Object.keys(e.diff_json ?? {}).length > 0
    || Object.keys(e.metadata_json ?? {}).length > 0
    || !!e.actor_ip
    || !!e.request_id
  );
}

function DetailPanel({ entry }: Readonly<{ entry: AuditEntry }>) {
  return (
    <Container
      header={
        <Header
          variant="h3"
          description={<code style={{ fontFamily: 'ui-monospace, monospace' }}>{entry.action}</code>}
        >
          {entry.target_type
            ? `${entry.target_type}:${(entry.target_id ?? '').slice(0, 8)}…`
            : 'detail'}
        </Header>
      }
    >
      <ColumnLayout columns={2} variant="text-grid">
        {(entry.actor_ip || entry.request_id) && (
          <Box>
            <Box variant="awsui-key-label">Request</Box>
            {entry.actor_ip && (
              <div>IP: <code style={{ fontFamily: 'ui-monospace, monospace' }}>{entry.actor_ip}</code></div>
            )}
            {entry.request_id && (
              <div>Request id: <code style={{ fontFamily: 'ui-monospace, monospace' }}>{entry.request_id}</code></div>
            )}
          </Box>
        )}
        {Object.keys(entry.diff_json ?? {}).length > 0 && (
          <Box>
            <Box variant="awsui-key-label">Diff</Box>
            <pre style={{
              overflowX: 'auto', fontFamily: 'ui-monospace, monospace',
              fontSize: 11, margin: 0,
            }}>{JSON.stringify(entry.diff_json, null, 2)}</pre>
          </Box>
        )}
        {Object.keys(entry.metadata_json ?? {}).length > 0 && (
          <Box>
            <Box variant="awsui-key-label">Metadata</Box>
            <pre style={{
              overflowX: 'auto', fontFamily: 'ui-monospace, monospace',
              fontSize: 11, margin: 0,
            }}>{JSON.stringify(entry.metadata_json, null, 2)}</pre>
          </Box>
        )}
      </ColumnLayout>
    </Container>
  );
}
