// Copyright (c) 2026 Reliant Labs

/**
 * The Strategy page's views live on the URL: the list, one strategy
 * (`?strategy=`), the library (`?view=library`), one library entry open
 * over it (`&entry=`, gtmLibraryPath).
 */

import { useNavigate } from "@tanstack/react-router";

export type StrategyPageView = "strategies" | "library";

/** Moves between the page's views. */
export function useStrategyNav() {
  const navigate = useNavigate();
  const go = (search: { view?: StrategyPageView; strategy?: string; entry?: string }) =>
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({ ...prev, view: search.view === "library" ? "library" : undefined, strategy: search.strategy, entry: search.entry }),
    } as never);
  return {
    list: () => go({}),
    library: () => go({ view: "library" }),
    entry: (key: string) => go({ view: "library", entry: key }),
    strategy: (id: string) => go({ strategy: id }),
  };
}
