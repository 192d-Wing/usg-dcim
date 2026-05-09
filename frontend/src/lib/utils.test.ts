import { describe, expect, test } from 'vitest';
import { cn, formatDate } from './utils';

describe('cn', () => {
  test('joins class strings', () => {
    expect(cn('a', 'b')).toBe('a b');
  });

  test('drops falsy values', () => {
    expect(cn('a', false, undefined, null, '', 'b')).toBe('a b');
  });

  test('lets twMerge resolve conflicting tailwind utilities', () => {
    // p-2 should be overridden by p-4; the merge keeps the last winner.
    expect(cn('p-2', 'p-4')).toBe('p-4');
  });
});

describe('formatDate', () => {
  test('returns em-dash for null/undefined/empty', () => {
    expect(formatDate(null)).toBe('—');
    expect(formatDate(undefined)).toBe('—');
    expect(formatDate('')).toBe('—');
  });

  test('returns em-dash for invalid date strings', () => {
    expect(formatDate('not a date')).toBe('—');
  });

  test('formats valid ISO timestamps to a non-empty locale string', () => {
    const out = formatDate('2026-05-09T12:00:00Z');
    expect(out).not.toBe('—');
    expect(out.length).toBeGreaterThan(0);
  });
});
