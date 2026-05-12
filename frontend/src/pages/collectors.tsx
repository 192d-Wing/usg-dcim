// Site collectors — Cloudscape table + enroll Modal + bootstrap snippet Modal.

import { useState } from 'react';
import { useTable, useGetIdentity, useList } from '@refinedev/core';
import { toast } from 'sonner';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator, { StatusIndicatorProps } from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import Tabs from '@cloudscape-design/components/tabs';

import { hasCap } from '@/lib/caps';
import { http } from '@/lib/http';
import { formatDate } from '@/lib/utils';

type Site = { id: string; code: string; name: string };
type Collector = {
  id: string; name: string; site_id: string; status: string;
  last_seen_at: string | null; buffered_samples: number; capabilities: string[];
  version: string | null;
};
type Enrollment = {
  collector_id: string;
  enrollment_token: string;
  expires_in_seconds: number;
};

type FreshnessType = 'success' | 'warning' | 'error' | 'pending';
function statusType(s: string): FreshnessType {
  if (s === 'healthy') return 'success';
  if (s === 'degraded') return 'warning';
  if (s === 'stale' || s === 'unreachable') return 'error';
  return 'pending';
}

const CAPABILITIES = ['snmp', 'redfish', 'modbus', 'rest', 'ipmi'] as const;

export function CollectorsPage() {
  const { tableQuery, result } = useTable<Collector>({
    resource: 'collectors',
    pagination: { pageSize: 200 },
  });
  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const sites = sitesRes.result.data ?? [];
  const sitesById = new Map(sites.map((s) => [s.id, s]));
  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const canEnroll = hasCap(identity?.capabilities, 'collectors:collectors:enroll');
  const data = result.data ?? [];

  const [enrollOpen, setEnrollOpen] = useState(false);
  const [enrolled, setEnrolled] = useState<{ enrollment: Enrollment; siteCode: string; name: string } | null>(null);

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          counter={`(${result.total ?? 0})`}
          description="Site-side agents that ingest telemetry and forward to the central API."
        >
          Site collectors
        </Header>
      }
    >
      <Table<Collector>
        variant="container"
        loading={tableQuery.isLoading}
        loadingText="Loading collectors…"
        items={data}
        trackBy="id"
        header={
          <Header
            counter={`(${data.length})`}
            actions={canEnroll && (
              <Button variant="primary" iconName="add-plus" onClick={() => setEnrollOpen(true)}>
                Enroll collector
              </Button>
            )}
          >
            Collectors
          </Header>
        }
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (c) => <span style={{ fontWeight: 500 }}>{c.name}</span> },
          {
            id: 'site', header: 'Site',
            cell: (c) => {
              const site = sitesById.get(c.site_id);
              return site
                ? `${site.code} · ${site.name}`
                : <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{c.site_id.slice(0, 8)}…</span>;
            },
          },
          {
            id: 'capabilities', header: 'Capabilities',
            cell: (c) => c.capabilities.length === 0
              ? <Box variant="span" color="text-status-inactive" fontSize="body-s">—</Box>
              : (
                <SpaceBetween size="xxs" direction="horizontal">
                  {c.capabilities.map((cap) => <Badge key={cap}>{cap}</Badge>)}
                </SpaceBetween>
              ),
          },
          {
            id: 'version', header: 'Version',
            cell: (c) => <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{c.version ?? '—'}</span>,
            width: 120,
          },
          {
            id: 'status', header: 'Status',
            cell: (c) => (
              <StatusIndicator type={statusType(c.status) as StatusIndicatorProps.Type}>
                {c.status}
              </StatusIndicator>
            ),
            width: 130,
          },
          {
            id: 'last_seen', header: 'Last seen',
            cell: (c) => (
              <Box variant="span" color="text-status-inactive" fontSize="body-s">
                {formatDate(c.last_seen_at)}
              </Box>
            ),
            width: 200,
          },
          {
            id: 'buffered', header: 'Buffered',
            cell: (c) => <span style={{ fontVariantNumeric: 'tabular-nums' }}>{c.buffered_samples?.toLocaleString() ?? 0}</span>,
            width: 100,
          },
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            No collectors enrolled. Use Enroll collector to bootstrap one.
          </Box>
        }
      />

      {canEnroll && (
        <Modal
          visible={enrollOpen}
          onDismiss={() => setEnrollOpen(false)}
          header="Enroll a new collector"
          size="medium"
        >
          <EnrollForm
            sites={sites}
            onEnrolled={(enrollment, siteCode, name) => {
              setEnrollOpen(false);
              setEnrolled({ enrollment, siteCode, name });
              tableQuery.refetch();
            }}
          />
        </Modal>
      )}

      <Modal
        visible={enrolled !== null}
        onDismiss={() => setEnrolled(null)}
        header="Bootstrap the collector"
        size="large"
      >
        {enrolled && (
          <Bootstrap
            token={enrolled.enrollment.enrollment_token}
            collectorId={enrolled.enrollment.collector_id}
            expiresInSeconds={enrolled.enrollment.expires_in_seconds}
            siteCode={enrolled.siteCode}
            name={enrolled.name}
          />
        )}
      </Modal>
    </ContentLayout>
  );
}

