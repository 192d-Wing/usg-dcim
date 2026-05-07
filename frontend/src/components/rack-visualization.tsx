import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router';
import {
  DndContext, DragEndEvent, DragOverlay, DragStartEvent,
  KeyboardSensor, MouseSensor, TouchSensor, useDraggable, useDroppable, useSensor, useSensors,
} from '@dnd-kit/core';
import { useUpdate } from '@refinedev/core';
import { useQueryClient } from '@tanstack/react-query';
import { resolveStencil, Stencil, useStencilCatalog } from './stencil';
import { Badge } from '@/components/ui/badge';
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs';
import { hasCapability } from '@/lib/access-control-provider';
import { cn } from '@/lib/utils';
import { toast } from 'sonner';

export type VizAsset = {
  id: string;
  name: string;
  hostname: string | null;
  kind: string;
  manufacturer?: string | null;
  model?: string | null;
  rack_position_u: number | null;
  rack_units: number;
  open_alerts: number;
  face?: 'front' | 'rear';
  mount?: 'rack' | 'vertical-left' | 'vertical-right';
  pdu_side?: 'A' | 'B' | 'C' | null;
  redundancy?: 'redundant' | 'single' | 'unpowered' | 'n/a';
};

type StyleMode = 'block' | 'stencil';

const KIND_COLOR: Record<string, string> = {
  server: '#3b82f6', switch: '#10b981', router: '#14b8a6', pdu: '#f97316',
  ups: '#a855f7', crac: '#06b6d4', sensor: '#facc15', storage: '#6366f1',
  chassis: '#94a3b8', blade: '#64748b', other: '#737373',
};
const colorFor = (k: string) => KIND_COLOR[k] ?? KIND_COLOR.other;

type Props = {
  rackId: string;
  uHeight: number;
  assets: VizAsset[];
  mode?: StyleMode;
  uPx?: number;
};

const VERT_W = 22;
const RACK_BODY_W = 240;

