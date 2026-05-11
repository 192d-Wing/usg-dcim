import { useEffect, useState } from 'react';
import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Icon from '@cloudscape-design/components/icon';
import Input from '@cloudscape-design/components/input';
import { http } from '@/lib/http';

type IpHit = {
  id: string;
  address: string;
  role: string;
  status: string;
  source: string;
  dns_name: string | null;
  subnet_id: string | null;
  subnet_prefix: string | null;
  vrf_id: string | null;
  vrf_name: string | null;
  fabric_id: string | null;
  fabric_name: string | null;
  asset_id: string | null;
  asset_name: string | null;
};

type SearchHit = {
  sites: { id: string; name: string; code: string }[];
  racks: { id: string; name: string; site_id: string }[];
  assets: { id: string; name: string; hostname?: string; serial?: string; kind: string; site_id: string }[];
  ips: IpHit[];
};

type SearchResponse = { results: SearchHit; parsed_ip: string | null };

// Popover wrapper styled to feel like a Cloudscape Container; positioned
// below the input. We hand-roll this because Cloudscape Autosuggest doesn't
// natively support grouped + multi-line option content.
const popoverStyle: React.CSSProperties = {
  position: 'absolute',
  left: 0,
  right: 0,
  top: '100%',
  marginTop: 8,
  zIndex: 30,
  maxHeight: '60vh',
  overflowY: 'auto',
  padding: 8,
  background: 'var(--color-background-container-content, #fff)',
  border: '1px solid var(--color-border-divider-default, #e9ebed)',
  borderRadius: 8,
  boxShadow: '0 2px 8px rgba(0,0,0,0.12)',
};

const rowBaseStyle: React.CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'flex-start',
  gap: 2,
  width: '100%',
  padding: '6px 8px',
  borderRadius: 6,
  textAlign: 'left',
  fontSize: 14,
  background: 'transparent',
  border: 'none',
  cursor: 'pointer',
};

const sectionLabelStyle: React.CSSProperties = {
  padding: '4px 8px',
  fontSize: 10,
  fontWeight: 600,
  textTransform: 'uppercase',
  letterSpacing: '0.05em',
  color: 'var(--color-text-status-inactive, #757575)',
};

export function GlobalSearch({ onSelect }: Readonly<{ onSelect: (href: string) => void }>) {
  const [q, setQ] = useState('');
  const [data, setData] = useState<SearchResponse | null>(null);
  const [open, setOpen] = useState(false);

  useEffect(() => {
    if (q.length < 2) { setData(null); return; }
    const t = setTimeout(async () => {
      try {
        const r = await http.get<SearchResponse>('/search', { params: { q } });
        setData(r.data);
      } catch {
        setData(null);
      }
    }, 200);
    return () => clearTimeout(t);
  }, [q]);

  function pick(href: string) {
    onSelect(href);
    setOpen(false);
    setQ('');
  }

  const results = data?.results;
  const parsedIp = data?.parsed_ip;
  const empty =
    !!results
    && !results.sites?.length
    && !results.racks?.length
    && !results.assets?.length
    && !results.ips?.length;

  return (
    <div style={{ position: 'relative' }}>
      <Input
        value={q}
        onChange={({ detail }) => { setQ(detail.value); setOpen(true); }}
        onFocus={() => setOpen(true)}
        onBlur={() => setTimeout(() => setOpen(false), 150)}
        placeholder="Search sites, racks, assets, hostnames, serials, IPs…"
        type="search"
      />
      {open && results && (
        <div style={popoverStyle}>
          {parsedIp && (
            <IpSection items={results.ips ?? []} onPick={pick} />
          )}
          {(['sites', 'racks', 'assets'] as const).map((kind) => {
            const items = results[kind] ?? [];
            if (items.length === 0) return null;
            return (
              <div key={kind} style={{ marginBottom: 8 }}>
                <div style={sectionLabelStyle}>{kind}</div>
                {items.slice(0, 8).map((it: any) => {
                  let href: string;
                  if (kind === 'sites') href = `/sites/${it.id}`;
                  else if (kind === 'racks') href = `/racks/${it.id}`;
                  else href = `/assets/${it.id}`;
                  return (
                    <button
                      key={it.id}
                      type="button"
                      onMouseDown={(e) => { e.preventDefault(); pick(href); }}
                      style={rowBaseStyle}
                      onMouseEnter={(e) => { e.currentTarget.style.background = 'var(--color-background-item-selected, #f2f8fd)'; }}
                      onMouseLeave={(e) => { e.currentTarget.style.background = 'transparent'; }}
                    >
                      <span style={{ fontWeight: 500 }}>{it.name}</span>
                      <span style={{ fontSize: 12, color: 'var(--color-text-status-inactive, #757575)' }}>
                        {it.code ?? it.hostname ?? it.serial ?? ''}{kind === 'assets' ? ` · ${it.kind}` : ''}
                      </span>
                    </button>
                  );
                })}
              </div>
            );
          })}
          {empty && (
            <Box padding="s" color="text-status-inactive">No matches.</Box>
          )}
        </div>
      )}
    </div>
  );
}

function IpSection({
  items, onPick,
}: Readonly<{
  items: IpHit[];
  onPick: (href: string) => void;
}>) {
  return (
    <div style={{ marginBottom: 8 }}>
      <div style={sectionLabelStyle}>ip address</div>
      {items.length === 0 ? (
        <Box padding="xs" color="text-status-inactive" fontSize="body-s">
          No IPAM entry for this address.
        </Box>
      ) : (
        items.map((ip) => {
          // The "go to" target is the asset detail page when we know the
          // asset (operators usually want the device). Otherwise, drop into
          // the IPAM view.
          const href = ip.asset_id ? `/assets/${ip.asset_id}` : '/ipam';
          return (
            <button
              key={ip.id}
              type="button"
              onMouseDown={(e) => { e.preventDefault(); onPick(href); }}
              style={rowBaseStyle}
              onMouseEnter={(e) => { e.currentTarget.style.background = 'var(--color-background-item-selected, #f2f8fd)'; }}
              onMouseLeave={(e) => { e.currentTarget.style.background = 'transparent'; }}
            >
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 8 }}>
                <Icon name="share" size="small" variant="subtle" />
                <span style={{ fontFamily: 'ui-monospace, monospace', fontWeight: 500 }}>{ip.address}</span>
                <Badge>{ip.role}</Badge>
                <Badge color={ip.source === 'dhcp' ? 'severity-medium' : 'grey'}>{ip.source}</Badge>
              </span>
              <span style={{ fontSize: 12, color: 'var(--color-text-status-inactive, #757575)' }}>
                {ip.subnet_prefix && <span style={{ fontFamily: 'ui-monospace, monospace' }}>{ip.subnet_prefix}</span>}
                {ip.fabric_name && <> · {ip.fabric_name}</>}
                {ip.vrf_name && <> · vrf {ip.vrf_name}</>}
                {ip.asset_name && <> · {ip.asset_name}</>}
                {ip.dns_name && <> · {ip.dns_name}</>}
              </span>
            </button>
          );
        })
      )}
    </div>
  );
}
