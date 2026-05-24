/**
 * Minimal RFC 4180 CSV parser.
 *
 * Handles: quoted fields, embedded quotes ("" inside ""), CRLF or LF line
 * endings, leading BOM, trailing newlines. Numbers are returned as strings —
 * callers coerce. This is intentionally inline (no dep) because the import
 * page is the only consumer.
 */

export type ParseResult = {
  header: string[];
  rows: Record<string, string>[];
};

export function parseCsv(text: string): ParseResult {
  // Strip UTF-8 BOM if present so the first column name doesn't get poisoned.
  if (text.charCodeAt(0) === 0xfeff) text = text.slice(1);

  const records: string[][] = [];
  let cur: string[] = [];
  let field = '';
  let inQuotes = false;
  let i = 0;
  const n = text.length;

  while (i < n) {
    const c = text[i];

    if (inQuotes) {
      if (c === '"') {
        if (i + 1 < n && text[i + 1] === '"') {
          field += '"';
          i += 2;
          continue;
        }
        inQuotes = false;
        i++;
        continue;
      }
      field += c;
      i++;
      continue;
    }

    if (c === '"') {
      inQuotes = true;
      i++;
      continue;
    }
    if (c === ',') {
      cur.push(field);
      field = '';
      i++;
      continue;
    }
    if (c === '\r') {
      // swallow; the \n on the next byte does the work
      i++;
      continue;
    }
    if (c === '\n') {
      cur.push(field);
      field = '';
      records.push(cur);
      cur = [];
      i++;
      continue;
    }
    field += c;
    i++;
  }
  if (field.length > 0 || cur.length > 0) {
    cur.push(field);
    records.push(cur);
  }
  // Drop fully-empty trailing rows that come from a final newline.
  while (records.length > 0) {
    const last = records[records.length - 1];
    if (last.length === 1 && last[0] === '') records.pop();
    else break;
  }

  if (records.length === 0) return { header: [], rows: [] };
  const header = records[0].map((h) => h.trim());
  const rows = records.slice(1).map((r) => {
    const obj: Record<string, string> = {};
    for (let k = 0; k < header.length; k++) {
      obj[header[k]] = (r[k] ?? '').trim();
    }
    return obj;
  });
  return { header, rows };
}
