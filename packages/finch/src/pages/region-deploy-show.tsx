// Region Deploy — detail page with live SSE log stream.
//
// Layout:
//   Header      — name, status badge, elapsed time, Abort/Retry buttons
//   Stage tree  — left column, 16 stages with success/in-progress/error
//                 icons + a count of events seen for each stage
//   Log pane    — right column, SSE-subscribed event feed, latest first
//
// Connection model:
//   - On mount: GET /events?since=0 (backfill) + open the stream.
//   - On reconnect (network blip, server restart): pass the last seen
//     event id back via ?since=, so the catch-up backend gives us
//     anything we missed.
//
// EventSource doesn't support custom headers — we'd lose the bearer
// auth — so we drive the stream via fetch + ReadableStream and parse
// SSE frames manually. The 15s heartbeat from the backend keeps the
// connection alive through proxies.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useParams } from 'react-router';
import { useOne } from '@refinedev/core';
import { toast } from 'sonner';

import Alert from '@cloudscape-design/components/alert';
import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Header from '@cloudscape-design/components/header';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator, {
  StatusIndicatorProps,
} from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';

import { http, TOKEN_KEY } from '@/lib/http';
import { formatDate } from '@/lib/utils';

// Stage order — must mirror docs/dev/region-deploy.md §6 and the
// backend's STAGES list (regiondeploy/orchestrator.py). Drift here
// produces "missing" stages in the UI even though the backend ran them.
const STAGES = [
  'preflight',
  'secrets',
  'render',
  'pxe.power',
  'pxe.install',
  'joining',
  'cni',
  'cni.bgp',
  'apps.cert-manager',
  'apps.dns_auth',
  'apps.dns_recursive',
  'apps.dhcp',
  'apps.collector',
  'seed',
  'verify',
  'finalize',
] as const;

type DeploymentStatus =
  | 'pending' | 'preflight' | 'provisioning' | 'joining' | 'cni' | 'apps'
  | 'verify' | 'ready' | 'failed' | 'aborted';

type Deployment = {
  id: string;
  name: string;
  site_id: string;
  status: DeploymentStatus;
  current_stage: string | null;
  last_error: string | null;
  created_at: string;
  started_at: string | null;
  finished_at: string | null;
};

type StreamEvent = {
  id: number;
  stage: string;
  level: 'info' | 'warn' | 'error';
  message: string;
  payload: Record<string, unknown>;
};

function statusType(s: DeploymentStatus): StatusIndicatorProps.Type {
  if (s === 'ready') return 'success';
  if (s === 'failed') return 'error';
  if (s === 'aborted') return 'stopped';
  if (s === 'pending') return 'pending';
  return 'in-progress';
}

// ─── SSE hook ──────────────────────────────────────────────────────────

/**
 * useDeploymentEvents — fetch-based SSE subscription with auto-reconnect.
 *
 * Returns the accumulated event list, latest first, and a `connected`
 * flag the UI can surface so an operator knows when the stream is
 * detached (vs the deployment just being quiet between stages).
 *
 * Reconnect strategy: linear backoff at 2s — the backend's 15s
 * heartbeat means we'll know within ~17s if a connection dies, and
 * any briefer hiccup catches up via the `?since=<id>` backfill.
 */
