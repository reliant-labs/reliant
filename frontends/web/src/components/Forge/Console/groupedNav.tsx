// Copyright (c) 2026 Reliant Labs

/**
 * The console's nav for a host with no shell around it (the public demo):
 * the shell's groups, in the shell's order (Layout/sidebar/navConfig/hybrid.ts),
 * so a visitor who signs up meets the same map. Each page keeps its technical
 * name, with a plain line under its title (PAGE_SUBTITLES):
 *
 *   Overview
 *   Dashboards        ▸   the app's own and starred dashboards — the ONE place (U6c bug 4)
 *   Observability     ▾   Overview · Service graph · Metrics · Logs · RPC explorer · Profiles ·
 *                         Errors · Alerts · Traces · Speed insights · Sessions
 *   Analytics         ▾   Product overview · Users · Product · Acquisition · Events & metrics ·
 *                         Experiments (CHANGES_UI) · registered rows (consoleNavRegistry.ts `group`)
 *   Deployments       ▾   Services · Releases · Environments › (each long-lived one, previews as one row)
 *   Tests             ▾   Overview · Coverage · Runs · Flaky tests
 *   Data · Storage · Auth
 *   Security          ▾   its registered rows (Vulnerabilities, SBOMs); not drawn without one
 *   Configure         ▾   Secrets · Domains · Usage · Activity
 *
 * The shell's Inbox and Agents, and its footer (Usage & billing, Settings),
 * are the signed-in product's, not an app's: they have no row here. The
 * shell's Observability is a drill-in; here it is a group, like the rest.
 * A group opens by itself on one of its pages. A host can leave out pages it
 * does not serve (`hiddenPages`: the demo's Usage).
 *
 * Nothing renders it unless a host turns the led console on (ledConsole.tsx).
 */

import type { ReactNode } from "react";
import { Activity, BarChart3, Database, FlaskConical, Gauge, HardDrive, LayoutDashboard, LayoutGrid, Rocket, ShieldCheck, UserRound, Wrench } from "lucide-react";

import type { SidebarNavItem } from "@/components/forge-ui/sidebar_layout";

import { consoleNavEntries, consoleNavEntryLights, consoleNavEntryTarget, CONSOLE_NAV_ANCHORS, type ConsoleNavAnchor, type ConsoleNavEntry, type ConsoleNavGroup } from "./consoleNavRegistry";
import { configureNavRows, experimentsListed, NAV_STARRED_MAX, type ConsoleNavDashboard, type ConsoleNavInput } from "./ConsoleShell";
import {
  ANALYTICS_TAB_LABELS,
  ANALYTICS_TABS,
  consolePath,
  OBS_NAV_LABELS,
  TESTS_VIEW_KINDS,
  TESTS_VIEW_LABELS,
  TESTS_VIEW_PAGE,
  TESTS_VIEWS,
  withSearch,
  type AnalyticsTab,
  type ConsolePage,
  type ObsNavView,
} from "./paths";

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
  services: "Everything your app runs, live: status, instances, CPU, memory and requests",
  usage: "What your app's hosting costs this month, per environment",
  events: "The product events and metrics your app sends",
  analytics: "Who your users are, what they do, and where they come from",
  "speed-insights": "How fast each page loads and responds for real visitors",
  storage: "Your app's object storage buckets: files, uploads and assets",
  experiments: "Turn things on for some people, and compare versions",
};

/** The Product analytics hub: the dashboard it opens on, and every dashboard key that is one of its tabs. */
export interface ProductAnalyticsHub {
  open: ConsolePage;
  keys: readonly string[];
}

export interface GroupedNavInput extends ConsoleNavInput {
  /** The project's Product analytics hub, when it has one: its dashboards light their Analytics tab. */
  productAnalytics?: ProductAnalyticsHub;
  /** A host page (one that is not a console page) is open: no console row is the current one. */
  hostPageActive?: boolean;
  /** The app's own dashboards (its repo's, the org's saved ones): listed under Dashboards. */
  customDashboards?: readonly ConsoleNavDashboard[];
  /** Rows other modules registered (consoleNavRegistry.ts); omitted, the registry's. */
  extensions?: readonly ConsoleNavEntry[];
  /** Pages the host does not serve: their rows are left out (the public demo's Usage). */
  hiddenPages?: readonly ConsolePage["kind"][];
}

