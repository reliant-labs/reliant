// Copyright (c) 2025 Reliant Labs

/**
 * THE CONSOLE'S URLS, and the mapping from every URL the console used to have
 * onto them.
 *
 *   /forge/p/:project                         Overview (project home: health, workloads)
 *   /forge/p/:project/workloads/:env/:name    one workload in one environment
 *   /forge/p/:project/observability           Observability Overview (U6e): golden signals per service,
 *                                             top errors, slowest endpoints, firing alerts, the map
 *   /forge/p/:project/releases                Releases
 *   /forge/p/:project/releases/:version       one release (its DORA strip is on Releases)
 *   /forge/p/:project/observability/:view     errors · sessions · logs · traces · metrics · profiles · dashboards · alerts
 *   /forge/p/:project/observability/dashboards/:kind/:key
 *                                             one dashboard: a release one (`release/<id>`)
 *                                             or a workspace one (`workspace/<slug>`)
 *   /forge/p/:project/storage                 Storage: the scoped environment's buckets, read-only (buckets only, U6e)
 *                                             (`bucket`, `prefix`, `object`, `token`: Storage/storageUrl-model.ts)
 *   /forge/p/:project/data                    Data: the database's home (U6e): each managed Postgres in scope,
 *                                             then the scoped environment's tables, read-only
 *                                             (`db`, `table`, `f`, `sort`, `cols`, `page`, `size`, `row`:
 *                                             Data/dataUrl-model.ts)
 *   /forge/p/:project/auth[/:tab]             Auth (U6d): users (who can sign in to the scoped environment,
 *                                             `user` and `q` as before) · sign-in (how the app signs people in)
 *   /forge/p/:project/users                   → auth (the old Users page; redirected, search kept)
 *   /forge/p/:project/environments            Environments (Configure)
 *   /forge/p/:project/environments/:env       one environment, as a configured object
 *   /forge/p/:project/secrets                 Secrets
 *   /forge/p/:project/domains                 Domains
 *   /forge/p/:project/activity                Activity (the audit log)
 *   /forge/p/:project/services                Services: every workload, the site and the database, live
 *                                             (`view=charts`: each workload's charts, U6e)
 *   /forge/p/:project/services/:name          one of them, across the environments in scope; a
 *                                             database's name redirects to Data (U6e)
 *   /forge/p/:project/usage                   Usage: what the app's hosting costs this month
 *   /forge/p/:project/events                  Product analytics › Events & metrics
 *   /forge/p/:project/analytics[/:tab]        Analytics: the built-in Product analytics dashboards as
 *                                             tabs (overview · users · product · acquisition) + events
 *   /forge/p/:project/site-insights[/:view]   Site insights: speed (web vitals per page, the default),
 *                                             seo · accessibility (`site`: one site's service.name)
 *   /forge/p/:project/speed-insights          → site-insights (its old name; redirected, search kept)
 *   /forge/p/:project/experiments             Experiments: the project's Forge Changes
 *                                             (remote config, rollouts, A/B tests)
 *   /forge/p/:project/experiments/:changeId   one change (`who` opens it on a "Who gets it?" answer)
 *   /forge/p/:project/security/vulnerabilities
 *                                             Security › Vulnerabilities: the findings of the
 *                                             scope's releases (`release`, `severity`, `fixable`,
 *                                             `kev`, `exempt`: Security/vulnModel.ts)
 *   /forge/p/:project/security/vulnerabilities/:advisory
 *                                             one advisory, across the project's releases
 *   /forge/p/:project/security/sboms          Security › SBOMs: each artifact's SBOM and scan (`release`)
 *   /forge/p/:project/tests                   Tests › Overview: coverage headline and trend, pass rate,
 *                                             flaky counts, the last run (`branch`)
 *   /forge/p/:project/tests/coverage          Tests › Coverage (`path`, `commit`, `lane`, `base`: a file
 *                                             path rides the search, never a segment)
 *   /forge/p/:project/tests/runs[/:run]       Tests › Runs, and one run (a ULID: URL-safe as it is)
 *   /forge/p/:project/tests/flaky[/:test]     Tests › Flaky tests, and one test (its id as base64url:
 *                                             encodeTestIdSegment)
 *
 * Retired, and redirected by the route tree: `/observability` (its home was
 * the Overview twice over) → the Overview; `/monitoring` (a placeholder) →
 * Alerts, where routing and silences live; `/settings` (the organization's
 * usage) → the app's Settings › Billing, which shows the same figures.
 *
 * The org pages sit beside them: the whole org, with app and environment as
 * filters (the `apps` and `envs` search keys) rather than a path segment.
 *
 *   /forge/observability                      org-wide Observability overview
 *   /forge/observability/:view                errors · sessions · logs · traces · metrics · profiles · dashboards · alerts
 *
 * The org's list of apps is not a console page: it is /apps (components/Apps),
 * and the bare /forge redirects there. `/forge?project=` and
 * `/forge?forgeProject=` (and the same on /forge/observability) still resolve
 * that one app's console: links written before the org pages (a control
 * plane's action URL, an old bookmark).
 *
 * `:project` is the FORGE project name (forge.yaml `name`), not a Reliant
 * project id. Everything the console shows — environments, releases,
 * telemetry, secrets — is filed by the control plane under that name, and it
 * is the only name the control plane can write into a link it sends
 * (a queued deploy's action URL). The Reliant project, which is what reaches
 * a daemon, is looked up from it (ConsoleLayout); a URL naming a forge
 * project no Reliant project here declares still opens, read from the control
 * plane alone.
 *
 * `?env=` is the ONE scope param (envScope.ts). Every list page
 * honours it; a page with an environment in its path pins that one instead.
 *
 * Nothing outside this module spells a console path: pages, the nav and the
 * redirects all build them here, so a URL change is one edit. The rest of the
 * app's paths are built the same way, in lib/paths, and both go through its
 * `withOrg` (the org segment of U3).
 */

