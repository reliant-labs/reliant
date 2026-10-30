// Copyright (c) 2025 Reliant Labs

/**
 * The layout route for the org pages — org-wide Observability
 * (/forge/observability[/:view]). The org's list of apps is /apps
 * (components/Apps), outside this console; the nav links to it.
 *
 * It is the org counterpart of Console/ConsoleLayout, and owns the same kind of
 * thing: the CHROME (OrgShell — the nav, the top bar with the breadcrumb, the
 * rail whose collapsed state is remembered) and what only the chrome needs
 * (the firing-alerts badge, the apps nested under Apps).
 *
 * It owns no scope. App and environment are filters the Observability panel
 * draws itself, so there is nothing to resolve or remember.
 *
 * A URL that names ONE app (`?project=`, `?forgeProject=`) is not an org page:
 * the route's component resolves it and redirects into that app's console. It
 * renders bare there, so the redirect does not flash the org chrome.
 *
 * Escape and the way out belong to the parent `_forge` layout (ForgeLayout).
 */

import { useCallback, useMemo, useState } from "react";
import { Outlet, useLocation, useNavigate, useSearch } from "@tanstack/react-router";
import { LayoutGrid } from "lucide-react";

import { useApps } from "@/hooks/apps-queries";
import { useAlerts, useObservabilityOrg } from "@/hooks/observability-queries";
import { useTitleBarChrome } from "@/hooks/useTitleBarChrome";
import { getParentRouteNavigateOptions } from "@/lib/routeParent";
import { useAuthStore } from "@/store/authStore";

import { ObsQuotaBanner } from "../Observability/ObsStates";
import { ForgeCloseButton } from "../ForgeHeader";
import { orgPageOf, type OrgPage } from "../Console/paths";
import { OrgSwitcher } from "@/components/Orgs/OrgSwitcher";
import { OrgShell } from "./OrgShell";

// One preference for both consoles: collapsing the rail in an app keeps it collapsed up here.
const RAIL_KEY = "forge.console.rail";

const OVERVIEW: OrgPage = { kind: "observability" };

export function OrgConsoleLayout() {
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  if (search.project || search.forgeProject) return <Outlet />;
  return <OrgChrome search={search} />;
}

function OrgChrome({ search }: { search: Record<string, unknown> }) {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const page = useMemo(() => orgPageOf(pathname) ?? OVERVIEW, [pathname]);

  // The Apps list's apps (hooks/apps-queries.ts), nested under Apps in the nav.
  const apps = useApps().apps.map((app) => app.name);

  // Firing alerts across every app: the same org-wide read the Alerts page shares.
  const org = useObservabilityOrg();
  const alerts = useAlerts(org.orgId);
  const alertsFiring = useMemo(() => (alerts.data ?? []).filter((alert) => alert.state === "firing").length, [alerts.data]);

  const [collapsed, setCollapsed] = useState(() => readRail());
  const toggleCollapsed = useCallback(() => {
    setCollapsed((prev) => {
      writeRail(!prev);
      return !prev;
    });
  }, []);

  const authUser = useAuthStore((state) => state.user);
  const user = authUser
    ? {
        name:
          (authUser.user_metadata?.full_name as string | undefined) ??
          (authUser.user_metadata?.name as string | undefined) ??
          authUser.email?.split("@")[0] ??
          "You",
        email: authUser.email ?? undefined,
      }
    : undefined;

  const { trafficLightPadding, dragRegionStyle } = useTitleBarChrome({ collapsedPadding: "0px" });
  const onClose = useCallback(() => void navigate(getParentRouteNavigateOptions(pathname)), [navigate, pathname]);

  return (
    <OrgShell
      orgSwitcher={<OrgSwitcher />}
      page={page}
      apps={apps}
      alertsFiring={alertsFiring}
      search={search}
      user={user}
      collapsed={collapsed}
      onToggleCollapsed={toggleCollapsed}
      brand={
        <div className="flex min-w-0 items-center gap-1" style={{ paddingLeft: trafficLightPadding, ...dragRegionStyle }}>
          <ForgeCloseButton onClose={onClose} />
          <div className="flex h-8 min-w-0 flex-1 items-center gap-2 px-1.5 text-sm font-semibold text-ink">
            <OrgMark />
            <span className="min-w-0 flex-1 truncate">Console</span>
          </div>
        </div>
      }
      brandCollapsed={<OrgMark />}
    >
      <div className="empty:hidden [&:not(:empty)]:mb-4"><ObsQuotaBanner /></div>
      <Outlet />
    </OrgShell>
  );
}

function OrgMark() {
  return (
    <span
      aria-hidden="true"
      className="grid h-5.5 w-5.5 shrink-0 place-items-center rounded-md bg-accent text-on-accent"
    >
      <LayoutGrid className="h-3.5 w-3.5" />
    </span>
  );
}

function readRail(): boolean {
  try {
    return window.localStorage.getItem(RAIL_KEY) === "1";
  } catch {
    return false;
  }
}

function writeRail(collapsed: boolean): void {
  try {
    window.localStorage.setItem(RAIL_KEY, collapsed ? "1" : "0");
  } catch {
    // Not persisted; the rail still toggles for this visit.
  }
}
