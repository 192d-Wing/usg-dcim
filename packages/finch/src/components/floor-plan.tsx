// Floor plan — true 2-D tile grid for a datacenter floor. Placed
// racks render as colored tiles (tone follows power utilization,
// falling back to U fill); unplaced racks wait in a tray. Drag a rack
// onto a tile to place it, between tiles to move it, or back to the
// tray to unplace. The thick edge marks the rack's front face
// (grid_rotation clockwise: 0 = bottom, 90 = left, 180 = top,
// 270 = right). Click a tile to open the rack.

import { useMemo, useState, type CSSProperties } from 'react';
import {
  DndContext, DragEndEvent, DragOverlay, DragStartEvent,
  KeyboardSensor, MouseSensor, TouchSensor, useDraggable, useDroppable, useSensor, useSensors,
} from '@dnd-kit/core';

import Box from '@cloudscape-design/components/box';

import {
  colorBackgroundContainerContent, colorBackgroundInputDisabled,
  colorBorderDividerDefault, colorTextStatusError, colorTextStatusInactive,
  colorTextStatusSuccess, colorTextStatusWarning,
} from '@cloudscape-design/design-tokens';

import { type RackNode } from '@/components/rack-tile';

const CELL = 46;
const GAP = 2;
const DEFAULT_COLS = 16;
const DEFAULT_ROWS = 10;

type Props = Readonly<{
  cols: number | null;
  rows: number | null;
  racks: RackNode[];
  canEdit: boolean;
  onOpenRack: (rackId: string) => void;
  onPlace: (rackId: string, x: number | null, y: number | null) => void;
  onRotate: (rackId: string, rotation: number) => void;
}>;

function utilizationTone(r: RackNode): string {
  const pct = r.kw_max !== null && r.kw_max > 0 && r.kw_current !== null
    ? (r.kw_current / r.kw_max) * 100
    : r.u_height > 0 ? (r.u_used / r.u_height) * 100 : 0;
  if (pct >= 90) return colorTextStatusError;
  if (pct >= 75) return colorTextStatusWarning;
  return colorTextStatusSuccess;
}

function rackTitle(r: RackNode): string {
  const kw = r.kw_max !== null
    ? ` · ${r.kw_current !== null ? r.kw_current.toFixed(1) : '—'}/${r.kw_max} kW`
    : '';
  return `${r.code} · ${r.name} — ${r.u_used}/${r.u_height} U${kw}`;
}

