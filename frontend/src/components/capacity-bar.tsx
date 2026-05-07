import { cn } from '@/lib/utils';

type Props = {
  used: number;
  total: number;
  /** Optional override of the formatted left/right labels */
  leftLabel?: string;
  rightLabel?: string;
  /** Pct thresholds for color tiers */
  warnAt?: number;
  critAt?: number;
  /** Compact mode for use inside small cards */
  compact?: boolean;
  /** When kw_current is unknown we still want to render the bar greyed out */
  unknown?: boolean;
};

export function CapacityBar({
  used, total, leftLabel, rightLabel,
  warnAt = 75, critAt = 90, compact, unknown,
}: Props) {
  const pct = total > 0 ? Math.min(100, (used / total) * 100) : 0;
  const tone =
    unknown ? 'bg-muted' :
    pct >= critAt ? 'bg-destructive' :
    pct >= warnAt ? 'bg-warning' :
    'bg-success';

  return (
    <div className={cn('space-y-1', compact && 'space-y-0.5')}>
      <div className={cn('flex items-baseline justify-between text-xs text-muted-foreground', compact && 'text-[11px]')}>
        <span>{leftLabel ?? `${used} / ${total}`}</span>
        <span className="tabular-nums">{unknown ? '—' : `${pct.toFixed(0)}%`}</span>
      </div>
      <div className={cn('h-2 w-full overflow-hidden rounded-full bg-secondary', compact && 'h-1.5')}>
        <div
          className={cn('h-full transition-all', tone)}
          style={{ width: unknown ? '0%' : `${pct}%` }}
        />
      </div>
      {rightLabel && !compact && (
        <div className="text-[11px] text-muted-foreground">{rightLabel}</div>
      )}
    </div>
  );
}
