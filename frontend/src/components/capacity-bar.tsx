import {
  colorBackgroundInputDisabled, colorTextStatusError,
  colorTextStatusInactive, colorTextStatusSuccess, colorTextStatusWarning,
} from '@cloudscape-design/design-tokens';

type Props = Readonly<{
  used: number;
  total: number;
  leftLabel?: string;
  rightLabel?: string;
  warnAt?: number;
  critAt?: number;
  compact?: boolean;
  unknown?: boolean;
}>;

function toneColor(pct: number, warnAt: number, critAt: number, unknown?: boolean): string {
  if (unknown) return colorBackgroundInputDisabled;
  if (pct >= critAt) return colorTextStatusError;
  if (pct >= warnAt) return colorTextStatusWarning;
  return colorTextStatusSuccess;
}

export function CapacityBar({
  used, total, leftLabel, rightLabel,
  warnAt = 75, critAt = 90, compact, unknown,
}: Props) {
  const pct = total > 0 ? Math.min(100, (used / total) * 100) : 0;
  const trackH = compact ? 6 : 8;
  const labelFontSize = compact ? 11 : 12;

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: compact ? 2 : 4 }}>
      <div style={{
        display: 'flex', alignItems: 'baseline', justifyContent: 'space-between',
        fontSize: labelFontSize,
        color: colorTextStatusInactive,
      }}>
        <span>{leftLabel ?? `${used} / ${total}`}</span>
        <span style={{ fontVariantNumeric: 'tabular-nums' }}>
          {unknown ? '—' : `${pct.toFixed(0)}%`}
        </span>
      </div>
      <div style={{
        height: trackH, width: '100%', overflow: 'hidden', borderRadius: 999,
        background: colorBackgroundInputDisabled,
      }}>
        <div
          style={{
            height: '100%',
            width: unknown ? '0%' : `${pct}%`,
            background: toneColor(pct, warnAt, critAt, unknown),
            transition: 'width 200ms ease',
          }}
        />
      </div>
      {rightLabel && !compact && (
        <div style={{ fontSize: 11, color: colorTextStatusInactive }}>
          {rightLabel}
        </div>
      )}
    </div>
  );
}
