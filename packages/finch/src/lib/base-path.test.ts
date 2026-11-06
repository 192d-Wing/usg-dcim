import { describe, expect, test } from 'vitest';
import { BASE, apiBase, normalizeBase, routerBasename, stripBase } from './base-path';

describe('base path', () => {
  test('standalone (default build) is /', () => {
    expect(BASE).toBe('/');
    expect(apiBase()).toBe('/api/v1');
    expect(routerBasename()).toBeUndefined();
    expect(stripBase('/t/dcim/racks')).toBe('/t/dcim/racks');
  });

  test('normalizeBase', () => {
    for (const v of [undefined, '', '/', './']) expect(normalizeBase(v)).toBe('/');
    for (const v of ['/t/dcim/', '/t/dcim', 't/dcim/', '//t/dcim//']) expect(normalizeBase(v)).toBe('/t/dcim/');
  });

  test('console build /t/dcim/', () => {
    const b = '/t/dcim/';
    expect(apiBase(b)).toBe('/t/dcim/api/v1');
    expect(routerBasename(b)).toBe('/t/dcim');
    expect(stripBase('/t/dcim/racks/1', b)).toBe('/racks/1');
    expect(stripBase('/t/dcim/', b)).toBe('/');
    expect(stripBase('/t/dcim', b)).toBe('/');
    expect(stripBase('/t/dcim?x=1', b)).toBe('/?x=1');
    expect(stripBase('/racks', b)).toBe('/racks');
    expect(stripBase('/t/dcimx/racks', b)).toBe('/t/dcimx/racks');
  });
});
