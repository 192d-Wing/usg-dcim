import { useMemo } from 'react';
import { useNavigate } from 'react-router';
import { useQuery } from '@tanstack/react-query';

import Badge from '@cloudscape-design/components/badge';
import Box from '@cloudscape-design/components/box';
import Container from '@cloudscape-design/components/container';
import Header from '@cloudscape-design/components/header';
import Link from '@cloudscape-design/components/link';
import SpaceBetween from '@cloudscape-design/components/space-between';
import Table from '@cloudscape-design/components/table';
import {
  colorBackgroundCellShaded, colorBackgroundInputDisabled,
  colorBorderDividerDefault, colorTextStatusInactive,
  colorTextStatusSuccess,
} from '@cloudscape-design/design-tokens';

import { http } from '@/lib/http';

type Cable = {
  id: string;
  a_asset_id: string; a_port: string | null;
  b_asset_id: string; b_port: string | null;
  medium: string | null; color: string | null;
  length_m: number | null; label: string | null; face: string | null;
};
type RemoteAsset = { id: string; name: string; kind: string; rack_id: string | null };

const MONO = { fontFamily: 'ui-monospace, monospace' } as const;

function portStyle(used: boolean): React.CSSProperties {
  return {
    display: 'flex',
    alignItems: 'center',
    justifyContent: 'center',
    width: 28, height: 24, borderRadius: 2,
    fontSize: 10, fontFamily: 'ui-monospace, monospace',
    border: '1px solid',
    borderColor: used
      ? colorTextStatusSuccess
      : colorBorderDividerDefault,
    background: used
      ? colorBackgroundCellShaded
      : colorBackgroundInputDisabled,
    color: used
      ? colorTextStatusSuccess
      : colorTextStatusInactive,
  };
}

