// Copyright (c) 2026 Reliant Labs

/**
 * The Strategy page's views live on the URL: the list, one strategy
 * (`?strategy=`), the library (`?view=library`), one library entry open
 * over it (`&entry=`, gtmLibraryPath), and the library's filters, as the
 * other GTM pages keep theirs:
 *   all=1          Show all (absent: Fits my business)
 *   cat=7,8        categories (GtmStrategyCategory numbers)
 *   model=1,4      business models (GtmBusinessModel numbers)
 *   type=3         business type (GtmBusinessType number)
 *   stage= effort= cost=   the library's own values
 *   q=             the search
 * Leaving the library drops its filters.
 */

import { useNavigate, useSearch } from "@tanstack/react-router";

import type { GtmBusinessModel, GtmBusinessType, GtmStrategyCategory } from "@/gen/reliant/v1/gtm_pb";

export type StrategyPageView = "strategies" | "library";

/** The library's filters, as the URL holds them. */
export interface LibraryFilters {
  /** Fits my business (the default) or Show all. */
  fits: boolean;
  categories: GtmStrategyCategory[];
  businessModels: GtmBusinessModel[];
  /** 0: any. */
  businessType: GtmBusinessType;
  stage: string;
  effort: string;
  cost: string;
  query: string;
}

const LIBRARY_PARAMS = ["all", "cat", "model", "type", "stage", "effort", "cost", "q"] as const;

type Search = Record<string, unknown>;

const str = (v: unknown) => (typeof v === "string" ? v : typeof v === "number" ? String(v) : "");
const nums = <T extends number>(v: unknown) =>
  str(v)
    .split(",")
    .map(Number)
    .filter((n) => Number.isInteger(n) && n > 0) as T[];

/** The library's filters from the page's search. */
export function libraryFiltersOf(search: Search): LibraryFilters {
  return {
    fits: str(search.all) !== "1",
    categories: nums<GtmStrategyCategory>(search.cat),
    businessModels: nums<GtmBusinessModel>(search.model),
    businessType: (nums<GtmBusinessType>(search.type)[0] ?? 0) as GtmBusinessType,
    stage: str(search.stage),
    effort: str(search.effort),
    cost: str(search.cost),
    query: str(search.q),
  };
}

/** The search params for `filters`; a default is absent. */
export function librarySearchOf(f: LibraryFilters): Search {
  const list = (v: number[]) => (v.length ? v.join(",") : undefined);
  return {
    all: f.fits ? undefined : "1",
    cat: list(f.categories),
    model: list(f.businessModels),
    type: f.businessType ? String(f.businessType) : undefined,
    stage: f.stage || undefined,
    effort: f.effort || undefined,
    cost: f.cost || undefined,
    q: f.query.trim() || undefined,
  };
}

const applyChange = (current: LibraryFilters, change: LibraryFilterChange): LibraryFilters => ({
  ...current,
  ...(typeof change === "function" ? change(current) : change),
});

const withoutLibraryParams = (prev: Search) => Object.fromEntries(Object.entries(prev).filter(([k]) => !(LIBRARY_PARAMS as readonly string[]).includes(k)));

/** A filter change: the fields to set, or a function of the filters the URL holds when it lands (two quick clicks each see the other's). */
export type LibraryFilterChange = Partial<LibraryFilters> | ((current: LibraryFilters) => Partial<LibraryFilters>);

/** Moves between the page's views, and sets the library's filters. */
export function useStrategyNav() {
  const navigate = useNavigate();
  const go = (search: { view?: StrategyPageView; strategy?: string; entry?: string; filters?: LibraryFilterChange }) =>
    void navigate({
      to: ".",
      search: (prev: Search) => {
        const library = search.view === "library";
        const base = library ? prev : withoutLibraryParams(prev);
        return {
          ...base,
          ...(library && search.filters ? librarySearchOf(applyChange(libraryFiltersOf(prev), search.filters)) : {}),
          view: library ? "library" : undefined,
          strategy: search.strategy,
          entry: search.entry,
        };
      },
      // A filter change replaces the history entry: Back leaves the library, not one filter.
      replace: !!search.filters,
    } as never);
  return {
    list: () => go({}),
    library: () => go({ view: "library" }),
    entry: (key: string) => go({ view: "library", entry: key }),
    strategy: (id: string) => go({ strategy: id }),
    /** Sets some of the library's filters, keeping the rest and the open entry. */
    filter: (filters: LibraryFilterChange, entry?: string) => go({ view: "library", entry, filters }),
  };
}

/** The library's filters on the URL now. */
export function useLibraryFilters(): LibraryFilters {
  return libraryFiltersOf(useSearch({ strict: false }) as Search);
}