function useDeploymentEvents(deploymentId: string | undefined) {
  const [events, setEvents] = useState<StreamEvent[]>([]);
  const [connected, setConnected] = useState(false);
  const lastIdRef = useRef(0);
  const cancelRef = useRef(false);

  const handleFrame = useCallback((line: string) => {
    // SSE frames look like:
    //   id: 42\n
    //   data: {"id":42,"stage":"preflight","level":"info","message":"..."}\n
    //   \n
    // ":" prefix = comment (heartbeat); ignore.
    if (!line || line.startsWith(':')) return;
    if (line.startsWith('id:')) {
      const v = parseInt(line.slice(3).trim(), 10);
      if (!Number.isNaN(v)) lastIdRef.current = v;
      return;
    }
    if (!line.startsWith('data:')) return;
    try {
      const ev = JSON.parse(line.slice(5).trim()) as StreamEvent;
      setEvents((prev) => [ev, ...prev]);
      if (ev.id > lastIdRef.current) lastIdRef.current = ev.id;
    } catch {
      // Malformed payload — ignore. The backend won't emit these
      // intentionally, but a partial frame from a torn connection
      // can land here mid-line.
    }
  }, []);

  useEffect(() => {
    if (!deploymentId) return;
    cancelRef.current = false;

    async function loop() {
      while (!cancelRef.current) {
        try {
          const token = localStorage.getItem(TOKEN_KEY) ?? '';
          const resp = await fetch(
            `/api/v1/region-deployments/${deploymentId}/events/stream?since=${lastIdRef.current}`,
            { headers: { Authorization: `Bearer ${token}` } },
          );
          if (!resp.ok || !resp.body) {
            await new Promise((r) => setTimeout(r, 2000));
            continue;
          }
          setConnected(true);
          const reader = resp.body.getReader();
          const decoder = new TextDecoder();
          let buf = '';
          // SSE framing: frames are separated by `\n\n`. Within a
          // frame, lines are separated by `\n`. We accumulate bytes
          // until we see the frame delimiter, then split into lines.
          while (true) {
            const { value, done } = await reader.read();
            if (done) break;
            buf += decoder.decode(value, { stream: true });
            let sep: number;
            // eslint-disable-next-line no-cond-assign
            while ((sep = buf.indexOf('\n\n')) !== -1) {
              const frame = buf.slice(0, sep);
              buf = buf.slice(sep + 2);
              for (const line of frame.split('\n')) handleFrame(line);
            }
          }
        } catch {
          // network blip; fall through to backoff
        } finally {
          setConnected(false);
        }
        if (!cancelRef.current) {
          await new Promise((r) => setTimeout(r, 2000));
        }
      }
    }

    void loop();
    return () => {
      cancelRef.current = true;
    };
  }, [deploymentId, handleFrame]);

  return { events, connected };
}

// ─── Page ──────────────────────────────────────────────────────────────

