// Region Deploy — wizard. PR 13 first cut.
//
// Walks operators through a 6-step build of a RegionDeployment, then
// POSTs to /region-deployments and (on success) navigates to the
// detail page where the SSE event stream takes over.
//
// Network + services configuration are flat JSON textareas for now;
// per-field formage lands in a follow-up. The wizard's structure is
// the part that matters here — the field-level UX evolves as we get
// operators in front of it.
//
// Preflight gates the final Start button: the review step calls
// /region-deployments/{id}/preflight after the create succeeds, and
// only shows the Start CTA when `ready: true`.

import { useState } from 'react';
import { useNavigate } from 'react-router';
import { useList } from '@refinedev/core';
import { toast } from 'sonner';

import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import Container from '@cloudscape-design/components/container';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Select, { SelectProps } from '@cloudscape-design/components/select';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';
import Textarea from '@cloudscape-design/components/textarea';
import Wizard from '@cloudscape-design/components/wizard';

import { http } from '@/lib/http';

type Site = { id: string; code: string; name: string };

type NodeRow = {
  hostname: string;
  mac: string;
  role: 'control_plane' | 'worker' | 'edge';
  bmc_address: string;
  primary_ip_v6: string;
};

type PreflightCheck = {
  key: string;
  label: string;
  passed: boolean;
  fix_hint: string | null;
};

type CreateResponse = { id: string };

const ROLE_OPTS: SelectProps.Option[] = [
  { label: 'control plane', value: 'control_plane' },
  { label: 'worker',         value: 'worker' },
  { label: 'edge',           value: 'edge' },
];

// Default config carries the JSONB shape documented in
// docs/dev/region-deploy.md §4. Operators edit it as JSON in the
// Network step — a structured form for these fields lands in a
// follow-up once we know which knobs operators actually flip.
const DEFAULT_CONFIG = JSON.stringify(
  {
    pod_cidr_v6: 'fd00:site:42:1000::/56',
    svc_cidr_v6: 'fd00:site:42:2000::/108',
    lb_pool_v6:  'fd00:site:42:3000::/112',
    vip_v6:      'fd00:site:42:0::1',
    bgp_local_asn: 65042,
    bgp_peers: [{ address: 'fd00:site:42:0::ffff', asn: 65000 }],
    upstream_dns_v6: ['2001:4860:4860::8888'],
    lb_mode: 'snat',
    edge_mode: 'nat46',
  },
  null,
  2,
);

