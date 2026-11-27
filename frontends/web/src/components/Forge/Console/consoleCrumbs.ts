// Copyright (c) 2026 Reliant Labs

/**
 * The words around a console page: its breadcrumb trail (ConsoleFrame's bar)
 * and the plain line under its title (PAGE_SUBTITLES). Pure, so both are
 * testable without rendering.
 *
 *  - consoleCrumbs: the console's own trail, after the app's name.
 *  - groupedCrumbs: the led console's rework of it (ledConsole.tsx), naming
 *    the group of the shell's nav a page sits in (Layout/sidebar/navConfig/
 *    items.tsx): "Performance & issues › Errors", "Deployments › Releases".
 */

import { shortVersion } from "../Releases/releaseModel";
import { testIdParts } from "../Tests/flakyModel";
import type { ProductAnalyticsHub } from "./consoleNavModel";
import { consoleNavEntries } from "./consoleNavRegistry";
import { ANALYTICS_TAB_LABELS, AUTH_TAB_LABELS, consolePath, OBS_NAV_LABELS, OBS_NAV_VIEWS, SITE_INSIGHTS_VIEW_LABELS, withSearch, type ConsolePage, type ObsNavView } from "./paths";

export type Crumb = { label: string; href?: string };

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
  "site-insights": "How fast, findable and accessible each page of your site is for real visitors",
  storage: "Your app's object storage buckets: files, uploads and assets",
  experiments: "Turn things on for some people, and compare versions",
};

/** The breadcrumb trail for `page`, after the project. The last crumb is the page itself. */
export function consoleCrumbs(project: string, page: ConsolePage, env: string, dashboardTitle?: string): Crumb[] {
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
    case "obs-overview":
      return [{ label: "Observability" }, { label: "Overview" }];
    case "obs-view":
      return [{ label: "Observability", href: href({ kind: "obs-view", view: OBS_NAV_VIEWS[0] }) }, { label: OBS_NAV_LABELS[page.view] }];
    case "dashboard":
      return [
        { label: "Observability", href: href({ kind: "obs-view", view: OBS_NAV_VIEWS[0] }) },
        { label: "Dashboards", href: href({ kind: "obs-view", view: "dashboards" }) },
        { label: dashboardTitle || page.key },
      ];
    case "storage":
      return [{ label: "Storage" }];
    case "data":
      return [{ label: "Data" }];
    case "users":
      return [{ label: "Users" }];
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
    case "services":
      return [{ label: "Services" }];
    case "service":
      return [{ label: "Services", href: href({ kind: "services" }) }, { label: page.name }];
    case "usage":
      return [{ label: "Usage" }];
    case "events":
      return [{ label: "Product analytics" }, { label: "Events & metrics" }];
    case "analytics":
      return [{ label: "Analytics", href: href({ kind: "analytics" }) }, { label: ANALYTICS_TAB_LABELS[page.tab ?? "overview"] }];
    case "speed-insights":
      return consoleCrumbs(project, { kind: "site-insights" }, env, dashboardTitle);
    case "site-insights":
      return [{ label: "Observability" }, { label: "Site insights", href: href({ kind: "site-insights" }) }, { label: SITE_INSIGHTS_VIEW_LABELS[page.view ?? "speed"] }];
    case "extension":
      return [{ label: consoleNavEntries().find((entry) => entry.id === page.id)?.label ?? page.segment }];
    case "tests":
      return [{ label: "Tests" }, { label: "Overview" }];
    case "tests-coverage":
      return [{ label: "Tests", href: href({ kind: "tests" }) }, { label: "Coverage" }];
    case "tests-runs":
      return [{ label: "Tests", href: href({ kind: "tests" }) }, { label: "Runs" }];
    case "tests-run":
      return [{ label: "Tests", href: href({ kind: "tests" }) }, { label: "Runs", href: href({ kind: "tests-runs" }) }, { label: page.run }];
    case "tests-flaky":
      return [{ label: "Tests", href: href({ kind: "tests" }) }, { label: "Flaky tests" }];
    case "tests-flaky-test":
      return [{ label: "Tests", href: href({ kind: "tests" }) }, { label: "Flaky tests", href: href({ kind: "tests-flaky" }) }, { label: testIdParts(page.test).name }];
    case "auth":
      return [{ label: "Auth", href: href({ kind: "auth" }) }, { label: AUTH_TAB_LABELS[page.tab ?? "users"] }];
    case "experiments":
      return [{ label: "Experiments" }];
    case "experiment":
      return [{ label: "Experiments", href: href({ kind: "experiments" }) }, { label: "Change" }];
    case "vulnerabilities":
      return [{ label: "Security" }, { label: "Vulnerabilities" }];
    case "vulnerability":
      return [{ label: "Security" }, { label: "Vulnerabilities", href: href({ kind: "vulnerabilities" }) }, { label: page.advisory }];
    case "sboms":
      return [{ label: "Security" }, { label: "SBOMs" }];
  }
}

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

/**
 * The led console's breadcrumb: a view under its group ("Issues ›
 * Errors", "More tools › Logs") instead of "Observability". Null keeps the
 * console's own trail (`own`).
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
