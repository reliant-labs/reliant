// Copyright (c) 2026 Reliant Labs

/**
 * The nav's items (U6d, the owner's hybrid; prototype): every destination
 * once, with its href, its match and its all-projects behaviour. hybrid.ts
 * arranges them.
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
  Boxes,
  Inbox,
  Plug,
  ShieldCheck,
  CreditCard,
  Database,
  Gauge,
  Globe,
  HardDrive,
  KeyRound,
  LayoutDashboard,
  LayoutGrid,
  Layers,
  Rocket,
  Receipt,
  Server,
  Settings,
  Tag,
  UserRound,
  Workflow,
  Wrench,
} from "lucide-react";
import type { ComponentType, ReactNode } from "react";

import { connectorsPath, homePath, inboxPath, projectChatsPath, projectPath, projectsPath, runsPath, sandboxesPath, settingsPath, usageBillingPath, withSearch, workflowLibraryPath } from "@/lib/paths";
import { isWorkflowsAreaPath, workflowsTabForPath } from "@/lib/workflowsArea";

import { consoleNavEntries, type ConsoleNavEntry } from "../../../Forge/Console/consoleNavRegistry";
import { ANALYTICS_TAB_LABELS, ANALYTICS_TABS, authPath, consolePath, obsOverviewPath, securityPath, type SecurityView, OBS_NAV_LABELS, orgPath, type AnalyticsTab, type ConsolePage, type ConsoleTarget, type ObsNavView } from "../../../Forge/Console/paths";
import { settingsNavGroups } from "../../../Settings/SettingsNavigation";
import { isAt, isAtOrBelow } from "../navLocation";
import { isChatPage } from "../shellProject";
import { BrandMark } from "../../../icons/BrandMark";
import type { NavArea, NavCtx, NavGroup, NavHeading, NavItem, NavRow, NavSlot } from "./types";

const icon = (Icon: typeof Activity): ReactNode => <Icon className="size-4" aria-hidden="true" />;

// ── Helpers ─────────────────────────────────────────────────────────────────

const hasApp = (ctx: NavCtx) => !!ctx.app;

/** A console page of the focused app, in the app's env scope. */
const consoleHref = (page: ConsoleTarget) => (ctx: NavCtx) =>
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

/** The Environments page's previews view (`?show=previews`), a URL of its own. */
const showsPreviews = (ctx: NavCtx) => ctx.search.show === "previews";
const isPreviewEnv = (ctx: NavCtx) =>
  ctx.page?.kind === "environment" && (ctx.console?.envs ?? []).some((env) => env.preview && env.name === (ctx.page as { env: string }).env);

export const environments: NavRow = {
  ...consoleRow("environments", "Environments", { kind: "environments" }, icon(Layers), ["environments", "environment"]),
  match: (ctx) => (ctx.page?.kind === "environments" && !showsPreviews(ctx)) || (ctx.page?.kind === "environment" && !isPreviewEnv(ctx)),
};

export const previews: NavRow = {
  kind: "row",
  id: "previews",
  label: "Previews",
  href: (ctx) => (ctx.app ? withSearch(consolePath(ctx.app, { kind: "environments" }), { env: ctx.console?.env, show: "previews" }) : undefined),
  match: (ctx) => (ctx.page?.kind === "environments" && showsPreviews(ctx)) || isPreviewEnv(ctx),
  available: hasApp,
};

/**
 * Deployments ▾ (U6e, Vercel's word; owner): what runs and how it got there —
 * Services first, then the releases list, the long-lived environments, the
 * previews. Promote and roll back stay on those pages.
 */
export const deploymentsGroup: NavGroup = {
  kind: "group",
  id: "deployments",
  label: "Deployments",
  icon: icon(Rocket),
  available: hasApp,
  children: [services, releases, environments, previews],
};
export const secrets = consoleRow("secrets", "Secrets", { kind: "secrets" }, icon(KeyRound), ["secrets"]);
export const domains = consoleRow("domains", "Domains", { kind: "domains" }, icon(Globe), ["domains"]);
/**
 * Auth: who can sign in to the app (its managed identity) and how. Beside
 * Data and Storage; "Sign-in users" left Configure for it (U6d).
 */
