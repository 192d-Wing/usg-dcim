import { useState } from 'react';
import Input from '@cloudscape-design/components/input';
import SegmentedControl from '@cloudscape-design/components/segmented-control';
import SpaceBetween from '@cloudscape-design/components/space-between';
import { colorTextStatusInactive } from '@cloudscape-design/design-tokens';

const COMMON_HEIGHTS = [12, 24, 42, 45, 48] as const;
const CUSTOM_ID = 'custom';

type Props = Readonly<{
  value: number;
  onChange: (u: number) => void;
  min?: number;
  max?: number;
}>;

// Quick-pick chips for common rack heights with a Custom escape hatch.
export function RackHeightPicker({ value, onChange, min = 1, max = 60 }: Props) {
  const [custom, setCustom] = useState(!COMMON_HEIGHTS.includes(value as any));

  return (
    <SpaceBetween size="xs">
      <SegmentedControl
        selectedId={custom ? CUSTOM_ID : String(value)}
        onChange={({ detail }) => {
          if (detail.selectedId === CUSTOM_ID) {
            setCustom(true);
            return;
          }
          setCustom(false);
          onChange(Number(detail.selectedId));
        }}
        options={[
          ...COMMON_HEIGHTS.map((u) => ({ id: String(u), text: `${u}U` })),
          { id: CUSTOM_ID, text: 'Custom' },
        ]}
      />
      {custom && (
        <SpaceBetween size="xxs">
          <div style={{ width: 140 }}>
            <Input
              type="number"
              value={String(value)}
              onChange={({ detail }) => {
                const n = Number(detail.value);
                if (Number.isFinite(n) && n >= min && n <= max) onChange(n);
              }}
            />
          </div>
          <span style={{ fontSize: 12, color: colorTextStatusInactive }}>
            Range: {min}–{max}U
          </span>
        </SpaceBetween>
      )}
    </SpaceBetween>
  );
}
