// Copyright (c) 2026 Reliant Labs

/**
 * THE LED CONSOLE: the console's same pages and data, each leading with what
 * a reader needs first ("62" on the density scale the public demo was tuned
 * on, research/2026-11-10-demo-errors/density-scale-brief.md):
 *
 *  - the nav grouped by what a reader is after, the technical names kept,
 *    with a plain subtitle under each page's title (groupedNav.tsx);
 *  - Errors, Traces and Sessions lead with one sentence and their one item
 *    worth opening (the newest issue, the slowest request with its timeline,
 *    the replay worth watching), then compact rows with "Show all"; a filter
 *    (or "All filters") opens the console's full list (Observability/Leads);
 *  - one issue, trace or session leads with "What happened" and its key
 *    facts, the less-used panels folded (Observability/detailDensity);
 *  - charts draw at most two series, under a headline figure
 *    (Observability/charts/chartDensity);
 *  - Overview and Releases lead with one sentence, and drop the Users card,
 *    the per-environment columns and the deploy log (`led`).
 *
 * Every sentence is built from the page's own data (Observability/Leads/
 * leadStory.ts), so it reads the same for any project. A host adds what is
 * its own through LedConsoleHost: the demo its Home and "How it was built"
 * rows, its agent cards and its sample shop's plain explanations.
 */

import { useMemo, type ReactNode } from "react";
import { useRouterState } from "@tanstack/react-router";

import type { SidebarNavItem } from "@/components/forge-ui/sidebar_layout";
import { useObservabilityOrg } from "@/hooks/observability-queries";
import type { Issue } from "@/services/controlPlane/observability/issuesTypes";
import type { ObsScope } from "@/services/controlPlane/observability/types";

import { ChartDensityProvider, type ChartDensity } from "../Observability/charts/chartDensity";
import { DetailDensityProvider, type DetailDensity } from "../Observability/detailDensity";
import { IssueLead, SessionLead, TraceLead } from "../Observability/Leads/DetailLeads";
import { ErrorsLead, SessionsLead, TracesLead, type LeadHref } from "../Observability/Leads/ListLeads";
import { useForgeConsole } from "./consoleContext";
import { ConsoleNavExtrasProvider, type ConsoleNavExtras } from "./consoleNavExtras";
import { ENV_ALL } from "./envScope";
import { buildGroupedConsoleNav, groupedCrumbs, PAGE_SUBTITLES, type ProductAnalyticsHub } from "./groupedNav";
import type { ObsNavView } from "./paths";

type Crumb = { label: string; href?: string };

/** What a host adds to the led console. Everything is optional: the product needs none of it. */
export interface LedConsoleHost {
  /** Rows above Overview (the demo's Home). */
  navLead?: SidebarNavItem[];
  /** Rows above Activity in the footer (the demo's "How it was built"). */
  navFooter?: SidebarNavItem[];
  /** A page of the host's own is open (not a console page): its breadcrumb; no console row is lit, and no lead is drawn. */
  hostPage?: { crumbs: Crumb[] } | null;
  /** Where Product analytics leads; omitted, the nav has no such row. */
  productAnalytics?: ProductAnalyticsHub;
  /** What the host adds after a page's lead, by page key ("errors", "traces", "alerts", "releases"…): an agent card on the demo. */
  aside?: (key: string) => ReactNode;
  /** A plain explanation of an issue, when the host has one. */
  explainIssue?: (issue: Issue) => string | undefined;
  /** "How releases work", from the Releases sentence. */
  releasesExplainer?: { href: string; label: string };
  envOrder?: readonly string[];
  drawerAppendix?: ReactNode;
}

const CHART_DENSITY: ChartDensity = { maxSeries: 2, headline: true };

const DETAIL_DENSITY: DetailDensity = {
  folded: ["issue-discussion", "issue-events", "issue-release", "issue-versions", "issue-related", "issue-history", "trace-toolbar", "trace-related", "session-details"],
  inlineTriage: true,
};

/** Search keys that open one item: its page leads with "What happened", not the list's subtitle. */
const DETAIL_KEYS = ["issue", "trace", "session"] as const;

/** The search keys that make a list the console's own (a filter was chosen, or "All filters"). */
const LIST_KEYS: Record<"errors" | "traces" | "sessions", readonly string[]> = {
  errors: ["q", "istatus", "platform", "isort", "assignee", "service", "user", "browser", "os", "build"],
  traces: ["q", "service", "errors", "minDuration", "lat", "f", "groupBy", "user", "rsession", "browser", "os", "fe", "be", "range"],
  sessions: ["q", "service", "errors", "user", "browser", "os", "fe", "cursor", "range"],
};

const isLedList = (view: ObsNavView): view is keyof typeof LIST_KEYS => view in LIST_KEYS;
const filtered = (search: Record<string, unknown>, view: keyof typeof LIST_KEYS) => LIST_KEYS[view].some((key) => search[key] !== undefined && search[key] !== "");
const detailOpen = (search: Record<string, unknown>) => DETAIL_KEYS.some((key) => !!search[key]);

