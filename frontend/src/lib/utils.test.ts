import { describe, expect, test } from 'vitest';
import { formatDate, relativeTime } from './utils';

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

describe('relativeTime', () => {
  test('returns em-dash for null/undefined', () => {
    expect(relativeTime(null)).toBe('—');
    expect(relativeTime(undefined)).toBe('—');
  });
});
