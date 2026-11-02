// Copyright (c) 2025 Reliant Labs

/**
 * The console's CHROME — nav, top bar, rail — with no data fetching, no
 * routing and no project resolution, so the dev preview harnesses render the
 * same chrome the product does (one copy; a harness that drew its own looked
 * like the product and was not).
 *
 * ── THE NAV IS THE PRODUCT'S NOUNS, NOT THE ENVIRONMENT ROSTER ─────────────
 *
 *   Overview                      project home: health, needs attention, workloads
 *   Releases                      releases × environments, and the deploys
 *   Observability ›               Errors · Logs · Traces · Metrics · Dashboards · Alerts
 *                                 Sessions / Profiles when data is available (or unknown)
 *                                 (up to three starred dashboards nested under Dashboards)
 *   Configure
 *     Environments ›              one entry per long-lived environment, previews as one
 *     Secrets · Domains
 *   ─
 *   Activity                      the audit log, pinned last above the user row
 *
 * The nav is the same rows however many environments a project has:
 * environment is the top bar's scope — the ONE environment selector — and an
 * environment keeps a page only as a configured object, under Configure.
 * Every link carries the scope (`?env=`) so moving between pages keeps it.
 *
 * Observability has no home of its own: its health strip is the Overview's,
 * so the parent row opens its first view.
 *
 * Every row is the same style (forge-ui SidebarLayout, the Cloudflare
 * pattern); a parent navigates to its own overview and expands its children
 * inline, and the group holding the current page stays open.
 */

import type { ReactNode } from "react";
import { Activity, Gauge, Globe, Home, KeyRound, Layers, Tag as TagIcon } from "lucide-react";

import SidebarLayout, { type SidebarNavItem, type SidebarUserInfo } from "@/components/forge-ui/sidebar_layout";
import Link from "@/components/forge-ui/link";

import type { DashboardPageKind } from "../Observability/Dashboards/dashboardModel";
import { shortVersion } from "../Releases/releaseModel";
import { EnvScopeControl } from "./EnvScopeControl";
import type { ScopeOption } from "./envScope";
import {
  consolePath,
  OBS_NAV_LABELS,
  OBS_NAV_VIEWS,
  pinnedEnvOf,
  withSearch,
  type ConsolePage,
} from "./paths";

export interface ConsoleNavEnv {
  name: string;
  preview: boolean;
  /** What this environment's QUEUED deploy waits on ("billing"), when one is. */
  queuedOn?: string;
}

export interface ConsoleNavInput {
  project: string;
  page: ConsolePage;
  /** The scope every link carries. */
  env: string;
  envs: ConsoleNavEnv[];
  /** Alerts firing in scope; a danger count on Alerts (and a dot on the rail). */
  alertsFiring?: number;
  /** The caller's starred dashboards; the first three nest under Dashboards. */
  starred?: ConsoleNavDashboard[];
  /** The open dashboard's title, for the breadcrumb (its key until the index answers). */
  dashboardTitle?: string;
  /** Omitted/unknown keeps useful views visible; false means a definite empty read. */
  optionalViews?: { sessions: boolean; profiles: boolean };
}

export interface ConsoleNavDashboard {
  title: string;
  dashKind: DashboardPageKind;
  key: string;
}

/** How many starred dashboards nest under Dashboards in the nav. */
export const NAV_STARRED_MAX = 3;

const icon = (Icon: typeof Home) => <Icon className="h-4 w-4" aria-hidden="true" />;

