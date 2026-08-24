// API tokens — Cloudscape table with issue Modal + plaintext-reveal
// Modal. Capabilities are picked from a checkbox list bounded by the
// caller's own capabilities (you can't grant what you don't hold).
// Wildcard (`*`) holders are the exception: their only literal cap is
// `*`, so the picker is populated from the admin capability catalog
// instead — otherwise a full admin literally could not issue a
// granular token.

import { useMemo, useState } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useGetIdentity } from '@refinedev/core';
import { toast } from 'sonner';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import ContentLayout from '@cloudscape-design/components/content-layout';
import Container from '@cloudscape-design/components/container';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Header from '@cloudscape-design/components/header';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import Table from '@cloudscape-design/components/table';

import { hasCap } from '@/lib/caps';
import { http } from '@/lib/http';
import { formatDate } from '@/lib/utils';

type Token = {
  id: string;
  name: string;
  permission_codes: string[];
  scope_json: Record<string, unknown>;
  created_at: string;
  expires_at: string | null;
  last_used_at: string | null;
  revoked: boolean;
  plaintext?: string | null;
};

// Wire shape of GET /admin/capabilities/catalog (see otter-go
// internal/admin/capabilities.go): domain → resource → [actions],
// plus two-segment specialty codes keyed by code.
type CapabilityCatalog = {
  catalog: Record<string, Record<string, string[]>>;
  specialties: Record<string, string>;
};

/** Flatten the catalog to sorted `domain:resource:action` codes plus
 *  the specialty codes. */
function flattenCatalog(cat: CapabilityCatalog): string[] {
  const codes: string[] = [];
  for (const [domain, resources] of Object.entries(cat.catalog)) {
    for (const [resource, actions] of Object.entries(resources)) {
      for (const action of actions) codes.push(`${domain}:${resource}:${action}`);
    }
  }
  codes.push(...Object.keys(cat.specialties));
  return codes.sort((a, b) => a.localeCompare(b));
}

