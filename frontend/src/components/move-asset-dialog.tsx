import { useEffect, useMemo, useState } from 'react';
import { useList, useUpdate } from '@refinedev/core';
import { toast } from 'sonner';
import {
  Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { Label } from '@/components/ui/label';
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select';
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs';

type MoveAsset = {
  id: string;
  name: string;
  site_id: string;
  rack_id: string | null;
  rack_position_u: number | null;
  rack_units: number;
  face: 'front' | 'rear';
};
type Site = { id: string; code: string; name: string };
type Rack = { id: string; name: string; code: string; u_height: number; site_id: string };
type RackAsset = {
  id: string;
  rack_id: string | null;
  rack_position_u: number | null;
  rack_units: number | null;
  face: 'front' | 'rear';
  mount: 'rack' | 'vertical-left' | 'vertical-right';
};

type Props = {
  asset: MoveAsset | null;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onMoved?: () => void;
};

export function MoveAssetDialog({ asset, open, onOpenChange, onMoved }: Props) {
  const [siteId, setSiteId] = useState<string>('');
  const [rackId, setRackId] = useState<string>('');
  const [face, setFace] = useState<'front' | 'rear'>('front');
  const [positionU, setPositionU] = useState<string>('');

  // Reset form whenever the dialog re-opens for a different asset.
  useEffect(() => {
    if (!asset || !open) return;
    setSiteId(asset.site_id);
    setRackId(asset.rack_id ?? '');
    setFace(asset.face);
    setPositionU(asset.rack_position_u != null ? String(asset.rack_position_u) : '');
  }, [asset, open]);

  const sitesRes = useList<Site>({
    resource: 'inventory/sites',
    pagination: { pageSize: 200 },
    queryOptions: { enabled: open },
  });
  const racksRes = useList<Rack>({
    resource: 'inventory/racks',
    pagination: { pageSize: 200 },
    filters: siteId ? [{ field: 'site_id', operator: 'eq', value: siteId }] : [],
    queryOptions: { enabled: open && !!siteId },
  });
  const targetAssetsRes = useList<RackAsset>({
    resource: 'inventory/assets',
    pagination: { pageSize: 500 },
    filters: rackId ? [{ field: 'rack_id', operator: 'eq', value: rackId }] : [],
    queryOptions: { enabled: open && !!rackId },
  });

  const sites = sitesRes.result.data ?? [];
  const racks = racksRes.result.data ?? [];
  const targetAssets = targetAssetsRes.result.data ?? [];
  const targetRack = racks.find((r) => r.id === rackId);
  const units = Math.max(1, asset?.rack_units ?? 1);
  const u = positionU ? Number(positionU) : null;
  const top = u != null ? u + units - 1 : null;

  const occupied = useMemo(() => {
    const occ = new Set<number>();
    for (const a of targetAssets) {
      if (asset && a.id === asset.id) continue;
      if (a.mount !== 'rack') continue;
      if (a.face !== face) continue;
      if (a.rack_position_u == null) continue;
      const span = Math.max(1, a.rack_units || 1);
      for (let i = a.rack_position_u; i < a.rack_position_u + span; i++) occ.add(i);
    }
    return occ;
  }, [targetAssets, asset, face]);

  let validation: { kind: 'ok' | 'overflow' | 'collision' | 'unplaced'; msg: string } = {
    kind: 'unplaced', msg: 'Will be moved without a U position.',
  };
  if (u != null && top != null && targetRack) {
    if (u < 1 || top > targetRack.u_height) {
      validation = {
        kind: 'overflow',
        msg: `U${u}–U${top} overflows ${targetRack.u_height}U rack.`,
      };
    } else {
      const hits: number[] = [];
      for (let i = u; i <= top; i++) if (occupied.has(i)) hits.push(i);
      if (hits.length) {
        validation = {
          kind: 'collision',
          msg: `U${hits.join(', U')} already occupied on the ${face} face.`,
        };
      } else {
        validation = { kind: 'ok', msg: `Will occupy U${u}${units > 1 ? `–U${top}` : ''} on ${face}.` };
      }
    }
  }

  const updateMutation = useUpdate();
  const isPending = (updateMutation as any).isPending ?? (updateMutation as any).isLoading ?? false;
  const noChange =
    asset != null
    && rackId === (asset.rack_id ?? '')
    && face === asset.face
    && u === asset.rack_position_u;
  const canSubmit =
    !!asset && !!rackId && !isPending && !noChange
    && validation.kind !== 'overflow' && validation.kind !== 'collision';

  function submit() {
    if (!asset || !rackId) return;
    updateMutation.mutate(
      {
        resource: 'inventory/assets',
        id: asset.id,
        values: {
          rack_id: rackId,
          rack_position_u: u,
          face,
        },
        successNotification: false,
      },
      {
        onSuccess: () => {
          toast.success(
            `Moved ${asset.name}${targetRack ? ` to ${targetRack.code}` : ''}${u != null ? ` · U${u}` : ''}`,
          );
          onOpenChange(false);
          onMoved?.();
        },
        onError: (err: any) => toast.error(err?.message ?? 'Move failed'),
      },
    );
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Move {asset?.name ?? 'asset'}</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-1.5">
              <Label>Target site</Label>
              <Select
                value={siteId}
                onValueChange={(v) => { setSiteId(v); setRackId(''); }}
              >
                <SelectTrigger><SelectValue placeholder="Pick a site" /></SelectTrigger>
                <SelectContent>
                  {sites.map((s) => (
                    <SelectItem key={s.id} value={s.id}>{s.code} · {s.name}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-1.5">
              <Label>Target rack</Label>
              <Select
                value={rackId}
                onValueChange={setRackId}
                disabled={!siteId || racksRes.result.isFetching}
              >
                <SelectTrigger><SelectValue placeholder="Pick a rack" /></SelectTrigger>
                <SelectContent>
                  {racks.map((r) => (
                    <SelectItem key={r.id} value={r.id}>
                      {r.code} · {r.name} ({r.u_height}U)
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </div>
          <div className="grid grid-cols-[auto_1fr] gap-3">
            <div className="space-y-1.5">
              <Label>Face</Label>
              <Tabs value={face} onValueChange={(v) => setFace(v as 'front' | 'rear')}>
                <TabsList>
                  <TabsTrigger value="front">Front</TabsTrigger>
                  <TabsTrigger value="rear">Rear</TabsTrigger>
                </TabsList>
              </Tabs>
            </div>
            <div className="space-y-1.5">
              <Label>
                Position U {targetRack && <span className="text-muted-foreground">(1–{targetRack.u_height})</span>}
                {units > 1 && <span className="text-muted-foreground"> · {units}U device</span>}
              </Label>
              <Input
                type="number"
                inputMode="numeric"
                min={1}
                max={targetRack?.u_height}
                value={positionU}
                placeholder="leave blank to unplace"
                onChange={(e) => setPositionU(e.target.value)}
              />
            </div>
          </div>
          {asset && (
            <p
              className={
                validation.kind === 'ok' ? 'text-xs text-success' :
                validation.kind === 'unplaced' ? 'text-xs text-muted-foreground' :
                'text-xs font-medium text-destructive'
              }
            >
              {validation.msg}
            </p>
          )}
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button onClick={submit} disabled={!canSubmit}>
            {isPending ? 'Moving…' : 'Move'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
