// Shared rack tile — compact U + kW meters on a clickable card.
// Used by the site hierarchy and the building floor view.

import {
  colorBackgroundContainerContent, colorBorderDividerDefault,
  colorTextStatusInactive,
} from '@cloudscape-design/design-tokens';

import { CapacityBar } from '@/components/capacity-bar';

export type RackNode = {
  id: string; name: string; code: string;
  u_height: number; u_used: number; u_pct: number;
  kw_max: number | null; kw_current: number | null;
  asset_count: number;
  // Floor-plan tile placement (null grid_x/grid_y = unplaced).
  grid_x: number | null; grid_y: number | null; grid_rotation: number;
};

export function RackTile({
  rack, onClick,
}: Readonly<{ rack: RackNode; onClick: () => void }>) {
  return (
    <button
      type="button"
      onClick={onClick}
      style={{
        display: 'flex', flexDirection: 'column', gap: 6,
        padding: 10, borderRadius: 8,
        textAlign: 'left',
        border: `1px solid ${colorBorderDividerDefault}`,
        background: colorBackgroundContainerContent,
        cursor: 'pointer',
      }}
    >
      <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 8 }}>
        <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
          <span style={{ fontFamily: 'ui-monospace, monospace', fontSize: 12, fontWeight: 500 }}>{rack.code}</span>
          <span style={{
            fontSize: 12, color: colorTextStatusInactive,
            whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis',
          }}>
            {rack.name}
          </span>
        </div>
        <span style={{ fontSize: 11, color: colorTextStatusInactive }}>
          {rack.asset_count}d
        </span>
      </div>
      <CapacityBar
        used={rack.u_used} total={rack.u_height}
        leftLabel={`${rack.u_used}/${rack.u_height} U`}
        compact
      />
      {rack.kw_max !== null && (
        <CapacityBar
          used={rack.kw_current ?? 0}
          total={rack.kw_max}
          unknown={rack.kw_current === null}
          leftLabel={
            rack.kw_current === null
              ? `—/${rack.kw_max} kW`
              : `${rack.kw_current.toFixed(1)}/${rack.kw_max} kW`
          }
          compact
        />
      )}
    </button>
  );
}