export function TokensPage() {
  const qc = useQueryClient();
  const { data: identity } = useGetIdentity<{ email: string | null; capabilities: string[] }>();
  const canManage = hasCap(identity?.capabilities, 'admin:api-tokens:read');
  const myCaps = identity?.capabilities ?? [];

  const tokens = useQuery({
    queryKey: ['tokens'],
    queryFn: async () => (await http.get<Token[]>('/auth/tokens')).data,
    enabled: canManage,
  });

  // A wildcard holder's literal capability list is just ['*'] — useless
  // as a granular picker. Populate from the capability catalog instead
  // (the endpoint is gated on admin:roles:read, which `*` grants).
  const isWildcard = myCaps.includes('*');
  const catalogQ = useQuery({
    queryKey: ['capability-catalog'],
    queryFn: async () => (await http.get<CapabilityCatalog>('/admin/capabilities/catalog')).data,
    enabled: canManage && isWildcard,
  });
  const offeredCaps = useMemo(() => {
    if (!isWildcard) return myCaps; // non-wildcard callers keep the old bound list
    if (!catalogQ.data) return myCaps; // catalog still loading → at least offer `*`
    return ['*', ...flattenCatalog(catalogQ.data)];
  }, [isWildcard, myCaps, catalogQ.data]);

  const [createOpen, setCreateOpen] = useState(false);
  const [issued, setIssued] = useState<Token | null>(null);

  async function revoke(t: Token) {
    if (!window.confirm(`Revoke token "${t.name}"? Existing clients using this token will fail.`)) {
      return;
    }
    try {
      await http.delete(`/auth/tokens/${t.id}`);
      toast.success('Token revoked');
      await qc.invalidateQueries({ queryKey: ['tokens'] });
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to revoke');
    }
  }

  if (!canManage) {
    return (
      <ContentLayout header={<Header variant="h1">API tokens</Header>}>
        <Box color="text-status-inactive">
          You don't have the <code style={{ fontFamily: 'ui-monospace, monospace' }}>admin:api-tokens:read</code>{' '}
          capability. Ask an admin to grant a role that includes it (e.g. RegionalAdmin,
          EnterpriseAdmin).
        </Box>
      </ContentLayout>
    );
  }

  const data = tokens.data ?? [];

  return (
    <ContentLayout
      header={
        <Header
          variant="h1"
          description={`Tokens issued to ${identity?.email ?? ''} · plaintext shown once at creation`}
        >
          API tokens
        </Header>
      }
    >
      <Table<Token>
        variant="container"
        loading={tokens.isLoading}
        loadingText="Loading tokens…"
        items={data}
        trackBy="id"
        header={
          <Header
            counter={`(${data.length})`}
            actions={
              <Button variant="primary" iconName="add-plus" onClick={() => setCreateOpen(true)}>
                Issue token
              </Button>
            }
          >
            Tokens
          </Header>
        }
        columnDefinitions={[
          { id: 'name', header: 'Name', cell: (t) => <span style={{ fontWeight: 500 }}>{t.name}</span> },
          {
            id: 'capabilities', header: 'Capabilities',
            cell: (t) => t.permission_codes.length === 0
              ? <Box variant="span" color="text-status-inactive" fontSize="body-s">none</Box>
              : (
                <SpaceBetween size="xxs" direction="horizontal">
                  {t.permission_codes.map((c) => <Badge key={c}>{c}</Badge>)}
                </SpaceBetween>
              ),
          },
          {
            id: 'created', header: 'Created',
            cell: (t) => <Box variant="span" color="text-status-inactive" fontSize="body-s">{formatDate(t.created_at)}</Box>,
            width: 180,
          },
          {
            id: 'expires', header: 'Expires',
            cell: (t) => <Box variant="span" color="text-status-inactive" fontSize="body-s">{t.expires_at ? formatDate(t.expires_at) : 'never'}</Box>,
            width: 180,
          },
          {
            id: 'last_used', header: 'Last used',
            cell: (t) => <Box variant="span" color="text-status-inactive" fontSize="body-s">{t.last_used_at ? formatDate(t.last_used_at) : 'never'}</Box>,
            width: 180,
          },
          {
            id: 'status', header: 'Status',
            cell: (t) => t.revoked
              ? <StatusIndicator type="stopped">revoked</StatusIndicator>
              : <StatusIndicator type="success">active</StatusIndicator>,
            width: 120,
          },
          {
            id: 'actions', header: '',
            cell: (t) => t.revoked
              ? null
              : <Button iconName="remove" variant="inline-icon" onClick={() => revoke(t)} ariaLabel={`Revoke ${t.name}`} />,
            width: 60,
          },
        ]}
        empty={
          <Box textAlign="center" color="inherit" padding="m">
            No tokens issued yet. Create one to authenticate scripts or integrations.
          </Box>
        }
      />

      <Modal
        visible={createOpen}
        onDismiss={() => setCreateOpen(false)}
        header="Issue API token"
        size="medium"
      >
        <IssueForm
          myCapabilities={offeredCaps}
          onIssued={async (t) => {
            setCreateOpen(false);
            setIssued(t);
            await qc.invalidateQueries({ queryKey: ['tokens'] });
          }}
        />
      </Modal>

      <Modal
        visible={issued !== null}
        onDismiss={() => setIssued(null)}
        header="Token created — copy it now"
        size="large"
      >
        {issued && <PlaintextReveal token={issued} />}
      </Modal>
    </ContentLayout>
  );
}