import { settingsPath, withOrg, withSearch, type PathContext } from "@/lib/paths";
import { orgFreePath } from "@/lib/paths/org";

import { isDashboardPageKind, type DashboardPageKind } from "../Observability/Dashboards/dashboardModel";
import { OBS_SEARCH_KEYS, type ObsSearch, type ObsView } from "../Observability/obsSearch";
import { consoleNavEntryForSegment } from "./consoleNavRegistry";

/** The console's first segment, for reading a pathname back (consolePageOf, orgPageOf). */
export const FORGE_ROOT = "/forge";

export { withSearch };

/**
 * The organization's usage (its telemetry allowance): the app's Settings ›
 * Billing, which renders the same UsageView the console's retired Project
 * settings page did.
 */
export function orgUsagePath(): string {
  return settingsPath("billing");
}

/**
 * The Observability sub-pages that are nav entries, in nav order. The first is
 * where Observability opens (its nav row, its breadcrumb, an unknown view's
 * redirect): Dashboards, the page that answers "how is it doing". The service
 * graph is the least-visited view, so it is last.
 */
export const OBS_NAV_VIEWS = ["dashboards", "rpc", "errors", "sessions", "logs", "traces", "metrics", "profiles", "alerts", "graph"] as const;
export type ObsNavView = (typeof OBS_NAV_VIEWS)[number];

export const OBS_NAV_LABELS: Record<ObsNavView, string> = {
  graph: "Service graph",
  rpc: "RPC explorer",
  errors: "Errors",
  sessions: "Sessions",
  logs: "Logs",
  traces: "Traces",
  metrics: "Metrics",
  profiles: "Profiles",
  dashboards: "Dashboards",
  alerts: "Alerts",
};

export function isObsNavView(value: string | undefined): value is ObsNavView {
  return (OBS_NAV_VIEWS as readonly string[]).includes(value ?? "");
}

/** The pages of the org console (not inside one app): org-wide Observability. */
export type OrgPage = { kind: "observability" } | { kind: "obs-view"; view: ObsNavView };

/**
 * The path of an org page. Its org form (flag `web.org_routes` on) is
 * `/o/<org>/observability[/<view>]` (design §3.1): withOrg rewrites it
 * (lib/paths/org.ts, orgFormOf).
 */
export function orgPath(page: OrgPage, ctx?: PathContext): string {
  switch (page.kind) {
    case "observability":
      return withOrg(`${FORGE_ROOT}/observability`, ctx);
    case "obs-view":
      return withOrg(`${FORGE_ROOT}/observability/${page.view}`, ctx);
  }
}

/** Which org page `pathname` is, or null for any other path. The inverse of orgPath. */
export function orgPageOf(pathname: string): OrgPage | null {
  const parts = orgFreePath(pathname).split("/").filter(Boolean);
  if (parts[0] !== "forge" || parts[1] !== "observability") return null;
  if (parts.length === 2) return { kind: "observability" };
  return parts.length === 3 && isObsNavView(parts[2]) ? { kind: "obs-view", view: parts[2] } : null;
}

