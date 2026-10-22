// Copyright (c) 2025 Reliant Labs

/**
 * The Runs filter bar (WORKFLOW_UI.md §5.2). Every filter is a search param,
 * so a filtered list is a link and the back button restores it.
 *
 * One compact row: search, then one menu per filter — Project, Started,
 * State, Started by — each a button that says its value ("State: Failed,
 * Live"), so an applied filter is always visible without a row of chips per
 * option. Filters that arrive on a link (a workflow, an automation, a parent
 * chat) show as removable badges in the same row, then Clear and the
 * grouping switch. The state ↔ URL mapping is runFilterState.ts.
 */

import { useEffect, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { X } from "lucide-react";

import { useDebounce } from "@/hooks/useDebounce";
import { WORKFLOWS_RUNS_PATH } from "@/lib/workflowsArea";
import type { RunRangeKey, RunStateFilterKey, RunsSearch } from "@/routeSchemas";
import { getWorkflowDisplayName } from "../workflow/useWorkflowInputs";
import { FilterSearch, MultiSelectFilter, SelectFilter } from "../workflows/FilterMenu";
import {
  DEFAULT_RUN_RANGE,
  RUN_KIND_OPTIONS,
  RUN_RANGE_OPTIONS,
  RUN_STATE_OPTIONS,
  applyRunFilter,
  clearRunFilters,
  hasActiveRunFilters,
  runProjectScope,
  scopePatch,
  type RunProjectScope,
} from "./runFilterState";

export { hasActiveRunFilters } from "./runFilterState";

interface RunFiltersProps {
  /** The project Runs is scoped to by default; unset when none is selected. */
  currentProjectName?: string;
  /** The automation named by `trigger`, when the list has resolved it. */
  triggerName?: string;
  /** The chat named by `parent`, when it has loaded. */
  parentTitle?: string;
}

/**
 * The Runs list's search params, and a setter that replaces them. Replace,
 * not push: changing a filter is adjusting a view, and the back button should
 * leave the list rather than undo filters one at a time.
 */
export function useRunsSearch(): [RunsSearch, (next: RunsSearch) => void] {
  const search = useSearch({ strict: false }) as RunsSearch;
  const navigate = useNavigate();
  const set = (next: RunsSearch) => {
    void navigate({ to: WORKFLOWS_RUNS_PATH, search: applyRunFilter(next, {}), replace: true });
  };
  return [search, set];
}

export function RunFilters({ currentProjectName, triggerName, parentTitle }: RunFiltersProps) {
  const [search, setSearch] = useRunsSearch();
  const update = (patch: Partial<RunsSearch>) => setSearch(applyRunFilter(search, patch));

  // The text box writes to the URL after typing pauses, not per keystroke.
  const [query, setQuery] = useState(search.q ?? "");
  const debouncedQuery = useDebounce(query, 250);
  useEffect(() => {
    if ((search.q ?? "") !== debouncedQuery.trim()) update({ q: debouncedQuery.trim() || undefined });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only the debounced text drives this
  }, [debouncedQuery]);
  useEffect(() => {
    // An outside change (Clear, back button) resets the box.
    if ((search.q ?? "") !== query.trim()) setQuery(search.q ?? "");
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only the URL drives this
  }, [search.q]);

  const scope = runProjectScope(search, Boolean(currentProjectName));
  const scopeOptions: Array<{ value: RunProjectScope; label: string }> = [
    ...(currentProjectName ? [{ value: "current" as const, label: currentProjectName }] : []),
    { value: "all", label: "All projects" },
  ];
  const groupRepeats = search.group !== false;

  return (
    <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Filter runs">
      <FilterSearch value={query} onChange={setQuery} placeholder="Search titles" label="Search run titles" />

      {/* No scope to offer before a project is selected: the list spans them all. */}
      {currentProjectName && (
        <SelectFilter
          label="Project"
          options={scopeOptions}
          value={scope}
          defaultValue="current"
          onChange={(next) => update(scopePatch(next))}
        />
      )}
      <SelectFilter<RunRangeKey>
        label="Started"
        options={RUN_RANGE_OPTIONS}
        value={search.range ?? DEFAULT_RUN_RANGE}
        defaultValue={DEFAULT_RUN_RANGE}
        onChange={(range) => update({ range })}
      />
      <MultiSelectFilter<RunStateFilterKey>
        label="State"
        options={RUN_STATE_OPTIONS}
        value={search.state ?? []}
        onChange={(state) => update({ state })}
      />
      <MultiSelectFilter
        label="Started by"
        options={RUN_KIND_OPTIONS}
        value={search.kind ?? []}
        onChange={(kind) => update({ kind })}
        emptyLabel="Anyone"
      />

      {search.parent && (
        <RemovableFilter label={`Started in: ${parentTitle ?? "a chat"}`} onRemove={() => update({ parent: undefined })} />
      )}
      {search.trigger && (
        <RemovableFilter
          label={`Automation: ${triggerName ?? "selected automation"}`}
          onRemove={() => update({ trigger: undefined })}
        />
      )}
      {search.workflow?.map((workflow) => (
        <RemovableFilter
          key={workflow}
          label={`Workflow: ${getWorkflowDisplayName(workflow, true)}`}
          onRemove={() => update({ workflow: search.workflow?.filter((w) => w !== workflow) })}
        />
      ))}

      {hasActiveRunFilters(search) && (
        <button
          type="button"
          onClick={() => {
            setQuery("");
            setSearch(clearRunFilters(search));
          }}
          className="h-8 rounded-md px-2 text-sm font-medium text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          aria-label="Clear filters"
        >
          Clear
        </button>
      )}

      <label className="ml-auto flex h-8 items-center gap-2 text-sm text-muted-foreground">
        <input
          type="checkbox"
          checked={groupRepeats}
          onChange={(event) => update({ group: event.target.checked ? undefined : false })}
          className="h-3.5 w-3.5 rounded border-border accent-primary"
        />
        Group repeats
      </label>
    </div>
  );
}

/** A filter that arrived on a link, with no menu of its own: shown so it can be removed. */
function RemovableFilter({ label, onRemove }: { label: string; onRemove: () => void }) {
  return (
    <span className="inline-flex h-8 max-w-[20rem] items-center gap-1 rounded-md border border-primary/50 bg-primary/10 pl-2.5 pr-1 text-sm text-foreground">
      <span className="truncate">{label}</span>
      <button
        type="button"
        onClick={onRemove}
        aria-label={`Remove filter ${label}`}
        className="inline-flex h-6 w-6 shrink-0 items-center justify-center rounded text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      >
        <X className="h-3.5 w-3.5" aria-hidden="true" />
      </button>
    </span>
  );
}
