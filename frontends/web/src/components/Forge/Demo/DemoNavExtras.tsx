// Copyright (c) 2025 Reliant Labs

/**
 * The public demo's additions to the console nav (Console/consoleNavExtras):
 *
 *  - in the simple view (the default; approachable/viewMode.ts), the five
 *    plain-language rows, renamed crumbs and titles, and the simple pages in
 *    place of the busiest console pages (approachable/simpleNav.tsx);
 *  - in the advanced view, "How it was built", the recorded forge run
 *    (`/forge/demo?section=built`), as the first row, and the page's own
 *    breadcrumb when it is open;
 *  - either way, the Advanced view switch under the rows, and the
 *    environments in promotion order (dev, staging, prod);
 *  - on a phone, the tour, Install Forge and the telemetry notice in the
 *    drawer, so the row under the header is one line (DemoScreenActions).
 */

import { useMemo, type ReactNode } from "react";
import { useRouterState } from "@tanstack/react-router";
import { Hammer } from "lucide-react";

import { ConsoleNavExtrasProvider, type ConsoleNavExtras } from "../Console/consoleNavExtras";
import { simpleCrumbs, simpleNav, simplePage, simpleTitle, ViewModeSwitch, withAgentStrip } from "./approachable/simpleNav";
import { useDemoViewMode } from "./approachable/viewMode";
import { DemoDrawerActions } from "./DemoScreenActions";
import { DEMO_SECTION_BUILT, FORGE_DEMO_PATH, type DemoSection } from "./demoPath";

/** The build page's one name: nav row, breadcrumb, banner and tour step. */
export const HOW_BUILT_LABEL = "How it was built";
export const HOW_BUILT_HREF = `${FORGE_DEMO_PATH}?section=${DEMO_SECTION_BUILT}`;
export const DEMO_ENV_ORDER = ["dev", "staging", "prod"] as const;

export function DemoNavExtras({ builtActive = false, section, children }: { builtActive?: boolean; section?: DemoSection; children: ReactNode }) {
  const search = useRouterState({ select: (s) => s.location.search }) as Record<string, unknown>;
  // A guided tour walks the full console (its spotlights name its rows and panels).
  const chosen = useDemoViewMode();
  const mode = search.tour ? "advanced" : chosen;
  const current = section ?? (builtActive ? DEMO_SECTION_BUILT : undefined);
  const value = useMemo<ConsoleNavExtras>(() => {
    const common = { envOrder: DEMO_ENV_ORDER, drawerAppendix: <DemoDrawerActions />, navAppendix: <ViewModeSwitch /> };
    if (mode === "advanced") {
      return {
        ...common,
        lead: { label: HOW_BUILT_LABEL, href: HOW_BUILT_HREF, icon: <Hammer className="h-4 w-4" aria-hidden="true" />, active: current === DEMO_SECTION_BUILT },
        page: (page, children) => (current || search.tour ? null : withAgentStrip(page, children)),
      };
    }
    return {
      ...common,
      hideScope: true,
      nav: (page, alertsFiring) => simpleNav(page, current, alertsFiring),
      crumbs: (page) => simpleCrumbs(page, current),
      title: simpleTitle,
      // A section page (Home, Agents…) is DemoConsole's own body: never replaced.
      page: (page, children) => (current ? null : (simplePage(page, search) ?? withAgentStrip(page, children))),
    };
  }, [mode, current, search]);
  return <ConsoleNavExtrasProvider value={value}>{children}</ConsoleNavExtrasProvider>;
}
