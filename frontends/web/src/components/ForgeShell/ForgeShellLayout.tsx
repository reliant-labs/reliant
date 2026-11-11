// Copyright (c) 2025 Reliant Labs

/**
 * The forge shell's chrome for the pages that are not one app's console:
 * Apps, Sites, Usage & spend and Settings. The same forge-ui sidebar the
 * console uses (ConsoleShell), so moving between the org's pages and an app's
 * console reads as one product.
 *
 * It carries the brand ("Forge by Reliant"), the org switcher, sign-out and
 * the way to Reliant ("Open in Reliant", a plain link to the Reliant app: a
 * different origin, with its own session).
 */

import { useState, type ReactNode } from "react";
import { Outlet, useNavigate, useRouterState } from "@tanstack/react-router";
import { ArrowUpRight, Gauge, Globe, LayoutGrid, LogOut, Settings } from "lucide-react";

import SidebarLayout, { type SidebarNavItem } from "@/components/forge-ui/sidebar_layout";
import { OrgSwitcher } from "@/components/Orgs/OrgSwitcher";
import { reliantAppOrigin } from "@/lib/shell";
import { useAuthStore } from "@/store/authStore";

import { ForgeBrand, ForgeMark } from "./ForgeBrand";

/** The shell's own pages, in nav order. */
export const FORGE_SHELL_NAV = [
  { label: "Apps", href: "/apps", icon: LayoutGrid },
  { label: "Sites", href: "/sites", icon: Globe },
  { label: "Usage & spend", href: "/usage", icon: Gauge },
  { label: "Settings", href: "/settings", icon: Settings },
] as const;

/** The nav entry a path belongs to. */
export function activeNavHref(pathname: string): string | null {
  const hit = FORGE_SHELL_NAV.find((item) => pathname === item.href || pathname.startsWith(`${item.href}/`));
  return hit?.href ?? null;
}

const RAIL_KEY = "forge.shell.rail";

export function ForgeShellLayout({ children }: { children?: ReactNode }) {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const navigate = useNavigate();
  const authUser = useAuthStore((state) => state.user);
  const signOut = useAuthStore((state) => state.signOut);
  const [collapsed, setCollapsed] = useState(() => {
    try {
      return window.localStorage.getItem(RAIL_KEY) === "1";
    } catch {
      return false;
    }
  });

  const active = activeNavHref(pathname);
  const navItems: SidebarNavItem[] = FORGE_SHELL_NAV.map(({ label, href, icon: Icon }) => ({
    label,
    href,
    active: active === href,
    icon: <Icon className="h-4 w-4" aria-hidden="true" />,
  }));
  const footerItems: SidebarNavItem[] = [
    {
      label: "Open in Reliant",
      href: reliantAppOrigin(),
      icon: <ArrowUpRight className="h-4 w-4" aria-hidden="true" />,
    },
  ];

  const user = authUser
    ? {
        name:
          (authUser.user_metadata?.full_name as string | undefined) ??
          (authUser.user_metadata?.name as string | undefined) ??
          authUser.email?.split("@")[0] ??
          (authUser.is_anonymous ? "Guest" : "You"),
        email: authUser.email ?? undefined,
      }
    : undefined;

  const onSignOut = async () => {
    try {
      await signOut();
    } finally {
      void navigate({ href: "/auth", replace: true });
    }
  };

  return (
    <div className="forge-ui forge-console h-dvh w-full" data-testid="forge-shell-layout">
      <SidebarLayout
        navLabel="Forge"
        brand={
          <a href="/apps" className="no-color flex min-w-0 items-center rounded-md px-1 py-0.5" aria-label="Forge by Reliant: all apps">
            <ForgeBrand />
          </a>
        }
        brandCollapsed={<ForgeMark className="h-6 w-6" />}
        navItems={navItems}
        footerItems={footerItems}
        user={user}
        collapsed={collapsed}
        onToggle={() =>
          setCollapsed((prev) => {
            try {
              window.localStorage.setItem(RAIL_KEY, prev ? "0" : "1");
            } catch {
              // Not persisted; the rail still toggles for this visit.
            }
            return !prev;
          })
        }
        headerContent={
          <div className="flex min-w-0 flex-1 items-center justify-end gap-2">
            <OrgSwitcher />
            <button
              type="button"
              onClick={() => void onSignOut()}
              className="inline-flex h-8 items-center gap-1.5 rounded-md px-2 text-sm text-ink-muted transition-colors hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              data-testid="forge-shell-sign-out"
            >
              <LogOut className="h-3.5 w-3.5" aria-hidden="true" />
              Sign out
            </button>
          </div>
        }
      >
        {children ?? <Outlet />}
      </SidebarLayout>
    </div>
  );
}
