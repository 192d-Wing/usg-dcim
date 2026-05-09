import { useMemo, useRef, useState } from 'react';
import { useList, useGetIdentity } from '@refinedev/core';
import {
  Upload, FileText, AlertTriangle, CheckCircle2, Trash2, Download,
} from 'lucide-react';
import { http } from '@/lib/http';
import { parseCsv } from '@/lib/csv';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Badge } from '@/components/ui/badge';
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table';
import {
  Select, SelectContent, SelectItem, SelectTrigger, SelectValue,
} from '@/components/ui/select';
import { toast } from 'sonner';

type Site = { id: string; code: string; name: string };
type Rack = { id: string; name: string; code: string; site_id: string };

const ASSET_KINDS = [
  'server', 'switch', 'router', 'pdu', 'ups', 'crac', 'sensor',
  'storage', 'chassis', 'blade', 'patch_panel', 'other',
];

// Required columns we map straight through. Anything in `header` not listed
// here is preserved into metadata_json so users can carry custom fields.
const REQUIRED = ['name', 'kind'] as const;
const SUPPORTED = [
  'name', 'kind', 'hostname', 'manufacturer', 'model', 'serial', 'firmware',
  'rack_code', 'rack_position_u', 'rack_units',
  'face', 'mount', 'pdu_side', 'psu_count', 'port_count',
  'mgmt_ip', 'mgmt_protocol', 'mgmt_port', 'lifecycle_state',
] as const;

type Issue = { row: number; message: string };

type Mapped = {
  body: Record<string, unknown>;
  issues: Issue[];
};

const SAMPLE = `name,kind,manufacturer,model,serial,hostname,rack_code,rack_position_u,rack_units,psu_count
R01-srv1,server,Dell,PowerEdge R750,ABC123,r01-srv1.dc1.local,R01,1,2,2
R01-sw1,switch,Cisco,Catalyst 9300,DEF456,r01-sw1.dc1.local,R01,3,1,2
R01-pdu-A,pdu,APC,AP8941,GHI789,,R01,,,
`;