/** Every page of the console, as the nav and the breadcrumb name it. */
export type ConsolePage =
  | { kind: "overview" }
  /** Observability's own Overview (U6e): golden signals per service, top errors, slowest endpoints, firing alerts, the map (Observability/ObsOverview). */
  | { kind: "obs-overview" }
  /** One workload. Its environment is in the path: a workload is one per environment, and so are its actions. */
  | { kind: "workload"; env: string; name: string }
  | { kind: "releases" }
  | { kind: "release"; version: string }
  | { kind: "obs-view"; view: ObsNavView }
  /** One dashboard. Its kind is in the path: a release dashboard and a workspace one may share a key. */
  | { kind: "dashboard"; dashKind: DashboardPageKind; key: string }
  /** The storage browser: the scoped environment's buckets (Storage/StoragePage.tsx). */
  | { kind: "storage" }
  /** The data browser: the scoped environment's databases (Data/DataPage.tsx). */
  | { kind: "data" }
  /** Who can sign in to the scoped environment: its managed identity's users (Users/UsersPage.tsx). */
  | { kind: "users" }
  | { kind: "environments" }
  | { kind: "environment"; env: string }
  | { kind: "secrets" }
  | { kind: "domains" }
  | { kind: "activity" }
  /** Everything the app runs, live: services, workers, jobs, the site and the database (Services/ServicesPage.tsx). */
  | { kind: "services" }
  /** One of them, across the environments in scope (Services/ServiceDetailPage.tsx). */
  | { kind: "service"; name: string }
  /** What the app costs this month: hosted compute, database storage, CDN (Usage/ProjectUsagePage.tsx). */
  | { kind: "usage" }
  /** Product analytics › Events & metrics: the app's custom product events and OTel metrics. */
  | { kind: "events" }
  /**
   * Analytics (U6c): the built-in Product analytics dashboards as tabs, plus
   * Events & metrics (Analytics/AnalyticsPage.tsx). The tab is a path
   * segment; omitted, the first (Product overview).
   */
  | { kind: "analytics"; tab?: AnalyticsTab }
  /**
   * Site insights (Analytics/SiteInsightsPage.tsx): Speed (web vitals per
   * page, from the browser runtime's RUM spans), SEO and Accessibility. The
   * view is a path segment; omitted, Speed.
   */
  | { kind: "site-insights"; view?: SiteInsightsView }
  /** @deprecated Use site-insights. The legacy page keeps its redirecting URL. */
  | { kind: "speed-insights" }
  /** Auth (U6d): who can sign in, and how (Users/AuthPage.tsx). Omitted tab: users. */
  | { kind: "auth"; tab?: AuthTab }
  /** Tests › Overview: one branch's test health (Tests/TestsOverviewPage.tsx). */
  | { kind: "tests" }
  /** Tests › Coverage: the coverage tree and a file's lines; the path is in the search (Tests/TestsPlaceholderPages.tsx until W2). */
  | { kind: "tests-coverage" }
  /** Tests › Runs: recorded runs, newest first. */
  | { kind: "tests-runs" }
  /** One recorded run. */
  | { kind: "tests-run"; run: string }
  /** Tests › Flaky tests: by flake rate, with host health below. */
  | { kind: "tests-flaky" }
  /** One test's flake record (its TestKey id). */
  | { kind: "tests-flaky-test"; test: string }
  /** A page another module registered in the nav (consoleNavRegistry.ts): `/<segment>` under the app. */
  | { kind: "extension"; id: string; segment: string }
  /** Forge Changes: remote config, gradual rollouts and A/B tests (Experiments/ExperimentsPage.tsx). */
  | { kind: "experiments" }
  /** One change, by its control-plane id: a change is one per environment, so its id names both. */
  | { kind: "experiment"; changeId: string }
  /** Security › Vulnerabilities: the findings of the scope's releases (Security/VulnerabilitiesPage.tsx). */
  | { kind: "vulnerabilities" }
  /** One advisory in the project (Security/VulnerabilityPage.tsx). */
  | { kind: "vulnerability"; advisory: string }
  /** Security › SBOMs: each release artifact's SBOM and its scan (Security/SbomsPage.tsx). */
  | { kind: "sboms" };

/**
 * The Security section's path segments: `/forge/p/<app>/security/<page>`. The
 * nav's Security group points its rows at these pages through consolePath;
 * the segments are exported for a reader of a pathname.
 */
export const SECURITY_SEGMENT = "security";
export const SECURITY_PAGES = { vulnerabilities: "vulnerabilities", sboms: "sboms" } as const;
/** The Security pages, in nav order. */
export const SECURITY_PAGE_KINDS = ["vulnerabilities", "sboms"] as const satisfies readonly ConsolePage["kind"][];

/** Auth's tabs (U6d): the people (the old Users page, the default) and how they sign in. */
export const AUTH_TABS = ["users", "sign-in"] as const;
export type AuthTab = (typeof AUTH_TABS)[number];
export const AUTH_TAB_LABELS: Record<AuthTab, string> = { users: "Users", "sign-in": "Sign-in" };
export function isAuthTab(value: string | undefined): value is AuthTab {
  return (AUTH_TABS as readonly string[]).includes(value ?? "");
}

