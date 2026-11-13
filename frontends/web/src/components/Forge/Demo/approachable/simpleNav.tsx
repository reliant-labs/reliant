// Copyright (c) 2026 Reliant Labs

/**
 * The simple view's nav, breadcrumbs, titles and pages (viewMode.ts): what
 * DemoNavExtras hands the console through ConsoleNavExtras when the reader is
 * in the simple view. Plain names, each with the console's term beside it.
 *
 *   Home               the story-led landing (?section=home)
 *   What's happening   the Overview, with under it, always open:
 *     Things that broke     (errors)
 *     Slow requests         (traces)    the slowest request's timeline inline
 *     Recordings            (sessions)  replays with play buttons
 *     Alerts
 *   Releases           How releases work (?section=releases), Release history
 *   People             the Product analytics hub
 *   Agents             the "put an agent on it" workflows (?section=agents)
 *   ─
 *   How it was built   the recorded forge run
 *
 * Traces and recordings are first-class here: their list pages are simple
 * pages, and one trace, one recording or one problem opens with a plain lead
 * (or, for a recording, a simple page that plays it) over the real thing.
 */

import type { ReactNode } from "react";
import { Bot, Gauge, Hammer, Home, Rocket, Users } from "lucide-react";

import type { SidebarNavItem } from "@/components/forge-ui/sidebar_layout";
import ToggleSwitch from "@/components/forge-ui/toggle_switch";

import type { ConsolePage } from "../../Console/paths";
import { demoSectionHref, DEMO_SECTION_AGENTS, DEMO_SECTION_BUILT, DEMO_SECTION_HOME, DEMO_SECTION_RELEASES, type DemoSection } from "../demoPath";
import { PageAgentStrip } from "./AgentCards";
import { DEMO_LINKS, PEOPLE_DASHBOARD_KEYS, SERVICE_DASHBOARD_KEY } from "./demoLinks";
import { ServiceLead, SimpleBroke, SimpleCheckout, SimpleHappening, SimplePeople, SimpleReleases } from "./SimplePages";
import { IssueLead, SimpleAlerts, SimpleRecording, SimpleRecordings, SimpleSlow, TraceLead } from "./SimpleTelemetry";
import { setDemoViewMode, useDemoViewMode } from "./viewMode";

const icon = (Icon: typeof Home) => <Icon className="h-4 w-4" aria-hidden="true" />;

/** The simple view's words for the console's jargon, and the jargon beside them. */
export const SIMPLE_LABELS = {
  happening: "What's happening",
  errors: "Things that broke",
  traces: "Slow requests",
  sessions: "Recordings",
  alerts: "Alerts",
  people: "People",
} as const;

const TERMS: Partial<Record<keyof typeof SIMPLE_LABELS, string>> = { errors: "errors", traces: "traces", sessions: "sessions" };

/** "(traces)", muted, after a nav row's plain name. */
const term = (key: keyof typeof SIMPLE_LABELS) => (TERMS[key] ? <span className="text-xs text-ink-subtle">({TERMS[key]})</span> : undefined);

type Search = Record<string, unknown>;

const isPeople = (page: ConsolePage) => page.kind === "dashboard" && PEOPLE_DASHBOARD_KEYS.includes(page.key);
const isObs = (page: ConsolePage, view: string) => page.kind === "obs-view" && page.view === view;
const SIMPLE_VIEWS = ["errors", "traces", "sessions", "alerts"] as const;
type SimpleView = (typeof SIMPLE_VIEWS)[number];
const simpleViewOf = (page: ConsolePage): SimpleView | null =>
  page.kind === "obs-view" && (SIMPLE_VIEWS as readonly string[]).includes(page.view) ? (page.view as SimpleView) : null;
const opened = (search: Search, key: string) => (typeof search[key] === "string" && search[key] ? (search[key] as string) : null);

