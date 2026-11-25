// Copyright (c) 2025 Reliant Labs

/**
 * What a host adds to the console without owning its pages. ConsoleFrame and
 * the console pages read it. The led console (ledConsole.tsx, the console's
 * default) provides it: a plain subtitle and a lead under each page's title,
 * the list pages that lead with their one item worth opening, and the calmer
 * Overview and Releases (`led`). Each hook returns null to keep the
 * console's own. The nav is not here: it is the shell's (Layout/sidebar/
 * navConfig/), whichever host draws it.
 */

import { createContext, useContext, type ReactNode } from "react";

import type { ConsolePage, ObsNavView } from "./paths";

export interface ConsoleNavExtras {
  /** The scope control's environments in this order (names it omits keep theirs, after). */
  envOrder?: readonly string[];
  /** Another breadcrumb trail for `page` (after the project), or null for the console's own (`own`, which it may rework). */
  crumbs?: (page: ConsolePage, own: { label: string; href?: string }[]) => { label: string; href?: string }[] | null;
  /**
   * A plain line under a page's title ("Traces: step-by-step timeline of one
   * request"), by page key: an Observability view's name, "overview",
   * "releases" or "data". Null for none.
   */
  subtitle?: (key: string) => string | null;
  /** What goes under a page's header (a detail page's "What happened", an agent card), by the same keys as `subtitle`. */
  intro?: (key: string) => ReactNode | null;
  /** Another body for an Observability view (it may render the console's own, `own`), or null to keep it. */
  listPage?: (view: ObsNavView, own: ReactNode) => ReactNode | null;
  /**
   * The led console's Overview and Releases: each leads with one sentence;
   * the Overview's rail leaves out the Users card (Product analytics has its
   * own row), and Releases shows one "Live in" column instead of one per
   * environment, without the symbol legend and the deploy log.
   */
  led?: boolean;
  /** "How releases work", linked from the led Releases page's sentence. */
  releasesExplainer?: { href: string; label: string };
}

const ConsoleNavExtrasContext = createContext<ConsoleNavExtras>({});

export function ConsoleNavExtrasProvider({ value, children }: { value: ConsoleNavExtras; children: ReactNode }) {
  return <ConsoleNavExtrasContext.Provider value={value}>{children}</ConsoleNavExtrasContext.Provider>;
}

export function useConsoleNavExtras(): ConsoleNavExtras {
  return useContext(ConsoleNavExtrasContext);
}
