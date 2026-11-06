import { describe, expect, test } from 'vitest';
import { fabricIdForSite, unmatchedSiteFor } from './fabric-scope';

const fabrics = [
  { id: 'f1', name: 'East DC', slug: 'east' },
  { id: 'f2', name: 'West DC', slug: 'west' },
  { id: 'f3', name: 'No slug' },
];

describe('console site -> fabric', () => {
  test('maps by exact slug', () => {
    expect(fabricIdForSite(fabrics, 'west')).toBe('f2');
  });
  test('unknown, case-different, name or id: no fabric (empty state)', () => {
    for (const s of ['north', 'East', 'East DC', 'f1']) expect(fabricIdForSite(fabrics, s)).toBeNull();
  });
  test('no site: no fabric', () => {
    expect(fabricIdForSite(fabrics, null)).toBeNull();
    expect(fabricIdForSite(fabrics, undefined)).toBeNull();
    expect(fabricIdForSite([], 'east')).toBeNull();
  });
});

describe('unmatchedSiteFor (A11Y-003)', () => {
  const fabrics = [{ id: 'f1', name: 'East', slug: 'east' }];
  test('unknown site is reported once loaded', () => {
    expect(unmatchedSiteFor(fabrics, 'west', false)).toBe('west');
  });
  test('matched, unset or loading -> null', () => {
    expect(unmatchedSiteFor(fabrics, 'east', false)).toBeNull();
    expect(unmatchedSiteFor(fabrics, null, false)).toBeNull();
    expect(unmatchedSiteFor([], 'west', true)).toBeNull();
  });
});
