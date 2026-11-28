// Copyright (c) 2026 Reliant Labs

/**
 * The console's AGGREGATE PAGES (owner, 2026-11-27): while the shell shows
 * several projects (`?projects=a,b`, `?projects=all`), a page registered here
 * stands in for the route's own page. It shows true aggregates the backend
 * computes over every project in scope, and a row per project that opens that
 * project's own page (AggregatePage.tsx). Where it differs from the
 * one-project page, a DrillDownHint says what selecting one project shows.
 *
 * A page registers itself with one entry below, keyed by its ConsolePage kind.
 * severalModeOf (../severalProjects.tsx) then answers "aggregate" for that
 * kind, and ConsoleLayout renders it through AggregateHost in place of the
 * route's Outlet. Every entry is its own chunk, loaded only when shown.
 *
 * An aggregate page takes everything it reads as props (AggregatePageProps),
 * never the focused project's console context, which the URL's project
 * segment still names but the page is not about. So the same component can be
 * mounted by another host later (the org console) unchanged.
 */

import { lazy, type ComponentType, type LazyExoticComponent } from "react";

import { isChunkLoadError, staleChunkModule } from "@/lib/lazyRoute";

import type { ConsolePage } from "../paths";
import type { SeveralApp } from "../severalProjects";

/** What every aggregate page is given. */
export interface AggregatePageProps {
  /** The console page it stands in for: the page each project's row opens, for that project alone. */
  page: ConsolePage;
  /** The projects in scope, each with its forge app. */
  apps: readonly SeveralApp[];
  /** `many`: a chosen several; `all`: every project. */
  mode: "many" | "all";
}

export type AggregatePageComponent = ComponentType<AggregatePageProps>;

/** `name` of a lazily loaded module, as an aggregate page; a chunk a deploy removed reloads the page (lib/lazyRoute). */
function lazyAggregate<M extends Record<string, unknown>, K extends keyof M & string>(
  load: () => Promise<M>,
  name: K,
): LazyExoticComponent<AggregatePageComponent> {
  return lazy(() =>
    load()
      .then((mod) => ({ default: mod[name] as AggregatePageComponent }))
      .catch((error: unknown) => {
        if (!isChunkLoadError(error)) throw error;
        return { default: staleChunkModule(error, "default").default as AggregatePageComponent };
      }),
  );
}

/**
 * The registered aggregate pages, by ConsolePage kind. To add one: export a
 * component taking AggregatePageProps (built on AggregatePage), and add its
 * kind here. Nothing else changes: the mode, the layout and the nav follow.
 */
const AGGREGATE_PAGES: Partial<Record<ConsolePage["kind"], LazyExoticComponent<AggregatePageComponent>>> = {
  overview: lazyAggregate(() => import("../ProjectsOverview"), "ProjectsOverviewPage"),
};

/** The kinds with an aggregate page, for tests and diagnostics. */
export function aggregateKinds(): ConsolePage["kind"][] {
  return Object.keys(AGGREGATE_PAGES) as ConsolePage["kind"][];
}

/** Whether `page` has an aggregate page (severalModeOf: "aggregate"). */
export function hasAggregatePage(page: ConsolePage): boolean {
  return Object.prototype.hasOwnProperty.call(AGGREGATE_PAGES, page.kind);
}

/** The aggregate page registered for `page`, or null. */
export function aggregatePageOf(page: ConsolePage): LazyExoticComponent<AggregatePageComponent> | null {
  return hasAggregatePage(page) ? (AGGREGATE_PAGES[page.kind] ?? null) : null;
}
