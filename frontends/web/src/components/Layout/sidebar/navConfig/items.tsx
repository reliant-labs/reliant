// Copyright (c) 2026 Reliant Labs

/**
 * The nav items the three variants share (U6c, prototype): every destination
 * once, with its href, its match and its all-projects behaviour. a.ts, b.ts
 * and c.ts only arrange these.
 *
 * All projects (several, or every one, in the switcher): Overview is the
 * projects' summary; Observability's views and Dashboards are the org-wide
 * pages (an in-page app filter); every other app page opens the focused
 * project's, with its in-page picker (PAGES); the chat list groups by project.
 */

import {
  Activity,
  ArrowUpRight,
  BarChart3,
  Bot,
  Boxes,
  CircleAlert,
  CreditCard,
  Database,
  Gauge,
  Globe,
  HardDrive,
  KeyRound,
  LayoutDashboard,
  LayoutGrid,
  Layers,
  Receipt,
  Server,
  Settings,
  Sparkles,
  Tag,
  UserRound,
  Wrench,
} from "lucide-react";
import type { ComponentType, ReactNode } from "react";

import { homePath, projectChatsPath, projectPath, projectsPath, runsPath, settingsPath, withSearch, workflowLibraryPath } from "@/lib/paths";
import { isWorkflowsAreaPath, workflowsTabForPath } from "@/lib/workflowsArea";

import { consoleNavEntries, type ConsoleNavEntry } from "../../../Forge/Console/consoleNavRegistry";
import { consolePath, OBS_NAV_LABELS, orgPath, type ConsolePage, type ObsNavView } from "../../../Forge/Console/paths";
import { settingsNavGroups } from "../../../Settings/SettingsNavigation";
import { isAt, isAtOrBelow } from "../navLocation";
import { isChatPage } from "../shellProject";
import { analyticsPath, ANALYTICS_TABS, sandboxesPath, speedInsightsPath, usageBillingPath, type AnalyticsTab } from "./stubPaths";
import type { NavArea, NavCtx, NavGroup, NavHeading, NavItem, NavRow, NavSlot } from "./types";

const icon = (Icon: typeof Activity): ReactNode => <Icon className="size-4" aria-hidden="true" />;

// ── Helpers ─────────────────────────────────────────────────────────────────

const hasApp = (ctx: NavCtx) => !!ctx.app;

/** A console page of the focused app, in the app's env scope. */
const consoleHref = (page: ConsolePage) => (ctx: NavCtx) =>
  ctx.app ? withSearch(consolePath(ctx.app, page), { env: ctx.console?.env }) : undefined;

const pageIs = (...kinds: ConsolePage["kind"][]) => (ctx: NavCtx) => !!ctx.page && kinds.includes(ctx.page.kind);

function consoleRow(id: string, label: string, page: ConsolePage, rowIcon: ReactNode | undefined, kinds: ConsolePage["kind"][]): NavRow {
  return { kind: "row", id, label, icon: rowIcon, href: consoleHref(page), match: pageIs(...kinds), available: hasApp };
}

export const heading = (id: string, label: string): NavHeading => ({ kind: "heading", id, label });
export const slot = (id: NavSlot["slot"]): NavSlot => ({ kind: "slot", id: `slot-${id}`, slot: id });

// ── The project ─────────────────────────────────────────────────────────────

/** ONE landing page per project (Home + Overview merged): its app's Overview, or the project's page; the summary across several. */
export const overview: NavRow = {
  kind: "row",
  id: "overview",
  testId: "sidebar-home-button",
  label: "Overview",
  icon: icon(LayoutDashboard),
  href: (ctx) => {
    if (ctx.mode !== "one") return projectsPath();
    if (ctx.app) return consolePath(ctx.app, { kind: "overview" });
    return ctx.projectId ? projectPath(ctx.projectId) : projectsPath();
  },
  match: (ctx) =>
    ctx.page?.kind === "overview" ||
    (ctx.mode !== "one" && isAt(ctx.pathname, projectsPath())) ||
    (!!ctx.projectId && isAt(ctx.pathname, projectPath(ctx.projectId))),
};