export function AssetCablesPanel({
  assetId, portCount,
}: Readonly<{
  assetId: string;
  portCount?: number | null;
}>) {
  const nav = useNavigate();
  const cablesRes = useQuery({
    queryKey: ['asset-cables', assetId],
    queryFn: async () => {
      const r = await http.get<{ items: Cable[] }>('/inventory/cables', {
        params: { asset_id: assetId, page_size: 500 },
      });
      return r.data.items ?? [];
    },
    enabled: !!assetId,
  });
  const cables = cablesRes.data ?? [];

  const remoteIds = useMemo(() => {
    const ids = new Set<string>();
    for (const c of cables) {
      if (c.a_asset_id !== assetId) ids.add(c.a_asset_id);
      if (c.b_asset_id !== assetId) ids.add(c.b_asset_id);
    }
    return Array.from(ids);
  }, [cables, assetId]);

  const remoteRes = useQuery({
    queryKey: ['asset-cables-remotes', remoteIds.sort().join(',')],
    queryFn: async () => {
      if (remoteIds.length === 0) return [] as RemoteAsset[];
      return Promise.all(
        remoteIds.map((id) => http.get<RemoteAsset>(`/inventory/assets/${id}`).then((r) => r.data)),
      );
    },
    enabled: remoteIds.length > 0,
  });
  const remoteById = useMemo(() => {
    const m = new Map<string, RemoteAsset>();
    for (const a of remoteRes.data ?? []) m.set(a.id, a);
    return m;
  }, [remoteRes.data]);

  // For port-bearing assets (patch panels), build a port → connected cable map
  // so the grid can show used vs free at a glance and tooltip the remote end.
  const portUse = useMemo(() => {
    const m = new Map<number, { remoteName: string; remotePort: string | null }>();
    if (!portCount || portCount <= 0) return m;
    for (const c of cables) {
      const localIsA = c.a_asset_id === assetId;
      const localPort = localIsA ? c.a_port : c.b_port;
      if (!localPort) continue;
      const n = Number(localPort);
      if (!Number.isInteger(n) || n < 1 || n > portCount) continue;
      const remoteId = localIsA ? c.b_asset_id : c.a_asset_id;
      const remote = remoteById.get(remoteId);
      m.set(n, {
        remoteName: remote?.name ?? remoteId.slice(0, 8),
        remotePort: localIsA ? c.b_port : c.a_port,
      });
    }
    return m;
  }, [cables, portCount, assetId, remoteById]);

  const ports = portCount ?? 0;

  return (
    <Container
      header={
        <Header
          variant="h2"
          counter={`(${cables.length})`}
          description={ports > 0 ? `${portUse.size} / ${ports} ports in use` : undefined}
        >
          Cables
        </Header>
      }
    >
      <SpaceBetween size="m">
        {ports > 0 && (
          <div style={{ display: 'flex', flexWrap: 'wrap', gap: 4 }}>
            {Array.from({ length: ports }, (_, i) => i + 1).map((n) => {
              const use = portUse.get(n);
              const remotePortSuffix = use?.remotePort ? ` port ${use.remotePort}` : '';
              const title = use ? `→ ${use.remoteName}${remotePortSuffix}` : 'free';
              return (
                <div key={n} style={portStyle(!!use)} title={title}>{n}</div>
              );
            })}
          </div>
        )}
        <Table<Cable>
          variant="embedded"
          loading={cablesRes.isLoading}
          loadingText="Loading cables…"
          items={cables}
          trackBy="id"
          columnDefinitions={[
            {
              id: 'localPort', header: 'Local port',
              cell: (c) => {
                const localIsA = c.a_asset_id === assetId;
                const localPort = localIsA ? c.a_port : c.b_port;
                return <span style={MONO}>{localPort ?? '—'}</span>;
              },
              width: 110,
            },
            {
              id: 'remote', header: 'Connected to',
              cell: (c) => {
                const localIsA = c.a_asset_id === assetId;
                const remoteId = localIsA ? c.b_asset_id : c.a_asset_id;
                const remote = remoteById.get(remoteId);
                if (!remote) return remoteId.slice(0, 8);
                return (
                  <Link
                    href={`/assets/${remote.id}`}
                    onFollow={(e) => { e.preventDefault(); nav(`/assets/${remote.id}`); }}
                  >
                    {remote.name} · {remote.kind}
                  </Link>
                );
              },
            },
            {
              id: 'remotePort', header: 'Remote port',
              cell: (c) => {
                const localIsA = c.a_asset_id === assetId;
                const remotePort = localIsA ? c.b_port : c.a_port;
                return <span style={MONO}>{remotePort ?? '—'}</span>;
              },
              width: 110,
            },
            {
              id: 'face', header: 'Face',
              cell: (c) => c.face
                ? <Badge>{c.face}</Badge>
                : <Box color="text-status-inactive">—</Box>,
              width: 90,
            },
            {
              id: 'medium', header: 'Medium',
              cell: (c) => c.medium ? <Badge>{c.medium}</Badge> : '—',
              width: 100,
            },
            {
              id: 'color', header: 'Color',
              cell: (c) => c.color ? (
                <SpaceBetween size="xxs" direction="horizontal">
                  <span style={{
                    display: 'inline-block', width: 12, height: 12, borderRadius: 2,
                    background: c.color, border: `1px solid ${colorBorderDividerDefault}`,
                  }} />
                  <Box fontSize="body-s">{c.color}</Box>
                </SpaceBetween>
              ) : '—',
              width: 100,
            },
            { id: 'len', header: 'Len (m)', cell: (c) => c.length_m ?? '—', width: 90 },
            {
              id: 'label', header: 'Label',
              cell: (c) => c.label
                ? <span>{c.label}</span>
                : <Box color="text-status-inactive">—</Box>,
            },
          ]}
          empty={
            <Box textAlign="center" color="inherit" padding="m">
              No cables connect to this asset.
            </Box>
          }
        />
      </SpaceBetween>
    </Container>
  );
}