/** A nav group that shows its rows on every page, not only while one is open (the app's own dashboards). */
export type OpenNavItem = SidebarNavItem & { alwaysOpen?: boolean };

/** The group each view sits in, for the breadcrumb: the shell's (Layout/sidebar/navConfig/items.tsx). */
const GROUP_OF: Partial<Record<ObsNavView, string>> = {
  errors: "Performance & issues",
  alerts: "Performance & issues",
  traces: "Performance & issues",
  rpc: "Observability",
  logs: "Observability",
  metrics: "Observability",
  profiles: "Observability",
  graph: "Observability",
  sessions: "Performance & issues",
};

const isHubPage = (page: ConsolePage, hub: ProductAnalyticsHub | undefined) => !!hub && page.kind === "dashboard" && hub.keys.includes(page.key);

/** The Analytics tab a hub dashboard is (its key, or `product-overview` for the first tab). */
const hubTab = (key: string): AnalyticsTab | null => {
  const tab = key === "product-overview" ? "overview" : key;
  return (ANALYTICS_TABS as readonly string[]).includes(tab) ? (tab as AnalyticsTab) : null;
};

/** A top-level row, with the registry anchors it stands for and the registry group it is. */
interface Placed {
  item: SidebarNavItem;
  anchors?: readonly ConsoleNavAnchor[];
  group?: ConsoleNavGroup;
}

