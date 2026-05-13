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
type ConfigOverrides = {
  dns_metrics_interval_seconds: number | null;
  device_poll_interval_seconds: number | null;
  heartbeat_interval_seconds: number | null;
};
type Collector = {
  id: string; name: string; site_id: string; status: string;
  last_seen_at: string | null; buffered_samples: number; capabilities: string[];
  version: string | null;
  enabled: boolean;
  config_overrides: ConfigOverrides;
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
  const [editingOverrides, setEditingOverrides] = useState<Collector | null>(null);
  const [selected, setSelected] = useState<Collector[]>([]);
  const [actionLoading, setActionLoading] = useState(false);
  const canEditOverrides = hasCap(identity?.capabilities, 'collectors:collectors:update');

  const enabledState = selected.length > 0 ? (
    selected.every((c) => c.enabled)
      ? 'disable'
      : selected.every((c) => !c.enabled)
        ? 'enable'
        : 'mixed'
  ) : null;

  async function handleToggleEnabled() {
    if (selected.length === 0) return;
    const shouldEnable = enabledState === 'enable';
    setActionLoading(true);
    try {
      await Promise.all(
        selected.map((c) =>
          http.patch(`/collectors/${c.id}/enabled`, { enabled: shouldEnable })
        )
      );
      toast.success(`${selected.length} collector${selected.length === 1 ? '' : 's'} ${shouldEnable ? 'enabled' : 'disabled'}`);
      setSelected([]);
      tableQuery.refetch();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to toggle enabled state');
    } finally {
      setActionLoading(false);
    }
  }

  async function handleDecommission() {
    if (selected.length === 0) return;
    setActionLoading(true);
    try {
      await Promise.all(selected.map((c) => http.delete(`/collectors/${c.id}`)));
      toast.success(`${selected.length} collector${selected.length === 1 ? '' : 's'} decommissioned`);
      setSelected([]);
      tableQuery.refetch();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to decommission');
    } finally {
      setActionLoading(false);
    }
  }

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
        selectionType="multi"
        selectedItems={selected}
        onSelectionChange={({ detail }) => setSelected(detail.selectedItems)}
        header={
          <Header
            counter={`(${data.length})`}
            actions={
              <SpaceBetween direction="horizontal" size="xs">
                {canEditOverrides && (
                  <>
                    <Button
                      disabled={selected.length !== 1 || !canEditOverrides}
                      iconName="settings"
                      onClick={() => setEditingOverrides(selected[0])}
                    >
                      Edit overrides
                    </Button>
                    <Button
                      disabled={selected.length === 0 || !canEditOverrides || actionLoading}
                      loading={actionLoading}
                      onClick={handleToggleEnabled}
                    >
                      {enabledState === 'enable' ? 'Enable' : enabledState === 'disable' ? 'Disable' : 'Enable / Disable'}
                    </Button>
                    <Button
                      disabled={selected.length === 0 || !canEditOverrides || actionLoading}
                      loading={actionLoading}
                      onClick={handleDecommission}
                    >
                      Decommission
                    </Button>
                  </>
                )}
                {canEnroll && (
                  <Button variant="primary" iconName="add-plus" onClick={() => setEnrollOpen(true)}>
                    Enroll collector
                  </Button>
                )}
              </SpaceBetween>
            }
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
          {
            id: 'overrides', header: 'Overrides',
            cell: (c) => <OverridesSummary overrides={c.config_overrides} />,
            width: 200,
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

      <Modal
        visible={editingOverrides !== null}
        onDismiss={() => setEditingOverrides(null)}
        header={editingOverrides ? `Runtime overrides — ${editingOverrides.name}` : 'Runtime overrides'}
        size="medium"
      >
        {editingOverrides && (
          <OverridesForm
            collector={editingOverrides}
            onSaved={() => {
              setEditingOverrides(null);
              tableQuery.refetch();
            }}
          />
        )}
      </Modal>
    </ContentLayout>
  );
}

function OverridesSummary({ overrides }: Readonly<{ overrides: ConfigOverrides | undefined }>) {
  const parts: string[] = [];
  if (overrides?.dns_metrics_interval_seconds) parts.push(`scrape ${overrides.dns_metrics_interval_seconds}s`);
  if (overrides?.device_poll_interval_seconds) parts.push(`poll ${overrides.device_poll_interval_seconds}s`);
  if (overrides?.heartbeat_interval_seconds) parts.push(`hb ${overrides.heartbeat_interval_seconds}s`);
  if (parts.length === 0) {
    return <Box variant="span" color="text-status-inactive" fontSize="body-s">defaults</Box>;
  }
  return (
    <Box variant="span" fontSize="body-s" fontWeight="normal">
      {parts.join(' · ')}
    </Box>
  );
}

// parseOverrideField: empty → null (clear override); valid 5..3600 → int;
// out-of-range → Number.NaN sentinel so the caller can refuse to PATCH.
function parseOverrideField(s: string): number | null {
  const t = s.trim();
  if (t === '') return null;
  const n = Number(t);
  if (!Number.isFinite(n) || n < 5 || n > 3600) return Number.NaN;
  return Math.round(n);
}

function OverridesForm({
  collector, onSaved,
}: Readonly<{
  collector: Collector;
  onSaved: () => void;
}>) {
  const initial = collector.config_overrides ?? ({} as ConfigOverrides);
  const [dnsScrape, setDnsScrape] = useState(initial.dns_metrics_interval_seconds?.toString() ?? '');
  const [poll, setPoll] = useState(initial.device_poll_interval_seconds?.toString() ?? '');
  const [hb, setHb] = useState(initial.heartbeat_interval_seconds?.toString() ?? '');
  const [submitting, setSubmitting] = useState(false);

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const body: ConfigOverrides = {
      dns_metrics_interval_seconds: parseOverrideField(dnsScrape),
      device_poll_interval_seconds: parseOverrideField(poll),
      heartbeat_interval_seconds: parseOverrideField(hb),
    };
    if (Object.values(body).some((v) => Number.isNaN(v))) {
      toast.error('Intervals must be 5–3600 seconds (or empty for default).');
      return;
    }
    setSubmitting(true);
    try {
      await http.patch(`/collectors/${collector.id}/config`, body);
      toast.success('Overrides saved — collector applies on next heartbeat.');
      onSaved();
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to save overrides');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form onSubmit={onSubmit}>
      <Form
        actions={
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="primary" formAction="submit" loading={submitting}>
              {submitting ? 'Saving…' : 'Save'}
            </Button>
          </SpaceBetween>
        }
      >
        <SpaceBetween size="m">
          <Box variant="p" color="text-body-secondary">
            Leave a field empty to use the collector's YAML default. Each value is 5–3600 seconds.
            Changes propagate on the collector's next heartbeat (up to one heartbeat-interval of lag).
          </Box>
          <FormField label="DNS metrics scrape interval (seconds)"
            description="How often the collector hits CoreDNS/Hickory /metrics and POSTs to central.">
            <Input
              value={dnsScrape}
              onChange={({ detail }) => setDnsScrape(detail.value)}
              type="number"
              placeholder="60 (default)"
            />
          </FormField>
          <FormField label="Device poll interval (seconds)"
            description="Override the per-device poll_interval_seconds in collector.yaml.">
            <Input
              value={poll}
              onChange={({ detail }) => setPoll(detail.value)}
              type="number"
              placeholder="(per-device default)"
            />
          </FormField>
          <FormField label="Heartbeat interval (seconds)"
            description="How often the collector reports buffer depth + liveness to central.">
            <Input
              value={hb}
              onChange={({ detail }) => setHb(detail.value)}
              type="number"
              placeholder="30 (default)"
            />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
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

function formatExpiry(seconds: number): string {
  if (seconds < 3600) return Math.round(seconds / 60) + ' minutes';
  const hours = Math.round(seconds / 3600);
  return hours + ' hour' + (hours === 1 ? '' : 's');
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
  // The collector container reads a YAML config at /etc/dcim/collector.yaml
  // (see collector/Dockerfile entrypoint) and a token file referenced
  // by `api_token_file:` in that YAML. Earlier env-var snippets here
  // were fictional — none of those env vars are read by the collector,
  // and pasting them got operators a container that started and
  // immediately failed config validation.
  //
  // The snippets below build a real `/etc/dcim-collector/collector.yaml`
  // + token file on the site host first, then mount both into the
  // container.
  const containerName = 'dcim-collector-' + siteCode.toLowerCase();
  // We don't know the site_id from the enrollment response (the API
  // only echoes back collector_id + token), so the operator has to
  // paste it in. Leaving a placeholder is honest; pretending we have
  // it would silently put the wrong value in the yaml.
  const collectorYaml = `# /etc/dcim-collector/collector.yaml
collector_id: ${collectorId}
site_id: <FILL-IN-FROM-DCIM-SITE-PAGE>
ingest_url: ${apiBase}
api_token_file: /etc/dcim-collector/token
heartbeat_interval_seconds: 30
buffer_path: /var/lib/dcim-collector/buffer.db
devices: []  # add SNMP / Redfish / Modbus / REST / IPMI entries here
`;
  const dockerCmd = `# Run on the site host. Writes config + token to disk, then starts
# the collector. The token file is the credential the API recognises;
# protect it with 0600 — anyone who reads it can heartbeat as this
# collector until you revoke the enrollment.
sudo mkdir -p /etc/dcim-collector /var/lib/dcim-collector
sudo install -m 0600 /dev/stdin /etc/dcim-collector/token <<'EOF'
${token}
EOF
sudo install -m 0644 /dev/stdin /etc/dcim-collector/collector.yaml <<'EOF'
${collectorYaml}EOF

docker run -d --name ${containerName} --restart unless-stopped \\
  -v /etc/dcim-collector:/etc/dcim:ro \\
  -v /var/lib/dcim-collector:/var/lib/dcim-collector \\
  ghcr.io/192d-wing/usg-dcim-collector:latest`;
  const systemdYaml = collectorYaml;
  const systemdUnit = `# /etc/systemd/system/dcim-collector.service
[Unit]
Description=DCIM site collector
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=dcim-collector
Group=dcim-collector
ExecStart=/usr/local/bin/dcim-collector --config /etc/dcim-collector/collector.yaml
Restart=on-failure
RestartSec=10
StateDirectory=dcim-collector
StateDirectoryMode=0750

[Install]
WantedBy=multi-user.target`;
  const expiresIn = formatExpiry(expiresInSeconds);

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
                  <Box variant="awsui-key-label">1. Write the config file</Box>
                  <CopyBlock value={systemdYaml} multiline />
                </Box>
                <Box>
                  <Box variant="awsui-key-label">2. Write the token file (chmod 0600)</Box>
                  <CopyBlock value={`sudo install -m 0600 /dev/stdin /etc/dcim-collector/token <<'EOF'\n${token}\nEOF`} multiline />
                </Box>
                <Box>
                  <Box variant="awsui-key-label">3. Install the unit file</Box>
                  <CopyBlock value={systemdUnit} multiline />
                </Box>
                <Box>
                  <Box variant="awsui-key-label">4. Enable + start the service</Box>
                  <CopyBlock value="sudo systemctl daemon-reload && sudo systemctl enable --now dcim-collector" />
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
