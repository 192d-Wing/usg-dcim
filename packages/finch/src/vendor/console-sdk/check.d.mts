export declare const HEADER_RE: RegExp;
export declare function sha256Hex(text: string): Promise<string>;
export declare function splitHeader(source: string): { header: string; body: string };
export type VerifyResult =
  | { ok: true; version: string; protocol: number; source: string; sha256: string }
  | { ok: false; error: string };
export declare function verifyVendoredSdk(
  source: string,
  expect?: { protocol?: number; version?: string; allowDirty?: boolean },
): Promise<VerifyResult>;
