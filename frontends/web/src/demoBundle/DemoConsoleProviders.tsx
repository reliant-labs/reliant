// Copyright (c) 2025 Reliant Labs

/**
 * What the forge console's pages read from context in the demo shell:
 * demoShellRoutes.tsx wraps its `_authenticated` layout in it, so the
 * console's pages and the demo's own (/forge/demo, DemoConsole) are inside it:
 *
 *  - the Observability views read nothing from here: the console's REAL
 *    client reads the shop fixture pack over the demo's transport
 *    (demoShellBoot.ts), read-only there, observability usage included (the
 *    pack answers it as a control plane without billing would, so the usage
 *    panels hide rather than fail);
 *  - "Ask Reliant" is in handoff mode: a sheet with `forge …` commands to
 *    copy, never a chat;
 *  - the led console with the demo's own additions (DemoConsoleHost).
 *
 * The nav is not here: it is the shell's (Forge/Demo/DemoNav.tsx, demoNavConfig.ts).
 */

import { useMemo, type ReactNode } from "react";
import { useRouterState } from "@tanstack/react-router";

import { DemoBrowserProviders } from "@/components/Forge/Demo/DemoBrowserProviders";
import { DEMO_SECTIONS, DEMO_SECTION_AGENTS, DEMO_SECTION_BUILT, DEMO_SECTION_HOME, DEMO_SECTION_RELEASES, demoSectionHref, FORGE_DEMO_PATH, type DemoSection } from "@/components/Forge/Demo/demoPath";
import { PageAgentStrip } from "@/components/Forge/Demo/sections/AgentCards";
import { PEOPLE_DASHBOARD_KEYS } from "@/components/Forge/Demo/sections/demoLinks";
import { explainShopIssue } from "@/components/Forge/Demo/sections/shopFacts";
import type { ProductAnalyticsHub } from "@/components/Forge/Console/consoleNavModel";
import { LedConsoleProvider, type LedConsoleHost } from "@/components/Forge/Console/ledConsole";
import { AskAgentModeProvider } from "@/components/Forge/Observability/AskReliant/askAgentMode";

const PRODUCT_ANALYTICS: ProductAnalyticsHub = { open: { kind: "dashboard", dashKind: "release", key: PEOPLE_DASHBOARD_KEYS[0] }, keys: PEOPLE_DASHBOARD_KEYS };

/** Each of the demo's own pages' breadcrumb. */
const SECTION_CRUMBS: Record<DemoSection, { label: string; href?: string }[]> = {
  [DEMO_SECTION_HOME]: [{ label: "Home" }],
  [DEMO_SECTION_BUILT]: [{ label: "How it was built" }],
  [DEMO_SECTION_AGENTS]: [{ label: "Put an agent on it" }],
  [DEMO_SECTION_RELEASES]: [{ label: "Releases" }, { label: "How releases work" }],
};

/** The agent card each page carries ("put an agent on it"), and the sentence that ties it to the page. */
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

/** The demo's own page on screen (`/forge/demo?section=`), if it is one. */
export function demoSectionOf(pathname: string, search: Record<string, unknown>): DemoSection | null {
  const section = search.section;
  return pathname === FORGE_DEMO_PATH && typeof section === "string" && (DEMO_SECTIONS as readonly string[]).includes(section) ? (section as DemoSection) : null;
}

/**
 * The led console (Console/ledConsole.tsx) with what is the demo's own:
 *
 *  - the demo's pages' breadcrumbs (a page of its own is not a console page:
 *    no console lead is drawn there);
 *  - Product analytics, the sample shop's Users · Product · Acquisition hub;
 *  - an agent card on Errors, Traces, Alerts and Releases, and the sample
 *    shop's issues explained in plain words;
 *  - "How releases work" from the Releases sentence, a page of the demo's.
 *
 * The environments keep the app's order (prod first, Console/envScope.ts).
 *
 * The guided tour walks this same console: its steps open the console's own
 * pages and ring the led pages' rows and cards.
 */
export function DemoConsoleHost({ children }: { children: ReactNode }) {
  const section = useRouterState({ select: (s) => demoSectionOf(s.location.pathname, s.location.search as Record<string, unknown>) });
  const host = useMemo<LedConsoleHost>(
    () => ({
      hostPage: section ? { crumbs: SECTION_CRUMBS[section] } : null,
      productAnalytics: PRODUCT_ANALYTICS,
      aside: agentAside,
      explainIssue: explainShopIssue,
      releasesExplainer: { href: demoSectionHref(DEMO_SECTION_RELEASES), label: "How releases work" },
    }),
    [section]
  );
  return <LedConsoleProvider host={host}>{children}</LedConsoleProvider>;
}

export function DemoConsoleProviders({ children }: { children: ReactNode }) {
  return (
    <AskAgentModeProvider mode="handoff" cli="forge" demo>
      <DemoBrowserProviders>
        <DemoConsoleHost>{children}</DemoConsoleHost>
      </DemoBrowserProviders>
    </AskAgentModeProvider>
  );
}
