// Copyright (c) 2025 Reliant Labs

/**
 * The Runs filter bar's state, which IS the URL's search params
 * (runsSearchSchema): what each control shows for a search, and the search a
 * change produces. Pure, so the filter ↔ URL mapping is pinned by tests
 * rather than by a rendered page.
 */

import { RUN_STATE_FILTERS } from "@/api/run-grpc";
import { launchKindDisplay } from "@/lib/runStatus";
import {
  RUN_KIND_FILTER_KEYS,
  RUN_RANGE_KEYS,
  type RunRangeKey,
  type RunStateFilterKey,
  type RunsSearch,
} from "@/routeSchemas";

export const DEFAULT_RUN_RANGE: RunRangeKey = "24h";

export const RUN_RANGE_LABELS: Record<RunRangeKey, string> = {
  "24h": "Last 24 hours",
  "7d": "Last 7 days",
  "30d": "Last 30 days",
  all: "All time",
};

export const RUN_RANGE_OPTIONS = RUN_RANGE_KEYS.map((key) => ({ value: key, label: RUN_RANGE_LABELS[key] }));

export const RUN_STATE_OPTIONS: Array<{ value: RunStateFilterKey; label: string }> = RUN_STATE_FILTERS.map(
  (filter) => ({ value: filter.key, label: filter.label }),
);

/** "builder.test" reads "Tests": a test run from the builder, not a launch kind a user names. */
export const RUN_KIND_OPTIONS: Array<{ value: string; label: string }> = RUN_KIND_FILTER_KEYS.map((kind) => ({
  value: kind,
  label: kind === "builder.test" ? "Tests" : launchKindDisplay(kind).shortLabel,
}));

export type RunProjectScope = "current" | "all";

/**
 * Which projects the list covers. With no current project there is nothing to
 * scope to, so it is "all" whatever the URL says.
 */
export function runProjectScope(search: RunsSearch, hasCurrentProject: boolean): RunProjectScope {
  return search.allProjects || !hasCurrentProject ? "all" : "current";
}

/** Whether any filter narrows the list beyond its defaults. */
export function hasActiveRunFilters(search: RunsSearch): boolean {
  return Boolean(
    search.state?.length ||
      search.kind?.length ||
      search.workflow?.length ||
      search.trigger ||
      search.parent ||
      search.q ||
      (search.range && search.range !== DEFAULT_RUN_RANGE),
  );
}

/**
 * The search a filter change produces. Defaults are written as ABSENT, so the
 * URL only names what narrows the list and a default view is a bare link.
 */
export function applyRunFilter(search: RunsSearch, patch: Partial<RunsSearch>): RunsSearch {
  const next: RunsSearch = { ...search, ...patch };
  if (next.range === DEFAULT_RUN_RANGE) next.range = undefined;
  if (next.allProjects === false) next.allProjects = undefined;
  return compactRunsSearch(next);
}

/** The project scope as a search patch. */
export function scopePatch(scope: RunProjectScope): Partial<RunsSearch> {
  return { allProjects: scope === "all" ? true : undefined };
}

/**
 * Clear every filter. The view settings survive — project scope, grouping and
 * the area's `project` param — because they are not filters on WHICH runs.
 */
export function clearRunFilters(search: RunsSearch): RunsSearch {
  return compactRunsSearch({ allProjects: search.allProjects, group: search.group, project: search.project });
}

/** Drop empty values so the URL only names what narrows the list. */
export function compactRunsSearch(search: RunsSearch): RunsSearch {
  const out: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(search)) {
    if (value === undefined || value === "" || (Array.isArray(value) && value.length === 0)) continue;
    out[key] = value;
  }
  return out as RunsSearch;
}