function IssueForm({
  myCapabilities, onIssued,
}: Readonly<{
  myCapabilities: string[];
  onIssued: (token: Token) => void;
}>) {
  const [name, setName] = useState('');
  const [selected, setSelected] = useState<string[]>([]);
  const [expiresAt, setExpiresAt] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [nameErr, setNameErr] = useState<string | undefined>();
  const [capsErr, setCapsErr] = useState<string | undefined>();

  function toggle(code: string, checked: boolean) {
    setSelected((cur) => checked ? [...cur, code] : cur.filter((c) => c !== code));
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    const nameOk = name.trim().length > 0;
    const capsOk = selected.length > 0;
    setNameErr(nameOk ? undefined : 'Name required');
    setCapsErr(capsOk ? undefined : 'Pick at least one capability');
    if (!nameOk || !capsOk) return;
    setSubmitting(true);
    try {
      const r = await http.post<Token>('/auth/tokens', {
        name,
        permission_codes: selected,
        scope_json: {},
        expires_at: expiresAt ? new Date(expiresAt).toISOString() : null,
      });
      toast.success('Token issued');
      onIssued(r.data);
    } catch (err: any) {
      toast.error(err?.message ?? 'failed to issue token');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <form onSubmit={onSubmit}>
      <Form
        actions={
          <Button variant="primary" formAction="submit" loading={submitting}>
            {submitting ? 'Issuing…' : 'Issue token'}
          </Button>
        }
      >
        <SpaceBetween size="m">
          <FormField label="Name" errorText={nameErr}>
            <Input
              value={name}
              onChange={({ detail }) => setName(detail.value)}
              placeholder="e.g. ansible-collector-bootstrap"
            />
          </FormField>
          <FormField
            label="Capabilities"
            description="You can only grant capabilities you hold. Pick the smallest set the token needs."
            errorText={capsErr}
          >
            {/* Two-column scrollable list of the grantable capabilities
                (the caller's own caps, or the full catalog for `*`
                holders). Cloudscape doesn't ship a native multi-select
                for this kind of list, so we render Checkboxes in a
                Box-bounded grid. */}
            {myCapabilities.length === 0 ? (
              <Box color="text-status-inactive" fontSize="body-s">
                Your account has no capabilities to delegate.
              </Box>
            ) : (
              <Box
                padding="s"
                // Style props on Cloudscape Box don't take arbitrary
                // CSS, so the grid layout uses an inline style.
              >
                <div style={{
                  display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 6,
                  maxHeight: 200, overflowY: 'auto',
                }}>
                  {myCapabilities.map((c) => (
                    <Checkbox
                      key={c}
                      checked={selected.includes(c)}
                      onChange={({ detail }) => toggle(c, detail.checked)}
                    >
                      <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12 }}>{c}</span>
                    </Checkbox>
                  ))}
                </div>
              </Box>
            )}
          </FormField>
          <FormField label="Expires (optional)">
            <Input
              type="text"
              placeholder="YYYY-MM-DDTHH:MM"
              value={expiresAt}
              onChange={({ detail }) => setExpiresAt(detail.value)}
            />
          </FormField>
        </SpaceBetween>
      </Form>
    </form>
  );
}

function PlaintextReveal({ token }: Readonly<{ token: Token }>) {
  const [copied, setCopied] = useState(false);
  if (!token.plaintext) {
    return <Box color="text-status-inactive">Plaintext not available.</Box>;
  }
  async function copy() {
    if (!token.plaintext) return;
    await navigator.clipboard.writeText(token.plaintext);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }
  return (
    <SpaceBetween size="m">
      <Box>Copy the token now — it cannot be displayed again.</Box>
      <Container>
        <SpaceBetween size="xs" direction="horizontal">
          <Box variant="span">
            <code style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12, wordBreak: 'break-all' }}>
              {token.plaintext}
            </code>
          </Box>
          <Button onClick={copy} iconName={copied ? 'status-positive' : 'copy'}>
            {copied ? 'Copied' : 'Copy'}
          </Button>
        </SpaceBetween>
      </Container>
      <Container header={<Header variant="h3">Use it</Header>}>
        <pre style={{
          fontFamily: 'ui-monospace, monospace', fontSize: 11,
          whiteSpace: 'pre-wrap', wordBreak: 'break-all', margin: 0,
        }}>
{`curl -H "Authorization: Bearer ${token.plaintext}" \\
  https://your-dcim/api/v1/auth/me`}
        </pre>
      </Container>
    </SpaceBetween>
  );
}
