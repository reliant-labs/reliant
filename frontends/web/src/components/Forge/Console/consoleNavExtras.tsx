// Copyright (c) 2025 Reliant Labs

/**
 * What a host adds to the console's nav without owning its pages: the public
 * demo's "How it was built" row (above Overview) and the actions its phone
 * drawer carries. ConsoleShell reads it; nothing provides it in the product,
 * so the product's nav is unchanged.
 *
 * `lead.active` means the open page is the lead's own, not a console page: its
 * row is the current one, and the breadcrumb names it.
 */

import { createContext, useContext, type ReactNode } from "react";

export interface ConsoleNavExtras {
  lead?: { label: string; href: string; icon?: ReactNode; active: boolean };
  /** Shown in the nav on a phone (the drawer) only. */
  drawerAppendix?: ReactNode;
  /** The scope control's environments in this order (names it omits keep theirs, after). */
  envOrder?: readonly string[];
}

const ConsoleNavExtrasContext = createContext<ConsoleNavExtras>({});

export function ConsoleNavExtrasProvider({ value, children }: { value: ConsoleNavExtras; children: ReactNode }) {
  return <ConsoleNavExtrasContext.Provider value={value}>{children}</ConsoleNavExtrasContext.Provider>;
}

export function useConsoleNavExtras(): ConsoleNavExtras {
  return useContext(ConsoleNavExtrasContext);
}
