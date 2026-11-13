// Copyright (c) 2026 Reliant Labs

/**
 * The public demo's console at the "50" density: the console's own names and
 * pages, grouped by what a reader is trying to do, each with a plain line
 * saying what it is for. What DemoNavExtras hands ConsoleShell through
 * ConsoleNavExtras.
 *
 *   Home                    the landing (?section=home)
 *   Overview
 *   Issues            ▾     Errors · Alerts
 *   Performance       ▾     Traces · RPC explorer
 *   Sessions
 *   Product analytics       the Users · Product · Acquisition hub
 *   Releases
 *   Data · Users
 *   More tools        ▸     Dashboards · Logs · Metrics · Profiles · Service graph
 *   ── Configure            Environments · Secrets · Domains
 *   footer                  How it was built · Activity
 *
 * Technical names stay; the plain words are subtitles (SUBTITLES), so a reader
 * who knows the term finds it, and one who does not learns it.
 */

import type { ReactNode } from "react";
import {
  Activity,
  BarChart3,
  CircleAlert,
  Database,
  Globe,
  Hammer,
  Home,
  KeyRound,
  Layers,
  LayoutDashboard,
  PlayCircle,
  Tag as TagIcon,
  Timer,
  Users,
  Wrench,
} from "lucide-react";

import type { SidebarNavItem } from "@/components/forge-ui/sidebar_layout";

import { consolePath, withSearch, type ConsolePage, type ObsNavView } from "../../Console/paths";
import { FIXTURE_PROJECT } from "../../Observability/__fixtures__/fixtureApi";
import { demoSectionHref, DEMO_SECTION_BUILT, DEMO_SECTION_HOME, DEMO_SECTION_RELEASES, type DemoSection } from "../demoPath";
import { PEOPLE_DASHBOARD_KEYS } from "../approachable/demoLinks";

const icon = (Icon: typeof Home) => <Icon className="h-4 w-4" aria-hidden="true" />;

/** The plain line under each page's technical name. */
export const SUBTITLES: Record<string, string> = {
  overview: "How the app is doing right now, in every environment",
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

/** A console page's link on the demo, keeping the environment the reader chose. */
export function demoHref(target: ConsolePage, env: string | undefined, extra: Record<string, unknown> = {}): string {
  return withSearch(consolePath(FIXTURE_PROJECT, target), { env, ...extra });
}

const isProduct = (page: ConsolePage) => page.kind === "dashboard" && PEOPLE_DASHBOARD_KEYS.includes(page.key);
const isObs = (page: ConsolePage, view: ObsNavView) => page.kind === "obs-view" && page.view === view;

export function balancedNav(page: ConsolePage, section: DemoSection | undefined, alertsFiring: number, env: string | undefined): { items: SidebarNavItem[]; footer: SidebarNavItem[] } {
  const at = (target: ConsolePage) => demoHref(target, env);
  const obs = (view: ObsNavView, label = view) => ({
    label: label === view ? OBS_LABEL[view] : label,
    href: at({ kind: "obs-view", view }),
    active: !section && isObs(page, view),
  });
  const row = (label: string, target: ConsolePage, rowIcon: ReactNode, active: boolean, extra: Partial<SidebarNavItem> = {}): SidebarNavItem => ({
    label,
    href: at(target),
    icon: rowIcon,
    active: !section && active,
    ...extra,
  });
  return {
    items: [
      { label: "Home", href: demoSectionHref(DEMO_SECTION_HOME), icon: icon(Home), active: section === DEMO_SECTION_HOME },
      row("Overview", { kind: "overview" }, icon(LayoutDashboard), page.kind === "overview" || page.kind === "workload"),
      {
        label: "Issues",
        icon: icon(CircleAlert),
        defaultExpanded: true,
        children: [
          obs("errors"),
          {
            ...obs("alerts"),
            badge: alertsFiring > 0 ? { type: "count" as const, value: alertsFiring, tone: "danger" as const, label: "firing" } : undefined,
          },
        ],
      },
      { label: "Performance", icon: icon(Timer), defaultExpanded: true, children: [obs("traces"), obs("rpc")] },
      row("Sessions", { kind: "obs-view", view: "sessions" }, icon(PlayCircle), isObs(page, "sessions")),
      row("Product analytics", { kind: "dashboard", dashKind: "release", key: "users" }, icon(BarChart3), isProduct(page)),
      {
        ...row("Releases", { kind: "releases" }, icon(TagIcon), page.kind === "releases" || page.kind === "release"),
        // "How releases work" is the Releases page's own explainer.
        active: section === DEMO_SECTION_RELEASES || (!section && (page.kind === "releases" || page.kind === "release")),
      },
      row("Data", { kind: "data" }, icon(Database), page.kind === "data"),
      row("Users", { kind: "users" }, icon(Users), page.kind === "users"),
      {
        // No page of its own: the row opens and closes the group (and it opens by itself on one of its pages).
        label: "More tools",
        icon: icon(Wrench),
        children: [
          {
            label: "Dashboards",
            href: at({ kind: "obs-view", view: "dashboards" }),
            active: !section && (isObs(page, "dashboards") || (page.kind === "dashboard" && !isProduct(page))),
          },
          obs("logs"),
          obs("metrics"),
          obs("profiles"),
          obs("graph"),
        ],
      },
      row("Environments", { kind: "environments" }, icon(Layers), page.kind === "environments", {
        section: "Configure",
        children: ["dev", "staging", "prod"].map((name) => ({
          label: name,
          href: at({ kind: "environment", env: name }),
          active: !section && page.kind === "environment" && page.env === name,
        })),
      }),
      row("Secrets", { kind: "secrets" }, icon(KeyRound), page.kind === "secrets", { section: "Configure" }),
      row("Domains", { kind: "domains" }, icon(Globe), page.kind === "domains", { section: "Configure" }),
    ],
    footer: [
      { label: "How it was built", href: demoSectionHref(DEMO_SECTION_BUILT), icon: icon(Hammer), active: section === DEMO_SECTION_BUILT },
      row("Activity", { kind: "activity" }, icon(Activity), page.kind === "activity"),
    ],
  };
}

const OBS_LABEL: Record<ObsNavView, string> = {
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

const SECTION_LABELS: Record<DemoSection, string> = {
  home: "Home",
  releases: "How releases work",
  agents: "Put an agent on it",
  built: "How it was built",
};

/** The group a page sits in, for the breadcrumb: "Issues › Errors", "More tools › Logs". */
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

type Crumb = { label: string; href?: string };

export function balancedCrumbs(page: ConsolePage, own: Crumb[], section: DemoSection | undefined, env: string | undefined): Crumb[] | null {
  if (section === DEMO_SECTION_RELEASES) return [{ label: "Releases", href: demoHref({ kind: "releases" }, env) }, { label: SECTION_LABELS.releases }];
  if (section) return [{ label: SECTION_LABELS[section] }];
  if (page.kind === "obs-view") {
    const group = GROUP_OF[page.view];
    return group ? [{ label: group }, { label: OBS_LABEL[page.view] }] : [{ label: OBS_LABEL[page.view] }];
  }
  if (isProduct(page)) return [{ label: "Product analytics" }];
  // A dashboard sits under More tools, not "Observability" (no such row here).
  if (page.kind === "dashboard") return [{ label: "More tools" }, ...own.filter((crumb) => crumb.label !== "Observability")];
  return null;
}