export function RackVisualization({ rackId, uHeight, assets, mode = 'stencil', uPx = 18 }: Props) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const updateMutation = useUpdate();
  const { data: catalog } = useStencilCatalog();
  const canWrite = hasCapability('inventory:write');

  const [face, setFace] = useState<'front' | 'rear'>('front');
  const [draggingId, setDraggingId] = useState<string | null>(null);
  const [hoverU, setHoverU] = useState<number | null>(null);

  const verticalLeft = assets.filter(
    (a) => a.mount === 'vertical-left' && (a.face ?? 'front') === face,
  );
  const verticalRight = assets.filter(
    (a) => a.mount === 'vertical-right' && (a.face ?? 'front') === face,
  );
  const rackMountThisFace = assets.filter(
    (a) => (a.mount ?? 'rack') === 'rack' && (a.face ?? 'front') === face,
  );
  const rackMountOtherFace = assets.filter(
    (a) => (a.mount ?? 'rack') === 'rack' && (a.face ?? 'front') !== face,
  );

  const occupiedExcludingDragged = useMemo(() => {
    const occ = new Set<number>();
    for (const a of rackMountThisFace) {
      if (a.id === draggingId) continue;
      if (!a.rack_position_u || a.rack_position_u < 1 || a.rack_position_u > uHeight) continue;
      const span = Math.max(1, a.rack_units || 1);
      for (let u = a.rack_position_u; u < a.rack_position_u + span; u++) occ.add(u);
    }
    return occ;
  }, [rackMountThisFace, draggingId, uHeight]);

  const draggingAsset = draggingId ? assets.find((a) => a.id === draggingId) ?? null : null;
  const draggingSpan = Math.max(1, draggingAsset?.rack_units ?? 1);
  const fitsAt = (startU: number): boolean => {
    if (!draggingAsset) return false;
    if (startU < 1 || startU + draggingSpan - 1 > uHeight) return false;
    for (let u = startU; u < startU + draggingSpan; u++) {
      if (occupiedExcludingDragged.has(u)) return false;
    }
    return true;
  };

  const sensors = useSensors(
    useSensor(MouseSensor, { activationConstraint: { distance: 5 } }),
    useSensor(TouchSensor, { activationConstraint: { delay: 200, tolerance: 5 } }),
    useSensor(KeyboardSensor),
  );

  function onDragStart(e: DragStartEvent) { setDraggingId(String(e.active.id)); setHoverU(null); }
  function onDragMove(e: any) {
    const overId = e?.over?.id;
    if (typeof overId === 'string' && overId.startsWith('u-')) {
      const u = Number(overId.slice(2));
      setHoverU(Number.isNaN(u) ? null : u);
    } else setHoverU(null);
  }
  function onDragEnd(e: DragEndEvent) {
    const assetId = String(e.active.id);
    const overId = e.over?.id;
    setDraggingId(null);
    setHoverU(null);
    if (!overId || typeof overId !== 'string' || !overId.startsWith('u-')) return;
    const target = Number(overId.slice(2));
    const asset = assets.find((a) => a.id === assetId);
    if (!asset) return;
    if (asset.rack_position_u === target) return;
    if (!fitsAt(target)) {
      toast.error(`U${target} doesn't fit ${asset.rack_units}U asset (collision or overflow)`);
      return;
    }
    qc.setQueryData(['rack-detail', rackId], (prev: any) => {
      if (!prev) return prev;
      return {
        ...prev,
        assets: prev.assets.map((a: any) =>
          a.id === assetId ? { ...a, rack_position_u: target } : a,
        ),
      };
    });
    updateMutation.mutate(
      { resource: 'inventory/assets', id: assetId, values: { rack_position_u: target }, successNotification: false },
      {
        onSuccess: () => {
          toast.success(`Moved ${asset.name} to U${target}`);
          qc.invalidateQueries({ queryKey: ['rack-detail', rackId] });
        },
        onError: (err: any) => {
          toast.error(err?.message ?? 'Move failed');
          qc.invalidateQueries({ queryKey: ['rack-detail', rackId] });
        },
      },
    );
  }

  const slotOwner: (VizAsset | null)[] = new Array(uHeight + 1).fill(null);
  const unplaced: VizAsset[] = [];
  for (const a of rackMountThisFace) {
    if (!a.rack_position_u || a.rack_position_u < 1 || a.rack_position_u > uHeight) {
      unplaced.push(a);
      continue;
    }
    const span = Math.max(1, a.rack_units);
    for (let u = a.rack_position_u; u < a.rack_position_u + span && u <= uHeight; u++) {
      slotOwner[u] = a;
    }
  }

  const totalH = uHeight * uPx;
  const fullW = RACK_BODY_W + VERT_W * 2 + 8;

  return (
    <DndContext sensors={sensors} onDragStart={onDragStart} onDragMove={onDragMove} onDragEnd={onDragEnd}>
      <div className="flex items-start gap-6">
        <div className="space-y-3">
          <div className="flex items-center justify-between gap-3" style={{ width: fullW }}>
            <Tabs value={face} onValueChange={(v) => setFace(v as 'front' | 'rear')}>
              <TabsList>
                <TabsTrigger value="front">Front</TabsTrigger>
                <TabsTrigger value="rear">Rear</TabsTrigger>
              </TabsList>
            </Tabs>
            <span className="text-[11px] uppercase tracking-wider text-muted-foreground">
              {face} view
            </span>
          </div>

          <div className="relative" style={{ width: fullW, height: totalH + 4 }}>
            <VerticalPduStrip
              x={0} height={totalH} width={VERT_W}
              pdus={verticalLeft} onClick={(id) => navigate(`/assets/${id}`)}
            />

            <div
              className="absolute rounded-md border-2 bg-secondary"
              style={{
                left: VERT_W + 4, top: 0, width: RACK_BODY_W, height: totalH,
                borderColor: 'hsl(var(--border))',
                boxShadow: 'inset 0 0 0 4px hsl(var(--card))',
              }}
            >
              {Array.from({ length: uHeight }, (_, i) => uHeight - i).map((u) => (
                <div
                  key={u}
                  className="absolute -left-7 w-6 text-right text-[10px] text-muted-foreground"
                  style={{ bottom: (u - 1) * uPx, height: uPx, lineHeight: `${uPx}px` }}
                >
                  U{u}
                </div>
              ))}

              {Array.from({ length: uHeight }, (_, i) => i + 1).map((u) => (
                <SlotDroppable
                  key={`drop-${u}`}
                  u={u} bottom={(u - 1) * uPx} height={uPx} width={RACK_BODY_W - 8}
                  showAccept={draggingId !== null && hoverU !== null && u >= hoverU && u < hoverU + draggingSpan && fitsAt(hoverU)}
                  showReject={draggingId !== null && hoverU !== null && u >= hoverU && u < hoverU + draggingSpan && !fitsAt(hoverU)}
                />
              ))}

              {rackMountThisFace
                .filter((a) => a.rack_position_u && a.rack_position_u >= 1 && a.rack_position_u <= uHeight)
                .map((a) => {
                  const span = Math.max(1, a.rack_units);
                  const blockH = span * uPx - 1;
                  const blockW = RACK_BODY_W - 8;
                  return (
                    <DraggableAsset
                      key={a.id}
                      asset={a}
                      blockH={blockH} blockW={blockW}
                      bottom={(a.rack_position_u! - 1) * uPx}
                      mode={mode} catalog={catalog} canDrag={canWrite}
                      onClick={() => navigate(`/assets/${a.id}`)}
                    />
                  );
                })}

              {rackMountOtherFace
                .filter((a) => a.rack_position_u && a.rack_position_u >= 1 && a.rack_position_u <= uHeight)
                .map((a) => {
                  const span = Math.max(1, a.rack_units);
                  return (
                    <div
                      key={`ghost-${a.id}`}
                      className="pointer-events-none absolute rounded-sm border border-dashed border-white/15"
                      style={{
                        left: 4, right: 4, bottom: (a.rack_position_u! - 1) * uPx, height: span * uPx - 1,
                        background: 'rgba(255,255,255,0.02)',
                      }}
                      title={`${a.name} (on ${a.face === 'rear' ? 'rear' : 'front'} face)`}
                    />
                  );
                })}

              {Array.from({ length: uHeight }, (_, i) => i + 1)
                .filter((u) => slotOwner[u] === null)
                .map((u) => (
                  <div
                    key={`empty-${u}`}
                    className="pointer-events-none absolute border-t border-dashed border-white/5"
                    style={{ left: 4, right: 4, bottom: (u - 1) * uPx, height: uPx - 1 }}
                  />
                ))}
            </div>

            <VerticalPduStrip
              x={VERT_W + 4 + RACK_BODY_W + 4} height={totalH} width={VERT_W}
              pdus={verticalRight} onClick={(id) => navigate(`/assets/${id}`)}
            />
          </div>
        </div>

        <div className="flex-1 space-y-4">
          <div>
            <h4 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              {mode === 'stencil' ? 'Vendor stencil view' : 'Block view'} · {face} face
            </h4>
            <p className="mt-1 text-xs text-muted-foreground">
              {canWrite
                ? <>Drag a device to a new U position. Toggle Front / Rear to see assets on the other face. Vertical PDUs sit on the side rails. Outline color shows power redundancy: <span className="text-success">green = redundant</span>, <span className="text-warning">yellow = single feed</span>, <span className="text-destructive">red = unpowered</span>.</>
                : <>Click a device to open its health page.</>}
            </p>
            {mode === 'block' && (
              <div className="mt-3 flex flex-wrap gap-2">
                {Object.entries(KIND_COLOR).map(([k, c]) => (
                  <div key={k} className="flex items-center gap-1.5 text-xs">
                    <span className="h-3 w-3 rounded-sm" style={{ background: c }} />
                    {k}
                  </div>
                ))}
              </div>
            )}
          </div>
          {unplaced.length > 0 && (
            <div>
              <h4 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
                Unplaced on {face} face ({unplaced.length})
              </h4>
              <ul className="mt-2 space-y-1 text-sm">
                {unplaced.map((a) => (
                  <li key={a.id}>
                    <button
                      type="button"
                      onClick={() => navigate(`/assets/${a.id}`)}
                      className="text-primary hover:underline"
                    >
                      {a.name}
                    </button>{' '}
                    <span className="text-muted-foreground">· {a.kind}</span>
                  </li>
                ))}
              </ul>
            </div>
          )}
        </div>
      </div>

      <DragOverlay dropAnimation={{ duration: 150 }}>
        {draggingAsset && (
          <DragGhost
            asset={draggingAsset}
            blockW={RACK_BODY_W - 8}
            blockH={Math.max(1, draggingAsset.rack_units) * uPx - 1}
            mode={mode} catalog={catalog}
          />
        )}
      </DragOverlay>
    </DndContext>
  );
}

