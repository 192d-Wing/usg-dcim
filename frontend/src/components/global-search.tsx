import { useEffect, useState } from 'react';
import { Search, Network } from 'lucide-react';
import { Input } from '@/components/ui/input';
import { Card } from '@/components/ui/card';
import { Badge } from '@/components/ui/badge';
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

export function GlobalSearch({ onSelect }: { onSelect: (href: string) => void }) {
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
    <div className="relative">
      <div className="relative">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={q}
          onChange={(e) => { setQ(e.target.value); setOpen(true); }}
          onFocus={() => setOpen(true)}
          onBlur={() => setTimeout(() => setOpen(false), 150)}
          placeholder="Search sites, racks, assets, hostnames, serials, IPs…"
          className="pl-8"
        />
      </div>
      {open && results && (
        <Card className="absolute left-0 right-0 top-full z-30 mt-2 max-h-[60vh] overflow-y-auto p-2 shadow-lg">
          {parsedIp && (
            <IpSection items={results.ips ?? []} onPick={pick} />
          )}
          {(['sites', 'racks', 'assets'] as const).map((kind) => {
            const items = results[kind] ?? [];
            if (items.length === 0) return null;
            return (
              <div key={kind} className="mb-2">
                <div className="px-2 py-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">{kind}</div>
                {items.slice(0, 8).map((it: any) => {
                  const href =
                    kind === 'sites' ? `/sites/${it.id}`
                    : kind === 'racks' ? `/racks/${it.id}`
                    : `/assets/${it.id}`;
                  return (
                    <button
                      key={it.id}
                      type="button"
                      onMouseDown={(e) => { e.preventDefault(); pick(href); }}
                      className="flex w-full flex-col items-start gap-0 rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent"
                    >
                      <span className="font-medium">{it.name}</span>
                      <span className="text-xs text-muted-foreground">
                        {it.code ?? it.hostname ?? it.serial ?? ''}{kind === 'assets' ? ` · ${it.kind}` : ''}
                      </span>
                    </button>
                  );
                })}
              </div>
            );
          })}
          {empty && (
            <p className="px-2 py-3 text-sm text-muted-foreground">No matches.</p>
          )}
        </Card>
      )}
    </div>
  );
}

function IpSection({
  items, onPick,
}: {
  items: IpHit[];
  onPick: (href: string) => void;
}) {
  return (
    <div className="mb-2">
      <div className="px-2 py-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
        ip address
      </div>
      {items.length === 0 ? (
        <p className="px-2 py-1.5 text-xs text-muted-foreground">No IPAM entry for this address.</p>
      ) : (
        items.map((ip) => {
          // The "go to" target is the asset detail page when we know the
          // asset (operators usually want the device). Otherwise, drop into
          // the IPAM view at the parent subnet (handled via /ipam querystring
          // — for now we link to /ipam and let the user re-navigate).
          const href = ip.asset_id ? `/assets/${ip.asset_id}` : '/ipam';
          return (
            <button
              key={ip.id}
              type="button"
              onMouseDown={(e) => { e.preventDefault(); onPick(href); }}
              className="flex w-full flex-col items-start gap-0.5 rounded-md px-2 py-1.5 text-left text-sm hover:bg-accent"
            >
              <span className="flex items-center gap-2">
                <Network className="h-3.5 w-3.5 text-muted-foreground" />
                <span className="font-mono font-medium">{ip.address}</span>
                <Badge variant="secondary" className="font-mono text-[10px]">{ip.role}</Badge>
                <Badge
                  variant={ip.source === 'dhcp' ? 'warning' : 'outline'}
                  className="text-[10px]"
                >
                  {ip.source}
                </Badge>
              </span>
              <span className="text-xs text-muted-foreground">
                {ip.subnet_prefix && <span className="font-mono">{ip.subnet_prefix}</span>}
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
