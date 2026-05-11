import { useEffect, useState } from 'react';
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

import { http } from '@/lib/http';

type AssetMini = {
  id: string;
  name: string;
  kind: string;
  serial: string | null;
  lifecycle_state: string;
};

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
      await http.post(`/inventory/assets/${asset.id}/decommission`, {
        sanitization_note: sanitization,
        reason,
      });
      toast.success(`${asset.name} decommissioned`);
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