// ----- subcomponents -----

function VerticalPduStrip({
  x, height, width, pdus, onClick,
}: {
  x: number; height: number; width: number;
  pdus: VizAsset[]; onClick: (id: string) => void;
}) {
  if (pdus.length === 0) {
    return (
      <div
        className="absolute rounded-sm border border-dashed border-border/40"
        style={{ left: x, top: 0, width, height, background: 'transparent' }}
        title="No vertical PDU on this side"
      />
    );
  }
  const slice = height / pdus.length;
  return (
    <>
      {pdus.map((p, i) => {
        const sideColor =
          p.pdu_side === 'A' ? '#3b82f6' :
          p.pdu_side === 'B' ? '#ef4444' :
          p.pdu_side === 'C' ? '#a855f7' :
          '#737373';
        return (
          <button
            key={p.id}
            type="button"
            onClick={() => onClick(p.id)}
            className="absolute flex flex-col items-center justify-between rounded-sm border-2 bg-[#1a1d23] p-1 text-[9px] font-bold text-white shadow-md transition-transform hover:scale-[1.02]"
            style={{
              left: x, top: i * slice, width, height: slice - (i < pdus.length - 1 ? 2 : 0),
              borderColor: sideColor, cursor: 'pointer',
            }}
            title={`${p.name} (vertical, side ${p.pdu_side ?? '—'})`}
          >
            <div
              className="rounded-sm px-1 py-0.5 text-[8px]"
              style={{ background: sideColor, color: 'white' }}
            >
              {p.pdu_side ?? 'PDU'}
            </div>
            <div className="flex flex-1 flex-col items-center justify-center gap-0.5">
              {Array.from({ length: Math.min(20, Math.max(4, Math.floor((slice - 24) / 8))) }).map((_, k) => (
                <span key={k} className="h-1 w-1 rounded-full bg-black/70 ring-1 ring-white/20" />
              ))}
            </div>
            <div
              className="overflow-hidden text-[8px] font-semibold leading-none"
              style={{ writingMode: 'vertical-rl', transform: 'rotate(180deg)' }}
            >
              {p.name.slice(-12)}
            </div>
          </button>
        );
      })}
    </>
  );
}

