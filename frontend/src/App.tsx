import { lazy, Suspense } from 'react';
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
import { Skeleton } from '@/components/ui/skeleton';

// Eagerly imported: small + on the critical path. Login is the unauth
// fallback; the dashboard is the index landing.
import { LoginPage } from '@/pages/login';
import { DashboardPage } from '@/pages/dashboard';

// Everything else is route-level split — vite emits a chunk per page so
// initial JS payload only carries the shell + dashboard.
const SitesListPage   = lazy(() => import('@/pages/sites').then((m) => ({ default: m.SitesListPage })));
const SiteShowPage    = lazy(() => import('@/pages/site-show').then((m) => ({ default: m.SiteShowPage })));
const RacksListPage   = lazy(() => import('@/pages/racks-list').then((m) => ({ default: m.RacksListPage })));
const RackShowPage    = lazy(() => import('@/pages/rack-show').then((m) => ({ default: m.RackShowPage })));
const RackCreatePage  = lazy(() => import('@/pages/rack-create').then((m) => ({ default: m.RackCreatePage })));
const AssetShowPage   = lazy(() => import('@/pages/asset-show').then((m) => ({ default: m.AssetShowPage })));
const AlertsPage      = lazy(() => import('@/pages/alerts').then((m) => ({ default: m.AlertsPage })));
const AlertRulesPage  = lazy(() => import('@/pages/alert-rules').then((m) => ({ default: m.AlertRulesPage })));
const MaintenancePage = lazy(() => import('@/pages/maintenance').then((m) => ({ default: m.MaintenancePage })));
const CollectorsPage  = lazy(() => import('@/pages/collectors').then((m) => ({ default: m.CollectorsPage })));
const CapacityPage    = lazy(() => import('@/pages/capacity').then((m) => ({ default: m.CapacityPage })));
const TokensPage      = lazy(() => import('@/pages/tokens').then((m) => ({ default: m.TokensPage })));
const AuditPage       = lazy(() => import('@/pages/audit').then((m) => ({ default: m.AuditPage })));
const ImportPage      = lazy(() => import('@/pages/import').then((m) => ({ default: m.ImportPage })));
const AdminPage       = lazy(() => import('@/pages/admin').then((m) => ({ default: m.AdminPage })));
const NotificationsPage = lazy(() => import('@/pages/notifications').then((m) => ({ default: m.NotificationsPage })));

const queryClient = new QueryClient({
  defaultOptions: { queries: { staleTime: 30_000, retry: 1 } },
});

function PageFallback() {
  return (
    <div className="space-y-4">
      <Skeleton className="h-8 w-72" />
      <Skeleton className="h-40 w-full" />
      <Skeleton className="h-60 w-full" />
    </div>
  );
}

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
              <Route element={<Suspense fallback={<PageFallback />}><Outlet /></Suspense>}>
                <Route path="/sites" element={<SitesListPage />} />
                <Route path="/sites/:id" element={<SiteShowPage />} />
                <Route path="/racks" element={<RacksListPage />} />
                <Route path="/racks/new" element={<RackCreatePage />} />
                <Route path="/racks/:id" element={<RackShowPage />} />
                <Route path="/assets/:id" element={<AssetShowPage />} />
                <Route path="/capacity" element={<CapacityPage />} />
                <Route path="/alerts" element={<AlertsPage />} />
                <Route path="/alerts/rules" element={<AlertRulesPage />} />
                <Route path="/maintenance" element={<MaintenancePage />} />
                <Route path="/collectors" element={<CollectorsPage />} />
                <Route path="/settings/tokens" element={<TokensPage />} />
                <Route path="/audit" element={<AuditPage />} />
                <Route path="/import" element={<ImportPage />} />
                <Route path="/admin" element={<AdminPage />} />
                <Route path="/settings/notifications" element={<NotificationsPage />} />
              </Route>
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