/** The Analytics page's tabs, in order: forge's four built-in Product analytics dashboards, then Events & metrics. */
export const ANALYTICS_TABS = ["overview", "users", "product", "acquisition", "events"] as const;
export type AnalyticsTab = (typeof ANALYTICS_TABS)[number];

export const ANALYTICS_TAB_LABELS: Record<AnalyticsTab, string> = {
  overview: "Product overview",
  users: "Users",
  product: "Product",
  acquisition: "Acquisition",
  events: "Events & metrics",
};

export function isAnalyticsTab(value: string | undefined): value is AnalyticsTab {
  return (ANALYTICS_TABS as readonly string[]).includes(value ?? "");
}

/** Site insights' views, in tab order: Speed (the default, the bare segment), then the two audits. */
export const SITE_INSIGHTS_VIEWS = ["speed", "seo", "accessibility"] as const;
export type SiteInsightsView = (typeof SITE_INSIGHTS_VIEWS)[number];
export const SITE_INSIGHTS_VIEW_LABELS: Record<SiteInsightsView, string> = { speed: "Speed", seo: "SEO", accessibility: "Accessibility" };
/** The path segment of Site insights, and of its old name (redirected to it). */
export const SITE_INSIGHTS_SEGMENT = "site-insights";
export const SPEED_INSIGHTS_SEGMENT = "speed-insights";

export function isSiteInsightsView(value: string | undefined): value is SiteInsightsView {
  return (SITE_INSIGHTS_VIEWS as readonly string[]).includes(value ?? "");
}


/**
 * The Tests section's path segments: `/forge/p/<app>/tests[/<page>]` (forge
 * docs/proposals/tests-coverage-flakes.md §3.1). Its Overview is the bare
 * segment; a coverage path rides the search (`?path=`), because a file path
 * would need segments consolePageOf does not split.
 */
export const TESTS_SEGMENT = "tests";
export const TESTS_PAGES = { coverage: "coverage", runs: "runs", flaky: "flaky" } as const;
/** The Tests pages, in nav order, and the page kinds each lights (a run is under Runs, a test under Flaky tests). */
export const TESTS_VIEWS = ["overview", "coverage", "runs", "flaky"] as const;
export type TestsView = (typeof TESTS_VIEWS)[number];
export const TESTS_VIEW_KINDS: Record<TestsView, readonly ConsolePage["kind"][]> = {
  overview: ["tests"],
  coverage: ["tests-coverage"],
  runs: ["tests-runs", "tests-run"],
  flaky: ["tests-flaky", "tests-flaky-test"],
};
export const TESTS_VIEW_LABELS: Record<TestsView, string> = { overview: "Overview", coverage: "Coverage", runs: "Runs", flaky: "Flaky tests" };
/** The page each Tests view is. */
export const TESTS_VIEW_PAGE: Record<TestsView, ConsolePage> = {
  overview: { kind: "tests" },
  coverage: { kind: "tests-coverage" },
  runs: { kind: "tests-runs" },
  flaky: { kind: "tests-flaky" },
};

/** Whether `page` is one of the Tests pages. */
export function isTestsPage(page: ConsolePage): boolean {
  return Object.values(TESTS_VIEW_KINDS).some((kinds) => kinds.includes(page.kind));
}

/**
 * What a link may name (U6c path-builder contract): every ConsolePage, plus
 * the names the nav configs use for pages that are an Observability view
 * underneath — `dashboards` (the top-level Dashboards page) and `metrics`
 * (the metrics explorer). Both resolve to the obs-view page they are, so
 * consolePageOf reads them back as that page.
 */
export type ConsoleTarget =
  | ConsolePage
  | { kind: "dashboards" }
  | { kind: "metrics" };

/** The page a target names. */
export function pageOfTarget(target: ConsoleTarget): ConsolePage {
  switch (target.kind) {
    case "dashboards":
      return { kind: "obs-view", view: "dashboards" };
    case "metrics":
      return { kind: "obs-view", view: "metrics" };
    default:
      return target;
  }
}

/**
 * One app's console. Its org form (flag `web.org_routes` on) is
 * `/o/<org>/apps/<app>` (design §3.1): withOrg rewrites it (lib/paths/org.ts,
 * orgFormOf).
 */
export function consoleBase(project: string, ctx?: PathContext): string {
  return withOrg(`${FORGE_ROOT}/p/${encodeURIComponent(project)}`, ctx);
}

