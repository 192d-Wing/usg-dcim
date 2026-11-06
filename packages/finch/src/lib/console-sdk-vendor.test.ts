// Compat test for the vendored console SDK (written only by
// usg-ops-console/sdk-js scripts/vendor.mjs; never hand-edit it).
import { describe, expect, test } from 'vitest';
import { verifyVendoredSdk } from '@/vendor/console-sdk/check.mjs';
import * as sdk from '@/vendor/console-sdk/console-sdk';

const raw = import.meta.glob(['../vendor/console-sdk/console-sdk.ts', '../vendor/console-sdk/fixtures/**/*'], {
  query: '?raw', import: 'default', eager: true,
}) as Record<string, string>;
const read = (p: string): string => {
  const v = raw[`../vendor/console-sdk/${p}`];
  if (v === undefined) throw new Error(`missing vendored file ${p}`);
  return v;
};
const fixture = (p: string) => read(`fixtures/${p}`).replace(/[ \t\r\n]+$/, '');

type Entry = {
  file: string; kind: string; direction: string; type: string | null;
  expect: 'decode' | 'ignore' | 'malformed' | 'too_large'; canonical?: string;
};

function errorKind(f: () => unknown): string {
  try { f(); } catch (e) {
    return e instanceof sdk.MessageError ? e.kind : `other:${String(e)}`;
  }
  return 'none';
}

describe('vendored console-sdk', () => {
  test('header and sha256 are intact, protocol 1', async () => {
    const r = await verifyVendoredSdk(read('console-sdk.ts'), { protocol: 1, version: '0.1.0' });
    expect(r).toMatchObject({ ok: true, protocol: 1, version: '0.1.0' });
    expect(sdk.PROTOCOL_VERSION).toBe(1);
  });

  test('vendored codec passes every sdk-message fixture', () => {
    const manifest = JSON.parse(fixture('MANIFEST.json')) as { protocol: number; fixtures: Entry[] };
    expect(manifest.protocol).toBe(1);
    const codecs: Record<string, { decode: (s: string) => any; encode: (m: any) => string }> = {
      'shell-to-tool': { decode: sdk.decodeShellToTool, encode: sdk.encodeShellToTool },
      'tool-to-shell': { decode: sdk.decodeToolToShell, encode: sdk.encodeToolToShell },
    };
    let n = 0;
    for (const e of manifest.fixtures.filter((x) => x.kind === 'sdk-message')) {
      const c = codecs[e.direction];
      if (!c) continue;
      const data = fixture(e.file);
      if (e.expect === 'decode') {
        const m = c.decode(data);
        expect(m?.type, e.file).toBe(e.type);
        expect(JSON.parse(c.encode(m)), e.file).toStrictEqual(JSON.parse(fixture(e.canonical ?? e.file)));
      } else if (e.expect === 'ignore') {
        expect(c.decode(data), e.file).toBeNull();
      } else {
        expect(errorKind(() => c.decode(data)), e.file).toBe(e.expect);
      }
      n += 1;
    }
    expect(n).toBeGreaterThan(0);
  });
});