/** The nav rows for `page` — pure, so the IA is testable without rendering. */
export function buildConsoleNav({ project, page, env, envs, alertsFiring = 0, starred = [], optionalViews }: ConsoleNavInput): {
  items: SidebarNavItem[];
  footer: SidebarNavItem[];
} {
  const href = (target: ConsolePage) => withSearch(consolePath(project, target), { env });
  const is = (kind: ConsolePage["kind"]) => page.kind === kind;
  const row = (label: string, target: ConsolePage, rowIcon: ReactNode, extra: Partial<SidebarNavItem> = {}): SidebarNavItem => ({
    label,
    href: href(target),
    icon: rowIcon,
    active: is(target.kind),
    ...extra,
  });

  const long = envs.filter((candidate) => !candidate.preview);
  const previews = envs.filter((candidate) => candidate.preview);
  // The environment whose OWN page is open. A workload page pins its
  // environment too, but it is an Overview page: Environments stays closed.
  const pinned = page.kind === "environment" ? pinnedEnvOf(page) : null;

  return {
    items: [
      row("Overview", { kind: "overview" }, icon(Home), { active: is("overview") || is("workload") }),
      row("Releases", { kind: "releases" }, icon(TagIcon), { active: is("releases") || is("release") }),
      row("Observability", { kind: "obs-view", view: OBS_NAV_VIEWS[0] }, icon(Gauge), {
        active: false,
        children: OBS_NAV_VIEWS.filter((view) =>
          (view !== "sessions" && view !== "profiles") || optionalViews?.[view] !== false || (page.kind === "obs-view" && page.view === view)
        ).map((view) => {
          const nested = view === "dashboards" ? starred.slice(0, NAV_STARRED_MAX) : [];
          const openStar = (candidate: ConsoleNavDashboard) =>
            page.kind === "dashboard" && page.dashKind === candidate.dashKind && page.key === candidate.key;
          return {
            label: OBS_NAV_LABELS[view],
            href: href({ kind: "obs-view", view }),
            // A dashboard that is not one of the nested stars lights Dashboards itself.
            active: (page.kind === "obs-view" && page.view === view) || (view === "dashboards" && page.kind === "dashboard" && !nested.some(openStar)),
            badge:
              view === "alerts" && alertsFiring > 0
                ? { type: "count" as const, value: alertsFiring, tone: "danger" as const, label: "firing" }
                : undefined,
            children: nested.length
              ? [
                  ...nested.map((candidate) => ({
                    label: candidate.title,
                    href: href({ kind: "dashboard", dashKind: candidate.dashKind, key: candidate.key }),
                    active: openStar(candidate),
                  })),
                  { label: "All dashboards", href: href({ kind: "obs-view", view }), active: false },
                ]
              : undefined,
          };
        }),
      }),
      row("Environments", { kind: "environments" }, icon(Layers), {
        section: "Configure",
        children: [
          ...long.map((candidate) => ({
            label: candidate.name,
            href: href({ kind: "environment", env: candidate.name }),
            active: pinned === candidate.name,
            trailing: candidate.queuedOn ? <QueuedMark env={candidate.name} on={candidate.queuedOn} /> : undefined,
          })),
          ...(previews.length > 0
            ? [
                {
                  label: "Previews",
                  href: href({ kind: "environments" }),
                  active: !!pinned && previews.some((candidate) => candidate.name === pinned),
                  badge: { type: "count" as const, value: previews.length, label: "preview environments" },
                },
              ]
            : []),
        ],
      }),
      row("Secrets", { kind: "secrets" }, icon(KeyRound), { section: "Configure" }),
      row("Domains", { kind: "domains" }, icon(Globe), { section: "Configure" }),
    ],
    footer: [row("Activity", { kind: "activity" }, icon(Activity))],
  };
}

/** The breadcrumb trail for `page`, after the project. The last crumb is the page itself. */
export function consoleCrumbs(project: string, page: ConsolePage, env: string, dashboardTitle?: string): { label: string; href?: string }[] {
  const href = (target: ConsolePage) => withSearch(consolePath(project, target), { env });
  switch (page.kind) {
    case "overview":
      return [{ label: "Overview" }];
    case "workload":
      // The workload's environment is pinned by its path: the trail leads back to the Overview scoped to it.
      return [{ label: "Overview", href: withSearch(consolePath(project, { kind: "overview" }), { env: page.env }) }, { label: page.name }];
    case "releases":
      return [{ label: "Releases" }];
    case "release":
      return [{ label: "Releases", href: href({ kind: "releases" }) }, { label: shortVersion(page.version) }];
    case "obs-view":
      return [{ label: "Observability", href: href({ kind: "obs-view", view: OBS_NAV_VIEWS[0] }) }, { label: OBS_NAV_LABELS[page.view] }];
    case "dashboard":
      return [
        { label: "Observability", href: href({ kind: "obs-view", view: OBS_NAV_VIEWS[0] }) },
        { label: "Dashboards", href: href({ kind: "obs-view", view: "dashboards" }) },
        { label: dashboardTitle || page.key },
      ];
    case "environments":
      return [{ label: "Environments" }];
    case "environment":
      return [{ label: "Environments", href: href({ kind: "environments" }) }, { label: page.env }];
    case "secrets":
      return [{ label: "Secrets" }];
    case "domains":
      return [{ label: "Domains" }];
    case "activity":
      return [{ label: "Activity" }];
  }
}