export const auth: NavRow = {
  kind: "row",
  id: "auth",
  label: "Auth",
  icon: icon(UserRound),
  href: (ctx) => (ctx.app ? withSearch(authPath(ctx.app), { env: ctx.console?.env }) : undefined),
  match: pageIs("auth", "users"),
  available: hasApp,
};

/** Inbox: a top-level row with its count, under Overview (owner, 15:02Z); the one place it is. */
export const inbox: NavSlot = slot("inbox");
/** Its href and match, for the slot's row and the tests. */
export const inboxRow: NavRow = { kind: "row", id: "inbox", label: "Inbox", icon: icon(Inbox), href: () => inboxPath(), match: (ctx) => isAtOrBelow(ctx.pathname, inboxPath()) };
export const activity = consoleRow("activity", "Activity", { kind: "activity" }, icon(Activity), ["activity"]);
/** What this project's hosting costs (the org's is Usage & billing, in the footer). */
export const projectUsage = consoleRow("project-usage", "Usage", { kind: "usage" }, icon(Receipt), ["usage"]);

/** Configure ▾: Secrets, Domains, Usage, Activity (Environments moved to Deployments, U6e). */
export const configureGroup: NavGroup = {
  kind: "group",
  id: "configure",
  label: "Configure",
  icon: icon(Wrench),
  available: hasApp,
  // Activity (the audit log) stays its last row (owner): no page loses its nav entry.
  children: [secrets, domains, projectUsage, activity],
};

// ── Security (new, U6d) ─────────────────────────────────────────────────────

function securityRow(view: SecurityView, label: string): NavRow {
  return {
    kind: "row",
    id: `security-${view}`,
    label,
    href: (ctx) => (ctx.app ? withSearch(securityPath(ctx.app, view), { env: ctx.console?.env }) : undefined),
    // An advisory's own page is under Vulnerabilities.
    match: view === "vulnerabilities" ? pageIs("vulnerabilities", "vulnerability") : pageIs(view),
    available: hasApp,
  };
}

/** Security ▾: vulnerabilities (CVEs from each release's SBOM) and the SBOMs themselves. */
export const securityGroup: NavGroup = {
  kind: "group",
  id: "security",
  label: "Security",
  icon: icon(ShieldCheck),
  available: hasApp,
  children: [securityRow("vulnerabilities", "Vulnerabilities"), securityRow("sboms", "SBOMs")],
};

// ── Dashboards (one place only: bug 4) ──────────────────────────────────────

const hubKeys = (ctx: NavCtx) => ctx.console?.productAnalytics?.keys ?? [];

/** A dashboard page no row of its own lights: not an Analytics tab, not one of the app's listed dashboards. */
function isUnlistedDashboard(ctx: NavCtx): boolean {
  const page = ctx.page;
  if (page?.kind !== "dashboard" || hubKeys(ctx).includes(page.key)) return false;
  return !(ctx.console?.customDashboards ?? []).some((d) => d.dashKind === page.dashKind && d.key === page.key);
}

const dashboardsAvailable = (ctx: NavCtx) => hasApp(ctx) || ctx.mode !== "one";

/**
 * Dashboards ▾: the Dashboards page, the built-in Live state, and the app's
 * own dashboards (its repo's, saved ones). The only place dashboards are
 * listed; across several projects, the org-wide page.
 */