export function simpleNav(page: ConsolePage, section: DemoSection | undefined, alertsFiring: number): { items: SidebarNavItem[]; footer: SidebarNavItem[] } {
  const onConsole = !section;
  const view = onConsole ? simpleViewOf(page) : null;
  const happening = onConsole && (page.kind === "overview" || page.kind === "workload" || view !== null);
  const releases = section === DEMO_SECTION_RELEASES || (onConsole && (page.kind === "releases" || page.kind === "release"));
  const child = (key: SimpleView, href: string): SidebarNavItem => ({ label: SIMPLE_LABELS[key], href, active: view === key, trailing: term(key) });
  return {
    items: [
      { label: "Home", href: demoSectionHref(DEMO_SECTION_HOME), icon: icon(Home), active: section === DEMO_SECTION_HOME },
      {
        label: SIMPLE_LABELS.happening,
        href: DEMO_LINKS.happening,
        icon: icon(Gauge),
        active: happening && view === null,
        defaultExpanded: true,
        children: [
          child("errors", DEMO_LINKS.errors),
          child("traces", DEMO_LINKS.traces),
          child("sessions", DEMO_LINKS.sessions),
          {
            ...child("alerts", DEMO_LINKS.alerts),
            badge: alertsFiring > 0 ? { type: "count" as const, value: alertsFiring, tone: "danger" as const, label: "firing" } : undefined,
          },
        ],
      },
      {
        label: "Releases",
        href: DEMO_LINKS.releases,
        icon: icon(Rocket),
        active: releases && page.kind !== "release" && section !== DEMO_SECTION_RELEASES,
        defaultExpanded: true,
        children: [
          { label: "How releases work", href: demoSectionHref(DEMO_SECTION_RELEASES), active: section === DEMO_SECTION_RELEASES },
          { label: "Release history", href: DEMO_LINKS.releases, active: onConsole && page.kind === "release" },
        ],
      },
      { label: SIMPLE_LABELS.people, href: DEMO_LINKS.people, icon: icon(Users), active: onConsole && isPeople(page), trailing: <span className="text-xs text-ink-subtle">(analytics)</span> },
      { label: "Agents", href: demoSectionHref(DEMO_SECTION_AGENTS), icon: icon(Bot), active: section === DEMO_SECTION_AGENTS },
    ],
    footer: [{ label: "How it was built", href: demoSectionHref(DEMO_SECTION_BUILT), icon: icon(Hammer), active: section === DEMO_SECTION_BUILT }],
  };
}

const SECTION_LABELS: Record<DemoSection, string> = {
  home: "Home",
  releases: "How releases work",
  agents: "Agents",
  built: "How it was built",
};

const withTerm = (key: keyof typeof SIMPLE_LABELS) => (TERMS[key] ? `${SIMPLE_LABELS[key]} (${TERMS[key]})` : SIMPLE_LABELS[key]);

export function simpleCrumbs(page: ConsolePage, section: DemoSection | undefined): { label: string; href?: string }[] | null {
  if (section) return [{ label: SECTION_LABELS[section] }];
  if (page.kind === "overview") return [{ label: SIMPLE_LABELS.happening }];
  const view = simpleViewOf(page);
  if (view) return [{ label: SIMPLE_LABELS.happening, href: DEMO_LINKS.happening }, { label: withTerm(view) }];
  if (isPeople(page)) return [{ label: SIMPLE_LABELS.people }];
  if (page.kind === "dashboard" && (page.key === "checkout" || page.key === SERVICE_DASHBOARD_KEY)) {
    return [{ label: SIMPLE_LABELS.happening, href: DEMO_LINKS.happening }, { label: page.key === "checkout" ? "Checkout" : "Orders" }];
  }
  return null;
}

/** The console's header over a page the simple view only leads: it names what follows as the technical view. */
export function simpleTitle(page: ConsolePage, search: Search): string | null {
  const view = simpleViewOf(page);
  if (!view) return null;
  if ((view === "traces" && opened(search, "trace")) || (view === "errors" && opened(search, "issue"))) return "As an engineer sees it";
  return withTerm(view);
}

/** Whether the simple view draws its own page for this URL (which then explains itself: no page tip). */
export function isSimplePage(page: ConsolePage, search: Search): boolean {
  return simpleKind(page, search) !== null;
}