/** The path of `target` in `project`. */
export function consolePath(project: string, target: ConsoleTarget, ctx?: PathContext): string {
  const base = consoleBase(project, ctx);
  const page = pageOfTarget(target);
  switch (page.kind) {
    case "overview":
      return base;
    case "obs-overview":
      return `${base}/observability`;
    case "workload":
      return `${base}/workloads/${encodeURIComponent(page.env)}/${encodeURIComponent(page.name)}`;
    case "releases":
      return `${base}/releases`;
    case "release":
      return `${base}/releases/${encodeURIComponent(page.version)}`;
    case "obs-view":
      return `${base}/observability/${page.view}`;
    case "dashboard":
      return `${base}/observability/dashboards/${page.dashKind}/${encodeURIComponent(page.key)}`;
    case "storage":
      return `${base}/storage`;
    case "data":
      return `${base}/data`;
    case "users":
      // The old Users page is Auth's first tab now (U6d); its route only redirects.
      return `${base}/auth`;
    case "auth":
      return !page.tab || page.tab === AUTH_TABS[0] ? `${base}/auth` : `${base}/auth/${page.tab}`;
    case "environments":
      return `${base}/environments`;
    case "environment":
      return `${base}/environments/${encodeURIComponent(page.env)}`;
    case "secrets":
      return `${base}/secrets`;
    case "domains":
      return `${base}/domains`;
    case "activity":
      return `${base}/activity`;
    case "services":
      return `${base}/services`;
    case "service":
      return `${base}/services/${encodeURIComponent(page.name)}`;
    case "usage":
      return `${base}/usage`;
    case "events":
      return `${base}/events`;
    case "analytics":
      return !page.tab || page.tab === ANALYTICS_TABS[0] ? `${base}/analytics` : `${base}/analytics/${page.tab}`;
    case "site-insights":
      return !page.view || page.view === SITE_INSIGHTS_VIEWS[0] ? `${base}/${SITE_INSIGHTS_SEGMENT}` : `${base}/${SITE_INSIGHTS_SEGMENT}/${page.view}`;
    case "speed-insights":
      return `${base}/${SPEED_INSIGHTS_SEGMENT}`;
    case "extension":
      return `${base}/${encodeURIComponent(page.segment)}`;
    case "experiments":
      return `${base}/experiments`;
    case "experiment":
      return `${base}/experiments/${encodeURIComponent(page.changeId)}`;
    case "vulnerabilities":
      return `${base}/${SECURITY_SEGMENT}/${SECURITY_PAGES.vulnerabilities}`;
    case "vulnerability":
      return `${base}/${SECURITY_SEGMENT}/${SECURITY_PAGES.vulnerabilities}/${encodeURIComponent(page.advisory)}`;
    case "sboms":
      return `${base}/${SECURITY_SEGMENT}/${SECURITY_PAGES.sboms}`;
    case "tests":
      return `${base}/${TESTS_SEGMENT}`;
    case "tests-coverage":
      return `${base}/${TESTS_SEGMENT}/${TESTS_PAGES.coverage}`;
    case "tests-runs":
      return `${base}/${TESTS_SEGMENT}/${TESTS_PAGES.runs}`;
    case "tests-run":
      return `${base}/${TESTS_SEGMENT}/${TESTS_PAGES.runs}/${encodeURIComponent(page.run)}`;
    case "tests-flaky":
      return `${base}/${TESTS_SEGMENT}/${TESTS_PAGES.flaky}`;
    case "tests-flaky-test":
      return `${base}/${TESTS_SEGMENT}/${TESTS_PAGES.flaky}/${encodeTestIdSegment(page.test)}`;
  }
}

// ── Named builders (U6c contract: the nav configs import these) ─────────────