export function RegionDeployNewPage() {
  const nav = useNavigate();
  const [step, setStep] = useState(0);
  const [submitting, setSubmitting] = useState(false);

  // Step 1 — site + name.
  const [siteOpt, setSiteOpt] = useState<SelectProps.Option | null>(null);
  const [name, setName] = useState('');

  // Step 2 — nodes.
  const [nodes, setNodes] = useState<NodeRow[]>([
    { hostname: '', mac: '', role: 'control_plane', bmc_address: '', primary_ip_v6: '' },
  ]);

  // Step 3 — network config (JSON textarea for now).
  const [configJson, setConfigJson] = useState(DEFAULT_CONFIG);
  const [configErr, setConfigErr] = useState<string | null>(null);

  // Step 4 — service-stack feature flags. These flip booleans in the
  // config JSONB on submit; defaults match the doc's recommendations
  // (SNAT default, NAT46 default, NAT64+DNS64 + DSR opt-in only).
  const [dsrEnabled, setDsrEnabled] = useState(false);
  const [nat64Enabled, setNat64Enabled] = useState(false);

  // Created deployment + preflight result for the review step.
  const [createdId, setCreatedId] = useState<string | null>(null);
  const [preflightReady, setPreflightReady] = useState(false);
  const [preflightChecks, setPreflightChecks] = useState<PreflightCheck[]>([]);

  const sites = useList<Site>({
    resource: 'inventory/sites',
    pagination: { pageSize: 200 },
  });

  // ─── Step validation ──────────────────────────────────────────────
  // Each step refuses to advance unless its inputs are coherent. Keeps
  // the create-time validation server-side for the source of truth but
  // catches obvious mistakes before the operator commits.

  function step1Valid() {
    return !!siteOpt && name.trim().length > 0;
  }
  function step2Valid() {
    if (nodes.length === 0) return false;
    return nodes.every(
      (n) => n.hostname && n.mac && n.bmc_address,
    );
  }
  function step3Valid() {
    try {
      JSON.parse(configJson);
      setConfigErr(null);
      return true;
    } catch (e: unknown) {
      setConfigErr(e instanceof Error ? e.message : 'invalid JSON');
      return false;
    }
  }

  // ─── Step actions ────────────────────────────────────────────────

  async function submitCreate() {
    if (!step1Valid() || !step2Valid() || !step3Valid()) {
      toast.error('Fix earlier steps before continuing');
      return;
    }
    setSubmitting(true);
    try {
      // Layer the step-4 toggles on top of the JSON config. Doing the
      // overlay here (rather than mutating configJson live) keeps the
      // textarea representation stable as the operator clicks between
      // steps — toggling NAT64 doesn't rewrite the JSON they're
      // editing.
      const parsedConfig = JSON.parse(configJson);
      const mergedConfig = {
        ...parsedConfig,
        lb_mode: dsrEnabled ? 'dsr' : 'snat',
        nat64_enabled: nat64Enabled,
        // edge_mode stays 'nat46' (the default for v6-only sites).
        // NAT64+DNS64 is an add-on, not a replacement for the NAT46
        // edge LB — doc §2 Edge model note.
      };
      const res = await http.post<CreateResponse>('/region-deployments', {
        site_id: siteOpt!.value,
        name: name.trim(),
        config: mergedConfig,
        nodes: nodes.map((n) => ({
          hostname: n.hostname,
          mac: n.mac,
          role: n.role,
          bmc_address: n.bmc_address,
          primary_ip_v6: n.primary_ip_v6 || null,
        })),
      });
      setCreatedId(res.data.id);
      await runPreflight(res.data.id);
    } catch (e: unknown) {
      toast.error(
        `Create failed: ${e instanceof Error ? e.message : 'unknown error'}`,
      );
    } finally {
      setSubmitting(false);
    }
  }

  async function runPreflight(id: string) {
    type PreflightResponse = { ready: boolean; checks: PreflightCheck[] };
    const res = await http.get<PreflightResponse>(
      `/region-deployments/${id}/preflight`,
    );
    setPreflightReady(res.data.ready);
    setPreflightChecks(res.data.checks);
  }

  async function clickStart() {
    if (!createdId) return;
    setSubmitting(true);
    try {
      await http.post(`/region-deployments/${createdId}/start`, {});
      toast.success('Deploy started');
      nav(`/region-deploy/${createdId}`);
    } catch (e: unknown) {
      toast.error(
        `Start failed: ${e instanceof Error ? e.message : 'unknown error'}`,
      );
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description="New bare-metal Kubernetes cluster bring-up."
        >
          New region deployment
        </Header>
      }
    >
      <Wizard
        activeStepIndex={step}
        onNavigate={({ detail }) => {
          // Block forward navigation when the current step is invalid.
          if (detail.requestedStepIndex > step) {
            const ok =
              (step === 0 && step1Valid()) ||
              (step === 1 && step2Valid()) ||
              (step === 2 && step3Valid()) ||
              step === 3 || step === 4;
            if (!ok) return;
            // Entering step 5 (review) → create the row + run preflight.
            if (step === 3 && detail.requestedStepIndex === 4 && !createdId) {
              void submitCreate();
              return; // submitCreate advances on success
            }
          }
          setStep(detail.requestedStepIndex);
        }}
        onCancel={() => nav('/region-deploy')}
        isLoadingNextStep={submitting}
        steps={[
          {
            title: 'Site & basics',
            content: (
              <Container header={<Header variant="h2">Site & name</Header>}>
                <SpaceBetween size="m">
                  <FormField label="Site">
                    <Select
                      selectedOption={siteOpt}
                      options={(sites.result?.data ?? []).map((s) => ({
                        label: `${s.code} — ${s.name}`,
                        value: s.id,
                      }))}
                      onChange={({ detail }) => setSiteOpt(detail.selectedOption)}
                      placeholder="Pick a site"
                    />
                  </FormField>
                  <FormField label="Deployment name">
                    <Input
                      value={name}
                      onChange={({ detail }) => setName(detail.value)}
                      placeholder="e.g. site42-prod"
                    />
                  </FormField>
                </SpaceBetween>
              </Container>
            ),
          },
          {
            title: 'Nodes',
            content: (
              <Container
                header={
                  <Header
                    variant="h2"
                    actions={
                      <Button
                        onClick={() =>
                          setNodes([
                            ...nodes,
                            { hostname: '', mac: '', role: 'worker', bmc_address: '', primary_ip_v6: '' },
                          ])
                        }
                      >
                        Add row
                      </Button>
                    }
                  >
                    Node inventory
                  </Header>
                }
              >
                <Table
                  items={nodes}
                  trackBy={(_n) => String(nodes.indexOf(_n))}
                  columnDefinitions={[
                    {
                      id: 'hostname', header: 'Hostname',
                      cell: (n) => (
                        <Input
                          value={n.hostname}
                          onChange={({ detail }) => updateNode(setNodes, nodes, n, 'hostname', detail.value)}
                        />
                      ),
                    },
                    {
                      id: 'mac', header: 'MAC',
                      cell: (n) => (
                        <Input
                          value={n.mac}
                          onChange={({ detail }) => updateNode(setNodes, nodes, n, 'mac', detail.value)}
                          placeholder="02:00:00:00:00:01"
                        />
                      ),
                    },
                    {
                      id: 'role', header: 'Role',
                      cell: (n) => (
                        <Select
                          selectedOption={ROLE_OPTS.find((o) => o.value === n.role) ?? null}
                          options={ROLE_OPTS}
                          onChange={({ detail }) =>
                            updateNode(
                              setNodes, nodes, n, 'role',
                              (detail.selectedOption.value ?? 'worker') as NodeRow['role'],
                            )
                          }
                        />
                      ),
                    },
                    {
                      id: 'bmc', header: 'BMC address',
                      cell: (n) => (
                        <Input
                          value={n.bmc_address}
                          onChange={({ detail }) => updateNode(setNodes, nodes, n, 'bmc_address', detail.value)}
                        />
                      ),
                    },
                    {
                      id: 'ip6', header: 'Primary IPv6 (optional)',
                      cell: (n) => (
                        <Input
                          value={n.primary_ip_v6}
                          onChange={({ detail }) => updateNode(setNodes, nodes, n, 'primary_ip_v6', detail.value)}
                        />
                      ),
                    },
                    {
                      id: 'rm', header: '',
                      cell: (n) => (
                        <Button
                          variant="link"
                          onClick={() => setNodes(nodes.filter((x) => x !== n))}
                        >
                          Remove
                        </Button>
                      ),
                    },
                  ]}
                  empty={<Box textAlign="center">No nodes yet — add a row.</Box>}
                />
              </Container>
            ),
          },
          {
            title: 'Network',
            content: (
              <Container
                header={
                  <Header
                    variant="h2"
                    description="JSON config — pod/svc/LB prefixes, BGP peers, LB mode, edge mode."
                  >
                    Network configuration
                  </Header>
                }
              >
                <FormField errorText={configErr ?? undefined}>
                  <Textarea
                    value={configJson}
                    onChange={({ detail }) => setConfigJson(detail.value)}
                    rows={20}
                  />
                </FormField>
              </Container>
            ),
          },
          {
            title: 'Services',
            content: (
              <Container
                header={
                  <Header
                    variant="h2"
                    description="Advanced opt-ins. Defaults match the documented production stance."
                  >
                    Site services & advanced opt-ins
                  </Header>
                }
              >
                <SpaceBetween size="m">
                  <Alert type="info" header="Default selection: all four services">
                    Auth DNS, recursive DNS, DHCP, and the site collector
                    ship in every region deploy. Per-service version
                    pinning + replicas land in a later iteration of this
                    step.
                  </Alert>

                  <FormField
                    label="LB mode: DSR (Direct Server Return)"
                    description={
                      <>
                        Default <b>off</b> (SNAT). DSR preserves the
                        client IP and lowers latency, but requires
                        symmetric routing across the site fabric — no
                        strict uRPF or stateful firewalls between
                        clients and the cluster LB IPs. Enable per-site
                        only after confirming the upstream routers and
                        any in-path middleboxes won't drop asymmetric
                        replies.
                      </>
                    }
                  >
                    <Checkbox
                      checked={dsrEnabled}
                      onChange={({ detail }) => setDsrEnabled(detail.checked)}
                    >
                      Enable DSR
                    </Checkbox>
                  </FormField>

                  <FormField
                    label="Edge: NAT64 + DNS64"
                    description={
                      <>
                        Default <b>off</b>. NAT46 at the edge LB covers
                        every north-south case for typical workloads
                        (auth DNS, recursive DNS, DHCP, collector). Turn
                        this on only when v6-only pods at this site
                        must reach IPv4-only external endpoints —
                        deploys a Jool/tayga gateway + DNS64 zone in
                        Hickory.
                      </>
                    }
                  >
                    <Checkbox
                      checked={nat64Enabled}
                      onChange={({ detail }) => setNat64Enabled(detail.checked)}
                    >
                      Enable NAT64 + DNS64
                    </Checkbox>
                  </FormField>
                </SpaceBetween>
              </Container>
            ),
          },
          {
            title: 'Review & launch',
            content: (
              <SpaceBetween size="l">
                <Container header={<Header variant="h2">Summary</Header>}>
                  <SpaceBetween size="s">
                    <Box><b>Site:</b> {siteOpt?.label ?? '—'}</Box>
                    <Box><b>Name:</b> {name}</Box>
                    <Box><b>Nodes:</b> {nodes.length}</Box>
                    {createdId && (
                      <Box>
                        <b>Deployment ID:</b> {createdId}
                      </Box>
                    )}
                  </SpaceBetween>
                </Container>

                {createdId && (
                  <Container
                    header={
                      <Header
                        variant="h2"
                        description="All checks must pass before Start enables."
                      >
                        Pre-flight ({preflightReady ? 'ready' : 'blocked'})
                      </Header>
                    }
                  >
                    <Table
                      items={preflightChecks}
                      trackBy="key"
                      columnDefinitions={[
                        {
                          id: 'status', header: '',
                          cell: (c) =>
                            c.passed ? (
                              <StatusIndicator type="success">ok</StatusIndicator>
                            ) : (
                              <StatusIndicator type="error">fail</StatusIndicator>
                            ),
                        },
                        { id: 'label', header: 'Check', cell: (c) => c.label },
                        { id: 'hint', header: 'Fix hint', cell: (c) => c.fix_hint ?? '' },
                      ]}
                    />
                  </Container>
                )}

                <Form
                  actions={
                    <SpaceBetween direction="horizontal" size="xs">
                      <Button
                        variant="link"
                        onClick={() => nav('/region-deploy')}
                      >
                        Cancel
                      </Button>
                      <Button
                        variant="primary"
                        disabled={!createdId || !preflightReady || submitting}
                        loading={submitting}
                        onClick={clickStart}
                      >
                        Start deploy
                      </Button>
                    </SpaceBetween>
                  }
                >
                  {!createdId && (
                    <Box color="text-status-inactive">
                      Click <b>Next</b> on the previous step to create the
                      deployment row and run pre-flight.
                    </Box>
                  )}
                </Form>
              </SpaceBetween>
            ),
          },
        ]}
      />
    </ContentLayout>
  );
}

function updateNode<K extends keyof NodeRow>(
  setNodes: (xs: NodeRow[]) => void,
  nodes: NodeRow[],
  target: NodeRow,
  field: K,
  value: NodeRow[K],
) {
  setNodes(
    nodes.map((n) => (n === target ? { ...n, [field]: value } : n)),
  );
}
