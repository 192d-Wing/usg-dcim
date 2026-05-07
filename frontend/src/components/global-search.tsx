import { useEffect, useState } from 'react';
import { Search } from 'lucide-react';
import { Input } from '@/components/ui/input';
import { Card } from '@/components/ui/card';
import { http } from '@/lib/http';

type SearchHit = {
  sites: { id: string; name: string; code: string }[];
  racks: { id: string; name: string; site_id: string }[];
  assets: { id: string; name: string; hostname?: string; serial?: string; kind: string; site_id: string }[];
};

export function GlobalSearch({ onSelect }: { onSelect: (href: string) => void }) {
  const [q, setQ] = useState('');
  const [data, setData] = useState<SearchHit | null>(null);
  const [open, setOpen] = useState(false);

  useEffect(() => {
    if (q.length < 2) { setData(null); return; }
    const t = setTimeout(async () => {
      try {
        const r = await http.get('/search', { params: { q } });
        setData(r.data.results);
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

  return (
    <div className="relative">
      <div className="relative">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
        <Input
          value={q}
          onChange={(e) => { setQ(e.target.value); setOpen(true); }}
          onFocus={() => setOpen(true)}
          onBlur={() => setTimeout(() => setOpen(false), 150)}
          placeholder="Search sites, racks, assets, hostnames, serials…"
          className="pl-8"
        />
      </div>
      {open && data && (
        <Card className="absolute left-0 right-0 top-full z-30 mt-2 max-h-[60vh] overflow-y-auto p-2 shadow-lg">
          {(['sites', 'racks', 'assets'] as const).map((kind) => {
            const items = data[kind] ?? [];
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
          {!data.sites?.length && !data.racks?.length && !data.assets?.length && (
            <p className="px-2 py-3 text-sm text-muted-foreground">No matches.</p>
          )}
        </Card>
      )}
    </div>
  );
}