function EnrollForm({
  sites, onEnrolled,
}: Readonly<{
  sites: Site[];
  onEnrolled: (enrollment: Enrollment, siteCode: string, name: string) => void;
}>) {
  const siteOptions: SelectProps.Option[] = sites.map((s) => ({ value: s.id, label: `${s.code} · ${s.name}` }));
  const [siteOpt, setSiteOpt] = useState<SelectProps.Option | null>(null);
  const [name, setName] = useState('');
  const [selected, setSelected] = useState<string[]>(['snmp']);
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  function toggle(cap: string, checked: boolean) {
    setSelected((cur) => checked ? [...cur, cap] : cur.filter((c) => c !== cap));
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!siteOpt?.value) errs.site = 'Site required';
    if (!name.trim()) errs.name = 'Name required';
    if (selected.length === 0) errs.caps = 'Pick at least one capability';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;
    setSubmitting(true);
    try {
      const r = await http.post<Enrollment>('/collectors/enroll', {
        site_id: siteOpt!.value, name, capabilities: selected,
      });
      const site = sites.find((s) => s.id === siteOpt!.value);
      toast.success('Collector enrolled — bootstrap token issued');
      onEnrolled(r.data, site?.code ?? 'site', name);
    } catch (err: any) {
      toast.error(err?.message ?? 'enrollment failed');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form onSubmit={onSubmit}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={submitting}>
            {submitting ? 'Enrolling…' : 'Enroll & issue token'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField label="Site" errorText={errors.site}>
            <Select
              placeholder="Pick a site"
              selectedOption={siteOpt}
              onChange={({ detail }) => setSiteOpt(detail.selectedOption)}
              options={siteOptions}
              expandToViewport
            />
          </FormField>
          <FormField label="Collector name" errorText={errors.name}>
            <Input value={name} onChange={({ detail }) => setName(detail.value)} placeholder="e.g. ops-collector-1" />
          </FormField>
          <FormField label="Capabilities" errorText={errors.caps}>
            <SpaceBetween size="xs" direction="horizontal">
              {CAPABILITIES.map((cap) => (
                <Checkbox
                  key={cap}
                  checked={selected.includes(cap)}
                  onChange={({ detail }) => toggle(cap, detail.checked)}
                >
                  <span style={{ fontFamily: 'ui-monospace, monospace' }}>{cap}</span>
                </Checkbox>
              ))}
            </SpaceBetween>
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}

function Bootstrap({
  token, collectorId, expiresInSeconds, siteCode, name,
}: Readonly<{
  token: string; collectorId: string; expiresInSeconds: number;
  siteCode: string; name: string;
}>) {
  const apiBase =
    typeof window !== 'undefined' && window.location.origin
      ? window.location.origin
      : 'https://your-dcim';
  const dockerCmd = `docker run -d --name dcim-collector-${siteCode.toLowerCase()} \\
  -e DCIM_API_URL=${apiBase} \\
  -e DCIM_COLLECTOR_ID=${collectorId} \\
  -e DCIM_ENROLLMENT_TOKEN=${token} \\
  -v /var/lib/dcim-collector:/data \\
  ghcr.io/192d-wing/usg-dcim-collector:latest`;
  const systemdEnv = `# /etc/dcim-collector.env
DCIM_API_URL=${apiBase}
DCIM_COLLECTOR_ID=${collectorId}
DCIM_ENROLLMENT_TOKEN=${token}
DCIM_DATA_DIR=/var/lib/dcim-collector`;
  const expiresIn = expiresInSeconds < 3600
    ? `${Math.round(expiresInSeconds / 60)} minutes`
    : `${(expiresInSeconds / 3600).toFixed(0)} hour${expiresInSeconds === 3600 ? '' : 's'}`;

  return (
    <SpaceBetween size="m">
      <Box>
        Collector <code style={{ fontFamily: 'ui-monospace, monospace' }}>{name}</code> registered for{' '}
        <code style={{ fontFamily: 'ui-monospace, monospace' }}>{siteCode}</code>. Run one of the
        snippets below on the site jump host. The token expires in <strong>{expiresIn}</strong>.
      </Box>
      <Container header={<Header variant="h3">Enrollment token (one-time)</Header>}>
        <CopyBlock value={token} />
      </Container>

      <Tabs
        tabs={[
          {
            id: 'docker',
            label: 'Docker',
            content: <CopyBlock value={dockerCmd} multiline />,
          },
          {
            id: 'systemd',
            label: 'systemd',
            content: (
              <SpaceBetween size="s">
                <Box>
                  <Box variant="awsui-key-label">1. Drop the env file</Box>
                  <CopyBlock value={systemdEnv} multiline />
                </Box>
                <Box>
                  <Box variant="awsui-key-label">2. Enable + start the service</Box>
                  <CopyBlock value="systemctl enable --now dcim-collector" />
                </Box>
              </SpaceBetween>
            ),
          },
        ]}
      />

      <Container header={<Header variant="h3">After bootstrap</Header>}>
        <Box color="text-status-inactive" fontSize="body-s">
          The collector exchanges this token for an mTLS cert + service token on first contact.
          It will appear in the table as <strong>healthy</strong> once heartbeats start arriving.
        </Box>
      </Container>
    </SpaceBetween>
  );
}

function CopyBlock({ value, multiline }: Readonly<{ value: string; multiline?: boolean }>) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    await navigator.clipboard.writeText(value);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }
  return (
    <div style={{ display: 'flex', alignItems: 'flex-start', gap: 8 }}>
      <pre style={{
        flex: 1, overflowX: 'auto', margin: 0, padding: 8, borderRadius: 8,
        fontFamily: 'ui-monospace, monospace', fontSize: 11,
        whiteSpace: multiline ? 'pre' : 'pre-wrap', wordBreak: multiline ? 'normal' : 'break-all',
      }}>{value}</pre>
      <Button onClick={copy} iconName={copied ? 'status-positive' : 'copy'}>
        {copied ? 'Copied' : 'Copy'}
      </Button>
    </div>
  );
}