/**
 * The scope a lead reads: the console's project and the environment in
 * scope. Null while it is unknown, or when the environment in scope has no
 * telemetry filed under it (an empty id list would read every environment);
 * the console's own page then says why.
 */
function useLedScope(): { scope: ObsScope; env: string | null } | null {
  const { forgeProject, env, roster } = useForgeConsole();
  const org = useObservabilityOrg();
  const envId = env === ENV_ALL ? null : (roster.envs.find((candidate) => candidate.name === env)?.live?.id ?? "");
  return useMemo(() => {
    if (!org.orgId || envId === "") return null;
    return { scope: { orgId: org.orgId, projectId: forgeProject, envIds: envId ? [envId] : [], filters: [] }, env: envId ? env : null };
  }, [org.orgId, forgeProject, envId, env]);
}

function useLeadHref(): LeadHref {
  const { href } = useForgeConsole();
  return (view, search) => href({ kind: "obs-view", view }, search);
}

/** An Errors, Traces or Sessions list as a led page, or the console's own (`own`) where the lead cannot read its scope. */
function LedListPage({ view, own, host }: { view: "errors" | "traces" | "sessions"; own: ReactNode; host: LedConsoleHost }) {
  const led = useLedScope();
  const hrefOf = useLeadHref();
  if (!led) return <>{own}</>;
  const props = { ...led, hrefOf, subtitle: PAGE_SUBTITLES[view], aside: host.aside?.(view) };
  if (view === "errors") return <ErrorsLead {...props} explainIssue={host.explainIssue} />;
  if (view === "traces") return <TracesLead {...props} />;
  return <SessionsLead {...props} />;
}

/** One open item's "What happened", above the console's detail page. */
function LedDetailLead({ view, search, host }: { view: string; search: Record<string, unknown>; host: LedConsoleHost }) {
  const led = useLedScope();
  const range = typeof search.range === "string" ? search.range : undefined;
  if (view === "errors" && typeof search.issue === "string") return <IssueLead issueId={search.issue} range={range} explainIssue={host.explainIssue} />;
  if (!led) return null;
  if (view === "traces" && typeof search.trace === "string") return <TraceLead scope={led.scope} traceId={search.trace} range={range} />;
  if (view === "sessions" && typeof search.session === "string") return <SessionLead scope={led.scope} sessionId={search.session} />;
  return null;
}

/** The led console's additions, as ConsoleNavExtras (pure, for tests). */
export function ledNavExtras(host: LedConsoleHost, search: Record<string, unknown>): ConsoleNavExtras {
  const { hostPage, productAnalytics } = host;
  return {
    envOrder: host.envOrder,
    drawerAppendix: host.drawerAppendix,
    nav: (input) => {
      const grouped = buildGroupedConsoleNav({ ...input, productAnalytics, hostPageActive: !!hostPage });
      return { items: [...(host.navLead ?? []), ...grouped.items], footer: [...(host.navFooter ?? []), ...grouped.footer] };
    },
    crumbs: (page, own) => (hostPage ? hostPage.crumbs : groupedCrumbs(page, own, productAnalytics)),
    subtitle: (key) => (hostPage || detailOpen(search) ? null : (PAGE_SUBTITLES[key] ?? null)),
    intro: (key) => {
      if (hostPage) return null;
      if (detailOpen(search)) return <LedDetailLead view={key} search={search} host={host} />;
      return host.aside?.(key) ?? null;
    },
    listPage: (view, own) => (hostPage || !isLedList(view) || detailOpen(search) || filtered(search, view) ? null : <LedListPage view={view} own={own} host={host} />),
    led: true,
    releasesExplainer: host.releasesExplainer,
  };
}

/**
 * The led console's spacing on the console's own chrome: a shorter gap above
 * each page, and the compact page heading a step smaller. Scoped to this
 * wrapper (display: contents), so it reaches nothing outside it.
 */
const LED_SPACING = "contents md:[&_main]:pt-4 [&_[data-density=compact]_h1]:text-base";

const NO_HOST: LedConsoleHost = {};

export function LedConsoleProvider({ host = NO_HOST, children }: { host?: LedConsoleHost; children: ReactNode }) {
  const search = useRouterState({ select: (s) => s.location.search }) as Record<string, unknown>;
  const value = useMemo(() => ledNavExtras(host, search), [host, search]);
  return (
    <ConsoleNavExtrasProvider value={value}>
      <ChartDensityProvider value={CHART_DENSITY}>
        <DetailDensityProvider value={DETAIL_DENSITY}>
          <div className={LED_SPACING}>{children}</div>
        </DetailDensityProvider>
      </ChartDensityProvider>
    </ConsoleNavExtrasProvider>
  );
}
