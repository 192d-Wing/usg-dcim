// AppLayout-driven shell. Replaces the hand-rolled sidebar + topbar.
//
// Layout slots:
//   - TopNavigation       : product name + global search + user menu
//   - SideNavigation      : same 11 items as before, capability-gated
//   - BreadcrumbGroup     : derived from the current path
//   - HelpPanel (tools)   : empty for now; per-page content lands here later
//   - content             : <Outlet /> from react-router
//
// Cloudscape brings its own focus management, keyboard nav, and ARIA
// semantics for these primitives — we don't reproduce them.

import { useMemo, useState } from 'react';
import { Outlet, useLocation, useNavigate } from 'react-router';
import { useGetIdentity, useLogout } from '@refinedev/core';
import AppLayout from '@cloudscape-design/components/app-layout';
import TopNavigation, { TopNavigationProps } from '@cloudscape-design/components/top-navigation';
import SideNavigation, {
  SideNavigationProps,
} from '@cloudscape-design/components/side-navigation';
import BreadcrumbGroup from '@cloudscape-design/components/breadcrumb-group';
import HelpPanel from '@cloudscape-design/components/help-panel';
import Box from '@cloudscape-design/components/box';
import { GlobalSearch } from '@/components/global-search';
import { FabricScopeProvider, useFabricScope } from '@/contexts/fabric-scope';

type NavItem = {
  href: string;
  text: string;
  /** Required capability (string for one, array for any-of). */
  cap?: string | string[];
};

const NAV_ITEMS: NavItem[] = [
  { href: '/',           text: 'Enterprise' },
  { href: '/sites',      text: 'Sites' },
  { href: '/racks',      text: 'Racks' },
  { href: '/capacity',   text: 'Capacity' },
  { href: '/alerts',     text: 'Alerts' },
  { href: '/maintenance', text: 'Maintenance' },
  { href: '/collectors', text: 'Collectors' },
  { href: '/ipam',       text: 'IPAM',       cap: 'inventory:read' },
  { href: '/import',     text: 'Import',     cap: 'inventory:bulk' },
  { href: '/audit',      text: 'Audit log',  cap: 'audit:read' },
  { href: '/admin',      text: 'Admin',      cap: ['users:manage', 'roles:manage'] },
];

function hasCap(caps: string[], cap: string | string[] | undefined): boolean {
  if (!cap) return true;
  if (Array.isArray(cap)) return cap.some((c) => caps.includes(c));
  return caps.includes(cap);
}

/** Convert a path like `/racks/abc-123` into Cloudscape breadcrumb items.
 *  Last segment is the leaf; UUID-shaped segments are abbreviated. */
function breadcrumbsFor(path: string): { text: string; href: string }[] {
  const segments = path.split('/').filter(Boolean);
  const items: { text: string; href: string }[] = [{ text: 'Home', href: '/' }];
  segments.forEach((seg, i) => {
    const href = '/' + segments.slice(0, i + 1).join('/');
    const isUuid = /^[0-9a-f-]{8,}$/i.test(seg);
    items.push({ text: isUuid ? `${seg.slice(0, 8)}…` : seg, href });
  });
  return items;
}

export function CloudscapeShell() {
  return (
    <FabricScopeProvider>
      <ShellBody />
    </FabricScopeProvider>
  );
}