export function FloorPlan({
  cols, rows, racks, canEdit, onOpenRack, onPlace, onRotate,
}: Props) {
  const [dragId, setDragId] = useState<string | null>(null);
  const sensors = useSensors(
    useSensor(MouseSensor, { activationConstraint: { distance: 5 } }),
    useSensor(TouchSensor, { activationConstraint: { delay: 200, tolerance: 5 } }),
    useSensor(KeyboardSensor),
  );

  const placed = racks.filter((r) => r.grid_x !== null && r.grid_y !== null);
  const unplaced = racks.filter((r) => r.grid_x === null || r.grid_y === null);

  // Auto-grow the grid so racks placed beyond the configured bounds
  // stay visible instead of silently disappearing.
  const nCols = Math.max(cols ?? DEFAULT_COLS, ...placed.map((r) => r.grid_x! + 1));
  const nRows = Math.max(rows ?? DEFAULT_ROWS, ...placed.map((r) => r.grid_y! + 1));

  const byCell = useMemo(() => {
    const m = new Map<string, RackNode>();
    for (const r of placed) m.set(`${r.grid_x},${r.grid_y}`, r);
    return m;
  }, [racks]);

  function onDragStart(e: DragStartEvent) {
    setDragId(String(e.active.id));
  }

  function onDragEnd(e: DragEndEvent) {
    setDragId(null);
    const rackId = String(e.active.id);
    const over = e.over ? String(e.over.id) : null;
    if (!over) return;
    if (over === 'floor-plan-tray') {
      const r = racks.find((x) => x.id === rackId);
      if (r && r.grid_x !== null) onPlace(rackId, null, null);
      return;
    }
    const m = /^cell-(\d+)-(\d+)$/.exec(over);
    if (!m) return;
    const x = Number(m[1]);
    const y = Number(m[2]);
    const occupant = byCell.get(`${x},${y}`);
    if (occupant && occupant.id !== rackId) return; // tile taken
    onPlace(rackId, x, y);
  }

  const dragRack = dragId ? racks.find((r) => r.id === dragId) : undefined;

  return (
    <DndContext sensors={sensors} onDragStart={onDragStart} onDragEnd={onDragEnd}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
        <div style={{ overflowX: 'auto', paddingBottom: 4 }}>
          <div style={{
            display: 'grid',
            gridTemplateColumns: `repeat(${nCols}, ${CELL}px)`,
            gridAutoRows: `${CELL}px`,
            gap: GAP,
            width: nCols * (CELL + GAP),
            padding: 6,
            borderRadius: 8,
            border: `1px solid ${colorBorderDividerDefault}`,
            background: colorBackgroundContainerContent,
          }}>
            {Array.from({ length: nRows }, (_, y) =>
              Array.from({ length: nCols }, (_, x) => {
                const rack = byCell.get(`${x},${y}`);
                return rack ? (
                  <RackCell
                    key={`${x}-${y}`}
                    rack={rack}
                    canEdit={canEdit}
                    onOpen={() => onOpenRack(rack.id)}
                    onRotate={() => onRotate(rack.id, ((rack.grid_rotation ?? 0) + 90) % 360)}
                  />
                ) : (
                  <EmptyCell key={`${x}-${y}`} x={x} y={y} active={dragId !== null} canEdit={canEdit} />
                );
              }))}
          </div>
        </div>

        {(canEdit || unplaced.length > 0) && (
          <Tray unplaced={unplaced} canEdit={canEdit} dragId={dragId} />
        )}

        <Box color="text-status-inactive" fontSize="body-s">
          {canEdit
            ? 'Drag racks onto tiles to place them, back to the tray to unplace. ↻ rotates the front face (thick edge). Click a rack to open it.'
            : 'The thick edge marks each rack’s front face. Click a rack to open it.'}
        </Box>
      </div>

      <DragOverlay dropAnimation={{ duration: 150 }}>
        {dragRack ? <GhostTile rack={dragRack} /> : null}
      </DragOverlay>
    </DndContext>
  );
}

function EmptyCell({
  x, y, active, canEdit,
}: Readonly<{ x: number; y: number; active: boolean; canEdit: boolean }>) {
  const { isOver, setNodeRef } = useDroppable({ id: `cell-${x}-${y}`, disabled: !canEdit });
  return (
    <div
      ref={setNodeRef}
      style={{
        width: CELL, height: CELL, borderRadius: 4,
        border: `1px ${active ? 'dashed' : 'solid'} ${colorBorderDividerDefault}`,
        background: isOver ? colorBackgroundInputDisabled : 'transparent',
        transition: 'background 100ms ease',
      }}
    />
  );
}

function facingEdge(rotation: number, tone: string): CSSProperties {
  const edge = `3px solid ${tone}`;
  switch (rotation) {
    case 90: return { borderLeft: edge };
    case 180: return { borderTop: edge };
    case 270: return { borderRight: edge };
    default: return { borderBottom: edge };
  }
}

function tileStyle(rack: RackNode): CSSProperties {
  const tone = utilizationTone(rack);
  return {
    position: 'relative',
    width: CELL, height: CELL, borderRadius: 4,
    border: `1px solid ${tone}`,
    ...facingEdge(rack.grid_rotation ?? 0, tone),
    background: `color-mix(in srgb, ${tone} 22%, ${colorBackgroundContainerContent})`,
    display: 'flex', alignItems: 'center', justifyContent: 'center',
    overflow: 'hidden',
  };
}

const tileLabel: CSSProperties = {
  fontFamily: 'ui-monospace, monospace', fontSize: 10, fontWeight: 600,
  whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
  maxWidth: CELL - 6,
};

