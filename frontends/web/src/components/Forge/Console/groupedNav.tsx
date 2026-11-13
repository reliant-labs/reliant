// Copyright (c) 2026 Reliant Labs

/**
 * The console's nav grouped by what a reader is after, each page keeping its
 * technical name with a plain line under its title (PAGE_SUBTITLES):
 *
 *   Overview
 *   Issues            ▾     Errors · Alerts
 *   Performance       ▾     Traces · RPC explorer
 *   Sessions
 *   Product analytics       the Users · Product · Acquisition hub, when the project has it
 *   Releases
 *   Data · Users
 *   More tools        ▸     Dashboards (and up to three stars) · Logs · Metrics · Profiles · Service graph · Storage
 *   ── Configure            Environments › · Secrets · Domains
 *   footer                  Activity
 *
 * The same rows, IA and links as buildConsoleNav, regrouped: a reader who
 * knows the term finds it, and one who does not learns it from the subtitle.
 * Nothing renders it unless a host turns the led console on (ledConsole.tsx).
 */

import type { ReactNode } from "react";
import { Activity, BarChart3, CircleAlert, Database, LayoutDashboard, PlayCircle, Tag as TagIcon, Timer, Users, Wrench } from "lucide-react";

import type { SidebarNavItem } from "@/components/forge-ui/sidebar_layout";

import { configureNavRows, NAV_STARRED_MAX, type ConsoleNavDashboard, type ConsoleNavInput } from "./ConsoleShell";
import { consolePath, OBS_NAV_LABELS, withSearch, type ConsolePage, type ObsNavView } from "./paths";

const icon = (Icon: typeof Activity) => <Icon className="h-4 w-4" aria-hidden="true" />;

/** The plain line under each page's technical name, by page key (an Observability view, "overview", "releases", "data"). */
export const PAGE_SUBTITLES: Readonly<Record<string, string>> = {
  overview: "How the app is doing right now",
  errors: "Problems your users hit, grouped by cause",
  alerts: "Rules that tell you when something crosses a line",
  traces: "Step-by-step timeline of one request",
  rpc: "Every API call, how often it runs and how fast",
  sessions: "Watch what a visitor did, with their errors marked",
  releases: "Every version you shipped, and where it runs now",
  data: "Browse the rows in your app's database",
  logs: "Every line your services printed, searchable",
  metrics: "Any number your services report, over time",
  profiles: "Where your code spends its CPU time",
  graph: "Which services call which, and how healthy each call is",
  dashboards: "Saved charts, yours and the ones Forge made",
};

/** The Product analytics hub: the dashboard its row opens, and every dashboard key that is one of its tabs. */
export interface ProductAnalyticsHub {
  open: ConsolePage;
  keys: readonly string[];
}

export interface GroupedNavInput extends ConsoleNavInput {
  /** Omitted, the project has no Product analytics hub and the row is left out. */
  productAnalytics?: ProductAnalyticsHub;
  /** A host page (one that is not a console page) is open: no console row is the current one. */
  hostPageActive?: boolean;
}

/** The group each view sits in, for the nav and the breadcrumb ("Issues › Errors"). */
const GROUP_OF: Partial<Record<ObsNavView, string>> = {
  errors: "Issues",
  alerts: "Issues",
  traces: "Performance",
  rpc: "Performance",
  logs: "More tools",
  metrics: "More tools",
  profiles: "More tools",
  graph: "More tools",
  dashboards: "More tools",
};

const isHubPage = (page: ConsolePage, hub: ProductAnalyticsHub | undefined) => !!hub && page.kind === "dashboard" && hub.keys.includes(page.key);

