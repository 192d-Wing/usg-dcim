// Global "region" for the app — analogous to AWS's region selector in
// the TopNavigation. Consumers read the currently-selected fabric id
// instead of carrying their own fabric picker.
//
// The selection is persisted in localStorage so it survives reloads,
// and the provider hydrates it lazily once the fabrics list resolves
// (we can't restore a stale id pointing at a deleted fabric, so we
// prefer the first available fabric in that case).

//
// Embedded in the console the source is the console scope instead: the
// `site` value maps to a fabric by slug, the picker is hidden, nothing is
// persisted, and an unknown (or inaccessible) site resolves to no fabric
// so pages show their empty state.

import {
  createContext, useContext, useEffect, useMemo, useState, useSyncExternalStore, type ReactNode,
} from 'react';
import { useGetIdentity, useList } from '@refinedev/core';
import { getConsoleScope, isEmbedded, subscribeEmbedded } from '@/lib/embedded';

type Fabric = { id: string; name: string; slug?: string };

/** Console `site` -> fabric id by exact slug match; null when unset/unknown. */
export function fabricIdForSite(fabrics: Fabric[], site: string | null | undefined): string | null {
  if (!site) return null;
  return fabrics.find((f) => f.slug === site)?.id ?? null;
}

/** Embedded only: the console site that matches no accessible fabric, else null.
 *  null while fabrics are loading or when no site is set. In that case
 *  fabricId is null and embedded pages show an empty state (never "all
 *  fabrics"). */
export function unmatchedSiteFor(
  fabrics: Fabric[], site: string | null | undefined, loading: boolean,
): string | null {
  if (loading || !site) return null;
  return fabricIdForSite(fabrics, site) ? null : site;
}

type FabricScopeValue = {
  fabricId: string | null;
  /** Embedded: console site with no matching fabric (see unmatchedSiteFor). */
  unmatchedSite: string | null;
  fabrics: Fabric[];
  isLoading: boolean;
  setFabricId: (id: string) => void;
};

const FabricScopeContext = createContext<FabricScopeValue | null>(null);
const STORAGE_KEY = 'dcim.fabric-scope';

export function FabricScopeProvider({ children }: { children: ReactNode }) {
  return isEmbedded()
    ? <EmbeddedFabricScopeProvider>{children}</EmbeddedFabricScopeProvider>
    : <StandaloneFabricScopeProvider>{children}</StandaloneFabricScopeProvider>;
}

function EmbeddedFabricScopeProvider({ children }: { children: ReactNode }) {
  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const authed = !!identity;
  const fabricsRes = useList<Fabric>({
    resource: 'ipam/fabrics',
    pagination: { pageSize: 500 },
    queryOptions: { enabled: authed },
  });
  const fabrics = authed ? (fabricsRes.result.data ?? []) : [];
  const scope = useSyncExternalStore(subscribeEmbedded, getConsoleScope, () => null);
  const fabricId = fabricIdForSite(fabrics, scope?.site);
  const loading = !authed || fabricsRes.query.isLoading;
  const unmatchedSite = unmatchedSiteFor(fabrics, scope?.site, loading);

  const value = useMemo<FabricScopeValue>(() => ({
    fabricId,
    unmatchedSite,
    fabrics,
    isLoading: fabricsRes.query.isLoading,
    // Scope changes come only from the console.
    setFabricId: () => {},
  }), [fabricId, unmatchedSite, fabrics, fabricsRes.query.isLoading]);

  return (
    <FabricScopeContext.Provider value={value}>
      {children}
    </FabricScopeContext.Provider>
  );
}

function StandaloneFabricScopeProvider({ children }: { children: ReactNode }) {
  // Hold the fabrics fetch until refine has resolved an identity —
  // otherwise the request fires before auth hydrates and refine's
  // authProvider bounces the user back to /login on a 401.
  const { data: identity } = useGetIdentity<{ capabilities: string[] }>();
  const authed = !!identity;
  const fabricsRes = useList<Fabric>({
    resource: 'ipam/fabrics',
    pagination: { pageSize: 500 },
    queryOptions: { enabled: authed },
  });
  const fabrics = authed ? (fabricsRes.result.data ?? []) : [];

  const [fabricId, setFabricIdState] = useState<string | null>(() => {
    try { return localStorage.getItem(STORAGE_KEY); } catch { return null; }
  });

  // Once fabrics load, validate the stored id; fall back to first.
  useEffect(() => {
    if (fabrics.length === 0) return;
    if (fabricId && fabrics.some((f) => f.id === fabricId)) return;
    setFabricIdState(fabrics[0].id);
  }, [fabrics, fabricId]);

  const setFabricId = (id: string) => {
    setFabricIdState(id);
    try { localStorage.setItem(STORAGE_KEY, id); } catch { /* ignore */ }
  };

  const value = useMemo<FabricScopeValue>(() => ({
    fabricId,
    unmatchedSite: null,
    fabrics,
    isLoading: fabricsRes.query.isLoading,
    setFabricId,
  }), [fabricId, fabrics, fabricsRes.query.isLoading]);

  return (
    <FabricScopeContext.Provider value={value}>
      {children}
    </FabricScopeContext.Provider>
  );
}

export function useFabricScope(): FabricScopeValue {
  const v = useContext(FabricScopeContext);
  if (!v) {
    throw new Error('useFabricScope must be used inside FabricScopeProvider');
  }
  return v;
}