function GhostTile({ rack }: Readonly<{ rack: RackNode }>) {
  return (
    <div style={{ ...tileStyle(rack), cursor: 'grabbing', boxShadow: '0 2px 8px rgba(0,0,0,0.35)' }}>
      <span style={tileLabel}>{rack.code}</span>
    </div>
  );
}

function RackCell({
  rack, canEdit, onOpen, onRotate,
}: Readonly<{
  rack: RackNode;
  canEdit: boolean;
  onOpen: () => void;
  onRotate: () => void;
}>) {
  const { attributes, listeners, setNodeRef, isDragging } = useDraggable({
    id: rack.id, disabled: !canEdit,
  });
  return (
    <div
      ref={setNodeRef}
      {...attributes}
      {...listeners}
      role="button"
      tabIndex={0}
      title={rackTitle(rack)}
      onClick={onOpen}
      onKeyDown={(e) => { if (e.key === 'Enter') onOpen(); }}
      style={{
        ...tileStyle(rack),
        cursor: canEdit ? 'grab' : 'pointer',
        opacity: isDragging ? 0.3 : 1,
      }}
    >
      <span style={tileLabel}>{rack.code}</span>
      {canEdit && (
        <button
          type="button"
          title="Rotate front face"
          onClick={(e) => { e.stopPropagation(); onRotate(); }}
          onPointerDown={(e) => e.stopPropagation()}
          onKeyDown={(e) => e.stopPropagation()}
          style={{
            position: 'absolute', top: 0, right: 0,
            width: 14, height: 14, lineHeight: '12px',
            fontSize: 10, padding: 0,
            border: 'none', borderRadius: '0 0 0 4px',
            background: colorBackgroundContainerContent,
            color: colorTextStatusInactive,
            cursor: 'pointer',
          }}
        >
          ↻
        </button>
      )}
    </div>
  );
}

function TrayChip({ rack, canEdit }: Readonly<{ rack: RackNode; canEdit: boolean }>) {
  const { attributes, listeners, setNodeRef, isDragging } = useDraggable({
    id: rack.id, disabled: !canEdit,
  });
  return (
    <div
      ref={setNodeRef}
      {...attributes}
      {...listeners}
      title={rackTitle(rack)}
      style={{
        display: 'inline-flex', alignItems: 'baseline', gap: 6,
        padding: '4px 8px', borderRadius: 4,
        border: `1px solid ${colorBorderDividerDefault}`,
        background: colorBackgroundContainerContent,
        cursor: canEdit ? 'grab' : 'default',
        opacity: isDragging ? 0.3 : 1,
      }}
    >
      <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 11, fontWeight: 600 }}>{rack.code}</span>
      <span style={{ fontSize: 11, color: colorTextStatusInactive }}>{rack.u_used}/{rack.u_height} U</span>
    </div>
  );
}

function Tray({
  unplaced, canEdit, dragId,
}: Readonly<{ unplaced: RackNode[]; canEdit: boolean; dragId: string | null }>) {
  const { isOver, setNodeRef } = useDroppable({ id: 'floor-plan-tray', disabled: !canEdit });
  return (
    <div
      ref={setNodeRef}
      style={{
        display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 6,
        minHeight: 36, padding: 6, borderRadius: 6,
        border: `1px dashed ${colorBorderDividerDefault}`,
        background: isOver && dragId ? colorBackgroundInputDisabled : 'transparent',
        transition: 'background 100ms ease',
      }}
    >
      <span style={{ fontSize: 11, color: colorTextStatusInactive }}>
        Unplaced ({unplaced.length}):
      </span>
      {unplaced.length === 0 && (
        <span style={{ fontSize: 11, color: colorTextStatusInactive }}>
          all racks are on the plan{canEdit ? ' — drop here to unplace' : ''}
        </span>
      )}
      {unplaced.map((r) => <TrayChip key={r.id} rack={r} canEdit={canEdit} />)}
    </div>
  );
}