export interface ConsoleShellProps extends ConsoleNavInput {
  /** The project switcher, in the brand row. */
  brand: ReactNode;
  /** The brand row on the collapsed rail (a monogram). */
  brandCollapsed?: ReactNode;
  scope: { long: ScopeOption[]; previews: ScopeOption[] };
  /** The open page reads platform telemetry, which belongs to no environment. */
  platformWide?: boolean;
  onEnvChange: (env: string) => void;
  user?: SidebarUserInfo;
  collapsed: boolean;
  onToggleCollapsed: () => void;
  /**
   * "fill" for a page that owns its own scroller and gutter. Default
   * "padded": the forge-ui page gutter (px-8, pt-6).
   */
  contentLayout?: "padded" | "fill";
  /** Product org switcher; injected to keep preview chrome free of queries. */
  orgSwitcher?: ReactNode;
  children: ReactNode;
}

export function ConsoleShell({
  brand,
  brandCollapsed,
  scope,
  platformWide,
  onEnvChange,
  user,
  collapsed,
  onToggleCollapsed,
  contentLayout,
  orgSwitcher,
  children,
  ...nav
}: ConsoleShellProps) {
  const { items, footer } = buildConsoleNav(nav);
  const crumbs = consoleCrumbs(nav.project, nav.page, nav.env, nav.dashboardTitle);

  return (
    /*
     * `forge-ui` is REQUIRED, not cosmetic: inside it forge's `accent` token
     * resolves to reliant's primary action colour; outside it, to reliant's
     * muted hover tint (src/index.css).
     */
    <div className="forge-ui h-dvh w-full" data-testid="forge-shell">
      <SidebarLayout
        navLabel={nav.project}
        brand={brand}
        brandCollapsed={brandCollapsed}
        navItems={items}
        footerItems={footer}
        user={user}
        collapsed={collapsed}
        onToggle={onToggleCollapsed}
        contentLayout={contentLayout}
        headerContent={
          <>
            <nav aria-label="Breadcrumb" className="flex min-w-0 flex-1 items-center gap-1.5 text-sm text-ink-muted">
              <Link
                href={withSearch(consolePath(nav.project, { kind: "overview" }), { env: nav.env })}
                className="truncate hover:text-ink"
              >
                {nav.project}
              </Link>
              {crumbs.map((crumb, index) => (
                <span key={`${crumb.label}:${index}`} className="flex min-w-0 items-center gap-1.5">
                  <span aria-hidden="true" className="text-ink-subtle">
                    ›
                  </span>
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
            <EnvScopeControl
              value={nav.env}
              long={scope.long}
              previews={scope.previews}
              onChange={onEnvChange}
              pinnedEnv={pinnedEnvOf(nav.page)}
              platformWide={platformWide}
            />
          </>
        }
      >
        {children}
      </SidebarLayout>
    </div>
  );
}

/** The nav's mark for an environment whose deploy is queued on a person. */
function QueuedMark({ env, on }: { env: string; on: string }) {
  return (
    <span
      className="inline-flex items-center gap-1 rounded-full border border-warning-border px-1.5 text-xs leading-4 text-warning-ink"
      title={`A deploy to ${env} is queued, waiting on ${on}`}
      data-testid={`forge-nav-queued-${env}`}
    >
      Queued
      <span className="sr-only">, waiting on {on}</span>
    </span>
  );
}