export const dashboards: NavGroup = {
  kind: "group",
  id: "dashboards",
  label: "Dashboards",
  icon: icon(LayoutGrid),
  available: dashboardsAvailable,
  children: (ctx) => [
    {
      kind: "row",
      id: "dashboards-all",
      label: "All dashboards",
      href: (c) => (c.mode !== "one" ? orgPath({ kind: "obs-view", view: "dashboards" }) : consoleHref({ kind: "dashboards" })(c)),
      match: (c) =>
        (c.page?.kind === "obs-view" && c.page.view === "dashboards") ||
        isUnlistedDashboard(c) ||
        (c.orgPage?.kind === "obs-view" && c.orgPage.view === "dashboards"),
    },
    ...(ctx.console?.customDashboards ?? []).map(
      (dashboard): NavRow => ({
        kind: "row",
        id: `dashboard-${dashboard.dashKind}-${dashboard.key}`,
        label: dashboard.title,
        href: consoleHref({ kind: "dashboard", dashKind: dashboard.dashKind, key: dashboard.key }),
        match: (c) => c.page?.kind === "dashboard" && c.page.dashKind === dashboard.dashKind && c.page.key === dashboard.key,
      }),
    ),
  ],
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
  href: consoleHref({ kind: "speed-insights" }),
  match: pageIs("speed-insights"),
  available: hasApp,
};

const alertsRow = obsRow("alerts", {
  badge: (ctx) => {
    const firing = ctx.console?.alertsFiring ?? 0;
    return firing > 0 ? { type: "count", value: firing, tone: "danger", label: "firing" } : undefined;
  },
});

/** Service graph, Metrics, Logs, RPC explorer, Profiles (where it has data). */
export const observabilityRows: readonly NavItem[] = [
  obsRow("graph"),
  obsRow("metrics"),
  obsRow("logs"),
  obsRow("rpc"),
  obsRow("profiles", { available: (ctx) => (hasApp(ctx) || ctx.mode !== "one") && ctx.console?.optionalViews?.profiles !== false }),
];
export const sessionsRow = obsRow("sessions");

/** "Performance & issues", a divider in the drill-in (U6e): its rows always show. */
export const performanceRows: readonly NavItem[] = [
  heading("performance-heading", "Performance & issues"),
  obsRow("errors"),
  alertsRow,
  obsRow("traces"),
  speedInsights,
  sessionsRow,
];

/** The Observability Overview: golden signals, top errors, slowest endpoints, firing alerts (OVERVIEW agent's page). */
const isObsOverview = (ctx: NavCtx) => ctx.page?.kind === "obs-overview" || ctx.orgPage?.kind === "observability";

export const obsOverview: NavRow = {
  kind: "row",
  id: "obs-overview",
  label: "Overview",
  href: (ctx) =>
    ctx.mode !== "one" ? orgPath({ kind: "observability" }) : ctx.app ? withSearch(obsOverviewPath(ctx.app), { env: ctx.console?.env }) : undefined,
  match: isObsOverview,
  available: (ctx) => hasApp(ctx) || ctx.mode !== "one",
};

/** The routes that are Observability: its Overview, every view but Dashboards (top-level), Speed insights, and the org-wide pages. */
export const isObservabilityRoute = (ctx: NavCtx): boolean =>
  isObsOverview(ctx) ||
  (ctx.page?.kind === "obs-view" && ctx.page.view !== "dashboards") ||
  ctx.page?.kind === "speed-insights" ||
  ctx.orgPage?.kind === "observability" ||
  (ctx.orgPage?.kind === "obs-view" && ctx.orgPage.view !== "dashboards");

/** Observability › — one of the two drill-ins; drilling in lands on its Overview. */
export const observabilityArea: NavArea = {
  kind: "area",
  id: "observability",
  label: "Observability",
  icon: icon(Gauge),
  match: isObservabilityRoute,
  href: obsOverview.href,
  available: (ctx) => hasApp(ctx) || ctx.mode !== "one",
  children: [obsOverview, ...observabilityRows, ...performanceRows],
};

// ── Analytics ───────────────────────────────────────────────────────────────

