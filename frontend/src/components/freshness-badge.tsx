import Badge from '@cloudscape-design/components/badge';

type Freshness = 'current' | 'stale' | 'estimated' | 'manual' | 'unknown';

// Map data-freshness states onto Cloudscape badge colors.
const colorFor: Record<Freshness, 'green' | 'red' | 'severity-medium' | 'grey'> = {
  current: 'green',
  stale: 'red',
  unknown: 'red',
  estimated: 'severity-medium',
  manual: 'grey',
};

export function FreshnessBadge({ state }: { state: string }) {
  const c = colorFor[(state as Freshness)] ?? 'grey';
  return <Badge color={c}>{state}</Badge>;
}
