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
 * `pageLead` is the public demo's one addition to a page body: a line under
 * the page's title (the demo's "Put an agent on it" card, the Releases
 * explainer). Null, or no provider, keeps the page as it is.
 */

import { createContext, useContext, type ReactNode } from "react";

import type { ConsolePage } from "./paths";

export interface ConsoleNavExtras {
  lead?: { label: string; href: string; icon?: ReactNode; active: boolean };
  /** Shown in the nav on a phone (the drawer) only. */
  drawerAppendix?: ReactNode;
  /** The scope control's environments in this order (names it omits keep theirs, after). */
  envOrder?: readonly string[];
  /** Drawn under `page`'s title, above its body; null for nothing. */
  pageLead?: (page: ConsolePage) => ReactNode | null;
}

const ConsoleNavExtrasContext = createContext<ConsoleNavExtras>({});

export function ConsoleNavExtrasProvider({ value, children }: { value: ConsoleNavExtras; children: ReactNode }) {
  return <ConsoleNavExtrasContext.Provider value={value}>{children}</ConsoleNavExtrasContext.Provider>;
}

export function useConsoleNavExtras(): ConsoleNavExtras {
  return useContext(ConsoleNavExtrasContext);
}

/** The host's line under this page's title, or null. */
export function ConsolePageLead({ page }: { page: ConsolePage }) {
  const { pageLead } = useConsoleNavExtras();
  return <>{pageLead?.(page) ?? null}</>;
}
