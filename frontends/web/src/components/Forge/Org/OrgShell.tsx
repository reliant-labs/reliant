// Copyright (c) 2025 Reliant Labs

/**
 * The ORG console's chrome — nav, top bar, rail — with no data fetching and no
 * routing, the counterpart of Console/ConsoleShell for the pages that are about
 * the whole organization rather than one app.
 *
 *   Apps ›                    All apps (the /apps list), then each app as a
 *                             door into that app's console
 *   Observability ›           Errors · Sessions · Logs · Traces · Metrics ·
 *                             Profiles · Dashboards · Alerts, across every app
 *
 * App and environment are FILTERS here (the `apps` and `envs` URL keys, drawn
 * by the Observability panel itself), not a place — so unlike the app console
 * there is no environment scope in the top bar, and the nav is two rows deep.
 * Every org link carries the filters (`carriedOrgSearch`), so moving between
 * org pages keeps what the reader narrowed to.
 */

import type { ReactNode } from "react";
import { Gauge, LayoutGrid } from "lucide-react";

import Link from "@/components/forge-ui/link";
import SidebarLayout, { type SidebarNavItem, type SidebarUserInfo } from "@/components/forge-ui/sidebar_layout";

import {
  APPS_PATH,
  carriedOrgSearch,
  consolePath,
  OBS_NAV_LABELS,
  OBS_NAV_VIEWS,
  orgPath,
  withSearch,
  type OrgPage,
} from "../Console/paths";

export interface OrgNavInput {
  page: OrgPage;
  /** The org's app names (the Apps list's, hooks/apps-queries.ts), each nested under Apps. */
  apps: readonly string[];
  /** Alerts firing across the org; a danger count on Alerts (and a dot on the rail). */
  alertsFiring?: number;
  /** The current URL search, for the keys every org link carries. */
  search?: Record<string, unknown>;
}

const icon = (Icon: typeof Gauge) => <Icon className="h-4 w-4" aria-hidden="true" />;

/** The nav rows for `page` — pure, so the IA is testable without rendering. */
export function buildOrgNav({ page, apps, alertsFiring = 0, search = {} }: OrgNavInput): { items: SidebarNavItem[] } {
  const carried = carriedOrgSearch(search);
  const href = (target: OrgPage) => withSearch(orgPath(target), carried);

  return {
    items: [
      {
        // The Apps list (/apps) is its own page outside the org console, so it
        // is never the current route here, and a group with an href only opens
        // on its current route. With no href the group opens in place, and
        // "All apps" is its first entry.
        label: "Apps",
        icon: icon(LayoutGrid),
        active: false,
        // An app's console is its own place with its own nav, so these do not carry the org filters.
        children: [
          { label: "All apps", href: APPS_PATH, active: false },
          ...apps.map((app) => ({ label: app, href: consolePath(app, { kind: "overview" }), active: false })),
        ],
      },
      {
        label: "Observability",
        href: href({ kind: "observability" }),
        icon: icon(Gauge),
        active: page.kind === "observability",
        children: OBS_NAV_VIEWS.map((view) => ({
          label: OBS_NAV_LABELS[view],
          href: href({ kind: "obs-view", view }),
          active: page.kind === "obs-view" && page.view === view,
          badge:
            view === "alerts" && alertsFiring > 0
              ? { type: "count" as const, value: alertsFiring, tone: "danger" as const, label: "firing" }
              : undefined,
        })),
      },
    ],
  };
}

/** The breadcrumb trail for `page`. The last crumb is the page itself. */
export function orgCrumbs(page: OrgPage, search: Record<string, unknown> = {}): { label: string; href?: string }[] {
  switch (page.kind) {
    case "observability":
      return [{ label: "Observability" }];
    case "obs-view":
      return [
        { label: "Observability", href: withSearch(orgPath({ kind: "observability" }), carriedOrgSearch(search)) },
        { label: OBS_NAV_LABELS[page.view] },
      ];
  }
}

export interface OrgShellProps extends OrgNavInput {
  /** The brand row: the way out, and what this place is. */
  brand: ReactNode;
  /** The brand row on the collapsed rail (a monogram). */
  brandCollapsed?: ReactNode;
  user?: SidebarUserInfo;
  collapsed: boolean;
  onToggleCollapsed: () => void;
  orgSwitcher?: ReactNode;
  children: ReactNode;
}

export function OrgShell({ brand, brandCollapsed, user, collapsed, onToggleCollapsed, orgSwitcher, children, ...nav }: OrgShellProps) {
  const { items } = buildOrgNav(nav);
  const crumbs = orgCrumbs(nav.page, nav.search);

  return (
    /*
     * `forge-ui` is REQUIRED, not cosmetic: inside it forge's `accent` token
     * resolves to reliant's primary action colour; outside it, to reliant's
     * muted hover tint (src/index.css).
     */
    <div className="forge-ui h-dvh w-full" data-testid="forge-org-shell">
      <SidebarLayout
        navLabel="Organization console"
        brand={brand}
        brandCollapsed={brandCollapsed}
        navItems={items}
        user={user}
        collapsed={collapsed}
        onToggle={onToggleCollapsed}
        headerContent={
          <>
          <nav aria-label="Breadcrumb" className="flex min-w-0 flex-1 items-center gap-1.5 text-sm text-ink-muted">
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
          {orgSwitcher}
          </>
        }
      >
        {children}
      </SidebarLayout>
    </div>
  );
}
