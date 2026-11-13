// Copyright (c) 2025 Reliant Labs

/**
 * What a host adds to the console's nav without owning its pages: the public
 * demo's "How it was built" row (above Overview) and the actions its phone
 * drawer carries. ConsoleShell reads it; nothing provides it in the product,
 * so the product's nav is unchanged.
 *
 * `lead.active` means the open page is the lead's own, not a console page: its
 * row is the current one, and the breadcrumb names it.
 *
 * The public demo's simple view (Forge/Demo/approachable) also replaces the
 * nav rows, the breadcrumb and, for a few pages, the page itself; each hook
 * returns null to keep the console's own.
 */

import { createContext, useContext, type ReactNode } from "react";

import type { SidebarNavItem } from "@/components/forge-ui/sidebar_layout";

import type { ConsolePage } from "./paths";

export interface ConsoleNavExtras {
  lead?: { label: string; href: string; icon?: ReactNode; active: boolean };
  /** Shown in the nav on a phone (the drawer) only. */
  drawerAppendix?: ReactNode;
  /** Shown under the nav rows at every width (the demo's view switch). */
  navAppendix?: ReactNode;
  /** The scope control's environments in this order (names it omits keep theirs, after). */
  envOrder?: readonly string[];
  /** Hide the top bar's environment scope (the demo's simple view shows production only). */
  hideScope?: boolean;
  /** Other nav rows for `page`, or null for the console's own. */
  nav?: (page: ConsolePage, alertsFiring: number) => { items: SidebarNavItem[]; footer: SidebarNavItem[] } | null;
  /** Another breadcrumb trail for `page` (after the project), or null for the console's own (`own`, which it may rework). */
  crumbs?: (page: ConsolePage, own: { label: string; href?: string }[]) => { label: string; href?: string }[] | null;
  /** Another page title for an Observability view, or null for the console's own. */
  title?: (page: ConsolePage) => string | null;
  /**
   * A plain line under a page's title ("Traces: step-by-step timeline of one
   * request"), by page key: an Observability view's name, "overview",
   * "releases" or "data". Null for none (the console's own pages have none).
   */
  subtitle?: (key: string) => ReactNode | null;
  /** A host's lead under a page's header (a summary sentence, an agent card), by the same keys as `subtitle`. */
  intro?: (key: string) => ReactNode | null;
  /** Another page body for `page` (it may wrap the console's own, `children`), or null to keep it. */
  page?: (page: ConsolePage, children: ReactNode) => ReactNode | null;
}

const ConsoleNavExtrasContext = createContext<ConsoleNavExtras>({});

export function ConsoleNavExtrasProvider({ value, children }: { value: ConsoleNavExtras; children: ReactNode }) {
  return <ConsoleNavExtrasContext.Provider value={value}>{children}</ConsoleNavExtrasContext.Provider>;
}

export function useConsoleNavExtras(): ConsoleNavExtras {
  return useContext(ConsoleNavExtrasContext);
}
