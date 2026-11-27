// Copyright (c) 2025 Reliant Labs

/**
 * The layout route for the org pages — org-wide Observability
 * (/forge/observability[/:view]) and the ML tab (/forge/ml/:view, Forge/Ml).
 * The org's list of apps is /apps
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
import { useConsoleUser } from "@/components/Forge/Console/consoleHost";
import { useObsNavOpen } from "@/components/Forge/Console/obsNavOpen";

import { ObsQuotaBanner } from "../Observability/ObsStates";
import { ForgeCloseButton } from "../ForgeHeader";
import { isForgeShell } from "@/lib/shell";
import { ForgeMark } from "@/components/ForgeShell/ForgeBrand";
import { orgPageOf, type OrgPage } from "../Console/paths";
import { OrgSwitcher } from "@/components/Orgs/OrgSwitcher";
import { orgCrumbs, OrgShell } from "./OrgShell";
import Link from "@/components/forge-ui/link";
import { PageMain, ShellPageHeader, useShellFrame } from "@/components/Layout/shell/shellFrame";
import { projectsHref } from "@/lib/paths";

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

  const [obsOpen, setObsOpen] = useObsNavOpen();

  const user = useConsoleUser();

  const { trafficLightPadding, dragRegionStyle } = useTitleBarChrome({ collapsedPadding: "0px" });
  const onClose = useCallback(() => void navigate(getParentRouteNavigateOptions(pathname)), [navigate, pathname]);
  const { inShell } = useShellFrame();
  // Observability's telemetry allowance: an ML page reads none of it.
  const quotaBanner = page.kind === "ml" ? null : <div className="empty:hidden [&:not(:empty)]:mb-4"><ObsQuotaBanner /></div>;

  // Inside the one shell, the Observability views are rows of its nav (under
  // Apps, while one is open): the org console is a page under a slim bar.
  if (inShell) {
    const crumbs = orgCrumbs(page, search);
    return (
      <div className="forge-ui forge-console flex h-full min-h-0 w-full flex-col" data-testid="forge-org-frame">
        <ShellPageHeader testId="forge-org-header">
          <nav aria-label="Breadcrumb" className="flex min-w-0 items-center gap-1.5 text-sm text-ink-muted">
            {crumbs.map((crumb, index) => (
              <span key={`${crumb.label}:${index}`} className="flex min-w-0 items-center gap-1.5">
                {index > 0 && (
                  <span aria-hidden="true" className="text-ink-subtle">
                    ›
                  </span>
                )}
                {crumb.href ? (
                  <Link href={crumb.href} className="truncate hover:text-ink">
                    {crumb.label}
                  </Link>
                ) : (
                  <span aria-current="page" className="truncate font-medium text-ink">
                    {crumb.label}
                  </span>
                )}
              </span>
            ))}
          </nav>
        </ShellPageHeader>
        <PageMain className="relative min-h-0 flex-1 overflow-y-auto px-8 pb-12 pt-6 max-md:px-4 max-md:pt-4">
          {quotaBanner}
          <Outlet />
        </PageMain>
      </div>
    );
  }

  return (
    <OrgShell
      orgSwitcher={<OrgSwitcher />}
      page={page}
      apps={apps}
      alertsFiring={alertsFiring}
      search={search}
      obsOpen={obsOpen}
      onObsOpenChange={setObsOpen}
      user={user}
      collapsed={collapsed}
      onToggleCollapsed={toggleCollapsed}
      brand={
        <div className="flex min-w-0 items-center gap-1" style={{ paddingLeft: trafficLightPadding, ...dragRegionStyle }}>
          {/* The forge shell has nothing to close back to: its mark leads home. */}
          {isForgeShell() ? (
            <a href={projectsHref("apps")} aria-label="Forge by Reliant: all apps" className="rounded-md p-0.5" data-testid="forge-shell-home">
              <ForgeMark className="h-6 w-6" />
            </a>
          ) : (
            <ForgeCloseButton onClose={onClose} />
          )}
          <div className="flex h-8 min-w-0 flex-1 items-center gap-2 px-1.5 text-sm font-semibold text-ink">
            <OrgMark />
            <span className="min-w-0 flex-1 truncate">Console</span>
          </div>
        </div>
      }
      brandCollapsed={<OrgMark />}
    >
      {quotaBanner}
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