export function RegionDeployShowPage() {
  const { id } = useParams<{ id: string }>();
  // Refine v5: useOne returns { query, result }; result is the
  // unwrapped record (not { data: ... }) and refetch lives on query.
  const { query, result: dep } = useOne<Deployment>({
    resource: 'region-deployments',
    id: id ?? '',
  });
  const refetch = query.refetch;
  const { events, connected } = useDeploymentEvents(id);

  // Per-stage rollup for the stage tree.
  const stageState = useMemo(() => {
    const m = new Map<string, { count: number; lastLevel: StreamEvent['level'] }>();
    for (const ev of events) {
      const cur = m.get(ev.stage) ?? { count: 0, lastLevel: 'info' as const };
      cur.count += 1;
      // events arrive newest-first — preserve the first (newest) level
      // we see per stage.
      if (cur.count === 1) cur.lastLevel = ev.level;
      m.set(ev.stage, cur);
    }
    return m;
  }, [events]);

  function stageIcon(stage: string): StatusIndicatorProps.Type {
    const st = stageState.get(stage);
    if (!st) return 'pending';
    if (st.lastLevel === 'error') return 'error';
    if (st.lastLevel === 'warn') return 'warning';
    // If this is the deployment's current_stage AND we've seen
    // events for it, the stage is in progress. Otherwise it's done.
    if (dep?.current_stage === stage) return 'in-progress';
    return 'success';
  }

  async function clickAbort() {
    if (!id) return;
    try {
      await http.post(`/region-deployments/${id}/abort`);
      toast.success('Abort signal sent');
      void refetch();
    } catch {
      toast.error('Abort failed');
    }
  }

  async function clickRetry() {
    if (!id) return;
    try {
      await http.post(`/region-deployments/${id}/start`);
      toast.success('Retry enqueued');
      void refetch();
    } catch {
      toast.error('Retry failed');
    }
  }

  if (!dep) {
    return (
      <ContentLayout header={<Header variant="h1">Loading…</Header>}>
        <Container><Box>Loading deployment…</Box></Container>
      </ContentLayout>
    );
  }

  const showRetry = dep.status === 'failed' || dep.status === 'aborted';
  const showAbort = !['ready', 'failed', 'aborted'].includes(dep.status);

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description={
            <SpaceBetween direction="horizontal" size="xs">
              <StatusIndicator type={statusType(dep.status)}>{dep.status}</StatusIndicator>
              <Badge>{dep.current_stage ?? 'idle'}</Badge>
              <Box variant="span" color="text-status-inactive">
                created {formatDate(dep.created_at)}
                {dep.finished_at && ` · finished ${formatDate(dep.finished_at)}`}
              </Box>
              <Badge color={connected ? 'green' : 'grey'}>
                stream {connected ? 'live' : 'reconnecting…'}
              </Badge>
            </SpaceBetween>
          }
          actions={
            <SpaceBetween direction="horizontal" size="xs">
              {showRetry && (
                <Button onClick={clickRetry}>Retry</Button>
              )}
              {showAbort && (
                <Button onClick={clickAbort}>Abort</Button>
              )}
            </SpaceBetween>
          }
        >
          {dep.name}
        </Header>
      }
    >
      <SpaceBetween size="m">
        {dep.last_error && (dep.status === 'failed' || dep.status === 'aborted') && (
          // Lift last_error out of the event-log pane and into a full-width
          // Alert above the columns. Operators triaging a stopped deploy
          // see what broke + the action (Retry or Abort) in the same
          // glance, without scanning the log first.
          <Alert
            type={dep.status === 'failed' ? 'error' : 'warning'}
            header={
              dep.status === 'failed'
                ? `Failed at stage ${dep.current_stage ?? 'unknown'}`
                : 'Deployment aborted'
            }
            action={
              showRetry
                ? <Button onClick={clickRetry}>Retry from {dep.current_stage ?? 'start'}</Button>
                : undefined
            }
          >
            {dep.last_error}
          </Alert>
        )}
        <ColumnLayout columns={2} variant="text-grid">
          <Container header={<Header variant="h2">Stages</Header>}>
          <Table
            variant="embedded"
            items={STAGES.map((s) => ({
              stage: s,
              count: stageState.get(s)?.count ?? 0,
            }))}
            trackBy="stage"
            columnDefinitions={[
              {
                id: 'icon', header: '',
                cell: (r) => (
                  <StatusIndicator type={stageIcon(r.stage)}>
                    {' '}
                  </StatusIndicator>
                ),
              },
              { id: 'stage', header: 'Stage', cell: (r) => r.stage },
              {
                id: 'events', header: 'Events',
                cell: (r) => (r.count > 0 ? String(r.count) : ''),
              },
            ]}
          />
        </Container>

        <Container
          header={
            <Header
              variant="h2"
              description="Newest first. Auto-reconnects on disconnect."
            >
              Event log
            </Header>
          }
        >
          <Table
            variant="embedded"
            items={events}
            trackBy={(e) => String(e.id)}
            columnDefinitions={[
              {
                id: 'level', header: '',
                cell: (e) => (
                  <StatusIndicator
                    type={
                      e.level === 'error' ? 'error'
                      : e.level === 'warn' ? 'warning'
                      : 'success'
                    }
                  >
                    {' '}
                  </StatusIndicator>
                ),
              },
              { id: 'stage', header: 'Stage', cell: (e) => e.stage },
              { id: 'msg', header: 'Message', cell: (e) => e.message },
            ]}
            empty={
              <Box textAlign="center" color="text-status-inactive">
                No events yet.
              </Box>
            }
          />
        </Container>
        </ColumnLayout>
      </SpaceBetween>
    </ContentLayout>
  );
}
