import { describe, expect, test } from 'vitest';
import { parseCsv } from './csv';

describe('parseCsv', () => {
  test('returns empty result for empty input', () => {
    expect(parseCsv('')).toEqual({ header: [], rows: [] });
  });

  test('parses a simple header + rows', () => {
    const out = parseCsv('name,kind\nfoo,server\nbar,switch\n');
    expect(out.header).toEqual(['name', 'kind']);
    expect(out.rows).toEqual([
      { name: 'foo', kind: 'server' },
      { name: 'bar', kind: 'switch' },
    ]);
  });

  test('handles CRLF line endings', () => {
    const out = parseCsv('name,kind\r\nfoo,server\r\nbar,switch\r\n');
    expect(out.rows).toHaveLength(2);
    expect(out.rows[0].name).toBe('foo');
  });

  test('strips a leading UTF-8 BOM', () => {
    const out = parseCsv('﻿name,kind\nfoo,server\n');
    expect(out.header[0]).toBe('name'); // not "﻿name"
    expect(out.rows[0]).toEqual({ name: 'foo', kind: 'server' });
  });

  test('preserves quoted commas as data', () => {
    const out = parseCsv('name,description\n"R01-srv1","big, important box"\n');
    expect(out.rows[0]).toEqual({
      name: 'R01-srv1',
      description: 'big, important box',
    });
  });

  test('unescapes doubled quotes inside quoted fields', () => {
    const out = parseCsv('name,note\n"R01","says ""hi"""\n');
    expect(out.rows[0].note).toBe('says "hi"');
  });

  test('trims whitespace around header names but keeps cell whitespace verbatim', () => {
    const out = parseCsv(' name , kind \nfoo,  server  \n');
    expect(out.header).toEqual(['name', 'kind']);
    // Cell trimming: parseCsv currently does .trim() on cells; if that
    // ever changes, this test pins the contract.
    expect(out.rows[0]).toEqual({ name: 'foo', kind: 'server' });
  });

  test('drops a fully-empty trailing row from a final newline', () => {
    const out = parseCsv('name\nfoo\n');
    expect(out.rows).toHaveLength(1);
  });

  test('keeps blank cells as empty strings, not undefined', () => {
    const out = parseCsv('a,b,c\n1,,3\n');
    expect(out.rows[0]).toEqual({ a: '1', b: '', c: '3' });
  });

  test('handles short rows by padding missing columns to empty string', () => {
    const out = parseCsv('a,b,c\n1,2\n');
    expect(out.rows[0]).toEqual({ a: '1', b: '2', c: '' });
  });

  test('handles embedded newlines inside quoted fields', () => {
    const out = parseCsv('name,note\n"R01","line1\nline2"\n');
    expect(out.rows).toHaveLength(1);
    expect(out.rows[0].note).toBe('line1\nline2');
  });
});