function simpleKind(page: ConsolePage, search: Search) {
  if (opened(search, "tour")) return null;
  if (isObs(page, "sessions") && opened(search, "session")) return "recording";
  if (["issue", "trace", "span", "rule", "session"].some((key) => opened(search, key))) return null;
  if (page.kind === "overview") return "happening";
  if (isObs(page, "errors")) return "broke";
  if (isObs(page, "traces")) return "slow";
  if (isObs(page, "sessions")) return "recordings";
  if (isObs(page, "alerts")) return "alerts";
  if (page.kind === "releases") return "releases";
  if (isPeople(page)) return "people";
  if (page.kind === "dashboard" && page.key === "checkout") return "checkout";
  return null;
}

/**
 * The simple pages that stand in for the console's: every list page, and one
 * recording (which plays at once). A link to one issue, one trace or one
 * alert rule still opens the real page, led by a plain summary (simpleLead).
 */
export function simplePage(page: ConsolePage, search: Search): ReactNode | null {
  switch (simpleKind(page, search)) {
    case "happening":
      return <SimpleHappening />;
    case "broke":
      return <SimpleBroke />;
    case "slow":
      return <SimpleSlow />;
    case "recordings":
      return <SimpleRecordings />;
    case "recording":
      return <SimpleRecording sessionId={opened(search, "session")!} atMs={typeof search.t === "number" ? search.t : undefined} />;
    case "alerts":
      return <SimpleAlerts />;
    case "releases":
      return <SimpleReleases />;
    case "people":
      return <SimplePeople />;
    case "checkout":
      return <SimpleCheckout />;
    default:
      return null;
  }
}

/** One trace, one problem, one service's dashboard: a plain lead over the console's own page. */
export function simpleLead(page: ConsolePage, search: Search, children: ReactNode): ReactNode | null {
  if (opened(search, "tour")) return null;
  const trace = isObs(page, "traces") ? opened(search, "trace") : null;
  const issue = isObs(page, "errors") ? opened(search, "issue") : null;
  const lead = trace ? <TraceLead traceId={trace} /> : issue ? <IssueLead issueId={issue} /> : page.kind === "dashboard" && page.key === SERVICE_DASHBOARD_KEY ? <ServiceLead /> : null;
  if (!lead) return null;
  return (
    <div className="space-y-6">
      {lead}
      {children}
    </div>
  );
}

/**
 * Errors, Alerts, Slow requests and Releases as the console draws them, with
 * the page's own "put an agent on it" card on top, in place of the small
 * "How an agent helps" disclosure. Null for every other page.
 */
export function withAgentStrip(page: ConsolePage, children: ReactNode): ReactNode | null {
  const strip =
    isObs(page, "errors") ? <PageAgentStrip id="fix-errors" lead="An agent can read a problem like these, find the change that caused it, and draft the fix." />
    : isObs(page, "alerts") ? <PageAgentStrip id="watch-alerts" lead="When an alert fires at 2am, an agent can look first and have a fix waiting by morning." />
    : isObs(page, "traces") ? <PageAgentStrip id="slow-pages" lead="Hand an agent one slow request and it finds where the time goes." />
    : page.kind === "releases" || page.kind === "release" ? <PageAgentStrip id="release-guard" lead="An agent can check every release against the hour before, and move customers back if it's bad." />
    : null;
  if (!strip) return null;
  return (
    <div className="space-y-6">
      {strip}
      {children}
    </div>
  );
}

/** The nav's switch between the simple view and the full console. */
export function ViewModeSwitch() {
  const mode = useDemoViewMode();
  return (
    <div className="border-t border-border px-3 pb-1 pt-3" data-testid="demo-view-switch">
      <ToggleSwitch
        checked={mode === "advanced"}
        onChange={(advanced) => setDemoViewMode(advanced ? "advanced" : "simple")}
        label="Advanced view"
        description={mode === "advanced" ? "The full console, every page" : "Every page, filter and number"}
        size="lg"
      />
    </div>
  );
}
