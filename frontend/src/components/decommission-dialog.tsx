import { useState } from 'react';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { AlertTriangle, ShieldCheck } from 'lucide-react';
import { http } from '@/lib/http';
import { Button } from '@/components/ui/button';
import {
  Dialog, DialogContent, DialogHeader, DialogTitle,
} from '@/components/ui/dialog';
import {
  Form, FormControl, FormField, FormItem, FormLabel, FormMessage,
} from '@/components/ui/form';
import { Input } from '@/components/ui/input';
import { toast } from 'sonner';

type AssetMini = {
  id: string;
  name: string;
  kind: string;
  serial: string | null;
  lifecycle_state: string;
};

const formSchema = z.object({
  confirm_name: z.string(),
  sanitization_note: z.string().min(3, 'Sanitization note required'),
  reason: z.string().min(3, 'Reason required'),
  acknowledged: z.literal(true, { errorMap: () => ({ message: 'You must confirm' }) }),
});

export function DecommissionDialog({
  asset, open, onOpenChange, onDecommissioned,
}: {
  asset: AssetMini | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDecommissioned: () => void;
}) {
  const form = useForm<z.infer<typeof formSchema>>({
    resolver: zodResolver(formSchema),
    defaultValues: {
      confirm_name: '',
      sanitization_note: '',
      reason: '',
      acknowledged: false as unknown as true,
    },
  });
  const [submitting, setSubmitting] = useState(false);

  // Reset the form whenever the dialog opens for a new asset.
  if (asset && form.formState.defaultValues?.confirm_name === '' && open === false) {
    // no-op — keep defaults; reset happens on close below.
  }

  async function onSubmit(v: z.infer<typeof formSchema>) {
    if (!asset) return;
    if (v.confirm_name.trim() !== asset.name) {
      form.setError('confirm_name', { message: `must match "${asset.name}"` });
      return;
    }
    setSubmitting(true);
    try {
      await http.post(`/inventory/assets/${asset.id}/decommission`, {
        sanitization_note: v.sanitization_note,
        reason: v.reason,
      });
      toast.success(`${asset.name} decommissioned`);
      form.reset();
      onDecommissioned();
      onOpenChange(false);
    } catch (err: any) {
      toast.error(err?.message ?? 'decommission failed');
    } finally {
      setSubmitting(false);
    }
  }

  function handleOpenChange(o: boolean) {
    if (!o) form.reset();
    onOpenChange(o);
  }

  if (!asset) return null;

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <AlertTriangle className="h-4 w-4 text-warning" /> Decommission asset
          </DialogTitle>
        </DialogHeader>

        <div className="rounded-md border bg-muted/30 p-3 text-xs space-y-1">
          <div><span className="text-muted-foreground">Name:</span> <span className="font-medium">{asset.name}</span></div>
          <div><span className="text-muted-foreground">Kind:</span> {asset.kind}</div>
          <div><span className="text-muted-foreground">Serial:</span> <span className="font-mono">{asset.serial ?? '—'}</span></div>
          <div><span className="text-muted-foreground">Current state:</span> {asset.lifecycle_state}</div>
        </div>

        <div className="rounded-md border border-warning/40 bg-warning/10 p-3 text-xs">
          <p className="font-medium text-warning">This action will:</p>
          <ul className="mt-1 list-disc pl-5 text-foreground/80 space-y-0.5">
            <li>flip the asset's lifecycle state to <span className="font-mono">decommissioned</span></li>
            <li>drop every power connection that lands on this asset</li>
            {asset.kind === 'pdu' && (
              <li>drop power connections served by this PDU's outlets (downstream devices will go unpowered)</li>
            )}
            <li>append an audit-log entry with your sanitization note + reason</li>
          </ul>
          <p className="mt-2 text-muted-foreground">
            The asset itself stays in inventory so historical reports keep resolving — flip to{' '}
            <span className="font-mono">retired</span> later to fully archive.
          </p>
        </div>

        <Form {...form}>
          <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
            <FormField control={form.control} name="reason" render={({ field }) => (
              <FormItem>
                <FormLabel>Reason</FormLabel>
                <FormControl>
                  <Input placeholder="e.g. EOL replacement, hardware failure" {...field} />
                </FormControl>
                <FormMessage />
              </FormItem>
            )} />
            <FormField control={form.control} name="sanitization_note" render={({ field }) => (
              <FormItem>
                <FormLabel className="flex items-center gap-1.5">
                  <ShieldCheck className="h-3.5 w-3.5" /> Sanitization note
                </FormLabel>
                <FormControl>
                  <Input placeholder="e.g. NIST 800-88 purge complete, certificate #DC-2026-0123" {...field} />
                </FormControl>
                <FormMessage />
              </FormItem>
            )} />
            <FormField control={form.control} name="confirm_name" render={({ field }) => (
              <FormItem>
                <FormLabel>Type the asset name to confirm</FormLabel>
                <FormControl>
                  <Input placeholder={asset.name} className="font-mono" {...field} />
                </FormControl>
                <FormMessage />
              </FormItem>
            )} />
            <FormField control={form.control} name="acknowledged" render={({ field }) => (
              <FormItem className="flex items-center gap-3 space-y-0">
                <FormControl>
                  <input
                    type="checkbox"
                    className="h-4 w-4"
                    checked={field.value === true}
                    onChange={(e) => field.onChange(e.target.checked)}
                  />
                </FormControl>
                <FormLabel className="!mt-0 text-sm font-normal">
                  I understand power connections will be dropped
                </FormLabel>
                <FormMessage />
              </FormItem>
            )} />
            <div className="flex justify-end gap-2">
              <Button type="button" variant="outline" onClick={() => handleOpenChange(false)}>
                Cancel
              </Button>
              <Button type="submit" variant="destructive" disabled={submitting}>
                {submitting ? 'Decommissioning…' : 'Decommission'}
              </Button>
            </div>
          </form>
        </Form>
      </DialogContent>
    </Dialog>
  );
}
