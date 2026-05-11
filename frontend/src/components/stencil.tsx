// Procedural vendor stencil renderer. Resolves the best catalog entry for an
// asset (exact mfg+model, then mfg+kind, then a generic kind block) and draws
// either the entry's `image_url` or a stylized SVG using the vendor palette
// and kind-specific front-panel art.
import { useQuery } from '@tanstack/react-query';
import { http } from '@/lib/http';

export type StencilEntry = {
  manufacturer: string;
  model: string;
  u: number;
  kind_hint?: string;
  port_count?: number;
  vertical?: boolean;
  image_url?: string;
};

export type StencilCatalog = {
  palette: Record<string, { primary: string; accent: string }>;
  stencils: StencilEntry[];
};

export function useStencilCatalog() {
  return useQuery({
    queryKey: ['stencil-catalog'],
    queryFn: async (): Promise<StencilCatalog> => {
      const r = await http.get('/stencils');
      return r.data;
    },
    staleTime: 5 * 60_000,
  });
}

export function resolveStencil(
  catalog: StencilCatalog | null | undefined,
  asset: { manufacturer: string | null; model: string | null; kind: string },
): { entry: StencilEntry | null; palette: { primary: string; accent: string } } {
  const mfg = (asset.manufacturer ?? '').trim().toLowerCase();
  const fallbackPalette = catalog?.palette[mfg] ?? { primary: '#6b7280', accent: '#374151' };
  if (!catalog) return { entry: null, palette: fallbackPalette };
  const exact = catalog.stencils.find(
    (s) => s.manufacturer.toLowerCase() === mfg && s.model.toLowerCase() === (asset.model ?? '').toLowerCase(),
  );
  if (exact) return { entry: exact, palette: fallbackPalette };
  const vendorKind = catalog.stencils.find(
    (s) => s.manufacturer.toLowerCase() === mfg && (s.kind_hint ?? '') === asset.kind,
  );
  if (vendorKind) return { entry: vendorKind, palette: fallbackPalette };
  return { entry: null, palette: fallbackPalette };
}

type Props = {
  asset: { name: string; manufacturer: string | null; model: string | null; kind: string };
  width: number;
  height: number;
  palette: { primary: string; accent: string };
  entry: StencilEntry | null;
  alertCount?: number;
};

export function Stencil({ asset, width, height, palette, entry, alertCount = 0 }: Props) {
  const kind = entry?.kind_hint ?? asset.kind;
  const ports = entry?.port_count ?? 0;
  const label = `${asset.manufacturer ?? ''} ${asset.model ?? asset.name}`.trim();
  const border = alertCount > 0 ? '#ef4444' : '#0a0c10';

  if (entry?.image_url) {
    return (
      <div
        style={{
          position: 'relative', height: '100%', width: '100%',
          overflow: 'hidden', borderRadius: 2,
          background: '#1a1d23', border: `1px solid ${border}`,
        }}
        title={label}
      >
        <img
          src={entry.image_url}
          alt={label}
          style={{ height: '100%', width: '100%', objectFit: 'fill' }}
        />
        {alertCount > 0 && (
          <span style={{
            position: 'absolute', right: 4, top: 4,
            padding: '0 6px', borderRadius: 999,
            fontSize: 10, fontWeight: 600,
            background: 'var(--color-text-status-error, #d91515)',
            color: '#fff',
          }}>
            {alertCount}
          </span>
        )}
      </div>
    );
  }

  return (
    <svg width={width} height={height} viewBox={`0 0 ${width} ${height}`} style={{ display: 'block' }}>
      <title>{label}</title>
      <rect x="0" y="0" width={width} height={height} rx="2" fill="#1a1d23" stroke={border} strokeWidth="1" />
      <rect x="0" y="0" width="4" height={height} fill={palette.primary} />
      <rect x="2" y="2" width="2" height={Math.max(2, height * 0.15)} fill={palette.accent} />
      <rect x="2" y={height - Math.max(2, height * 0.15) - 2} width="2" height={Math.max(2, height * 0.15)} fill={palette.accent} />
      <rect x={width - 4} y="2" width="2" height={Math.max(2, height * 0.15)} fill={palette.accent} />
      <rect x={width - 4} y={height - Math.max(2, height * 0.15) - 2} width="2" height={Math.max(2, height * 0.15)} fill={palette.accent} />

      {kind === 'server' && <ServerFront width={width} height={height} palette={palette} />}
      {kind === 'switch' && <SwitchFront width={width} height={height} palette={palette} ports={ports || 24} />}
      {kind === 'router' && <SwitchFront width={width} height={height} palette={palette} ports={ports || 8} />}
      {kind === 'pdu' && (entry?.vertical
        ? <PduVertical width={width} height={height} palette={palette} outlets={ports || 24} />
        : <PduHorizontal width={width} height={height} palette={palette} outlets={ports || 8} />)}
      {kind === 'ups' && <UpsFront width={width} height={height} palette={palette} />}
      {kind === 'crac' && <CracFront width={width} height={height} palette={palette} />}
      {kind === 'sensor' && <SensorFront width={width} height={height} palette={palette} />}
      {kind === 'storage' && <ServerFront width={width} height={height} palette={palette} drives />}
      {(kind === 'chassis' || kind === 'blade') && <ChassisFront width={width} height={height} palette={palette} />}

      {height >= 16 && (
        <text x={width / 2} y={height / 2 + 4} textAnchor="middle" fontSize={Math.min(11, height * 0.35)}
              fill="white" fontFamily="ui-monospace, monospace" fontWeight={600}>
          {truncate(label || asset.name, Math.floor(width / 7))}
        </text>
      )}
      {alertCount > 0 && (
        <g>
          <circle cx={width - 10} cy={10} r="7" fill="#ef4444" />
          <text x={width - 10} y={13} textAnchor="middle" fontSize="9" fill="white" fontWeight="700">{alertCount}</text>
        </g>
      )}
    </svg>
  );
}

