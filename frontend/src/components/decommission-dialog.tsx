import { useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { toast } from 'sonner';

import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Checkbox from '@cloudscape-design/components/checkbox';
import ColumnLayout from '@cloudscape-design/components/column-layout';
import Form from '@cloudscape-design/components/form';
import FormField from '@cloudscape-design/components/form-field';
import Input from '@cloudscape-design/components/input';
import Modal from '@cloudscape-design/components/modal';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Spinner from '@cloudscape-design/components/spinner';

import { http } from '@/lib/http';

type AssetMini = {
  id: string;
  name: string;
  kind: string;
  serial: string | null;
  lifecycle_state: string;
};

type DecommissionImpact = {
  consumer_drops: number;
  pdu_drops: number;
  downstream_assets: string[];
};

type DecommissionResult = {
  asset: { id: string; lifecycle_state: string };
  impact: DecommissionImpact;
};

function plural(n: number, singular: string, pluralForm?: string): string {
  const word = n === 1 ? singular : (pluralForm ?? singular + 's');
  return n + ' ' + word;
}

function formatImpactDetail(impact: DecommissionImpact): string {
  const total = impact.consumer_drops + impact.pdu_drops;
  if (total === 0) return 'no power connections affected';
  const conn = plural(total, 'power connection') + ' dropped';
  const downstream = impact.downstream_assets.length;
  if (downstream === 0) return conn;
  return conn + ', ' + plural(downstream, 'downstream asset') + ' unpowered';
}

export function DecommissionDialog({
  asset, open, onOpenChange, onDecommissioned,
}: Readonly<{
  asset: AssetMini | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDecommissioned: () => void;
}>) {
  const [reason, setReason] = useState('');
  const [sanitization, setSanitization] = useState('');
  const [confirmName, setConfirmName] = useState('');
  const [acknowledged, setAcknowledged] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});

  useEffect(() => {
    if (open) {
      setReason('');
      setSanitization('');
      setConfirmName('');
      setAcknowledged(false);
      setErrors({});
    }
  }, [open, asset?.id]);

  // Pre-flight impact preview — counts the power connections that
  // would drop and the downstream asset names that would lose power
  // BEFORE the operator commits. Read-only on the backend; runs only
  // when the dialog is open + we have an asset.
  const impactQ = useQuery({
    enabled: open && asset !== null,
    queryKey: ['decommission-preview', asset?.id],
    queryFn: async () => (
      await http.get<DecommissionImpact>(
        `/inventory/assets/${asset!.id}/decommission/preview`,
      )
    ).data,
  });
  const impact = impactQ.data ?? null;

  if (!asset) return null;

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!asset) return;
    const errs: Record<string, string> = {};
    if (reason.trim().length < 3) errs.reason = 'Reason required';
    if (sanitization.trim().length < 3) errs.sanitization = 'Sanitization note required';
    if (confirmName.trim() !== asset.name) errs.confirm = `must match "${asset.name}"`;
    if (!acknowledged) errs.ack = 'You must confirm';
    setErrors(errs);
    if (Object.keys(errs).length > 0) return;

    setSubmitting(true);
    try {
      const resp = await http.post<DecommissionResult>(
        `/inventory/assets/${asset.id}/decommission`,
        { sanitization_note: sanitization, reason },
      );
      // Surface the actual blast radius in the toast — operators
      // routinely don't open the audit log after every action, so
      // burying the count there only loses information.
      toast.success(`${asset.name} decommissioned — ${formatImpactDetail(resp.data.impact)}`);
      onDecommissioned();
      onOpenChange(false);
    } catch (err: any) {
      toast.error(err?.message ?? 'decommission failed');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal
      visible={open}
      onDismiss={() => onOpenChange(false)}
      header="Decommission asset"
      size="medium"
    >
      <SpaceBetween size="m">
        <Box>
          <ColumnLayout columns={2} variant="text-grid">
            <div>
              <Box variant="awsui-key-label">Name</Box>
              <Box>{asset.name}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Kind</Box>
              <Box>{asset.kind}</Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Serial</Box>
              <Box>
                <span style={{ fontFamily: 'ui-monospace, monospace' }}>{asset.serial ?? '—'}</span>
              </Box>
            </div>
            <div>
              <Box variant="awsui-key-label">Current state</Box>
              <Box>{asset.lifecycle_state}</Box>
            </div>
          </ColumnLayout>
        </Box>

        <Alert type="warning" header="This action will:">
          <ul style={{ margin: 0, paddingLeft: 20 }}>
            <li>flip the asset's lifecycle state to <span style={{ fontFamily: 'ui-monospace, monospace' }}>decommissioned</span></li>
            <li>drop every power connection that lands on this asset</li>
            {asset.kind === 'pdu' && (
              <li>drop power connections served by this PDU's outlets (downstream devices will go unpowered)</li>
            )}
            <li>append an audit-log entry with your sanitization note + reason</li>
          </ul>
          <Box color="text-status-inactive" fontSize="body-s" padding={{ top: 'xs' }}>
            The asset itself stays in inventory so historical reports keep resolving — flip to{' '}
            <span style={{ fontFamily: 'ui-monospace, monospace' }}>retired</span> later to fully archive.
          </Box>
        </Alert>

        {/* Pre-flight impact preview — pulled from the backend on dialog
            open. Surfaces the exact blast radius so operators don't
            commit blind, and elevates to a red banner when downstream
            devices would lose power. */}
        {impactQ.isLoading && (
          <Box color="text-status-inactive">
            <Spinner /> <span style={{ marginLeft: 8 }}>Computing impact…</span>
          </Box>
        )}
        {impact && (() => {
          const total = impact.consumer_drops + impact.pdu_drops;
          const downstream = impact.downstream_assets.length;
          if (total === 0) {
            return (
              <Alert type="info" header="No power connections to drop">
                This asset has no power connections wired. The decommission
                only flips the lifecycle state.
              </Alert>
            );
          }
          return (
            <Alert
              type={downstream > 0 ? 'error' : 'warning'}
              header={`Impact: ${formatImpactDetail(impact)}`}
            >
              <ul style={{ margin: 0, paddingLeft: 20 }}>
                {impact.consumer_drops > 0 && (
                  <li>
                    {plural(impact.consumer_drops, 'incoming power connection')}{' '}
                    (this asset as consumer)
                  </li>
                )}
                {impact.pdu_drops > 0 && (
                  <li>
                    {plural(impact.pdu_drops, 'outgoing power connection')}{' '}
                    served by this PDU's outlets
                  </li>
                )}
              </ul>
              {downstream > 0 && (
                <Box padding={{ top: 'xs' }}>
                  <Box variant="awsui-key-label">Downstream assets that will lose power:</Box>
                  <Box>
                    <span style={{ fontFamily: 'ui-monospace, monospace' }}>
                      {impact.downstream_assets.join(', ')}
                    </span>
                  </Box>
                </Box>
              )}
            </Alert>
          );
        })()}

        <form onSubmit={onSubmit}>
          <Form
            actions={
              <SpaceBetween size="xs" direction="horizontal">
                <Button onClick={() => onOpenChange(false)} variant="link">Cancel</Button>
                <Button variant="primary" formAction="submit" loading={submitting}>
                  {submitting ? 'Decommissioning…' : 'Decommission'}
                </Button>
              </SpaceBetween>
            }
          >
            <SpaceBetween size="m">
              <FormField label="Reason" errorText={errors.reason}>
                <Input
                  value={reason}
                  onChange={({ detail }) => setReason(detail.value)}
                  placeholder="e.g. EOL replacement, hardware failure"
                />
              </FormField>
              <FormField label="Sanitization note" errorText={errors.sanitization}>
                <Input
                  value={sanitization}
                  onChange={({ detail }) => setSanitization(detail.value)}
                  placeholder="e.g. NIST 800-88 purge complete, certificate #DC-2026-0123"
                />
              </FormField>
              <FormField label="Type the asset name to confirm" errorText={errors.confirm}>
                <Input
                  value={confirmName}
                  onChange={({ detail }) => setConfirmName(detail.value)}
                  placeholder={asset.name}
                />
              </FormField>
              <FormField errorText={errors.ack}>
                <Checkbox
                  checked={acknowledged}
                  onChange={({ detail }) => setAcknowledged(detail.checked)}
                >
                  I understand power connections will be dropped
                </Checkbox>
              </FormField>
            </SpaceBetween>
          </Form>
        </form>
      </SpaceBetween>
    </Modal>
  );
}
