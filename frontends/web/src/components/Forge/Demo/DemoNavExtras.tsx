// Copyright (c) 2025 Reliant Labs

/**
 * The public demo's console: the led console (Console/ledConsole.tsx), plus
 * what is the demo's own:
 *
 *  - Home above Overview and "How it was built" in the footer, its two pages
 *    of its own (`?section=` of /forge/demo, DemoConsole), and "How releases
 *    work" linked from Releases;
 *  - Product analytics, the sample shop's Users · Product · Acquisition hub;
 *  - an agent card on Errors, Traces, Alerts and Releases ("put an agent on
 *    it"), and the sample shop's issues explained in plain words;
 *  - the environments in promotion order (dev, staging, prod), and on a phone
 *    the tour, Install Forge and the telemetry notice in the drawer.
 *
 * The guided tour walks this same console: its steps open the console's own
 * pages and ring the led pages' rows and cards.
 */

import { useMemo, type ReactNode } from "react";
import { Hammer, Home } from "lucide-react";

import type { ProductAnalyticsHub } from "../Console/groupedNav";
import { LedConsoleProvider, type LedConsoleHost } from "../Console/ledConsole";
import { DemoDrawerActions } from "./DemoScreenActions";
import { DEMO_SECTION_AGENTS, DEMO_SECTION_BUILT, DEMO_SECTION_HOME, DEMO_SECTION_RELEASES, demoSectionHref, type DemoSection } from "./demoPath";
import { PageAgentStrip } from "./sections/AgentCards";
import { PEOPLE_DASHBOARD_KEYS } from "./sections/demoLinks";
import { explainShopIssue } from "./sections/shopFacts";

export const DEMO_ENV_ORDER = ["dev", "staging", "prod"] as const;

const icon = (Icon: typeof Home) => <Icon className="h-4 w-4" aria-hidden="true" />;

const PRODUCT_ANALYTICS: ProductAnalyticsHub = { open: { kind: "dashboard", dashKind: "release", key: PEOPLE_DASHBOARD_KEYS[0] }, keys: PEOPLE_DASHBOARD_KEYS };

/** Each section page's breadcrumb. */
const SECTION_CRUMBS: Record<DemoSection, { label: string; href?: string }[]> = {
  [DEMO_SECTION_HOME]: [{ label: "Home" }],
  [DEMO_SECTION_BUILT]: [{ label: "How it was built" }],
  [DEMO_SECTION_AGENTS]: [{ label: "Put an agent on it" }],
  [DEMO_SECTION_RELEASES]: [{ label: "Releases" }, { label: "How releases work" }],
};

/** The agent card each page carries, and the sentence that ties it to the page. */
function agentAside(key: string): ReactNode {
  switch (key) {
    case "errors":
      return <PageAgentStrip id="fix-errors" lead="An agent can read an issue like these, find the change that caused it, and open the fix for you to review." />;
    case "traces":
      return (
        <PageAgentStrip
          id="slow-pages"
          lead="We spent three months trying to make our own chat pages load faster. The day we handed an agent a trace like these, it found the slow query and fixed it."
        />
      );
    case "alerts":
      return <PageAgentStrip id="watch-alerts" lead="When an alert fires at 2am, an agent can look first and have a fix waiting by morning." />;
    case "releases":
      return <PageAgentStrip id="release-guard" lead="An agent can check every release against the hour before it went out, and move prod back to the last good build if it's bad." />;
    default:
      return null;
  }
}

export function DemoNavExtras({ section, children }: { section?: DemoSection; children: ReactNode }) {
  const host = useMemo<LedConsoleHost>(
    () => ({
      navLead: [{ label: "Home", href: demoSectionHref(DEMO_SECTION_HOME), icon: icon(Home), active: section === DEMO_SECTION_HOME }],
      navFooter: [{ label: "How it was built", href: demoSectionHref(DEMO_SECTION_BUILT), icon: icon(Hammer), active: section === DEMO_SECTION_BUILT }],
      hostPage: section ? { crumbs: SECTION_CRUMBS[section] } : null,
      productAnalytics: PRODUCT_ANALYTICS,
      aside: agentAside,
      explainIssue: explainShopIssue,
      releasesExplainer: { href: demoSectionHref(DEMO_SECTION_RELEASES), label: "How releases work" },
      envOrder: DEMO_ENV_ORDER,
      drawerAppendix: <DemoDrawerActions />,
    }),
    [section]
  );
  return <LedConsoleProvider host={host}>{children}</LedConsoleProvider>;
}