function ShellBody() {
  const { data: identity } = useGetIdentity<{ email: string | null; capabilities: string[] }>();
  const { mutate: logout } = useLogout();
  const location = useLocation();
  const navigate = useNavigate();
  const [navOpen, setNavOpen] = useState(true);
  const [toolsOpen, setToolsOpen] = useState(false);
  const { fabricId, fabrics, setFabricId } = useFabricScope();

  const caps = identity?.capabilities ?? [];

  const sideNavItems = useMemo<SideNavigationProps.Item[]>(() => (
    NAV_ITEMS
      .filter((it) => hasCap(caps, it.cap))
      .map((it) => ({ type: 'link' as const, text: it.text, href: it.href }))
  ), [caps]);

  // "Services" menu — AWS console pattern, jumps to any top-level
  // area. Mirrors the SideNavigation but provides a one-click shortcut
  // from anywhere on the page without opening the side panel.
  const servicesUtility = useMemo<TopNavigationProps.MenuDropdownUtility>(() => ({
    type: 'menu-dropdown',
    text: 'Services',
    iconName: 'menu',
    ariaLabel: 'Services',
    items: NAV_ITEMS
      .filter((it) => hasCap(caps, it.cap))
      .map((it) => ({ id: it.href, text: it.text })),
    onItemClick: ({ detail }) => navigate(detail.id),
  }), [caps, navigate]);

  // "Region" selector — analogous to AWS's region picker. Persists in
  // localStorage so the user lands on the same fabric next session.
  const activeFabric = fabrics.find((f) => f.id === fabricId);
  const regionUtility = useMemo<TopNavigationProps.MenuDropdownUtility>(() => ({
    type: 'menu-dropdown',
    text: activeFabric?.name ?? (fabrics.length === 0 ? 'No fabrics' : 'Pick a fabric'),
    description: 'Fabric',
    iconName: 'globe',
    ariaLabel: 'Fabric scope',
    items: fabrics.length === 0
      ? [{ id: '__empty', text: 'No fabrics defined', disabled: true }]
      : fabrics.map((f) => ({ id: f.id, text: f.name })),
    onItemClick: ({ detail }) => {
      if (detail.id !== '__empty') setFabricId(detail.id);
    },
  }), [activeFabric, fabrics, setFabricId]);

  const crumbs = useMemo(() => breadcrumbsFor(location.pathname), [location.pathname]);

  return (
    <>
      {/* TopNavigation lives outside AppLayout so it spans the full width
          and floats above the side panels — the canonical AWS console
          layout. */}
      <TopNavigation
        identity={{
          href: '/',
          title: 'USG DCIM',
          onFollow: (e) => { e.preventDefault(); navigate('/'); },
        }}
        // Cloudscape doesn't ship a search input, so we slot our existing
        // GlobalSearch into the search slot. The component renders fine
        // outside its prior shadcn context as long as it gets onSelect.
        search={
          <Box padding={{ vertical: 'xxs' }}>
            <GlobalSearch onSelect={(href) => navigate(href)} />
          </Box>
        }
        utilities={[
          servicesUtility,
          regionUtility,
          {
            type: 'menu-dropdown',
            text: identity?.email ?? 'unauthenticated',
            description: 'Account',
            iconName: 'user-profile',
            items: [
              { id: 'tokens', text: 'API tokens' },
              { id: 'signout', text: 'Sign out' },
            ],
            onItemClick: ({ detail }) => {
              if (detail.id === 'tokens') navigate('/settings/tokens');
              if (detail.id === 'signout') logout();
            },
          },
        ]}
      />

      <AppLayout
        headerSelector="#topnav"
        navigationOpen={navOpen}
        onNavigationChange={({ detail }) => setNavOpen(detail.open)}
        toolsOpen={toolsOpen}
        onToolsChange={({ detail }) => setToolsOpen(detail.open)}
        navigation={
          <SideNavigation
            header={{ href: '/', text: 'Enterprise data-center ops' }}
            activeHref={location.pathname}
            onFollow={(e) => {
              if (!e.detail.external) {
                e.preventDefault();
                navigate(e.detail.href);
              }
            }}
            items={sideNavItems}
          />
        }
        breadcrumbs={
          <BreadcrumbGroup
            items={crumbs}
            onFollow={(e) => {
              e.preventDefault();
              navigate(e.detail.href);
            }}
          />
        }
        // HelpPanel is empty for now — per-page content will land here in
        // follow-ups via a context (route component sets the panel body
        // for its own context-sensitive help).
        tools={<HelpPanel header={<h2>Help</h2>}><p>Per-page help will appear here.</p></HelpPanel>}
        content={<Outlet />}
      />
      {/* Anchor for TopNavigation's headerSelector. Empty span — the actual
          top nav is the Cloudscape component above; this just gives
          AppLayout a stable selector to compute viewport offsets from. */}
      <span id="topnav" style={{ display: 'none' }} />
    </>
  );
}

// Aliased so App.tsx can swap from the legacy `Shell` import without
// touching the rest of the file.
export { CloudscapeShell as Shell };
