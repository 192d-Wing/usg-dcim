// Alert rules — Cloudscape table + create/edit Modal.

import { useState } from 'react';
import { useNavigate } from 'react-router';
import { useTable, useGetIdentity, useList } from '@refinedev/core';
import { toast } from 'sonner';

import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import Pagination from '@cloudscape-design/components/pagination';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator, { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';

import { http } from '@/lib/http';

const SEVERITIES = ['info', 'warning', 'minor', 'major', 'critical'] as const;
const OPERATORS = ['>', '<', '>=', '<=', '==', '!='] as const;
type Severity = typeof SEVERITIES[number];

type Site = { id: string; code: string; name: string };
type Rule = {
  id: string;
  name: string;
  description: string | null;
  metric: string;
  operator: string;
  threshold: number;
  duration_seconds: number;
  severity: Severity;
  site_scope_id: string | null;
  enabled: boolean;
  runbook_url: string | null;
  asset_filter_json: Record<string, unknown>;
};

function sevType(s: Severity): StatusIndicatorProps.Type {
  if (s === 'critical' || s === 'major') return 'error';
  if (s === 'minor' || s === 'warning') return 'warning';
  if (s === 'info') return 'info';
  return 'success';
}

function fmtDuration(s: number): string {
  if (s < 60) return `${s}s`;
  if (s < 3600) return `${Math.round(s / 60)}m`;
  return `${(s / 3600).toFixed(1)}h`;
}

const ALL_SITES_OPT: SelectProps.Option = { value: '__all__', label: 'Enterprise default' };
const SEV_OPTIONS: SelectProps.Option[] = SEVERITIES.map((s) => ({ value: s, label: s }));
const OP_OPTIONS: SelectProps.Option[] = OPERATORS.map((o) => ({ value: o, label: o }));

export function AlertRulesPage() {
  const nav = useNavigate();
  const { tableQuery, result, currentPage, pageCount, setCurrentPage } = useTable<Rule>({
    resource: 'alerts/rules',
    pagination: { pageSize: 50 },
    sorters: { initial: [{ field: 'name', order: 'asc' }] },
  });
  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const sites = sitesRes.result.data ?? [];
  const sitesById = new Map(sites.map((s) => [s.id, s]));
  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const canConfigure = identity?.capabilities.includes('alerts:configure');
  const data = result.data ?? [];
  const total = result.total ?? 0;

  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<Rule | null>(null);

  async function refresh() { await tableQuery.refetch(); }

  async function toggle(r: Rule) {
    try {
      await http.patch(`/alerts/rules/${r.id}`, { enabled: !r.enabled });
      toast.success(r.enabled ? 'Rule disabled' : 'Rule enabled');
      await refresh();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  async function remove(r: Rule) {
    if (!window.confirm(`Delete alert rule "${r.name}"?`)) return;
    try {
      await http.delete(`/alerts/rules/${r.id}`);
      toast.success('Alert rule deleted');
      await refresh();
    } catch (err: any) { toast.error(err?.message ?? 'failed to delete'); }
  }

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          counter={`(${total})`}
          description="Evaluated against telemetry every cycle."
          actions={
            <SpaceBetween size="xs" direction="horizontal">
              <Button onClick={() => nav('/alerts')} iconName="angle-left">Back to alerts</Button>
              <Button onClick={() => nav('/settings/notifications')}>Notification channels</Button>
            </SpaceBetween>
          }
        >
          Alert rules
        </Header>
      }
    >
      <Table<Rule>
        variant="container"
        loading={tableQuery.isLoading}
        loadingText="Loading rules…"
        items={data}
        trackBy="id"
        header={
          <Header
            counter={`(${data.length})`}
            actions={canConfigure && (
              <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                New rule
              </Button>
            )}
          >
            Rules
          </Header>
        }
        columnDefinitions={[
          {
            id: 'severity', header: 'Severity',
            cell: (r) => <StatusIndicator type={sevType(r.severity)}>{r.severity}</StatusIndicator>,
            width: 120,
          },
          {
            id: 'name', header: 'Name',
            cell: (r) => (
              <SpaceBetween size="xxxs">
                <span style={{ fontWeight: 500 }}>{r.name}</span>
                {r.description && (
                  <Box variant="span" color="text-status-inactive" fontSize="body-s">{r.description}</Box>
                )}
              </SpaceBetween>
            ),
          },
          {
            id: 'trigger', header: 'Trigger',
            cell: (r) => (
              <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>
                {r.metric} {r.operator} {r.threshold}
              </span>
            ),
          },
          {
            id: 'for', header: 'For',
            cell: (r) => <span style={{ fontVariantNumeric: 'tabular-nums', fontSize: 12 }}>{fmtDuration(r.duration_seconds)}</span>,
            width: 80,
          },
          {
            id: 'scope', header: 'Scope',
            cell: (r) => {
              const site = r.site_scope_id ? sitesById.get(r.site_scope_id) : null;
              if (site) return `${site.code} · ${site.name}`;
              if (r.site_scope_id) return `site ${r.site_scope_id.slice(0, 8)}…`;
              return <Box variant="span" color="text-status-inactive">enterprise default</Box>;
            },
          },
          {
            id: 'status', header: 'Status',
            cell: (r) => r.enabled
              ? <StatusIndicator type="success">enabled</StatusIndicator>
              : <StatusIndicator type="stopped">disabled</StatusIndicator>,
            width: 130,
          },
          {
            id: 'doc', header: 'Doc',
            cell: (r) => r.runbook_url
              ? <Button iconName="external" variant="inline-icon" href={r.runbook_url} target="_blank" ariaLabel="Open runbook" />
              : <Box variant="span" color="text-status-inactive">—</Box>,
            width: 60,
          },
          ...(canConfigure ? [{
            id: 'actions', header: '',
            cell: (r: Rule) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button iconName={r.enabled ? 'status-stopped' : 'status-positive'}
                  variant="inline-icon" onClick={() => toggle(r)}
                  ariaLabel={r.enabled ? `Disable ${r.name}` : `Enable ${r.name}`} />
                <Button iconName="edit" variant="inline-icon" onClick={() => setEditing(r)} ariaLabel={`Edit ${r.name}`} />
                <Button iconName="remove" variant="inline-icon" onClick={() => remove(r)} ariaLabel={`Delete ${r.name}`} />
              </SpaceBetween>
            ),
            width: 140,
          }] : []),
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            No alert rules configured.
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

      {canConfigure && (
        <Modal
          visible={createOpen}
          onDismiss={() => setCreateOpen(false)}
          header="New alert rule"
          size="medium"
        >
          <RuleForm sites={sites} onSaved={async () => { setCreateOpen(false); await refresh(); }} />
        </Modal>
      )}

      <Modal
        visible={editing !== null}
        onDismiss={() => setEditing(null)}
        header="Edit alert rule"
        size="medium"
      >
        {editing && (
          <RuleForm
            sites={sites}
            rule={editing}
            onSaved={async () => { setEditing(null); await refresh(); }}
          />
        )}
      </Modal>
    </ContentLayout>
  );
}

function RuleForm({
  sites, rule, onSaved,
}: Readonly<{
  sites: Site[];
  rule?: Rule;
  onSaved: () => void;
}>) {
  const editing = !!rule;
  const siteOptions: SelectProps.Option[] = [
    ALL_SITES_OPT,
    ...sites.map((s) => ({ value: s.id, label: `${s.code} · ${s.name}` })),
  ];

  const [name, setName] = useState(rule?.name ?? '');
  const [description, setDescription] = useState(rule?.description ?? '');
  const [metric, setMetric] = useState(rule?.metric ?? '');
  const [opOpt, setOpOpt] = useState<SelectProps.Option>(
    OP_OPTIONS.find((o) => o.value === (rule?.operator ?? '>')) ?? OP_OPTIONS[0],
  );
  const [threshold, setThreshold] = useState(String(rule?.threshold ?? 0));
  const [durationSec, setDurationSec] = useState(String(rule?.duration_seconds ?? 60));
  const [sevOpt, setSevOpt] = useState<SelectProps.Option>(
    SEV_OPTIONS.find((s) => s.value === (rule?.severity ?? 'major'))!,
  );
  const [scopeOpt, setScopeOpt] = useState<SelectProps.Option>(
    siteOptions.find((s) => s.value === rule?.site_scope_id) ?? ALL_SITES_OPT,
  );
  const [runbook, setRunbook] = useState(rule?.runbook_url ?? '');
  const [enabled, setEnabled] = useState(rule?.enabled ?? true);
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Name required';
    if (!metric.trim()) errs.metric = 'Metric required';
    const thr = Number(threshold);
    if (Number.isNaN(thr)) errs.threshold = 'Number required';
    const dur = Number(durationSec);
    if (Number.isNaN(dur) || dur < 0 || dur > 86400) errs.duration = '0–86400';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;

    setSubmitting(true);
    const body = {
      name,
      description: description || null,
      metric,
      operator: opOpt.value,
      threshold: thr,
      duration_seconds: dur,
      severity: sevOpt.value,
      site_scope_id: scopeOpt.value === ALL_SITES_OPT.value ? null : scopeOpt.value,
      runbook_url: runbook || null,
      enabled,
      asset_filter_json: rule?.asset_filter_json ?? {},
    };
    try {
      if (editing && rule) {
        await http.patch(`/alerts/rules/${rule.id}`, body);
        toast.success('Alert rule updated');
      } else {
        await http.post('/alerts/rules', body);
        toast.success('Alert rule created');
      }
      onSaved();
    } catch (err: any) {
      toast.error(err?.message ?? 'save failed');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form onSubmit={onSubmit}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={submitting}>
            {submitting ? 'Saving…' : editing ? 'Save changes' : 'Create rule'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField label="Name" errorText={errors.name}>
            <Input value={name} onChange={({ detail }) => setName(detail.value)} placeholder="e.g. PDU input kW above 80%" />
          </FormField>
          <FormField label="Description (optional)">
            <Input value={description ?? ''} onChange={({ detail }) => setDescription(detail.value)} />
          </FormField>
          <FormField label="Metric" errorText={errors.metric}>
            <Input value={metric} onChange={({ detail }) => setMetric(detail.value)} placeholder="e.g. pdu.input.kw, sensor.temp.c" />
          </FormField>
          <ColumnLayout columns={3}>
            <FormField label="Operator">
              <Select selectedOption={opOpt} onChange={({ detail }) => setOpOpt(detail.selectedOption)}
                options={OP_OPTIONS} expandToViewport />
            </FormField>
            <FormField label="Threshold" errorText={errors.threshold}>
              <Input type="number" value={threshold} onChange={({ detail }) => setThreshold(detail.value)} />
            </FormField>
            <FormField label="Duration (s)" errorText={errors.duration}>
              <Input type="number" value={durationSec} onChange={({ detail }) => setDurationSec(detail.value)} />
            </FormField>
          </ColumnLayout>
          <ColumnLayout columns={2}>
            <FormField label="Severity">
              <Select selectedOption={sevOpt} onChange={({ detail }) => setSevOpt(detail.selectedOption)}
                options={SEV_OPTIONS} expandToViewport />
            </FormField>
            <FormField label="Scope">
              <Select selectedOption={scopeOpt} onChange={({ detail }) => setScopeOpt(detail.selectedOption)}
                options={siteOptions} expandToViewport />
            </FormField>
          </ColumnLayout>
          <FormField label="Runbook URL (optional)">
            <Input type="url" value={runbook ?? ''} onChange={({ detail }) => setRunbook(detail.value)}
              placeholder="https://runbooks.example/pdu-overload" />
          </FormField>
          <Checkbox checked={enabled} onChange={({ detail }) => setEnabled(detail.checked)}>Enabled</Checkbox>
        </SpaceBetween>
      </Form>
    </form>
  );
}
