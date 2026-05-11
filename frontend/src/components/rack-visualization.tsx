import { useMemo, useState } from 'react';
import { useNavigate } from 'react-router';
import {
  DndContext, DragEndEvent, DragOverlay, DragStartEvent,
  KeyboardSensor, MouseSensor, TouchSensor, useDraggable, useDroppable, useSensor, useSensors,
} from '@dnd-kit/core';
import { useUpdate } from '@refinedev/core';
import { useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import SegmentedControl from '@cloudscape-design/components/segmented-control';
import {
  colorBackgroundContainerContent, colorBackgroundContainerHeader,
  colorBorderDividerDefault, colorTextStatusError,
  colorTextStatusInactive, colorTextStatusInfo,
  colorTextStatusSuccess, colorTextStatusWarning,
} from '@cloudscape-design/design-tokens';

import { resolveStencil, Stencil, useStencilCatalog } from './stencil';
import { hasCapability } from '@/lib/access-control-provider';

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
  chassis: '#94a3b8', blade: '#64748b', patch_panel: '#22c55e', other: '#737373',
};
const colorFor = (k: string) => KIND_COLOR[k] ?? KIND_COLOR.other;

type Props = Readonly<{
  rackId: string;
  uHeight: number;
  assets: VizAsset[];
  mode?: StyleMode;
  uPx?: number;
}>;

const VERT_W = 22;
const RACK_BODY_W = 240;

function ringColorFor(r: VizAsset['redundancy']): string | null {
  if (r === 'redundant') return colorTextStatusSuccess;
  if (r === 'single') return colorTextStatusWarning;
  if (r === 'unpowered') return colorTextStatusError;
  return null;
}