export const services = consoleRow("services", "Services", { kind: "services" }, icon(Boxes), ["services", "service", "workload"]);
/** Buckets and managed Postgres (PAGES makes the page top-level). */
export const storage = consoleRow("storage", "Storage", { kind: "storage" }, icon(HardDrive), ["storage"]);
export const data = consoleRow("data", "Data", { kind: "data" }, icon(Database), ["data"]);
export const releases = consoleRow("releases", "Releases", { kind: "releases" }, icon(Tag), ["releases", "release"]);
export const environments = consoleRow("environments", "Environments", { kind: "environments" }, icon(Layers), ["environments", "environment"]);
export const secrets = consoleRow("secrets", "Secrets", { kind: "secrets" }, icon(KeyRound), ["secrets"]);
export const domains = consoleRow("domains", "Domains", { kind: "domains" }, icon(Globe), ["domains"]);
/** Who can sign in to the app (its managed identity); "Users" alone is Analytics' tab. */
export const signInUsers = consoleRow("users", "Sign-in users", { kind: "users" }, icon(UserRound), ["users"]);
/** What this project's hosting costs (the org's is Usage & billing, in the footer). */
export const projectUsage = consoleRow("project-usage", "Usage", { kind: "usage" }, icon(Receipt), ["usage"]);
export const activity = consoleRow("activity", "Activity", { kind: "activity" }, icon(Activity), ["activity"]);

/** Configure ▾ (A, C): the project's settings-like pages. */
export const configureGroup: NavGroup = {
  kind: "group",
  id: "configure",
  label: "Configure",
  icon: icon(Wrench),
  available: hasApp,
  children: [environments, secrets, domains, signInUsers, projectUsage, activity],
};

// ── Dashboards (one place only: bug 4) ──────────────────────────────────────

const hubKeys = (ctx: NavCtx) => ctx.console?.productAnalytics?.keys ?? [];

export const dashboards: NavRow = {
  kind: "row",
  id: "dashboards",
  label: "Dashboards",
  icon: icon(LayoutGrid),
  href: (ctx) => (ctx.mode !== "one" ? orgPath({ kind: "obs-view", view: "dashboards" }) : consoleHref({ kind: "obs-view", view: "dashboards" })(ctx)),
  match: (ctx) =>
    (ctx.page?.kind === "obs-view" && ctx.page.view === "dashboards") ||
    (ctx.page?.kind === "dashboard" && !hubKeys(ctx).includes(ctx.page.key)) ||
    (ctx.orgPage?.kind === "obs-view" && ctx.orgPage.view === "dashboards"),
  available: (ctx) => hasApp(ctx) || ctx.mode !== "one",
};

// ── Observability ───────────────────────────────────────────────────────────

/** One Observability view: the focused app's, or the org-wide page across several projects. */
function obsRow(view: ObsNavView, extra: Partial<NavRow> = {}): NavRow {
  return {
    kind: "row",
    id: `obs-${view}`,
    label: OBS_NAV_LABELS[view],
    href: (ctx) => (ctx.mode !== "one" ? orgPath({ kind: "obs-view", view }) : consoleHref({ kind: "obs-view", view })(ctx)),
    match: (ctx) =>
      (ctx.page?.kind === "obs-view" && ctx.page.view === view) || (ctx.orgPage?.kind === "obs-view" && ctx.orgPage.view === view),
    available: (ctx) => hasApp(ctx) || ctx.mode !== "one",
    ...extra,
  };
}

const speedInsights: NavRow = {
  kind: "row",
  id: "speed-insights",
  label: "Speed insights",
  href: (ctx) => (ctx.app ? speedInsightsPath(ctx.app) : undefined),
  match: (ctx) => !!ctx.app && isAt(ctx.pathname, speedInsightsPath(ctx.app)),
  available: hasApp,
};

const alertsRow = obsRow("alerts", {
  badge: (ctx) => {
    const firing = ctx.console?.alertsFiring ?? 0;
    return firing > 0 ? { type: "count", value: firing, tone: "danger", label: "firing" } : undefined;
  },
});

/** Round 5: Service graph, Metrics, Logs, RPC explorer, Speed insights (+ Profiles where it has data). */
export const observabilityRows: readonly NavItem[] = [
  obsRow("graph"),
  obsRow("metrics"),
  obsRow("logs"),
  obsRow("rpc"),
  obsRow("profiles", { available: (ctx) => (hasApp(ctx) || ctx.mode !== "one") && ctx.console?.optionalViews?.profiles !== false }),
  speedInsights,
];
/** Round 5: "Performance & issues" = Errors, Alerts, Traces. */
export const performanceRows: readonly NavItem[] = [obsRow("errors"), alertsRow, obsRow("traces")];
export const sessionsRow = obsRow("sessions");