function SlotDroppable({
  u, bottom, height, width, showAccept, showReject,
}: {
  u: number; bottom: number; height: number; width: number;
  showAccept: boolean; showReject: boolean;
}) {
  const { isOver, setNodeRef } = useDroppable({ id: `u-${u}` });
  return (
    <div
      ref={setNodeRef}
      className={cn(
        'absolute',
        showAccept && 'bg-success/30',
        showReject && 'bg-destructive/25',
        isOver && !showAccept && !showReject && 'bg-primary/15',
      )}
      style={{ left: 4, width, bottom, height: height - 1 }}
    />
  );
}

function DraggableAsset({
  asset, blockH, blockW, bottom, mode, catalog, canDrag, onClick,
}: {
  asset: VizAsset; blockH: number; blockW: number; bottom: number;
  mode: StyleMode; catalog: any; canDrag: boolean;
  onClick: () => void;
}) {
  const { attributes, listeners, setNodeRef, isDragging } = useDraggable({
    id: asset.id, disabled: !canDrag,
  });
  const opacity = isDragging ? 0 : 1;
  const cursor = canDrag ? 'grab' : 'pointer';

  function handleClick(e: React.MouseEvent) { e.preventDefault(); onClick(); }

  const ringColor =
    asset.redundancy === 'redundant' ? 'hsl(var(--success))' :
    asset.redundancy === 'single' ? 'hsl(var(--warning))' :
    asset.redundancy === 'unpowered' ? 'hsl(var(--destructive))' :
    null;

  if (mode === 'stencil') {
    const { entry, palette } = resolveStencil(catalog ?? null, {
      manufacturer: asset.manufacturer ?? null,
      model: asset.model ?? null, kind: asset.kind,
    });
    return (
      <button
        ref={setNodeRef} type="button"
        {...listeners} {...attributes}
        onClick={handleClick}
        className={cn('absolute p-0 transition-transform', !isDragging && 'hover:scale-[1.01]')}
        style={{
          left: 4, bottom, width: blockW, height: blockH,
          background: 'transparent', border: 'none', cursor, opacity,
          outline: ringColor ? `2px solid ${ringColor}` : undefined,
          outlineOffset: ringColor ? '-2px' : undefined,
        }}
        title={`${asset.name} · ${asset.manufacturer ?? ''} ${asset.model ?? ''}`.trim() + (asset.redundancy && asset.redundancy !== 'n/a' ? ` · power: ${asset.redundancy}` : '')}
      >
        <Stencil
          asset={{ name: asset.name, manufacturer: asset.manufacturer ?? null, model: asset.model ?? null, kind: asset.kind }}
          width={blockW} height={blockH}
          palette={palette} entry={entry}
          alertCount={asset.open_alerts}
        />
      </button>
    );
  }

  const bg = colorFor(asset.kind);
  return (
    <button
      ref={setNodeRef} type="button"
      {...listeners} {...attributes}
      onClick={handleClick}
      className={cn('absolute flex items-center justify-between overflow-hidden rounded-sm px-1.5 text-[11px] font-semibold text-slate-900 transition-transform', !isDragging && 'hover:scale-[1.01]')}
      style={{
        left: 4, right: 4, bottom, height: blockH, background: bg, opacity, cursor,
        border: asset.open_alerts > 0 ? '2px solid hsl(var(--destructive))' : '1px solid rgba(0,0,0,0.2)',
        outline: ringColor ? `2px solid ${ringColor}` : undefined,
        outlineOffset: ringColor ? '-2px' : undefined,
      }}
      title={`${asset.name} · ${asset.kind} · U${asset.rack_position_u}` + (asset.redundancy && asset.redundancy !== 'n/a' ? ` · power: ${asset.redundancy}` : '')}
    >
      <span className="truncate">{asset.name}</span>
      {asset.open_alerts > 0 && <Badge variant="destructive" className="text-[10px]">{asset.open_alerts}</Badge>}
    </button>
  );
}