const analyticsTabOf = (ctx: NavCtx): AnalyticsTab | null =>
  ctx.page?.kind === "analytics" ? (ctx.page.tab ?? "overview") : ctx.page?.kind === "events" ? "events" : null;

/** One Analytics tab as a row (Product overview, Users, Product, Acquisition, Events & metrics). */
function analyticsTabRow(tab: AnalyticsTab): NavRow {
  return {
    kind: "row",
    id: `analytics-${tab}`,
    label: ANALYTICS_TAB_LABELS[tab],
    href: consoleHref({ kind: "analytics", tab }),
    match: (ctx) => analyticsTabOf(ctx) === tab,
    available: hasApp,
  };
}

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

const analyticsChildren = (ctx: NavCtx): readonly NavItem[] => [...ANALYTICS_TABS.map(analyticsTabRow), ...registeredRows(ctx)];

/** The routes that are Analytics: its page, Events & metrics, the built-in hub's dashboards, the registered pages. */
export const isAnalyticsRoute = (ctx: NavCtx): boolean =>
  analyticsTabOf(ctx) !== null ||
  (ctx.page?.kind === "dashboard" && hubKeys(ctx).includes(ctx.page.key)) ||
  !!registeredEntryOf(ctx);

/** Analytics ▾: the built-in dashboards' tabs, Events & metrics, then registered rows (Experiments). */
export const analyticsGroup: NavGroup = {
  kind: "group",
  id: "analytics",
  label: "Analytics",
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
  icon: icon(Workflow),
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


/** Connectors: integrations and MCP servers on one page (owner, U6d). */
export const connectors: NavRow = {
  kind: "row",
  id: "connectors",
  label: "Connectors",
  icon: icon(Plug),
  href: () => connectorsPath(),
  match: (ctx) => isAtOrBelow(ctx.pathname, connectorsPath()) || isAtOrBelow(ctx.pathname, connectorsPath("mcp")),
};

/** The routes that are Agents: the chats (home, a project's, one chat, New chat), Workflows, the sandboxes, Connectors. */
export const isAgentsRoute = (ctx: NavCtx): boolean =>
  isChatPage(ctx.pathname) || isWorkflowsAreaPath(ctx.pathname) || isAtOrBelow(ctx.pathname, sandboxesPath()) || connectors.match!(ctx);

/** Agents' rows; the chat list last, so in the drill-in it takes the rest of the column. */
// AI providers left Agents (U6e, owner): it lives in Settings only.
const agentsChildren: readonly NavItem[] = [slot("new-chat"), workflowsGroup, sandboxes, connectors, slot("chat-list")];

/** Agents › — one of the two drill-ins; its mark is Reliant's, to set it apart. */
export const agentsArea: NavArea = {
  kind: "area",
  id: "agents",
  label: "Agents",
  icon: <BrandMark className="size-4 shrink-0" />,
  match: isAgentsRoute,
  // Its first page: the chats (the project's, or every project's).
  href: (ctx) => (ctx.mode === "one" && ctx.projectId ? projectChatsPath(ctx.projectId) : homePath()),
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

const isSettingsRoute = (ctx: NavCtx) => isAtOrBelow(ctx.pathname, settingsPath());

/** Settings: a plain link (U6d, owner); bare /settings opens the section last visited. */
export const settings: NavRow = {
  kind: "row",
  id: "settings",
  label: "Settings",
  icon: icon(Settings),
  href: () => settingsPath(),
  match: isSettingsRoute,
  action: "update-button",
  testId: "sidebar-settings-button",
};

/** On a Settings page its sections take the nav column, as Vercel's do: a section page, not a drill-in row. */
export const settingsSections: NavArea = {
  kind: "area",
  id: "settings",
  label: "Settings",
  match: isSettingsRoute,
  children: settingsChildren,
};

export const footer: readonly NavItem[] = [usageBilling, settings];

export const externalMark = <ArrowUpRight className="size-3.5" aria-hidden="true" />;
