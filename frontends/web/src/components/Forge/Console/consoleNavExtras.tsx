// Copyright (c) 2025 Reliant Labs

/**
 * What a host adds to the console without owning its pages. ConsoleShell and
 * the console pages read it; nothing provides it in the product, so the
 * product's console is unchanged.
 *
 *  - The public demo's "How it was built" row (`lead`, above Overview) and the
 *    actions its phone drawer carries.
 *  - The led console (ledConsole.tsx): the grouped nav, a plain subtitle and
 *    a lead under each page's title, the list pages that lead with their one
 *    item worth opening, and the calmer Overview and Releases (`led`). Each
 *    hook returns null to keep the console's own.
 *
 * `lead.active` means the open page is the lead's own, not a console page: its
 * row is the current one, and the breadcrumb names it.
 */

import { createContext, useContext, type ReactNode } from "react";

import type { SidebarNavItem } from "@/components/forge-ui/sidebar_layout";

import type { ConsoleNavInput } from "./ConsoleShell";
import type { ConsolePage, ObsNavView } from "./paths";

export interface ConsoleNavExtras {
  lead?: { label: string; href: string; icon?: ReactNode; active: boolean };
  /** Shown in the nav on a phone (the drawer) only. */
  drawerAppendix?: ReactNode;
  /** The scope control's environments in this order (names it omits keep theirs, after). */
  envOrder?: readonly string[];
  /** Other nav rows for the console's nav input, or null for the console's own. */
  nav?: (input: ConsoleNavInput) => { items: SidebarNavItem[]; footer: SidebarNavItem[] } | null;
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