function sideColorFor(side: VizAsset['pdu_side']): string {
  if (side === 'A') return '#3b82f6';
  if (side === 'B') return '#ef4444';
  if (side === 'C') return '#a855f7';
  return '#737373';
}

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
      <div style={{ display: 'flex', alignItems: 'flex-start', gap: 24 }}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, width: fullW }}>
            <SegmentedControl
              selectedId={face}
              onChange={({ detail }) => setFace(detail.selectedId as 'front' | 'rear')}
              options={[
                { id: 'front', text: 'Front' },
                { id: 'rear', text: 'Rear' },
              ]}
            />
            <Box variant="awsui-key-label">{face} view</Box>
          </div>

          <div style={{ position: 'relative', width: fullW, height: totalH + 4 }}>
            <VerticalPduStrip
              x={0} height={totalH} width={VERT_W}
              pdus={verticalLeft} onClick={(id) => navigate(`/assets/${id}`)}
            />

            <div
              style={{
                position: 'absolute',
                left: VERT_W + 4, top: 0, width: RACK_BODY_W, height: totalH,
                borderRadius: 6,
                border: `2px solid ${colorBorderDividerDefault}`,
                background: colorBackgroundContainerContent,
                boxShadow: `inset 0 0 0 4px ${colorBackgroundContainerHeader}`,
              }}
            >
              {Array.from({ length: uHeight }, (_, i) => uHeight - i).map((u) => (
                <div
                  key={u}
                  style={{
                    position: 'absolute',
                    left: -28, width: 24, textAlign: 'right',
                    fontSize: 10,
                    color: colorTextStatusInactive,
                    bottom: (u - 1) * uPx, height: uPx, lineHeight: `${uPx}px`,
                  }}
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
                      style={{
                        pointerEvents: 'none',
                        position: 'absolute',
                        borderRadius: 2,
                        border: '1px dashed rgba(0,0,0,0.15)',
                        left: 4, right: 4,
                        bottom: (a.rack_position_u! - 1) * uPx,
                        height: span * uPx - 1,
                        background: 'rgba(0,0,0,0.02)',
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
                    style={{
                      pointerEvents: 'none',
                      position: 'absolute',
                      borderTop: '1px dashed rgba(0,0,0,0.05)',
                      left: 4, right: 4, bottom: (u - 1) * uPx, height: uPx - 1,
                    }}
                  />
                ))}
            </div>

            <VerticalPduStrip
              x={VERT_W + 4 + RACK_BODY_W + 4} height={totalH} width={VERT_W}
              pdus={verticalRight} onClick={(id) => navigate(`/assets/${id}`)}
            />
          </div>
        </div>

        <div style={{ flex: 1, display: 'flex', flexDirection: 'column', gap: 16 }}>
          <div>
            <Box variant="awsui-key-label">
              {mode === 'stencil' ? 'Vendor stencil view' : 'Block view'} · {face} face
            </Box>
            <Box color="text-status-inactive" fontSize="body-s" padding={{ top: 'xxs' }}>
              {canWrite
                ? <>Drag a device to a new U position. Toggle Front / Rear to see assets on the other face. Vertical PDUs sit on the side rails. Outline color shows power redundancy: green = redundant, yellow = single feed, red = unpowered.</>
                : <>Click a device to open its health page.</>}
            </Box>
            {mode === 'block' && (
              <div style={{ marginTop: 12, display: 'flex', flexWrap: 'wrap', gap: 8 }}>
                {Object.entries(KIND_COLOR).map(([k, c]) => (
                  <div key={k} style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 12 }}>
                    <span style={{ width: 12, height: 12, borderRadius: 2, background: c }} />
                    {k}
                  </div>
                ))}
              </div>
            )}
          </div>
          {unplaced.length > 0 && (
            <div>
              <Box variant="awsui-key-label">
                Unplaced on {face} face ({unplaced.length})
              </Box>
              <ul style={{ marginTop: 8, paddingLeft: 16, fontSize: 14, listStyle: 'disc' }}>
                {unplaced.map((a) => (
                  <li key={a.id}>
                    <button
                      type="button"
                      onClick={() => navigate(`/assets/${a.id}`)}
                      style={{
                        background: 'transparent', border: 'none', padding: 0,
                        color: colorTextStatusInfo,
                        textDecoration: 'underline', cursor: 'pointer',
                      }}
                    >
                      {a.name}
                    </button>{' '}
                    <span style={{ color: colorTextStatusInactive }}>· {a.kind}</span>
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

function VerticalPduStrip({
  x, height, width, pdus, onClick,
}: Readonly<{
  x: number; height: number; width: number;
  pdus: VizAsset[]; onClick: (id: string) => void;
}>) {
  if (pdus.length === 0) {
    return (
      <div
        style={{
          position: 'absolute',
          left: x, top: 0, width, height,
          borderRadius: 2,
          border: `1px dashed ${colorBorderDividerDefault}`,
          background: 'transparent',
        }}
        title="No vertical PDU on this side"
      />
    );
  }
  const slice = height / pdus.length;
  return (
    <>
      {pdus.map((p, i) => {
        const sideColor = sideColorFor(p.pdu_side);
        return (
          <button
            key={p.id}
            type="button"
            onClick={() => onClick(p.id)}
            style={{
              position: 'absolute',
              display: 'flex',
              flexDirection: 'column',
              alignItems: 'center',
              justifyContent: 'space-between',
              borderRadius: 2,
              border: `2px solid ${sideColor}`,
              background: '#1a1d23',
              padding: 4,
              fontSize: 9,
              fontWeight: 700,
              color: '#fff',
              boxShadow: '0 1px 3px rgba(0,0,0,0.2)',
              left: x, top: i * slice,
              width, height: slice - (i < pdus.length - 1 ? 2 : 0),
              cursor: 'pointer',
            }}
            title={`${p.name} (vertical, side ${p.pdu_side ?? '—'})`}
          >
            <div
              style={{
                padding: '2px 4px',
                borderRadius: 2,
                fontSize: 8,
                background: sideColor,
                color: '#fff',
              }}
            >
              {p.pdu_side ?? 'PDU'}
            </div>
            <div style={{ display: 'flex', flex: 1, flexDirection: 'column', alignItems: 'center', justifyContent: 'center', gap: 2 }}>
              {Array.from({ length: Math.min(20, Math.max(4, Math.floor((slice - 24) / 8))) }).map((_, k) => (
                <span key={k} style={{
                  width: 4, height: 4, borderRadius: 999,
                  background: 'rgba(0,0,0,0.7)',
                  boxShadow: '0 0 0 1px rgba(255,255,255,0.2)',
                }} />
              ))}
            </div>
            <div
              style={{
                overflow: 'hidden',
                fontSize: 8,
                fontWeight: 600,
                lineHeight: 1,
                writingMode: 'vertical-rl',
                transform: 'rotate(180deg)',
              }}
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
}: Readonly<{
  u: number; bottom: number; height: number; width: number;
  showAccept: boolean; showReject: boolean;
}>) {
  const { isOver, setNodeRef } = useDroppable({ id: `u-${u}` });
  let background = 'transparent';
  if (showAccept) background = 'rgba(3, 127, 12, 0.3)';
  else if (showReject) background = 'rgba(217, 21, 21, 0.25)';
  else if (isOver) background = 'rgba(9, 114, 211, 0.15)';
  return (
    <div
      ref={setNodeRef}
      style={{ position: 'absolute', left: 4, width, bottom, height: height - 1, background }}
    />
  );
}

function DraggableAsset({
  asset, blockH, blockW, bottom, mode, catalog, canDrag, onClick,
}: Readonly<{
  asset: VizAsset; blockH: number; blockW: number; bottom: number;
  mode: StyleMode; catalog: any; canDrag: boolean;
  onClick: () => void;
}>) {
  const { attributes, listeners, setNodeRef, isDragging } = useDraggable({
    id: asset.id, disabled: !canDrag,
  });
  const opacity = isDragging ? 0 : 1;
  const cursor = canDrag ? 'grab' : 'pointer';

  function handleClick(e: React.MouseEvent) { e.preventDefault(); onClick(); }

  const ring = ringColorFor(asset.redundancy);
  const powerSuffix = asset.redundancy && asset.redundancy !== 'n/a' ? ` · power: ${asset.redundancy}` : '';

  if (mode === 'stencil') {
    const { entry, palette } = resolveStencil(catalog ?? null, {
      manufacturer: asset.manufacturer ?? null,
      model: asset.model ?? null, kind: asset.kind,
    });
    const stencilTitle = `${asset.name} · ${asset.manufacturer ?? ''} ${asset.model ?? ''}`.trim() + powerSuffix;
    return (
      <button
        ref={setNodeRef} type="button"
        {...listeners} {...attributes}
        onClick={handleClick}
        style={{
          position: 'absolute',
          padding: 0,
          left: 4, bottom, width: blockW, height: blockH,
          background: 'transparent', border: 'none', cursor, opacity,
          outline: ring ? `2px solid ${ring}` : undefined,
          outlineOffset: ring ? -2 : undefined,
        }}
        title={stencilTitle}
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
      style={{
        position: 'absolute',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        overflow: 'hidden',
        borderRadius: 2,
        padding: '0 6px',
        fontSize: 11,
        fontWeight: 600,
        color: '#0f172a',
        left: 4, right: 4, bottom, height: blockH,
        background: bg, opacity, cursor,
        border: asset.open_alerts > 0
          ? `2px solid ${colorTextStatusError}`
          : '1px solid rgba(0,0,0,0.2)',
        outline: ring ? `2px solid ${ring}` : undefined,
        outlineOffset: ring ? -2 : undefined,
      }}
      title={`${asset.name} · ${asset.kind} · U${asset.rack_position_u}${powerSuffix}`}
    >
      <span style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
        {asset.name}
      </span>
      {asset.open_alerts > 0 && <Badge color="red">{asset.open_alerts}</Badge>}
    </button>
  );
}

function DragGhost({
  asset, blockW, blockH, mode, catalog,
}: Readonly<{
  asset: VizAsset; blockW: number; blockH: number; mode: StyleMode; catalog: any;
}>) {
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
      style={{
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'space-between',
        overflow: 'hidden',
        borderRadius: 2,
        padding: '0 6px',
        fontSize: 11,
        fontWeight: 600,
        color: '#0f172a',
        boxShadow: '0 2px 8px rgba(0,0,0,0.12)',
        width: blockW, height: blockH,
        background: bg, opacity: 0.92, transform: 'rotate(0.5deg)',
        border: asset.open_alerts > 0
          ? `2px solid ${colorTextStatusError}`
          : '1px solid rgba(0,0,0,0.2)',
      }}
    >
      <span style={{ whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
        {asset.name}
      </span>
    </div>
  );
}