function DragGhost({
  asset, blockW, blockH, mode, catalog,
}: {
  asset: VizAsset; blockW: number; blockH: number; mode: StyleMode; catalog: any;
}) {
  if (mode === 'stencil') {
    const { entry, palette } = resolveStencil(catalog ?? null, {
      manufacturer: asset.manufacturer ?? null, model: asset.model ?? null, kind: asset.kind,
    });
    return (
      <div style={{ width: blockW, height: blockH, opacity: 0.92, transform: 'rotate(0.5deg)' }}>
        <Stencil
          asset={{ name: asset.name, manufacturer: asset.manufacturer ?? null, model: asset.model ?? null, kind: asset.kind }}
          width={blockW} height={blockH}
          palette={palette} entry={entry}
          alertCount={asset.open_alerts}
        />
      </div>
    );
  }
  const bg = colorFor(asset.kind);
  return (
    <div
      className="flex items-center justify-between overflow-hidden rounded-sm px-1.5 text-[11px] font-semibold text-slate-900 shadow-lg"
      style={{
        width: blockW, height: blockH, background: bg, opacity: 0.92, transform: 'rotate(0.5deg)',
        border: asset.open_alerts > 0 ? '2px solid hsl(var(--destructive))' : '1px solid rgba(0,0,0,0.2)',
      }}
    >
      <span className="truncate">{asset.name}</span>
    </div>
  );
}
