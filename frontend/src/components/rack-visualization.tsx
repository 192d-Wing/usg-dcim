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
};

type Mode = 'block' | 'stencil';

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
  mode?: Mode;
  uPx?: number;
};

const RACK_WIDTH = 240;
const BLOCK_W = RACK_WIDTH - 8;

export function RackVisualization({ rackId, uHeight, assets, mode = 'stencil', uPx = 18 }: Props) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const updateMutation = useUpdate();
  const { data: catalog } = useStencilCatalog();
  const canWrite = hasCapability('inventory:write');

  const [draggingId, setDraggingId] = useState<string | null>(null);
  const [hoverU, setHoverU] = useState<number | null>(null);

  // Compute occupied slots EXCLUDING the dragged asset (so it can be dropped where it currently is).
  const occupiedExcludingDragged = useMemo(() => {
    const occ = new Set<number>();
    for (const a of assets) {
      if (a.id === draggingId) continue;
      if (!a.rack_position_u || a.rack_position_u < 1 || a.rack_position_u > uHeight) continue;
      const span = Math.max(1, a.rack_units || 1);
      for (let u = a.rack_position_u; u < a.rack_position_u + span; u++) occ.add(u);
    }
    return occ;
  }, [assets, draggingId, uHeight]);

  const draggingAsset = draggingId ? assets.find((a) => a.id === draggingId) ?? null : null;
  const draggingSpan = Math.max(1, draggingAsset?.rack_units ?? 1);

  // Validate whether a drop at startU would fit (no overflow + no collision)
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

  function onDragStart(e: DragStartEvent) {
    setDraggingId(String(e.active.id));
    setHoverU(null);
  }

  function onDragMove(e: any) {
    const overId = e?.over?.id;
    if (typeof overId === 'string' && overId.startsWith('u-')) {
      const u = Number(overId.slice(2));
      setHoverU(Number.isNaN(u) ? null : u);
    } else {
      setHoverU(null);
    }
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
    // Optimistic update so the block jumps immediately while the PATCH is in flight.
    qc.setQueryData(['rack-detail', rackId], (prev: any) => {
      if (!prev) return prev;
      return {
        ...prev,
        assets: prev.assets.map((a: any) => a.id === assetId ? { ...a, rack_position_u: target } : a),
      };
    });
    updateMutation.mutate(
      {
        resource: 'inventory/assets',
        id: assetId,
        values: { rack_position_u: target },
        successNotification: false,
      },
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

  // Build slot ownership for empty-slot rendering (using ACTUAL positions, not dragged-excluded)
  const slotOwner: (VizAsset | null)[] = new Array(uHeight + 1).fill(null);
  const unplaced: VizAsset[] = [];
  for (const a of assets) {
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

  return (
    <DndContext
      sensors={sensors}
      onDragStart={onDragStart}
      onDragMove={onDragMove}
      onDragEnd={onDragEnd}
    >
      <div className="flex items-start gap-6">
        <div>
          <div
            className="relative rounded-md border-2 bg-secondary"
            style={{
              width: RACK_WIDTH, height: totalH,
              borderColor: 'hsl(var(--border))',
              boxShadow: 'inset 0 0 0 4px hsl(var(--card))',
            }}
          >
            {/* U-position labels on the left */}
            {Array.from({ length: uHeight }, (_, i) => uHeight - i).map((u) => (
              <div
                key={u}
                className="absolute -left-7 w-6 text-right text-[10px] text-muted-foreground"
                style={{ bottom: (u - 1) * uPx, height: uPx, lineHeight: `${uPx}px` }}
              >
                U{u}
              </div>
            ))}

            {/* Drop targets — one per U slot */}
            {Array.from({ length: uHeight }, (_, i) => i + 1).map((u) => (
              <SlotDroppable
                key={`drop-${u}`}
                u={u}
                bottom={(u - 1) * uPx}
                height={uPx}
                width={BLOCK_W}
                showAccept={draggingId !== null && hoverU !== null && u >= hoverU && u < hoverU + draggingSpan && fitsAt(hoverU)}
                showReject={draggingId !== null && hoverU !== null && u >= hoverU && u < hoverU + draggingSpan && !fitsAt(hoverU)}
              />
            ))}

            {/* Asset blocks (draggable when canWrite) */}
            {assets
              .filter((a) => a.rack_position_u && a.rack_position_u >= 1 && a.rack_position_u <= uHeight)
              .map((a) => {
                const span = Math.max(1, a.rack_units);
                const blockH = span * uPx - 1;
                return (
                  <DraggableAsset
                    key={a.id}
                    asset={a}
                    blockH={blockH}
                    blockW={BLOCK_W}
                    bottom={(a.rack_position_u! - 1) * uPx}
                    mode={mode}
                    catalog={catalog}
                    canDrag={canWrite}
                    onClick={() => navigate(`/assets/${a.id}`)}
                  />
                );
              })}

            {/* Empty-slot dashed dividers */}
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
        </div>

        <div className="flex-1 space-y-4">
          <div>
            <h4 className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">
              {mode === 'stencil' ? 'Vendor stencil view' : 'Block view'}
            </h4>
            <p className="mt-1 text-xs text-muted-foreground">
              {canWrite
                ? <>Drag a device to a new U position to move it. Click to open its health page.</>
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
                Unplaced ({unplaced.length})
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

      {/* Floating drag preview */}
      <DragOverlay dropAnimation={{ duration: 150 }}>
        {draggingAsset && (
          <DragGhost
            asset={draggingAsset}
            blockW={BLOCK_W}
            blockH={Math.max(1, draggingAsset.rack_units) * uPx - 1}
            mode={mode}
            catalog={catalog}
          />
        )}
      </DragOverlay>
    </DndContext>
  );
}

// ----- subcomponents -----

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
  mode: Mode; catalog: any; canDrag: boolean;
  onClick: () => void;
}) {
  const { attributes, listeners, setNodeRef, isDragging } = useDraggable({
    id: asset.id,
    disabled: !canDrag,
  });

  // Hide the source while dragging — the DragOverlay shows a floating ghost
  const opacity = isDragging ? 0 : 1;
  const cursor = canDrag ? 'grab' : 'pointer';

  // Click handler that doesn't fire after a drag (dnd-kit suppresses click via activationConstraint distance)
  function handleClick(e: React.MouseEvent) {
    e.preventDefault();
    onClick();
  }

  if (mode === 'stencil') {
    const { entry, palette } = resolveStencil(catalog ?? null, {
      manufacturer: asset.manufacturer ?? null,
      model: asset.model ?? null,
      kind: asset.kind,
    });
    return (
      <button
        ref={setNodeRef}
        type="button"
        {...listeners}
        {...attributes}
        onClick={handleClick}
        className={cn('absolute p-0 transition-transform', !isDragging && 'hover:scale-[1.01]')}
        style={{
          left: 4, bottom, width: blockW, height: blockH,
          background: 'transparent', border: 'none', cursor, opacity,
        }}
        title={`${asset.name} · ${asset.manufacturer ?? ''} ${asset.model ?? ''}`.trim()}
      >
        <Stencil
          asset={{ name: asset.name, manufacturer: asset.manufacturer ?? null, model: asset.model ?? null, kind: asset.kind }}
          width={blockW}
          height={blockH}
          palette={palette}
          entry={entry}
          alertCount={asset.open_alerts}
        />
      </button>
    );
  }

  const bg = colorFor(asset.kind);
  return (
    <button
      ref={setNodeRef}
      type="button"
      {...listeners}
      {...attributes}
      onClick={handleClick}
      className={cn('absolute flex items-center justify-between overflow-hidden rounded-sm px-1.5 text-[11px] font-semibold text-slate-900 transition-transform', !isDragging && 'hover:scale-[1.01]')}
      style={{
        left: 4, right: 4, bottom, height: blockH, background: bg, opacity, cursor,
        border: asset.open_alerts > 0 ? '2px solid hsl(var(--destructive))' : '1px solid rgba(0,0,0,0.2)',
      }}
      title={`${asset.name} · ${asset.kind} · U${asset.rack_position_u}`}
    >
      <span className="truncate">{asset.name}</span>
      {asset.open_alerts > 0 && <Badge variant="destructive" className="text-[10px]">{asset.open_alerts}</Badge>}
    </button>
  );
}

function DragGhost({
  asset, blockW, blockH, mode, catalog,
}: {
  asset: VizAsset; blockW: number; blockH: number; mode: Mode; catalog: any;
}) {
  if (mode === 'stencil') {
    const { entry, palette } = resolveStencil(catalog ?? null, {
      manufacturer: asset.manufacturer ?? null, model: asset.model ?? null, kind: asset.kind,
    });
    return (
      <div style={{ width: blockW, height: blockH, opacity: 0.92, transform: 'rotate(0.5deg)' }}>
        <Stencil
          asset={{ name: asset.name, manufacturer: asset.manufacturer ?? null, model: asset.model ?? null, kind: asset.kind }}
          width={blockW}
          height={blockH}
          palette={palette}
          entry={entry}
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