/** The routes that are Observability: every view but Dashboards (top-level), Speed insights, and the org-wide pages. */
export const isObservabilityRoute = (ctx: NavCtx): boolean =>
  (ctx.page?.kind === "obs-view" && ctx.page.view !== "dashboards") ||
  speedInsights.match!(ctx) ||
  ctx.orgPage?.kind === "observability" ||
  (ctx.orgPage?.kind === "obs-view" && ctx.orgPage.view !== "dashboards");

/** Observability › (B, C). */
export const observabilityArea: NavArea = {
  kind: "area",
  id: "observability",
  label: "Observability",
  icon: icon(Gauge),
  match: isObservabilityRoute,
  available: (ctx) => hasApp(ctx) || ctx.mode !== "one",
  children: [
    ...observabilityRows,
    heading("performance-heading", "Performance & issues"),
    ...performanceRows,
    heading("frontend-heading", "Frontend"),
    sessionsRow,
  ],
};

/** Observability ▾ (A). */
export const observabilityGroup: NavGroup = {
  kind: "group",
  id: "observability",
  label: "Observability",
  icon: icon(Gauge),
  available: observabilityArea.available,
  children: [...observabilityRows, sessionsRow],
};

/** Performance & issues ▾ (A). */
export const performanceGroup: NavGroup = {
  kind: "group",
  id: "performance",
  label: "Performance & issues",
  icon: icon(CircleAlert),
  available: observabilityArea.available,
  children: performanceRows,
};

// ── Analytics ───────────────────────────────────────────────────────────────

const TAB_LABELS: Record<(typeof ANALYTICS_TABS)[number], string> = {
  overview: "Product overview",
  users: "Users",
  product: "Product",
  acquisition: "Acquisition",
};

function analyticsTabRow(tab: (typeof ANALYTICS_TABS)[number]): NavRow {
  return {
    kind: "row",
    id: `analytics-${tab}`,
    label: TAB_LABELS[tab],
    href: (ctx) => (ctx.app ? analyticsPath(ctx.app, tab) : undefined),
    match: (ctx) => !!ctx.app && isAt(ctx.pathname, analyticsPath(ctx.app)) && ((ctx.search.tab as AnalyticsTab | undefined) ?? "overview") === tab,
    available: hasApp,
  };
}

const eventsRow = consoleRow("analytics-events", "Events & metrics", { kind: "events" }, undefined, ["events"]);

function registeredEntryOf(ctx: NavCtx): ConsoleNavEntry | undefined {
  const page = ctx.page;
  return page?.kind === "extension" ? consoleNavEntries().find((entry) => entry.id === page.id) : undefined;
}

/**
 * The rows other modules register (consoleNavRegistry.ts; Experiments, first),
 * at the end of Analytics, each while the app has it.
 */
function registeredRows(ctx: NavCtx): NavRow[] {
  const model = ctx.console;
  if (!model) return [];
  return consoleNavEntries()
    .filter((entry) => entry.available?.(model) ?? true)
    .map((entry) => ({
      kind: "row" as const,
      id: `registered-${entry.id}`,
      label: entry.label,
      href: consoleHref({ kind: "extension", id: entry.id, segment: entry.segment }),
      match: (c: NavCtx) => c.page?.kind === "extension" && c.page.id === entry.id,
    }));
}

const analyticsChildren = (ctx: NavCtx): readonly NavItem[] => [...ANALYTICS_TABS.map(analyticsTabRow), eventsRow, ...registeredRows(ctx)];

/** The routes that are Analytics: its page, Events & metrics, the built-in hub's dashboards, the registered pages. */
export const isAnalyticsRoute = (ctx: NavCtx): boolean =>
  ctx.page?.kind === "events" ||
  (!!ctx.app && isAt(ctx.pathname, analyticsPath(ctx.app))) ||
  (ctx.page?.kind === "dashboard" && hubKeys(ctx).includes(ctx.page.key)) ||
  !!registeredEntryOf(ctx);

/** Analytics › (B). */
export const analyticsArea: NavArea = {
  kind: "area",
  id: "analytics",
  label: "Analytics",
  icon: icon(BarChart3),
  match: isAnalyticsRoute,
  available: hasApp,
  children: analyticsChildren,
};

/** Product analytics ▾ (A, C). */
export const analyticsGroup: NavGroup = {
  kind: "group",
  id: "analytics",
  label: "Product analytics",
  icon: icon(BarChart3),
  available: hasApp,
  match: isAnalyticsRoute,
  children: analyticsChildren,
};

