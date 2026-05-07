import { useState } from 'react';
import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';
import { cn } from '@/lib/utils';

const COMMON_HEIGHTS = [12, 24, 42, 45, 48] as const;

type Props = {
  value: number;
  onChange: (u: number) => void;
  min?: number;
  max?: number;
  className?: string;
};

/** Quick-pick chips for common rack heights with a Custom escape hatch. */
export function RackHeightPicker({ value, onChange, min = 1, max = 60, className }: Props) {
  const [custom, setCustom] = useState(!COMMON_HEIGHTS.includes(value as any));

  return (
    <div className={cn('space-y-2', className)}>
      <div className="flex flex-wrap gap-2">
        {COMMON_HEIGHTS.map((u) => (
          <Button
            key={u}
            type="button"
            size="sm"
            variant={!custom && value === u ? 'default' : 'outline'}
            onClick={() => { setCustom(false); onChange(u); }}
          >
            {u}U
          </Button>
        ))}
        <Button
          type="button"
          size="sm"
          variant={custom ? 'default' : 'outline'}
          onClick={() => setCustom(true)}
        >
          Custom
        </Button>
      </div>
      {custom && (
        <Input
          type="number"
          min={min}
          max={max}
          value={value}
          onChange={(e) => {
            const n = Number(e.target.value);
            if (Number.isFinite(n)) onChange(n);
          }}
          className="w-32"
        />
      )}
    </div>
  );
}