export function ImportPage() {
  const sitesRes = useList<Site>({ resource: 'inventory/sites', pagination: { pageSize: 200 } });
  const sites = sitesRes.result.data ?? [];
  const [siteId, setSiteId] = useState('');
  const racksRes = useList<Rack>({
    resource: 'inventory/racks',
    pagination: { pageSize: 500 },
    filters: siteId ? [{ field: 'site_id', operator: 'eq', value: siteId }] : [],
    queryOptions: { enabled: !!siteId },
  });
  const racks = racksRes.result.data ?? [];
  const racksByCode = useMemo(
    () => new Map(racks.map((r) => [r.code.toLowerCase(), r])), [racks],
  );

  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const canBulk = identity?.capabilities.includes('inventory:bulk');

  const [filename, setFilename] = useState<string | null>(null);
  const [header, setHeader] = useState<string[]>([]);
  const [rows, setRows] = useState<Record<string, string>[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState<{ inserted: number; updated: number; failed: number; errors: { serial?: string | null; error: string }[] } | null>(null);
  const [dragOver, setDragOver] = useState(false);
  const fileInput = useRef<HTMLInputElement | null>(null);

  function clearFile() {
    setFilename(null);
    setHeader([]);
    setRows([]);
    setResult(null);
  }

  async function loadFile(file: File) {
    const text = await file.text();
    const parsed = parseCsv(text);
    if (parsed.header.length === 0) {
      toast.error('CSV is empty');
      return;
    }
    setFilename(file.name);
    setHeader(parsed.header);
    setRows(parsed.rows);
    setResult(null);
  }

  const validation = useMemo(() => mapRows(rows, racksByCode), [rows, racksByCode]);

  async function submit() {
    if (!siteId) return toast.error('Pick a site first');
    if (validation.issues.some((i) => i.message.startsWith('FATAL'))) {
      return toast.error('Fix fatal issues before importing');
    }
    setSubmitting(true);
    try {
      const payload = validation.bodies.map((b) => ({ ...b, site_id: siteId }));
      const r = await http.post('/inventory/assets/bulk', payload);
      setResult(r.data);
      toast.success(`Imported: ${r.data.inserted} inserted, ${r.data.updated} updated, ${r.data.failed} failed`);
    } catch (err: any) {
      toast.error(err?.message ?? 'import failed');
    } finally {
      setSubmitting(false);
    }
  }

  function downloadSample() {
    const blob = new Blob([SAMPLE], { type: 'text/csv' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'dcim-assets-sample.csv';
    a.click();
    URL.revokeObjectURL(url);
  }

  if (!canBulk) {
    return (
      <div className="space-y-2">
        <h1 className="text-2xl font-semibold tracking-tight">Bulk import</h1>
        <p className="text-sm text-muted-foreground">
          You don't have <code className="font-mono">inventory:bulk</code>. Ask an admin for a role
          that includes it (EnterpriseAdmin).
        </p>
      </div>
    );
  }

  const requiredMissing = REQUIRED.filter((c) => !header.includes(c));
  const unknownColumns = header.filter((c) => !SUPPORTED.includes(c as typeof SUPPORTED[number]));
  const fatal = validation.issues.filter((i) => i.message.startsWith('FATAL'));
  const warnings = validation.issues.filter((i) => !i.message.startsWith('FATAL'));

  return (
    <div className="space-y-4">
      <div className="flex items-end justify-between">
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">Bulk import — assets</h1>
          <p className="text-sm text-muted-foreground">
            Upsert by (manufacturer, serial). Required columns: <code className="font-mono">name</code>, <code className="font-mono">kind</code>.
          </p>
        </div>
        <Button variant="outline" onClick={downloadSample}>
          <Download className="h-4 w-4" /> Download sample
        </Button>
      </div>

      <Card>
        <CardContent className="p-4 space-y-3">
          <div className="space-y-1">
            <label className="text-xs font-medium text-muted-foreground">Target site</label>
            <Select value={siteId} onValueChange={setSiteId}>
              <SelectTrigger className="w-full"><SelectValue placeholder="Pick a site" /></SelectTrigger>
              <SelectContent>
                {sites.map((s) => (
                  <SelectItem key={s.id} value={s.id}>{s.code} · {s.name}</SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">
              All rows will be assigned to this site. The optional <code className="font-mono">rack_code</code> column places them in a rack.
            </p>
          </div>
        </CardContent>
      </Card>

      {!filename ? (
        <button
          type="button"
          onClick={() => fileInput.current?.click()}
          onDragEnter={(e) => { e.preventDefault(); setDragOver(true); }}
          onDragLeave={() => setDragOver(false)}
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => {
            e.preventDefault();
            setDragOver(false);
            const file = e.dataTransfer.files?.[0];
            if (file) loadFile(file);
          }}
          className={`flex w-full flex-col items-center justify-center gap-2 rounded-md border-2 border-dashed p-12 text-sm transition-colors ${
            dragOver ? 'border-primary bg-primary/5' : 'border-muted hover:border-muted-foreground/50'
          }`}
        >
          <Upload className="h-8 w-8 text-muted-foreground" />
          <span className="font-medium">Drop a CSV here, or click to choose</span>
          <span className="text-xs text-muted-foreground">UTF-8 · header row required</span>
          <input
            ref={fileInput}
            type="file"
            accept=".csv,text/csv"
            className="hidden"
            onChange={(e) => {
              const file = e.target.files?.[0];
              if (file) loadFile(file);
              e.target.value = '';
            }}
          />
        </button>
      ) : (
        <Card>
          <CardHeader className="flex-row items-center justify-between space-y-0 pb-3">
            <CardTitle className="flex items-center gap-2 text-base">
              <FileText className="h-4 w-4" /> {filename}
              <Badge variant="secondary" className="ml-2">{rows.length} rows</Badge>
            </CardTitle>
            <Button variant="ghost" size="sm" onClick={clearFile}>
              <Trash2 className="h-3.5 w-3.5" /> Clear
            </Button>
          </CardHeader>
          <CardContent className="space-y-3">
            {requiredMissing.length > 0 && (
              <Issuebar
                tone="danger"
                title={`Missing required column${requiredMissing.length === 1 ? '' : 's'}`}
                detail={requiredMissing.map((c) => `"${c}"`).join(', ')}
              />
            )}
            {unknownColumns.length > 0 && (
              <Issuebar
                tone="warn"
                title={`Unrecognized column${unknownColumns.length === 1 ? '' : 's'} (will be ignored)`}
                detail={unknownColumns.map((c) => `"${c}"`).join(', ')}
              />
            )}
            {fatal.length > 0 && <Issuebar tone="danger" title={`${fatal.length} fatal row issue${fatal.length === 1 ? '' : 's'}`} issues={fatal} />}
            {warnings.length > 0 && <Issuebar tone="warn" title={`${warnings.length} warning${warnings.length === 1 ? '' : 's'}`} issues={warnings} />}

            <div className="overflow-x-auto rounded-md border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-12 text-right">#</TableHead>
                    {header.map((h) => (
                      <TableHead key={h} className={SUPPORTED.includes(h as typeof SUPPORTED[number]) ? '' : 'text-muted-foreground line-through'}>
                        {h}
                      </TableHead>
                    ))}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {rows.slice(0, 10).map((r, i) => (
                    <TableRow key={`row-${i}`}>
                      <TableCell className="text-muted-foreground tabular-nums">{i + 1}</TableCell>
                      {header.map((h) => (
                        <TableCell key={h} className="text-xs font-mono whitespace-nowrap">{r[h]}</TableCell>
                      ))}
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
              {rows.length > 10 && (
                <p className="px-3 py-2 text-xs text-muted-foreground border-t">
                  Showing 10 of {rows.length} rows. All will be sent on import.
                </p>
              )}
            </div>

            <div className="flex justify-end gap-2">
              <Button variant="outline" onClick={clearFile}>Cancel</Button>
              <Button
                onClick={submit}
                disabled={submitting || !siteId || requiredMissing.length > 0 || fatal.length > 0 || rows.length === 0}
              >
                {submitting ? 'Importing…' : `Import ${rows.length} row${rows.length === 1 ? '' : 's'}`}
              </Button>
            </div>
          </CardContent>
        </Card>
      )}

      {result && (
        <Card>
          <CardHeader className="pb-2">
            <CardTitle className="flex items-center gap-2 text-base">
              <CheckCircle2 className="h-4 w-4 text-success" /> Import complete
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-3 text-sm">
            <div className="flex gap-2">
              <Badge variant="success">{result.inserted} inserted</Badge>
              <Badge variant="secondary">{result.updated} updated</Badge>
              {result.failed > 0
                ? <Badge variant="critical">{result.failed} failed</Badge>
                : <Badge variant="success">0 failed</Badge>}
            </div>
            {result.errors.length > 0 && (
              <div className="space-y-1">
                <div className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">Errors</div>
                <ul className="rounded-md border bg-muted/30 p-2 text-xs space-y-1 max-h-48 overflow-y-auto">
                  {result.errors.map((e, i) => (
                    <li key={`err-${i}`} className="font-mono">
                      {e.serial ? `${e.serial}: ` : ''}<span className="text-destructive">{e.error}</span>
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </CardContent>
        </Card>
      )}
    </div>
  );
}

function Issuebar({
  tone, title, detail, issues,
}: {
  tone: 'warn' | 'danger';
  title: string;
  detail?: string;
  issues?: Issue[];
}) {
  const cls = tone === 'danger'
    ? 'border-destructive/40 bg-destructive/10'
    : 'border-warning/40 bg-warning/10';
  const titleCls = tone === 'danger' ? 'text-destructive' : 'text-warning';
  return (
    <div className={`rounded-md border p-3 text-xs ${cls}`}>
      <p className={`font-medium ${titleCls} flex items-center gap-1.5`}>
        <AlertTriangle className="h-3.5 w-3.5" /> {title}
      </p>
      {detail && <p className="mt-1 text-foreground/80">{detail}</p>}
      {issues && issues.length > 0 && (
        <ul className="mt-1 list-disc pl-5 text-foreground/80 space-y-0.5">
          {issues.slice(0, 8).map((i, k) => (
            <li key={`iss-${k}`}>row {i.row}: {i.message.replace(/^FATAL /, '')}</li>
          ))}
          {issues.length > 8 && <li>…and {issues.length - 8} more</li>}
        </ul>
      )}
    </div>
  );
}

function mapRows(
  rows: Record<string, string>[],
  racksByCode: Map<string, Rack>,
): { bodies: Record<string, unknown>[]; issues: Issue[] } {
  const bodies: Record<string, unknown>[] = [];
  const issues: Issue[] = [];
  rows.forEach((r, idx) => {
    const rowNum = idx + 2; // +1 for 1-index, +1 for header row
    const m = mapRow(r, racksByCode, rowNum, issues);
    if (m) bodies.push(m.body);
  });
  return { bodies, issues };
}

function mapRow(
  r: Record<string, string>,
  racksByCode: Map<string, Rack>,
  rowNum: number,
  issues: Issue[],
): Mapped | null {
  const name = (r.name ?? '').trim();
  const kind = (r.kind ?? '').trim().toLowerCase();
  if (!name) {
    issues.push({ row: rowNum, message: 'FATAL name is empty' });
    return null;
  }
  if (!ASSET_KINDS.includes(kind)) {
    issues.push({ row: rowNum, message: `FATAL unknown kind "${kind}"` });
    return null;
  }

  const body: Record<string, unknown> = {
    name,
    kind,
    hostname: r.hostname || null,
    manufacturer: r.manufacturer || null,
    model: r.model || null,
    serial: r.serial || null,
    firmware: r.firmware || null,
    face: r.face || 'front',
    mount: r.mount || 'rack',
    pdu_side: r.pdu_side || null,
    mgmt_ip: r.mgmt_ip || null,
    mgmt_protocol: r.mgmt_protocol || null,
    lifecycle_state: r.lifecycle_state || 'active',
    metadata_json: {},
  };

  // Numeric coercions with row-level issues so the user sees what's wrong.
  for (const [col, key] of [
    ['rack_position_u', 'rack_position_u'],
    ['rack_units', 'rack_units'],
    ['psu_count', 'psu_count'],
    ['port_count', 'port_count'],
    ['mgmt_port', 'mgmt_port'],
  ] as const) {
    const v = r[col];
    if (v === undefined || v === '') continue;
    const n = Number(v);
    if (Number.isNaN(n)) {
      issues.push({ row: rowNum, message: `${col} "${v}" is not a number (skipping)` });
    } else {
      body[key] = n;
    }
  }
  if (body.rack_units === undefined && body.rack_position_u !== undefined) body.rack_units = 1;

  // rack_code → rack_id lookup. Warn (don't fail) if unknown — the asset is
  // still importable as unplaced.
  const rackCode = (r.rack_code ?? '').trim();
  if (rackCode) {
    const rk = racksByCode.get(rackCode.toLowerCase());
    if (rk) body.rack_id = rk.id;
    else issues.push({ row: rowNum, message: `rack_code "${rackCode}" not found at this site (asset will be unplaced)` });
  }

  return { body, issues: [] };
}