/** The project's landing page: Home and Overview, merged. */
export const overviewPath = (app: string, ctx?: PathContext) => consolePath(app, { kind: "overview" }, ctx);
/** Observability's Overview (U6e): where drilling into Observability lands. */
export const obsOverviewPath = (app: string, ctx?: PathContext) => consolePath(app, { kind: "obs-overview" }, ctx);
export const servicesPath = (app: string, ctx?: PathContext) => consolePath(app, { kind: "services" }, ctx);
/** Storage: buckets (object storage) only (U6e). */
export const storagePath = (app: string, ctx?: PathContext) => consolePath(app, { kind: "storage" }, ctx);
/** Dashboards: built-in and custom, the one place they are listed. */
export const dashboardsPath = (app: string, ctx?: PathContext) => consolePath(app, { kind: "dashboards" }, ctx);
export const analyticsPath = (app: string, tab?: AnalyticsTab, ctx?: PathContext) => consolePath(app, { kind: "analytics", tab }, ctx);
/** Data: the database's home (U6e): its status, backups and metrics as reported, then its tables. */
export const dataPath = (app: string, ctx?: PathContext) => consolePath(app, { kind: "data" }, ctx);
/** Services' charts view (U6e): every workload's charts. */
export const servicesChartsPath = (app: string, ctx?: PathContext) => withSearch(servicesPath(app, ctx), { view: "charts" });
/** Site insights: `speed` (the default), `seo` or `accessibility`. */
export const siteInsightsPath = (app: string, view?: SiteInsightsView, ctx?: PathContext) => consolePath(app, { kind: "site-insights", view }, ctx);
/** @deprecated Speed insights is Site insights' Speed view: use siteInsightsPath. */
export const speedInsightsPath = (app: string, ctx?: PathContext) => consolePath(app, { kind: "speed-insights" }, ctx);
export const metricsPath = (app: string, ctx?: PathContext) => consolePath(app, { kind: "metrics" }, ctx);
export const obsViewPath = (app: string, view: ObsNavView, ctx?: PathContext) => consolePath(app, { kind: "obs-view", view }, ctx);
/** Auth (U6d): `users` (the old Users page; the default) or `sign-in`. */
export const authPath = (app: string, tab?: AuthTab, ctx?: PathContext) => consolePath(app, { kind: "auth", tab }, ctx);
export const projectUsagePath = (app: string, ctx?: PathContext) => consolePath(app, { kind: "usage" }, ctx);
/** Tests (the Tests nav group): `overview` (the default), `coverage`, `runs` or `flaky`. */
export const testsPath = (app: string, view: TestsView = "overview", ctx?: PathContext) => consolePath(app, TESTS_VIEW_PAGE[view], ctx);
/** One recorded run, under Tests › Runs. */
export const testRunPath = (app: string, runId: string, ctx?: PathContext) => consolePath(app, { kind: "tests-run", run: runId }, ctx);
/** One test's flake record, under Tests › Flaky tests, by its TestKey id ("go:<pkg>:TestRefund"). */
export const flakyTestPath = (app: string, testId: string, ctx?: PathContext) => consolePath(app, { kind: "tests-flaky-test", test: testId }, ctx);

// ── A test id in a path segment ─────────────────────────────────────────────
//
// A TestKey id ("go:github.com/acme/shop/internal/billing:TestRefund/partial")
// holds `/`, and percent-encoding it as `%2F` is not enough: many static
// hosts, proxies and CDNs reject an encoded slash in a path (a plain "Bad
// request") or decode it into a second segment, so a shared link or a reload
// broke while clicking through worked. The segment is therefore opaque:
// base64url of the id's UTF-8 bytes, unpadded, which is only [A-Za-z0-9_-].
// These two are the only place it is made or read — the path builder above
// and the route (consolePageOf, and the page's own `$test` param) both call them.

const BASE64URL_SEGMENT = /^[A-Za-z0-9_-]+$/;