function ServerFront({ width, height, palette, drives = false }: { width: number; height: number; palette: any; drives?: boolean }) {
  const drivesX = width * 0.45, drivesW = width * 0.5;
  const cols = drives ? 12 : 6;
  const rows = Math.max(1, Math.floor(height / 6));
  const cw = drivesW / cols, ch = (height - 6) / rows;
  return (
    <>
      <rect x="8" y="3" width="4" height="4" fill={palette.primary} />
      <rect x="14" y="3" width="2" height="2" fill="#10b981" />
      <rect x="18" y="3" width="2" height="2" fill="#facc15" />
      {Array.from({ length: rows }).map((_, r) =>
        Array.from({ length: cols }).map((__, c) => (
          <rect key={`${r}-${c}`} x={drivesX + c * cw + 0.5} y={3 + r * ch + 0.5} width={cw - 1} height={ch - 1}
                fill="#2a2d33" stroke="#3a3d43" strokeWidth="0.5" />
        )),
      )}
    </>
  );
}
function SwitchFront({ width, height, palette, ports }: { width: number; height: number; palette: any; ports: number }) {
  const cols = Math.min(ports, Math.max(8, Math.floor(width / 5)));
  const portW = (width - 16) / cols;
  const portH = Math.max(3, height * 0.45);
  return (
    <>
      <rect x="6" y={(height - portH) / 2} width={width - 12} height={portH} fill="#0e1014" />
      {Array.from({ length: cols }).map((_, c) => (
        <rect key={c} x={8 + c * portW} y={(height - portH) / 2 + 1}
              width={portW - 0.5} height={portH - 2}
              fill={c % 2 === 0 ? palette.primary : palette.accent} opacity={0.85} />
      ))}
    </>
  );
}
function PduHorizontal({ width, height, palette, outlets }: { width: number; height: number; palette: any; outlets: number }) {
  const cols = Math.min(outlets, Math.floor((width - 12) / 9));
  const ow = (width - 12) / cols;
  return (
    <>
      <rect x="0" y={height / 2 - 1} width={width} height="2" fill={palette.accent} />
      {Array.from({ length: cols }).map((_, c) => (
        <circle key={c} cx={6 + c * ow + ow / 2} cy={height / 2} r={Math.min(3, ow / 3)}
                fill="#0e1014" stroke={palette.primary} strokeWidth="0.5" />
      ))}
    </>
  );
}
function PduVertical({ width, height, palette, outlets }: { width: number; height: number; palette: any; outlets: number }) {
  const rows = Math.min(outlets, Math.floor((height - 8) / 6));
  const oh = (height - 8) / rows;
  return (
    <>
      <rect x={width / 2 - 1} y="0" width="2" height={height} fill={palette.accent} />
      {Array.from({ length: rows }).map((_, r) => (
        <circle key={r} cx={width / 2} cy={4 + r * oh + oh / 2} r="2.5"
                fill="#0e1014" stroke={palette.primary} strokeWidth="0.5" />
      ))}
    </>
  );
}
function UpsFront({ width, height, palette }: { width: number; height: number; palette: any }) {
  const dispW = Math.min(width * 0.3, 60);
  return (
    <>
      <rect x={(width - dispW) / 2} y={height * 0.15} width={dispW} height={height * 0.35} fill="#0a3a2a" stroke={palette.primary} />
      <rect x={(width - dispW) / 2 + 6} y={height * 0.2} width={dispW - 12} height={height * 0.05} fill="#10b981" opacity="0.6" />
      <rect x={(width - dispW) / 2 + 6} y={height * 0.3} width={dispW - 12} height={height * 0.05} fill="#10b981" opacity="0.4" />
      {Array.from({ length: 8 }).map((_, i) => (
        <rect key={i} x={8 + i * ((width - 16) / 8)} y={height * 0.65} width={(width - 16) / 8 - 2} height="2" fill={palette.accent} />
      ))}
    </>
  );
}
function CracFront({ width, height, palette }: { width: number; height: number; palette: any }) {
  const lines = Math.floor(height / 3);
  return (
    <>
      <rect x="6" y="3" width={width - 12} height={height - 6} fill="#0a0c10" stroke={palette.primary} strokeWidth="0.5" />
      {Array.from({ length: lines }).map((_, i) => (
        <line key={i} x1="8" x2={width - 8} y1={5 + i * 3} y2={5 + i * 3} stroke={palette.accent} strokeWidth="0.4" />
      ))}
    </>
  );
}
function SensorFront({ width, height, palette }: { width: number; height: number; palette: any }) {
  return (
    <>
      <rect x="6" y={height * 0.25} width={width - 12} height={height * 0.5} fill="#0a0c10" stroke={palette.primary} />
      <circle cx={width / 2} cy={height / 2} r={Math.min(3, height / 4)} fill={palette.primary} />
    </>
  );
}
function ChassisFront({ width, height, palette }: { width: number; height: number; palette: any }) {
  const slots = 8, sw = (width - 12) / slots;
  return (
    <>
      {Array.from({ length: slots }).map((_, i) => (
        <rect key={i} x={6 + i * sw + 0.5} y="3" width={sw - 1} height={height - 6}
              fill="#1f2228" stroke={palette.accent} strokeWidth="0.5" />
      ))}
    </>
  );
}
function truncate(s: string, n: number): string {
  if (s.length <= n) return s;
  return s.slice(0, Math.max(0, n - 1)) + '…';
}