export function buildGroupedConsoleNav(input: GroupedNavInput): { items: SidebarNavItem[]; footer: SidebarNavItem[] } {
  const { project, page, env, alertsFiring = 0, starred = [], optionalViews, linkTo, productAnalytics, hostPageActive = false } = input;
  const href = linkTo ?? ((target: ConsolePage) => withSearch(consolePath(project, target), { env }));
  // On a host page, every console row is a way out, none the current one.
  const lit = (active: boolean) => !hostPageActive && active;
  const isObs = (view: ObsNavView) => page.kind === "obs-view" && page.view === view;
  const row = (label: string, target: ConsolePage, rowIcon: ReactNode, active: boolean, extra: Partial<SidebarNavItem> = {}): SidebarNavItem => ({
    label,
    href: href(target),
    icon: rowIcon,
    active: lit(active),
    ...extra,
  });
  const obs = (view: ObsNavView, extra: Partial<SidebarNavItem> = {}): SidebarNavItem => ({
    label: OBS_NAV_LABELS[view],
    href: href({ kind: "obs-view", view }),
    active: lit(isObs(view)),
    ...extra,
  });

  const nested = starred.slice(0, NAV_STARRED_MAX);
  const openStar = (candidate: ConsoleNavDashboard) => page.kind === "dashboard" && page.dashKind === candidate.dashKind && page.key === candidate.key;
  const onHub = isHubPage(page, productAnalytics);
  const dashboards: SidebarNavItem = {
    ...obs("dashboards"),
    // A dashboard that is neither a hub tab nor a nested star lights Dashboards itself.
    active: lit(isObs("dashboards") || (page.kind === "dashboard" && !onHub && !nested.some(openStar))),
    children: nested.length
      ? [
          ...nested.map((candidate) => ({ label: candidate.title, href: href({ kind: "dashboard", dashKind: candidate.dashKind, key: candidate.key }), active: lit(openStar(candidate)) })),
          { label: "All dashboards", href: href({ kind: "obs-view", view: "dashboards" }), active: false },
        ]
      : undefined,
  };
  // Profiles hides on a definite empty read, as in the console's own nav, unless it is the open page.
  const profiles = optionalViews?.profiles !== false || isObs("profiles");

  const items: SidebarNavItem[] = [
    row("Overview", { kind: "overview" }, icon(LayoutDashboard), page.kind === "overview" || page.kind === "workload"),
    {
      label: "Issues",
      icon: icon(CircleAlert),
      defaultExpanded: true,
      children: [
        obs("errors"),
        obs("alerts", { badge: alertsFiring > 0 ? { type: "count" as const, value: alertsFiring, tone: "danger" as const, label: "firing" } : undefined }),
      ],
    },
    { label: "Performance", icon: icon(Timer), defaultExpanded: true, children: [obs("traces"), obs("rpc")] },
    row("Sessions", { kind: "obs-view", view: "sessions" }, icon(PlayCircle), isObs("sessions")),
    ...(productAnalytics ? [row("Product analytics", productAnalytics.open, icon(BarChart3), onHub)] : []),
    row("Releases", { kind: "releases" }, icon(TagIcon), page.kind === "releases" || page.kind === "release"),
    row("Data", { kind: "data" }, icon(Database), page.kind === "data"),
    row("Users", { kind: "users" }, icon(Users), page.kind === "users"),
    {
      // No page of its own: the row opens and closes the group, which opens by itself on one of its pages.
      label: "More tools",
      icon: icon(Wrench),
      children: [
        dashboards,
        obs("logs"),
        obs("metrics"),
        ...(profiles ? [obs("profiles")] : []),
        obs("graph"),
        { label: "Storage", href: href({ kind: "storage" }), active: lit(page.kind === "storage") },
      ],
    },
    ...configureNavRows(input, href).map((item) => ({ ...item, active: lit(!!item.active), children: item.children?.map((child) => ({ ...child, active: lit(!!child.active) })) })),
  ];
  return { items, footer: [row("Activity", { kind: "activity" }, icon(Activity), page.kind === "activity")] };
}

type Crumb = { label: string; href?: string };

/**
 * The breadcrumb for the grouped nav: a view under its group ("Issues ›
 * Errors", "More tools › Logs") instead of "Observability", which is no row
 * here. Null keeps the console's own trail (`own`).
 */
export function groupedCrumbs(page: ConsolePage, own: Crumb[], productAnalytics?: ProductAnalyticsHub): Crumb[] | null {
  if (page.kind === "obs-view") {
    const group = GROUP_OF[page.view];
    return group ? [{ label: group }, { label: OBS_NAV_LABELS[page.view] }] : [{ label: OBS_NAV_LABELS[page.view] }];
  }
  if (isHubPage(page, productAnalytics)) return [{ label: "Product analytics" }, ...own.slice(-1)];
  if (page.kind === "dashboard") return [{ label: "More tools" }, ...own.filter((crumb) => crumb.label !== "Observability")];
  return null;
}
