import { Link, NavLink, Outlet, useLocation, useNavigate } from 'react-router';
import { useGetIdentity, useLogout } from '@refinedev/core';
import {
  LayoutDashboard, Building2, Server, Bell, Cpu, Gauge, LogOut, ChevronRight, Wrench, KeyRound, ScrollText, Upload,
} from 'lucide-react';
import { cn } from '@/lib/utils';
import { Button } from '@/components/ui/button';
import { Separator } from '@/components/ui/separator';
import { GlobalSearch } from '@/components/global-search';
import {
  DropdownMenu, DropdownMenuTrigger, DropdownMenuContent,
  DropdownMenuItem, DropdownMenuLabel, DropdownMenuSeparator,
} from '@/components/ui/dropdown-menu';

type NavItem = { to: string; label: string; icon: React.ComponentType<{ className?: string }>; cap?: string };

const NAV: NavItem[] = [
  { to: '/',           label: 'Enterprise', icon: LayoutDashboard },
  { to: '/sites',      label: 'Sites',      icon: Building2 },
  { to: '/racks',      label: 'Racks',      icon: Server },
  { to: '/capacity',   label: 'Capacity',   icon: Gauge },
  { to: '/alerts',     label: 'Alerts',     icon: Bell },
  { to: '/maintenance', label: 'Maintenance', icon: Wrench },
  { to: '/collectors', label: 'Collectors', icon: Cpu },
  { to: '/import',     label: 'Import',     icon: Upload, cap: 'inventory:bulk' },
  { to: '/audit',      label: 'Audit log',  icon: ScrollText, cap: 'audit:read' },
];

export function Shell() {
  const { data: identity } = useGetIdentity<{ email: string | null; capabilities: string[] }>();
  const { mutate: logout } = useLogout();
  const location = useLocation();
  const navigate = useNavigate();

  return (
    <div className="grid min-h-screen grid-cols-[240px_1fr] bg-background">
      <aside className="flex flex-col border-r bg-card">
        <div className="px-4 py-4">
          <Link to="/" className="text-base font-semibold tracking-tight">USG DCIM</Link>
          <p className="text-xs text-muted-foreground">Enterprise data-center ops</p>
        </div>
        <Separator />
        <nav className="flex flex-1 flex-col gap-1 p-2">
          {NAV.filter((item) => !item.cap || (identity?.capabilities ?? []).includes(item.cap)).map((item) => (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.to === '/'}
              className={({ isActive }) =>
                cn(
                  'flex items-center gap-2 rounded-md px-3 py-2 text-sm transition-colors',
                  isActive
                    ? 'bg-accent text-accent-foreground'
                    : 'text-muted-foreground hover:bg-accent hover:text-foreground',
                )
              }
            >
              <item.icon className="h-4 w-4" />
              <span className="flex-1">{item.label}</span>
              <ChevronRight className="h-3.5 w-3.5 opacity-0 group-[.active]:opacity-100" />
            </NavLink>
          ))}
        </nav>
        <Separator />
        <div className="p-3">
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="ghost" className="w-full justify-start font-normal">
                <span className="flex h-7 w-7 items-center justify-center rounded-full bg-primary text-primary-foreground text-xs font-semibold">
                  {(identity?.email ?? '?').slice(0, 1).toUpperCase()}
                </span>
                <span className="truncate text-xs text-muted-foreground">{identity?.email ?? 'unauthenticated'}</span>
              </Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="start">
              <DropdownMenuLabel>{identity?.email}</DropdownMenuLabel>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={() => navigate('/settings/tokens')}>
                <KeyRound className="h-4 w-4" /> API tokens
              </DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuItem onClick={() => logout()}>
                <LogOut className="h-4 w-4" /> Sign out
              </DropdownMenuItem>
            </DropdownMenuContent>
          </DropdownMenu>
        </div>
      </aside>

      <main className="flex min-h-screen flex-col">
        <header className="sticky top-0 z-20 flex h-14 items-center gap-3 border-b bg-card/80 px-6 backdrop-blur supports-[backdrop-filter]:bg-card/60">
          <Crumb path={location.pathname} />
          <div className="ml-auto w-full max-w-md">
            <GlobalSearch onSelect={(href) => navigate(href)} />
          </div>
        </header>
        <div className="flex-1 p-6">
          <Outlet />
        </div>
      </main>
    </div>
  );
}

function Crumb({ path }: { path: string }) {
  const seg = path.split('/').filter(Boolean);
  if (seg.length === 0) return <span className="text-sm text-muted-foreground">Enterprise</span>;
  return (
    <div className="flex items-center gap-1.5 text-sm text-muted-foreground">
      <Link to="/" className="hover:text-foreground">Home</Link>
      {seg.map((s, i) => {
        const href = '/' + seg.slice(0, i + 1).join('/');
        const isLast = i === seg.length - 1;
        const isUuid = /^[0-9a-f-]{8,}$/i.test(s);
        const label = isUuid ? `${s.slice(0, 8)}…` : s;
        return (
          <span key={href} className="flex items-center gap-1.5">
            <ChevronRight className="h-3.5 w-3.5" />
            {isLast ? (
              <span className="text-foreground">{label}</span>
            ) : (
              <Link to={href} className="hover:text-foreground">{label}</Link>
            )}
          </span>
        );
      })}
    </div>
  );
}
