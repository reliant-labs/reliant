// Copyright (c) 2026 Reliant Labs

/**
 * The simple view's nav, breadcrumbs, titles and pages (viewMode.ts): what
 * DemoNavExtras hands the console through ConsoleNavExtras when the reader is
 * in the simple view.
 *
 *   Home               the story-led landing (?section=home)
 *   What's happening   the Overview, with Things that broke, Slow requests
 *                      and Alerts under it
 *   Releases           How releases work (?section=releases), Release history
 *   People             the Product analytics hub
 *   Agents             the "put an agent on it" workflows (?section=agents)
 *   ─
 *   How it was built   the recorded forge run
 */

import type { ReactNode } from "react";
import { Bot, Gauge, Hammer, Home, Rocket, Users } from "lucide-react";

import type { SidebarNavItem } from "@/components/forge-ui/sidebar_layout";
import ToggleSwitch from "@/components/forge-ui/toggle_switch";

import type { ConsolePage } from "../../Console/paths";
import { demoSectionHref, DEMO_SECTION_AGENTS, DEMO_SECTION_BUILT, DEMO_SECTION_HOME, DEMO_SECTION_RELEASES, type DemoSection } from "../demoPath";
import { PageAgentStrip } from "./AgentCards";
import { DEMO_LINKS, PEOPLE_DASHBOARD_KEYS } from "./demoLinks";
import { SimpleBroke, SimpleCheckout, SimpleHappening, SimplePeople, SimpleReleases, SimpleSlow } from "./SimplePages";
import { setDemoViewMode, useDemoViewMode } from "./viewMode";

const icon = (Icon: typeof Home) => <Icon className="h-4 w-4" aria-hidden="true" />;

/** The simple view's words for the console's jargon. */
export const SIMPLE_LABELS = {
  happening: "What's happening",
  errors: "Things that broke",
  traces: "Slow requests",
  alerts: "Alerts",
  people: "People",
} as const;

const isPeople = (page: ConsolePage) => page.kind === "dashboard" && PEOPLE_DASHBOARD_KEYS.includes(page.key);
const isObs = (page: ConsolePage, view: string) => page.kind === "obs-view" && page.view === view;

export function simpleNav(page: ConsolePage, section: DemoSection | undefined, alertsFiring: number): { items: SidebarNavItem[]; footer: SidebarNavItem[] } {
  const onConsole = !section;
  const happening = onConsole && (page.kind === "overview" || page.kind === "workload" || (page.kind === "obs-view" && ["errors", "traces", "alerts"].includes(page.view)));
  const releases = section === DEMO_SECTION_RELEASES || (onConsole && (page.kind === "releases" || page.kind === "release"));
  return {
    items: [
      { label: "Home", href: demoSectionHref(DEMO_SECTION_HOME), icon: icon(Home), active: section === DEMO_SECTION_HOME },
      {
        label: SIMPLE_LABELS.happening,
        href: DEMO_LINKS.happening,
        icon: icon(Gauge),
        active: happening && page.kind !== "obs-view",
        defaultExpanded: true,
        children: [
          { label: SIMPLE_LABELS.errors, href: DEMO_LINKS.errors, active: onConsole && isObs(page, "errors") },
          { label: SIMPLE_LABELS.traces, href: DEMO_LINKS.traces, active: onConsole && isObs(page, "traces") },
          {
            label: SIMPLE_LABELS.alerts,
            href: DEMO_LINKS.alerts,
            active: onConsole && isObs(page, "alerts"),
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
      { label: SIMPLE_LABELS.people, href: DEMO_LINKS.people, icon: icon(Users), active: onConsole && isPeople(page) },
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

export function simpleCrumbs(page: ConsolePage, section: DemoSection | undefined): { label: string; href?: string }[] | null {
  if (section) return [{ label: SECTION_LABELS[section] }];
  if (page.kind === "overview") return [{ label: SIMPLE_LABELS.happening }];
  if (page.kind === "obs-view" && (page.view === "errors" || page.view === "traces" || page.view === "alerts")) {
    return [{ label: SIMPLE_LABELS.happening, href: DEMO_LINKS.happening }, { label: SIMPLE_LABELS[page.view] }];
  }
  if (isPeople(page)) return [{ label: SIMPLE_LABELS.people }];
  if (page.kind === "dashboard" && page.key === "checkout") return [{ label: SIMPLE_LABELS.happening, href: DEMO_LINKS.happening }, { label: "Checkout" }];
  return null;
}

export function simpleTitle(page: ConsolePage): string | null {
  if (page.kind !== "obs-view") return null;
  return page.view === "errors" || page.view === "traces" ? SIMPLE_LABELS[page.view] : null;
}

/** Whether the simple view draws its own page for this URL (which then explains itself: no page tip). */
export function isSimplePage(page: ConsolePage, search: Record<string, unknown>): boolean {
  return simpleKind(page, search) !== null;
}

function simpleKind(page: ConsolePage, search: Record<string, unknown>) {
  const opened = ["issue", "trace", "span", "rule", "session", "tour"].some((key) => typeof search[key] === "string" && search[key]);
  if (opened) return null;
  if (page.kind === "overview") return "happening";
  if (isObs(page, "errors")) return "broke";
  if (isObs(page, "traces")) return "slow";
  if (page.kind === "releases") return "releases";
  if (isPeople(page)) return "people";
  if (page.kind === "dashboard" && page.key === "checkout") return "checkout";
  return null;
}

/**
 * The simple pages that stand in for the console's: only on a list page with
 * nothing open in it, so a link to one issue, one trace or one release still
 * opens the real page.
 */
export function simplePage(page: ConsolePage, search: Record<string, unknown>): ReactNode | null {
  switch (simpleKind(page, search)) {
    case "happening":
      return <SimpleHappening />;
    case "broke":
      return <SimpleBroke />;
    case "slow":
      return <SimpleSlow />;
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
        description={mode === "advanced" ? "The full console, every page" : "Show every page and number"}
        size="lg"
      />
    </div>
  );
}