// ── Agents ──────────────────────────────────────────────────────────────────

export const workflowsGroup: NavGroup = {
  kind: "group",
  id: "workflows",
  label: "Workflows",
  trail: "workflows-pill",
  children: [
    { kind: "row", id: "workflows-runs", label: "Runs", href: () => runsPath(), match: (ctx) => workflowsTabForPath(ctx.pathname) === "runs" },
    { kind: "row", id: "workflows-library", label: "Library", href: () => workflowLibraryPath(), match: (ctx) => workflowsTabForPath(ctx.pathname) === "workflows" },
  ],
};

/** "Machines" in the UI is "Agent sandboxes" now (round 5); under Agents, "Sandboxes". */
export const sandboxes: NavRow = {
  kind: "row",
  id: "sandboxes",
  label: "Sandboxes",
  icon: icon(Server),
  href: () => sandboxesPath(),
  match: (ctx) => isAtOrBelow(ctx.pathname, sandboxesPath()),
};

export const aiProviders: NavRow = {
  kind: "row",
  id: "ai-providers",
  label: "AI providers",
  icon: icon(Sparkles),
  external: true,
  href: () => settingsPath("ai"),
};

/** The routes that are Agents: the chats (home, a project's, one chat, New chat), Workflows, the sandboxes. */
export const isAgentsRoute = (ctx: NavCtx): boolean =>
  isChatPage(ctx.pathname) || isWorkflowsAreaPath(ctx.pathname) || isAtOrBelow(ctx.pathname, sandboxesPath());

/** Agents' rows; the chat list last, so in a drill-in it takes the rest of the column. */
const agentsChildren: readonly NavItem[] = [slot("new-chat"), workflowsGroup, sandboxes, aiProviders, slot("chat-list")];

/** Agents › (B, C). */
export const agentsArea: NavArea = {
  kind: "area",
  id: "agents",
  label: "Agents",
  icon: icon(Bot),
  match: isAgentsRoute,
  // Its first page: the chats (the project's, or every project's).
  href: (ctx) => (ctx.mode === "one" && ctx.projectId ? projectChatsPath(ctx.projectId) : homePath()),
  trail: "chats-need-you",
  children: agentsChildren,
};

/** Agents ▾ (A). */
export const agentsGroup: NavGroup = {
  kind: "group",
  id: "agents",
  label: "Agents",
  icon: icon(Bot),
  match: isAgentsRoute,
  trail: "chats-need-you",
  children: agentsChildren,
};

// ── The footer ──────────────────────────────────────────────────────────────

export const usageBilling: NavRow = {
  kind: "row",
  id: "usage-billing",
  label: "Usage & billing",
  icon: icon(CreditCard),
  href: () => usageBillingPath(),
  match: (ctx) => isAt(ctx.pathname, usageBillingPath()),
};

function settingsRow(id: Parameters<typeof settingsPath>[0] & string, label: string, Icon: ComponentType<{ className?: string }>): NavRow {
  return {
    kind: "row",
    id: `settings-${id}`,
    label,
    icon: <Icon className="size-4" aria-hidden="true" />,
    href: () => settingsPath(id),
    match: (ctx) => isAtOrBelow(ctx.pathname, settingsPath(id)),
  };
}

/** Settings' sections, from Settings' own list (SettingsNavigation), as the drill-in's rows. */
function settingsChildren(): readonly NavItem[] {
  const { groups, about } = settingsNavGroups();
  const items: NavItem[] = [];
  for (const group of groups) {
    const rows = group.items.map((item) => settingsRow(item.id, item.label, item.icon));
    const id = `settings-group-${group.label.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`;
    if (group.collapsible) items.push({ kind: "group", id, label: group.label, children: rows });
    else items.push(heading(id, group.label), ...rows);
  }
  items.push(settingsRow(about.id, about.label, about.icon));
  return items;
}

/** Settings › — every variant: it REPLACES the nav (owner item 8). */
export const settingsArea: NavArea = {
  kind: "area",
  id: "settings",
  label: "Settings",
  icon: icon(Settings),
  match: (ctx) => isAtOrBelow(ctx.pathname, settingsPath()),
  // Bare /settings opens the section last visited.
  href: () => settingsPath(),
  action: "update-button",
  testId: "sidebar-settings-button",
  children: settingsChildren,
};

export const footer: readonly NavItem[] = [slot("inbox"), usageBilling, settingsArea];

export const externalMark = <ArrowUpRight className="size-3.5" aria-hidden="true" />;
