// Notification channels — Cloudscape table + create/edit Modals.
// Per-kind config fields (webhook / slack / email) are switched in the
// form by the selected `kind`.

import { useState } from 'react';
import { useTable, useGetIdentity } from '@refinedev/core';
import { toast } from 'sonner';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import Pagination from '@cloudscape-design/components/pagination';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';

import { http } from '@/lib/http';

type Severity = 'info' | 'warning' | 'minor' | 'major' | 'critical';
type Kind = 'webhook' | 'slack' | 'email';
type Channel = {
  id: string;
  name: string;
  kind: Kind;
  config_json: Record<string, unknown>;
  min_severity: Severity;
  notify_on_fire: boolean;
  notify_on_resolve: boolean;
  enabled: boolean;
  description: string | null;
};

const SEVERITY_OPTIONS: SelectProps.Option[] = [
  { value: 'info', label: 'info' },
  { value: 'warning', label: 'warning' },
  { value: 'minor', label: 'minor' },
  { value: 'major', label: 'major' },
  { value: 'critical', label: 'critical' },
];

const KIND_OPTIONS: SelectProps.Option[] = [
  { value: 'webhook', label: 'Generic webhook' },
  { value: 'slack', label: 'Slack incoming webhook' },
  { value: 'email', label: 'Email (SMTP)' },
];

function configSummary(c: Channel): string {
  if (c.kind === 'webhook') return (c.config_json.url as string) ?? '(no URL)';
  if (c.kind === 'slack') return (c.config_json.webhook_url as string) ?? '(no URL)';
  if (c.kind === 'email') {
    const r = c.config_json.recipients as string[] | undefined;
    return r && r.length ? r.join(', ') : '(no recipients)';
  }
  return '';
}