/** The path segment for a test id: its UTF-8 bytes as unpadded base64url. */
export function encodeTestIdSegment(testId: string): string {
  let binary = "";
  for (const byte of new TextEncoder().encode(testId)) binary += String.fromCharCode(byte);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

/**
 * The test id a `$test` segment names, given the segment already
 * percent-decoded (as the router hands its params over). An encodeTestIdSegment
 * segment decodes to its id. Anything else is read as the id itself: the
 * percent-encoded form links carried before the segment went opaque. The two
 * cannot be confused, because a TestKey id always holds a `:`
 * ("<runner>:<package>:<name>") and base64url never does; a segment is also
 * read as base64url only when it is canonical (re-encodes to itself) and its
 * bytes are valid UTF-8.
 */
export function decodeTestIdSegment(segment: string): string {
  if (!BASE64URL_SEGMENT.test(segment) || segment.length % 4 === 1) return segment;
  try {
    const padded = segment.replace(/-/g, "+").replace(/_/g, "/") + "=".repeat((4 - (segment.length % 4)) % 4);
    const binary = atob(padded);
    const bytes = Uint8Array.from(binary, (ch) => ch.charCodeAt(0));
    const testId = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
    return encodeTestIdSegment(testId) === segment ? testId : segment;
  } catch {
    return segment;
  }
}

function safeDecode(segment: string): string {
  try {
    return decodeURIComponent(segment);
  } catch {
    return segment;
  }
}

/**
 * Which console page `pathname` is, and in which project — or null for a path
 * outside the console. The inverse of consolePath, for the nav's active entry
 * and the breadcrumb.
 */
export function consolePageOf(pathname: string): { project: string; page: ConsolePage } | null {
  const parts = orgFreePath(pathname).split("/").filter(Boolean);
  if (parts[0] !== "forge" || parts[1] !== "p" || !parts[2]) return null;
  const project = safeDecode(parts[2]);
  const [section, sub, dashKind, dashKey, ...rest] = parts.slice(3);
  const page = ((): ConsolePage | null => {
    switch (section) {
      case undefined:
        return { kind: "overview" };
      case "workloads":
        return sub && dashKind && !dashKey ? { kind: "workload", env: safeDecode(sub), name: safeDecode(dashKind) } : null;
      case "releases":
        return sub ? { kind: "release", version: safeDecode(sub) } : { kind: "releases" };
      case "observability":
        if (!sub) return { kind: "obs-overview" };
        if (sub === "dashboards" && dashKind) {
          return isDashboardPageKind(dashKind) && dashKey && rest.length === 0 ? { kind: "dashboard", dashKind, key: safeDecode(dashKey) } : null;
        }
        return isObsNavView(sub) && !dashKind ? { kind: "obs-view", view: sub } : null;
      case "environments":
        return sub ? { kind: "environment", env: safeDecode(sub) } : { kind: "environments" };
      case "services":
        if (!sub) return { kind: "services" };
        return dashKind ? null : { kind: "service", name: safeDecode(sub) };
      case "auth":
        if (!sub) return { kind: "auth" };
        return isAuthTab(sub) && !dashKind ? { kind: "auth", tab: sub } : null;
      case "analytics":
        if (!sub) return { kind: "analytics" };
        return isAnalyticsTab(sub) && !dashKind ? { kind: "analytics", tab: sub } : null;
      case "experiments":
        if (dashKind) return null;
        return sub ? { kind: "experiment", changeId: safeDecode(sub) } : { kind: "experiments" };
      case "storage":
      case "data":
      case "users":
      case "secrets":
      case "domains":
      case "activity":
      case "usage":
      case "events":
        return sub ? null : { kind: section };
      case SITE_INSIGHTS_SEGMENT:
        if (!sub) return { kind: "site-insights" };
        return isSiteInsightsView(sub) && !dashKind ? { kind: "site-insights", view: sub } : null;
      // Keep the deprecated target round-trip intact for hosts that still render
      // the legacy component. The main app's route redirects to Site insights.
      case SPEED_INSIGHTS_SEGMENT:
        return sub ? null : { kind: "speed-insights" };
      case SECURITY_SEGMENT:
        if (sub === SECURITY_PAGES.vulnerabilities && !dashKey) {
          return dashKind ? { kind: "vulnerability", advisory: safeDecode(dashKind) } : { kind: "vulnerabilities" };
        }
        return sub === SECURITY_PAGES.sboms && !dashKind ? { kind: "sboms" } : null;
      case TESTS_SEGMENT:
        if (!sub) return { kind: "tests" };
        if (dashKey) return null;
        if (sub === TESTS_PAGES.coverage) return dashKind ? null : { kind: "tests-coverage" };
        if (sub === TESTS_PAGES.runs) return dashKind ? { kind: "tests-run", run: safeDecode(dashKind) } : { kind: "tests-runs" };
        if (sub === TESTS_PAGES.flaky) return dashKind ? { kind: "tests-flaky-test", test: decodeTestIdSegment(safeDecode(dashKind)) } : { kind: "tests-flaky" };
        return null;
      default: {
        // A page registered in the nav, while it is registered.
        const entry = sub ? undefined : consoleNavEntryForSegment(section);
        return entry ? { kind: "extension", id: entry.id, segment: entry.segment } : null;
      }
    }
  })();
  return page ? { project, page } : null;
}

/** Whether a page has an environment in its path, so the top-bar scope does not apply to it. */
export function pinnedEnvOf(page: ConsolePage): string | null {
  return page.kind === "environment" || page.kind === "workload" ? page.env : null;
}

// ── The Observability panel's views, as console pages ──────────────────────

/**
 * Where an Observability view lives now. The panel still speaks in views
 * (obsSearch.ts) — its links say "open Logs", "open Traces" — and this turns
 * each into the page that hosts it: the nav entries under Observability. The
 * panel's own overview is the console's Overview, which carries the same
 * health strip. Workloads and Delivery are no views any more: the workloads
 * are the Overview's own tables (with a page per workload), and the DORA
 * figures are the Releases page's strip.
 */
export function pageOfObsView(view: ObsView | undefined): ConsolePage {
  switch (view) {
    case undefined:
    case "overview":
      return { kind: "overview" };
    default:
      return { kind: "obs-view", view };
  }
}

/** The panel view a console page hosts, when it hosts one. */
export function obsViewOfPage(page: ConsolePage): ObsView | null {
  switch (page.kind) {
    case "obs-view":
      // Metrics (the explorer, ../Observability/Metrics) and Dashboards are
      // console pages of their own, not panel views.
      return page.view === "metrics" || page.view === "dashboards" ? null : page.view;
    default:
      return null;
  }
}

/**
 * The `?env=` value for what the old `envs` filter said: one environment is
 * that environment; none (every environment) or several is All — the console
 * scope is one environment or all of them.
 */
export function envScopeOfEnvs(envs: readonly string[] | undefined): string {
  return envs && envs.length === 1 ? envs[0]! : "all";
}

/**
 * The Observability keys of `search`, without the three a project console does
 * not take: the view and the environment filter (moved into the path and the
 * `env` scope), and the org's app filter.
 */
export function carriedObsSearch(search: Record<string, unknown>): Record<string, unknown> {
  const kept: Record<string, unknown> = {};
  for (const key of OBS_SEARCH_KEYS) {
    if (key === "view" || key === "envs" || key === "apps") continue;
    if (search[key] !== undefined) kept[key] = search[key];
  }
  return kept;
}

/** The keys to carry between org pages: the Observability ones, plus the org's own filters, `envs` and `apps`. */
export function carriedOrgSearch(search: Record<string, unknown>): Record<string, unknown> {
  const kept = carriedObsSearch(search);
  if (search.envs !== undefined) kept.envs = search.envs;
  if (search.apps !== undefined) kept.apps = search.apps;
  return kept;
}

// ── The retired URLs ────────────────────────────────────────────────────────

/**
 * What a retired Observability URL carried: the current keys, plus the
 * retired `view=workloads` and the `workload` it had open.
 */
export type LegacyObsSearch = Omit<ObsSearch, "view"> & { view?: ObsView | "workloads" | "usage"; workload?: string };

/** What a legacy `/forge/env/:env` URL carried. */
export interface LegacyEnvSearch extends LegacyObsSearch {
  tab?: string;
  secret?: string;
  /** The org the link was written for (a queued deploy's action URL); the environment page asks before switching to it. */
  org?: string;
}

/**
 * The retired Workloads view → the Overview (where the workloads are now), or
 * the workload's own page when the URL names it and one environment.
 */
function legacyWorkloadsTarget(project: string, env: string, workload: string | undefined): string {
  if (workload && env !== "all") return withSearch(consolePath(project, { kind: "workload", env, name: workload }), { env });
  return withSearch(consolePath(project, { kind: "overview" }), { env, q: workload });
}

/**
 * `/forge/env/:env?tab=…` → its page now, with `?env=` set to that env so the
 * scope a notification or bookmark meant survives the move.
 *
 * The per-environment resource tabs become the project's resource pages
 * scoped to that environment; everything else about the
 * environment — overview, the local dev stack, domain bindings, the retired
 * tab names — is the environment's own page under Configure.
 *
 * `org` rides along to the environment's own page, the one page that honours
 * it (its OrgSwitchGate asks before acting in another org): the control
 * plane's queued-deploy link is `/forge/env/<env>?forgeProject=…&org=…`.
 */
export function legacyEnvTarget(project: string, env: string, search: LegacyEnvSearch): string {
  switch (search.tab) {
    case "releases":
      return withSearch(consolePath(project, { kind: "releases" }), { env });
    case "activity":
      return withSearch(consolePath(project, { kind: "activity" }), { env });
    case "secrets":
      return withSearch(consolePath(project, { kind: "secrets" }), { env, secret: search.secret });
    case "observability":
      if (search.view === "usage") return orgUsagePath();
      if (search.view === "workloads") return legacyWorkloadsTarget(project, env, search.workload);
      return withSearch(consolePath(project, pageOfObsView(search.view)), {
        env,
        ...carriedObsSearch(search as Record<string, unknown>),
      });
    case "domains":
      return withSearch(consolePath(project, { kind: "domains" }), { env });
    case "identity":
    case "running":
      return withSearch(consolePath(project, { kind: "environment", env }), { env, tab: search.tab, org: search.org });
    default:
      return withSearch(consolePath(project, { kind: "environment", env }), { env, org: search.org });
  }
}

/** `/forge/observability?view=…&envs=…` → the page hosting that view, scoped. */
export function legacyObservabilityTarget(project: string, search: LegacyObsSearch): string {
  if (search.view === "usage") return orgUsagePath();
  if (search.view === "workloads") return legacyWorkloadsTarget(project, envScopeOfEnvs(search.envs), search.workload);
  return withSearch(consolePath(project, pageOfObsView(search.view)), {
    env: envScopeOfEnvs(search.envs),
    ...carriedObsSearch(search as Record<string, unknown>),
  });
}
