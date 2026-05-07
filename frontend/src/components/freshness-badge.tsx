import { Badge } from '@/components/ui/badge';

type Freshness = 'current' | 'stale' | 'estimated' | 'manual' | 'unknown';

const variantFor: Record<Freshness, 'success' | 'warning' | 'critical' | 'secondary'> = {
  current: 'success',
  stale: 'critical',
  unknown: 'critical',
  estimated: 'warning',
  manual: 'secondary',
};

export function FreshnessBadge({ state }: { state: string }) {
  const v = variantFor[(state as Freshness)] ?? 'secondary';
  return <Badge variant={v}>{state}</Badge>;
}
