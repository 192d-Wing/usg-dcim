import { useNavigate } from 'react-router';
import { useList } from '@refinedev/core';
import { useForm } from 'react-hook-form';
import { zodResolver } from '@hookform/resolvers/zod';
import { z } from 'zod';
import { ArrowLeft, Plus } from 'lucide-react';
import { http } from '@/lib/http';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import {
  Form, FormControl, FormDescription, FormField, FormItem, FormLabel, FormMessage,
} from '@/components/ui/form';
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select';
import { RackHeightPicker } from '@/components/rack-height-picker';
import { useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

type Site = { id: string; code: string; name: string };
type HierarchyItem = { id: string; code: string; name: string };

const schema = z.object({
  site_id: z.string().uuid(),
  building_id: z.string().uuid(),
  room_id: z.string().uuid(),
  row_id: z.string().uuid(),
  name: z.string().min(1),
  code: z.string().min(1),
  u_height: z.coerce.number().min(1).max(60).default(42),
  max_kw: z.string().optional(),
  serial: z.string().optional(),
});

export function RackCreatePage() {
  const nav = useNavigate();
  const qc = useQueryClient();
  const form = useForm<z.infer<typeof schema>>({
    resolver: zodResolver(schema),
    defaultValues: { u_height: 42 },
  });

  const siteId = form.watch('site_id');
  const buildingId = form.watch('building_id');
  const roomId = form.watch('room_id');

  const sites = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const buildings = useList<HierarchyItem>({
    resource: 'inventory/buildings',
    pagination: { pageSize: 200 },
    filters: siteId ? [{ field: 'site_id', operator: 'eq', value: siteId }] : [],
    queryOptions: { enabled: !!siteId },
  });
  const rooms = useList<HierarchyItem>({
    resource: 'inventory/rooms',
    pagination: { pageSize: 200 },
    filters: buildingId ? [{ field: 'building_id', operator: 'eq', value: buildingId }] : [],
    queryOptions: { enabled: !!buildingId },
  });
  const rows = useList<HierarchyItem>({
    resource: 'inventory/rows',
    pagination: { pageSize: 200 },
    filters: roomId ? [{ field: 'room_id', operator: 'eq', value: roomId }] : [],
    queryOptions: { enabled: !!roomId },
  });

  async function quickCreate(kind: 'building' | 'room' | 'row', label: string) {
    if (!label.trim()) return;
    try {
      const payload =
        kind === 'building' ? { site_id: siteId, name: label, code: label } :
        kind === 'room' ? { building_id: buildingId, name: label, code: label } :
        { room_id: roomId, name: label, code: label };
      const r = await http.post(`/inventory/${kind}s`, payload);
      toast.success(`${kind} created`);
      if (kind === 'building') {
        await qc.invalidateQueries({ queryKey: ['data', 'inventory/buildings'] });
        form.setValue('building_id', r.data.id);
        form.resetField('room_id');
        form.resetField('row_id');
      } else if (kind === 'room') {
        await qc.invalidateQueries({ queryKey: ['data', 'inventory/rooms'] });
        form.setValue('room_id', r.data.id);
        form.resetField('row_id');
      } else {
        await qc.invalidateQueries({ queryKey: ['data', 'inventory/rows'] });
        form.setValue('row_id', r.data.id);
      }
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  async function onSubmit(v: z.infer<typeof schema>) {
    try {
      const r = await http.post('/inventory/racks', {
        site_id: v.site_id,
        row_id: v.row_id,
        name: v.name,
        code: v.code,
        u_height: v.u_height,
        max_kw: v.max_kw ? Number(v.max_kw) : null,
        serial: v.serial || null,
      });
      toast.success('Rack created');
      nav(`/racks/${r.data.id}`);
    } catch (err: any) {
      toast.error(err?.message ?? 'failed');
    }
  }

  return (
    <div className="space-y-4">
      <Button variant="ghost" size="sm" onClick={() => nav('/racks')} className="-ml-2">
        <ArrowLeft className="h-4 w-4" /> All racks
      </Button>
      <Card className="max-w-2xl">
        <CardHeader>
          <CardTitle>New rack</CardTitle>
        </CardHeader>
        <CardContent>
          <Form {...form}>
            <form onSubmit={form.handleSubmit(onSubmit)} className="space-y-4">
              <FormField control={form.control} name="site_id" render={({ field }) => (
                <FormItem>
                  <FormLabel>Site</FormLabel>
                  <Select value={field.value ?? ''} onValueChange={(v) => {
                    field.onChange(v);
                    form.resetField('building_id'); form.resetField('room_id'); form.resetField('row_id');
                  }}>
                    <FormControl><SelectTrigger><SelectValue placeholder="Pick a site…" /></SelectTrigger></FormControl>
                    <SelectContent>
                      {(sites.result.data ?? []).map((s) => (
                        <SelectItem key={s.id} value={s.id}>{s.code} · {s.name}</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <FormMessage />
                </FormItem>
              )} />

              <CascadeRow
                label="Building" value={form.watch('building_id') ?? ''}
                disabled={!siteId}
                items={buildings.result.data ?? []}
                onChange={(v) => { form.setValue('building_id', v); form.resetField('room_id'); form.resetField('row_id'); }}
                onAdd={(label) => quickCreate('building', label)}
              />
              <CascadeRow
                label="Room" value={form.watch('room_id') ?? ''}
                disabled={!buildingId}
                items={rooms.result.data ?? []}
                onChange={(v) => { form.setValue('room_id', v); form.resetField('row_id'); }}
                onAdd={(label) => quickCreate('room', label)}
              />
              <CascadeRow
                label="Row" value={form.watch('row_id') ?? ''}
                disabled={!roomId}
                items={rows.result.data ?? []}
                onChange={(v) => form.setValue('row_id', v)}
                onAdd={(label) => quickCreate('row', label)}
              />

              <div className="grid grid-cols-2 gap-3">
                <FormField control={form.control} name="name" render={({ field }) => (
                  <FormItem><FormLabel>Name</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
                )} />
                <FormField control={form.control} name="code" render={({ field }) => (
                  <FormItem><FormLabel>Code</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
                )} />
              </div>
              <FormField control={form.control} name="u_height" render={({ field }) => (
                <FormItem>
                  <FormLabel>Rack height</FormLabel>
                  <FormControl>
                    <RackHeightPicker value={Number(field.value) || 42} onChange={field.onChange} />
                  </FormControl>
                  <FormMessage />
                </FormItem>
              )} />
              <div className="grid grid-cols-2 gap-3">
                <FormField control={form.control} name="max_kw" render={({ field }) => (
                  <FormItem><FormLabel>Max kW</FormLabel><FormControl><Input type="number" step="0.1" {...field} /></FormControl><FormMessage /></FormItem>
                )} />
                <FormField control={form.control} name="serial" render={({ field }) => (
                  <FormItem><FormLabel>Serial</FormLabel><FormControl><Input {...field} /></FormControl><FormMessage /></FormItem>
                )} />
              </div>
              <Button type="submit" disabled={form.formState.isSubmitting}>
                {form.formState.isSubmitting ? 'Creating…' : 'Create rack'}
              </Button>
            </form>
          </Form>
        </CardContent>
      </Card>
    </div>
  );
}

function CascadeRow({
  label, value, disabled, items, onChange, onAdd,
}: {
  label: string;
  value: string;
  disabled: boolean;
  items: { id: string; code: string; name: string }[];
  onChange: (v: string) => void;
  onAdd: (label: string) => void;
}) {
  return (
    <FormItem>
      <FormLabel>{label}</FormLabel>
      <div className="flex gap-2">
        <Select value={value} onValueChange={onChange} disabled={disabled}>
          <SelectTrigger className="flex-1">
            <SelectValue placeholder={disabled ? 'Pick the parent first' : `Select a ${label.toLowerCase()}…`} />
          </SelectTrigger>
          <SelectContent>
            {items.map((o) => <SelectItem key={o.id} value={o.id}>{o.code} · {o.name}</SelectItem>)}
          </SelectContent>
        </Select>
        <QuickAddButton disabled={disabled} label={label} onAdd={onAdd} />
      </div>
      {!disabled && items.length === 0 && (
        <FormDescription>None yet — use <strong>+ New</strong> to create one.</FormDescription>
      )}
    </FormItem>
  );
}

function QuickAddButton({ disabled, label, onAdd }: { disabled: boolean; label: string; onAdd: (label: string) => void }) {
  return (
    <Button
      type="button" variant="outline" size="sm" disabled={disabled}
      onClick={() => {
        const v = window.prompt(`New ${label.toLowerCase()} (used for both name and code):`);
        if (v) onAdd(v.trim());
      }}
    >
      <Plus className="h-4 w-4" /> New
    </Button>
  );
}
