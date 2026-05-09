import { Refine, Authenticated } from '@refinedev/core';
import routerProvider, {
  CatchAllNavigate, NavigateToResource,
} from '@refinedev/react-router';
import { BrowserRouter, Outlet, Route, Routes } from 'react-router';
import { QueryClient } from '@tanstack/react-query';

import { dataProvider } from '@/lib/data-provider';
import { authProvider } from '@/lib/auth-provider';
import { accessControlProvider } from '@/lib/access-control-provider';

import { Shell } from '@/components/layout/shell';
import { Toaster } from '@/components/ui/sonner';
import { TooltipProvider } from '@/components/ui/tooltip';

import { LoginPage } from '@/pages/login';
import { DashboardPage } from '@/pages/dashboard';
import { SitesListPage } from '@/pages/sites';
import { SiteShowPage } from '@/pages/site-show';
import { RacksListPage } from '@/pages/racks-list';
import { RackShowPage } from '@/pages/rack-show';
import { RackCreatePage } from '@/pages/rack-create';
import { AssetShowPage } from '@/pages/asset-show';
import { AlertsPage } from '@/pages/alerts';
import { CollectorsPage } from '@/pages/collectors';
import { CapacityPage } from '@/pages/capacity';

const queryClient = new QueryClient({
  defaultOptions: { queries: { staleTime: 30_000, retry: 1 } },
});

export function App() {
  return (
    <BrowserRouter>
      <TooltipProvider>
        <Refine
          dataProvider={dataProvider}
          authProvider={authProvider}
          accessControlProvider={accessControlProvider}
          routerProvider={routerProvider}
          options={{
            syncWithLocation: true,
            warnWhenUnsavedChanges: true,
            disableTelemetry: true,
            reactQuery: { clientConfig: queryClient },
          }}
          resources={[
            { name: 'inventory/sites', list: '/sites', show: '/sites/:id', meta: { label: 'Sites' } },
            { name: 'inventory/racks', list: '/racks', show: '/racks/:id', create: '/racks/new', meta: { label: 'Racks' } },
            { name: 'inventory/assets', show: '/assets/:id' },
            { name: 'alerts', list: '/alerts' },
            { name: 'collectors', list: '/collectors' },
          ]}
        >
          <Routes>
            <Route element={
              <Authenticated key="auth-required" fallback={<CatchAllNavigate to="/login" />}>
                <Shell />
              </Authenticated>
            }>
              <Route index element={<DashboardPage />} />
              <Route path="/sites" element={<SitesListPage />} />
              <Route path="/sites/:id" element={<SiteShowPage />} />
              <Route path="/racks" element={<RacksListPage />} />
              <Route path="/racks/new" element={<RackCreatePage />} />
              <Route path="/racks/:id" element={<RackShowPage />} />
              <Route path="/assets/:id" element={<AssetShowPage />} />
              <Route path="/capacity" element={<CapacityPage />} />
              <Route path="/alerts" element={<AlertsPage />} />
              <Route path="/collectors" element={<CollectorsPage />} />
            </Route>
            <Route element={
              <Authenticated key="auth-fallback" fallback={<Outlet />}>
                <NavigateToResource resource="inventory/sites" />
              </Authenticated>
            }>
              <Route path="/login" element={<LoginPage />} />
            </Route>
          </Routes>
          <Toaster />
        </Refine>
      </TooltipProvider>
    </BrowserRouter>
  );
}