export function buildGroupedConsoleNav(input: GroupedNavInput): { items: SidebarNavItem[]; footer: SidebarNavItem[] } {
  const { project, page, env, alertsFiring = 0, starred = [], optionalViews, experiments = false, linkTo, productAnalytics, hostPageActive = false, customDashboards = [], hiddenPages = [], navCounts } = input;
  const href = linkTo ?? ((target: ConsolePage) => withSearch(consolePath(project, target), { env }));
  // On a host page, every console row is a way out, none the current one.
  const lit = (active: boolean) => !hostPageActive && active;
  // The rows of pages the host does not serve, pruned once the tree is built.
  const hidden = new WeakSet<SidebarNavItem>();
  const isObs = (view: ObsNavView) => page.kind === "obs-view" && page.view === view;
  const row = (label: string, target: ConsolePage, rowIcon: ReactNode | undefined, active: boolean, extra: Partial<SidebarNavItem> = {}): SidebarNavItem => {
    const item: SidebarNavItem = { label, href: href(target), ...(rowIcon ? { icon: rowIcon } : {}), active: lit(active), ...extra };
    if (hiddenPages.includes(target.kind)) hidden.add(item);
    return item;
  };
  const prune = (items: SidebarNavItem[]): SidebarNavItem[] =>
    items.filter((item) => !hidden.has(item)).map((item) => (item.children ? { ...item, children: prune(item.children) } : item));
  const obs = (view: ObsNavView, extra: Partial<SidebarNavItem> = {}): SidebarNavItem => row(OBS_NAV_LABELS[view], { kind: "obs-view", view }, undefined, isObs(view), extra);
  const firing = alertsFiring > 0 ? { type: "count" as const, value: alertsFiring, tone: "danger" as const, label: "firing" } : undefined;

  const nested = starred.slice(0, NAV_STARRED_MAX);
  const onHub = isHubPage(page, productAnalytics);
  const isOpen = (candidate: ConsoleNavDashboard) => page.kind === "dashboard" && page.dashKind === candidate.dashKind && page.key === candidate.key;
  // The app's own dashboards, then its starred ones not already listed.
  const listed = [...customDashboards, ...nested.filter((star) => !customDashboards.some((own) => own.dashKind === star.dashKind && own.key === star.key))];
  // Dashboards: ONE top-level row (U6c, owner bug 4), the app's own dashboards under it.
  const dashboards: OpenNavItem = {
    label: "Dashboards",
    href: href({ kind: "obs-view", view: "dashboards" }),
    icon: icon(LayoutGrid),
    // A dashboard that is not a hub tab and not listed lights Dashboards itself.
    active: lit(isObs("dashboards") || (page.kind === "dashboard" && !onHub && !listed.some(isOpen))),
    alwaysOpen: customDashboards.length > 0,
    children: listed.length
      ? [
          ...listed.map((candidate) => ({ label: candidate.title, href: href({ kind: "dashboard", dashKind: candidate.dashKind, key: candidate.key }), active: lit(isOpen(candidate)) })),
          { label: "All dashboards", href: href({ kind: "obs-view", view: "dashboards" }), active: false },
        ]
      : undefined,
  };
  // Profiles hides on a definite empty read, as in the console's own nav, unless it is the open page.
  const profiles = optionalViews?.profiles !== false || isObs("profiles");

  // Observability: the shell's drill-in rows, in its order; the firing count shows on the group while it is folded.
  const observability: SidebarNavItem = {
    label: "Observability",
    icon: icon(Gauge),
    badge: firing,
    children: [
      row("Overview", { kind: "obs-overview" }, undefined, page.kind === "obs-overview"),
      obs("graph"),
      obs("metrics"),
      obs("logs"),
      obs("rpc"),
      ...(profiles ? [obs("profiles")] : []),
      obs("errors"),
      obs("alerts", { badge: firing }),
      obs("traces"),
      row("Speed insights", { kind: "speed-insights" }, undefined, page.kind === "speed-insights"),
      obs("sessions"),
    ],
  };

  // Analytics: the Analytics page's tabs (a hub dashboard lights its tab), then Experiments.
  const tabOpen = (tab: AnalyticsTab) =>
    (page.kind === "analytics" && (page.tab ?? "overview") === tab) || (tab === "events" && page.kind === "events") || (onHub && page.kind === "dashboard" && hubTab(page.key) === tab);
  const analytics: SidebarNavItem = {
    label: "Analytics",
    icon: icon(BarChart3),
    children: [
      ...ANALYTICS_TABS.map((tab) => row(ANALYTICS_TAB_LABELS[tab], { kind: "analytics", tab }, undefined, tabOpen(tab))),
      ...(experimentsListed(page, experiments) ? [row("Experiments", { kind: "experiments" }, undefined, page.kind === "experiments" || page.kind === "experiment")] : []),
    ],
  };

  // Environments (each long-lived one, previews as one row): the console's own row, out of its Configure section.
  const { section: _section, icon: _icon, ...environments } = configureNavRows(input, href)[0]!;
  const deployments: SidebarNavItem = {
    label: "Deployments",
    icon: icon(Rocket),
    children: [
      row("Services", { kind: "services" }, undefined, page.kind === "services" || page.kind === "service" || page.kind === "workload"),
      row("Releases", { kind: "releases" }, undefined, page.kind === "releases" || page.kind === "release"),
      {
        ...environments,
        active: lit(!!environments.active),
        children: environments.children?.map(({ trailing: _trailing, ...child }) => ({ ...child, active: lit(!!child.active) })),
        // The sidebar draws no mark on a third-level row: an environment's queued deploy is marked on Environments.
        trailing: environments.children?.find((child) => child.trailing)?.trailing,
      },
    ],
  };

  const testsCount = navCounts?.tests ?? 0;
  const tests: SidebarNavItem = {
    label: "Tests",
    icon: icon(FlaskConical),
    // Not the danger tone: a flaky test is work, not an outage (navConfig/tests.tsx).
    badge: testsCount > 0 ? { type: "count", value: testsCount, label: testsCount === 1 ? "test needing attention" : "tests needing attention" } : undefined,
    children: TESTS_VIEWS.map((view) => row(TESTS_VIEW_LABELS[view], TESTS_VIEW_PAGE[view], undefined, TESTS_VIEW_KINDS[view].includes(page.kind))),
  };

  const configure: SidebarNavItem = {
    label: "Configure",
    icon: icon(Wrench),
    // Activity (the audit log) stays its last row, as in the shell's nav.
    children: (["secrets", "domains", "usage", "activity"] as const).map((kind) =>
      row({ secrets: "Secrets", domains: "Domains", usage: "Usage", activity: "Activity" }[kind], { kind }, undefined, page.kind === kind)
    ),
  };

  const placed: Placed[] = [
    { item: row("Overview", { kind: "overview" }, icon(LayoutDashboard), page.kind === "overview"), anchors: ["overview"] },
    { item: dashboards, anchors: ["dashboards"] },
    { item: observability, anchors: ["issues", "performance", "sessions", "more-tools"] },
    { item: analytics, anchors: ["product-analytics"], group: "product-analytics" },
    { item: deployments, anchors: ["services", "releases"] },
    { item: tests },
    { item: row("Data", { kind: "data" }, icon(Database), page.kind === "data"), anchors: ["data"] },
    { item: row("Storage", { kind: "storage" }, icon(HardDrive), page.kind === "storage") },
    { item: row("Auth", { kind: "auth" }, icon(UserRound), page.kind === "auth" || page.kind === "users"), anchors: ["users"] },
    { item: { label: "Security", icon: icon(ShieldCheck), children: [] }, group: "security" },
    { item: configure },
  ];
  const items = prune(withExtensions(placed, input, href, lit).map((entry) => entry.item))
    // A group with no rows (Security until its pages register) is not drawn, so no group is ever empty.
    .filter((item) => item.href || (item.children?.length ?? 0) > 0);
  return { items, footer: [] };
}