export function NotificationsPage() {
  const { tableQuery, result, currentPage, pageCount, setCurrentPage } = useTable<Channel>({
    resource: 'notifications/channels',
    pagination: { pageSize: 50 },
    sorters: { initial: [{ field: 'name', order: 'asc' }] },
  });
  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const canConfigure = identity?.capabilities.includes('alerts:configure');
  const data = result.data ?? [];

  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<Channel | null>(null);

  async function refresh() { await tableQuery.refetch(); }

  async function toggle(c: Channel) {
    try {
      await http.patch(`/notifications/channels/${c.id}`, { enabled: !c.enabled });
      toast.success(c.enabled ? 'Channel disabled' : 'Channel enabled');
      await refresh();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  async function remove(c: Channel) {
    if (!window.confirm(`Delete channel "${c.name}"?`)) return;
    try {
      await http.delete(`/notifications/channels/${c.id}`);
      toast.success('Channel deleted');
      await refresh();
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  async function sendTest(c: Channel) {
    try {
      const r = await http.post<{ delivered: boolean; error: string | null }>(
        `/notifications/channels/${c.id}/test`, {},
      );
      if (r.data.delivered) toast.success(`Test delivered to ${c.name}`);
      else toast.error(`Test failed: ${r.data.error ?? 'unknown error'}`);
    } catch (err: any) { toast.error(err?.message ?? 'failed'); }
  }

  if (!canConfigure) {
    return (
      <ContentLayout header={<Header variant="h1">Notifications</Header>}>
        <Box color="text-status-inactive">
          You don't have <code style={{ fontFamily: 'ui-monospace, monospace' }}>alerts:configure</code>.
          Ask an admin for a role that includes it.
        </Box>
      </ContentLayout>
    );
  }

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description="Outbound delivery for alert.fire and alert.resolve events"
        >
          Notification channels
        </Header>
      }
    >
      <Table<Channel>
        variant="container"
        loading={tableQuery.isLoading}
        loadingText="Loading channels…"
        items={data}
        trackBy="id"
        header={
          <Header
            counter={`(${data.length})`}
            actions={
              <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                New channel
              </Button>
            }
          >
            Channels
          </Header>
        }
        columnDefinitions={[
          {
            id: 'kind', header: 'Kind',
            cell: (c) => <Badge>{c.kind}</Badge>,
            width: 120,
          },
          {
            id: 'name', header: 'Name',
            cell: (c) => (
              <SpaceBetween size="xxxs">
                <span style={{ fontWeight: 500 }}>{c.name}</span>
                {c.description && (
                  <Box variant="span" color="text-status-inactive" fontSize="body-s">{c.description}</Box>
                )}
              </SpaceBetween>
            ),
          },
          {
            id: 'target', header: 'Target',
            cell: (c) => (
              <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>
                {configSummary(c)}
              </span>
            ),
          },
          {
            id: 'routing', header: 'Routing',
            cell: (c) => {
              const events: string[] = [];
              if (c.notify_on_fire) events.push('fire');
              if (c.notify_on_resolve) events.push('resolve');
              return (
                <Box variant="span" fontSize="body-s">
                  ≥ <span style={{ fontWeight: 500 }}>{c.min_severity}</span>
                  {' · '}
                  <Box variant="span" color="text-status-inactive">
                    {events.length === 0 ? '(no events)' : events.join(' + ')}
                  </Box>
                </Box>
              );
            },
          },
          {
            id: 'status', header: 'Status',
            cell: (c) => c.enabled
              ? <StatusIndicator type="success">enabled</StatusIndicator>
              : <StatusIndicator type="stopped">disabled</StatusIndicator>,
            width: 130,
          },
          {
            id: 'actions', header: '',
            cell: (c) => (
              <SpaceBetween size="xxs" direction="horizontal">
                <Button iconName="upload" variant="inline-icon" onClick={() => sendTest(c)} ariaLabel={`Send test to ${c.name}`} />
                <Button iconName={c.enabled ? 'status-stopped' : 'status-positive'}
                  variant="inline-icon" onClick={() => toggle(c)}
                  ariaLabel={c.enabled ? `Disable ${c.name}` : `Enable ${c.name}`} />
                <Button iconName="edit" variant="inline-icon" onClick={() => setEditing(c)} ariaLabel={`Edit ${c.name}`} />
                <Button iconName="remove" variant="inline-icon" onClick={() => remove(c)} ariaLabel={`Delete ${c.name}`} />
              </SpaceBetween>
            ),
            width: 200,
          },
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            No channels configured. Alerts will fire silently until you add one.
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

      <Modal
        visible={createOpen}
        onDismiss={() => setCreateOpen(false)}
        header="New notification channel"
        size="medium"
      >
        <ChannelForm onSaved={async () => { setCreateOpen(false); await refresh(); }} />
      </Modal>

      <Modal
        visible={editing !== null}
        onDismiss={() => setEditing(null)}
        header="Edit notification channel"
        size="medium"
      >
        {editing && (
          <ChannelForm
            channel={editing}
            onSaved={async () => { setEditing(null); await refresh(); }}
          />
        )}
      </Modal>
    </ContentLayout>
  );
}

function ChannelForm({
  channel, onSaved,
}: Readonly<{
  channel?: Channel;
  onSaved: () => void;
}>) {
  const editing = !!channel;
  const initialConfig = channel?.config_json ?? {};
  const [name, setName] = useState(channel?.name ?? '');
  const [description, setDescription] = useState(channel?.description ?? '');
  const [kindOpt, setKindOpt] = useState<SelectProps.Option>(
    KIND_OPTIONS.find((k) => k.value === (channel?.kind ?? 'webhook'))!,
  );
  const [sevOpt, setSevOpt] = useState<SelectProps.Option>(
    SEVERITY_OPTIONS.find((s) => s.value === (channel?.min_severity ?? 'warning'))!,
  );
  const [notifyFire, setNotifyFire] = useState(channel?.notify_on_fire ?? true);
  const [notifyResolve, setNotifyResolve] = useState(channel?.notify_on_resolve ?? true);
  const [enabled, setEnabled] = useState(channel?.enabled ?? true);
  const [webhookUrl, setWebhookUrl] = useState((initialConfig.url as string) ?? '');
  const [slackUrl, setSlackUrl] = useState((initialConfig.webhook_url as string) ?? '');
  const [recipients, setRecipients] = useState(((initialConfig.recipients as string[]) ?? []).join(', '));
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  const kind = kindOpt.value as Kind;

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const errs: Record<string, string> = {};
    if (!name.trim()) errs.name = 'Name required';
    let config_json: Record<string, unknown> = {};
    if (kind === 'webhook') {
      if (!webhookUrl) errs.webhook_url = 'URL required for webhook channels';
      else config_json = { url: webhookUrl };
    } else if (kind === 'slack') {
      if (!slackUrl) errs.slack_url = 'Slack webhook URL required';
      else config_json = { webhook_url: slackUrl };
    } else {
      const list = recipients.split(',').map((s) => s.trim()).filter(Boolean);
      if (list.length === 0) errs.recipients = 'At least one recipient required';
      else config_json = { recipients: list };
    }
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;

    const body: Record<string, unknown> = {
      name,
      description: description || null,
      min_severity: sevOpt.value,
      notify_on_fire: notifyFire,
      notify_on_resolve: notifyResolve,
      enabled,
      config_json,
    };
    if (!editing) body.kind = kind;
    setSubmitting(true);
    try {
      if (editing && channel) {
        await http.patch(`/notifications/channels/${channel.id}`, body);
        toast.success('Channel updated');
      } else {
        await http.post('/notifications/channels', body);
        toast.success('Channel created');
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
            {submitting ? 'Saving…' : editing ? 'Save' : 'Create'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField label="Name" errorText={errors.name}>
            <Input value={name} onChange={({ detail }) => setName(detail.value)} placeholder="e.g. ops-slack" />
          </FormField>
          <FormField label="Description (optional)">
            <Input value={description ?? ''} onChange={({ detail }) => setDescription(detail.value)} />
          </FormField>
          <FormField
            label="Kind"
            description={editing ? 'Kind cannot be changed after creation.' : undefined}
          >
            <Select
              selectedOption={kindOpt}
              onChange={({ detail }) => setKindOpt(detail.selectedOption)}
              options={KIND_OPTIONS}
              disabled={editing}
              expandToViewport
            />
          </FormField>

          {kind === 'webhook' && (
            <FormField label="Webhook URL" errorText={errors.webhook_url}>
              <Input
                type="url"
                value={webhookUrl}
                onChange={({ detail }) => setWebhookUrl(detail.value)}
                placeholder="https://hook.example/dcim"
              />
            </FormField>
          )}
          {kind === 'slack' && (
            <FormField label="Slack webhook URL" errorText={errors.slack_url}>
              <Input
                type="url"
                value={slackUrl}
                onChange={({ detail }) => setSlackUrl(detail.value)}
                placeholder="https://hooks.slack.com/services/…"
              />
            </FormField>
          )}
          {kind === 'email' && (
            <FormField label="Recipients (comma-separated)" errorText={errors.recipients}>
              <Input
                value={recipients}
                onChange={({ detail }) => setRecipients(detail.value)}
                placeholder="ops@example.org, oncall@example.org"
              />
            </FormField>
          )}

          <FormField label="Min severity">
            <Select
              selectedOption={sevOpt}
              onChange={({ detail }) => setSevOpt(detail.selectedOption)}
              options={SEVERITY_OPTIONS}
              expandToViewport
            />
          </FormField>
          <SpaceBetween size="xs" direction="horizontal">
            <Checkbox checked={enabled} onChange={({ detail }) => setEnabled(detail.checked)}>Enabled</Checkbox>
            <Checkbox checked={notifyFire} onChange={({ detail }) => setNotifyFire(detail.checked)}>Notify on fire</Checkbox>
            <Checkbox checked={notifyResolve} onChange={({ detail }) => setNotifyResolve(detail.checked)}>Notify on resolve</Checkbox>
          </SpaceBetween>
        </SpaceBetween>
      </Form>
    </form>
  );
}
