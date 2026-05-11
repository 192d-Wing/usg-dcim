// Global "region" for the app — analogous to AWS's region selector in
// the TopNavigation. Consumers read the currently-selected fabric id
// instead of carrying their own fabric picker.
//
// The selection is persisted in localStorage so it survives reloads,
// and the provider hydrates it lazily once the fabrics list resolves
// (we can't restore a stale id pointing at a deleted fabric, so we
// prefer the first available fabric in that case).

import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { useList } from '@refinedev/core';

type Fabric = { id: string; name: string };

type FabricScopeValue = {
  fabricId: string | null;
  fabrics: Fabric[];
  isLoading: boolean;
  setFabricId: (id: string) => void;
};

const FabricScopeContext = createContext<FabricScopeValue | null>(null);
const STORAGE_KEY = 'dcim.fabric-scope';

export function FabricScopeProvider({ children }: { children: ReactNode }) {
  const fabricsRes = useList<Fabric>({
    resource: 'ipam/fabrics',
    pagination: { pageSize: 500 },
  });
  const fabrics = fabricsRes.result.data ?? [];

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