/**
 * The registered rows (consoleNavRegistry.ts) merged in: each one the app
 * has (`available`) — inside its `group` (Analytics, Security) as the
 * group's last child, else after the row that stands for its anchor, or,
 * when this nav draws no row for that anchor, after the row for the nearest
 * anchor before it.
 */
function withExtensions(placed: Placed[], input: GroupedNavInput, href: (target: ConsolePage) => string, lit: (active: boolean) => boolean): Placed[] {
  const extensions = (input.extensions ?? consoleNavEntries()).filter((entry) => entry.available?.(input) ?? true);
  const out = [...placed];
  for (const entry of extensions) {
    const rowItem: SidebarNavItem = {
      label: entry.label,
      href: href(consoleNavEntryTarget(entry)),
      active: lit(consoleNavEntryLights(entry, input.page)),
    };
    const group = entry.group ? out.findIndex((candidate) => candidate.group === entry.group) : -1;
    if (group >= 0) {
      const parent = out[group]!;
      out[group] = { ...parent, item: { ...parent.item, children: [...(parent.item.children ?? []), rowItem] } };
      continue;
    }
    // The row for the anchor, or for the nearest anchor before it; the top of the list with none.
    let at = -1;
    for (let rank = CONSOLE_NAV_ANCHORS.indexOf(entry.after); rank >= 0 && at < 0; rank--) {
      at = out.findIndex((candidate) => candidate.anchors?.includes(CONSOLE_NAV_ANCHORS[rank]!));
    }
    // After rows registered to the same anchor before it.
    while (at + 1 < out.length && extensions.some((other) => other.label === out[at + 1]!.item.label && other.after === entry.after)) at += 1;
    out.splice(at + 1, 0, { item: { ...rowItem, icon: entry.icon } });
  }
  return out;
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
  if (isHubPage(page, productAnalytics)) return [{ label: "Analytics" }, ...own.slice(-1)];
  // Events & metrics is a tab of Analytics, as in the shell's nav.
  if (page.kind === "events") return [{ label: "Analytics" }, { label: "Events & metrics" }];
  // Services, Releases and Environments sit in the shell nav's Deployments ▾ (U6e).
  if (
    page.kind === "services" ||
    page.kind === "service" ||
    page.kind === "workload" ||
    page.kind === "releases" ||
    page.kind === "release" ||
    page.kind === "environments" ||
    page.kind === "environment"
  ) {
    return own[0]?.label === "Deployments" ? own : [{ label: "Deployments" }, ...own];
  }
  if (page.kind === "dashboard") {
    const rest = own.filter((crumb) => crumb.label !== "Observability");
    return rest[0]?.label === "Dashboards" ? rest : [{ label: "Dashboards" }, ...rest];
  }
  return null;
}
