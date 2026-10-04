// Copyright (c) 2025 Reliant Labs

/**
 * The Runs filter row (WORKFLOW_UI.md §5.2). Every filter is a search param,
 * so a filtered list is a link and the back button restores it.
 *
 * Chips, not a popover: each chip says its own state (aria-pressed), and an
 * active filter never hides behind a count badge — the n8n pattern §10 says
 * to avoid. Exclusive groups (project scope, time) are single-select; state
 * and kind are multi-select toggles.
 */

import { useEffect, useState, type ReactNode } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Search, X } from "lucide-react";

import { RUN_STATE_FILTERS } from "@/api/run-grpc";
import { launchKindDisplay } from "@/lib/runStatus";
import { cn } from "@/lib/utils";
import { useDebounce } from "@/hooks/useDebounce";
import {
  RUN_KIND_FILTER_KEYS,
  RUN_RANGE_KEYS,
  type RunRangeKey,
  type RunsSearch,
} from "@/routeSchemas";
import { getWorkflowDisplayName } from "../workflow/useWorkflowInputs";

const RANGE_LABELS: Record<RunRangeKey, string> = {
  "24h": "24 hours",
  "7d": "7 days",
  "30d": "30 days",
  all: "All time",
};

interface RunFiltersProps {
  /** The project Runs is scoped to by default; unset when none is selected. */
  currentProjectName?: string;
  /** The automation named by `trigger`, when the list has resolved it. */
  triggerName?: string;
  /** The chat named by `parent`, when it has loaded. */
  parentTitle?: string;
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
      (search.range && search.range !== "24h"),
  );
}

/**
 * The Runs list's search params, and a setter that replaces them. Replace,
 * not push: flipping a chip is adjusting a view, and the back button should
 * leave the list rather than undo filters one at a time.
 */
export function useRunsSearch(): [RunsSearch, (next: RunsSearch) => void] {
  const search = useSearch({ strict: false }) as RunsSearch;
  const navigate = useNavigate();
  const set = (next: RunsSearch) => {
    // Drop empty values so the URL only names what narrows the list.
    void navigate({ to: "/runs", search: compact(next), replace: true });
  };
  return [search, set];
}

export function RunFilters({ currentProjectName, triggerName, parentTitle }: RunFiltersProps) {
  const [search, setSearch] = useRunsSearch();
  const update = (patch: Partial<RunsSearch>) => setSearch({ ...search, ...patch });

  const toggleIn = <T extends string>(list: T[] | undefined, value: T): T[] | undefined => {
    const next = list?.includes(value) ? list.filter((v) => v !== value) : [...(list ?? []), value];
    return next.length > 0 ? next : undefined;
  };

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

  const range = search.range ?? "24h";
  const active = hasActiveRunFilters(search);

  return (
    <div className="space-y-3" role="group" aria-label="Filter runs">
      <div className="flex flex-wrap items-center gap-2">
        <label className="relative flex min-w-[14rem] flex-1 items-center sm:max-w-xs">
          <span className="sr-only">Search run titles</span>
          <Search className="pointer-events-none absolute left-2.5 h-4 w-4 text-muted-foreground" aria-hidden="true" />
          <input
            type="search"
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder="Search titles"
            className="h-8 w-full rounded-md border border-border bg-background pl-8 pr-2 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          />
        </label>

        <ChipGroup label="Project">
          {currentProjectName && (
            <Chip pressed={!search.allProjects} onClick={() => update({ allProjects: undefined })}>
              {currentProjectName}
            </Chip>
          )}
          <Chip pressed={Boolean(search.allProjects) || !currentProjectName} onClick={() => update({ allProjects: true })}>
            All projects
          </Chip>
        </ChipGroup>

        <ChipGroup label="Started">
          {RUN_RANGE_KEYS.map((key) => (
            <Chip
              key={key}
              pressed={range === key}
              onClick={() => update({ range: key === "24h" ? undefined : key })}
            >
              {RANGE_LABELS[key]}
            </Chip>
          ))}
        </ChipGroup>
      </div>

      <div className="flex flex-wrap items-center gap-2">
        <ChipGroup label="State">
          {RUN_STATE_FILTERS.map((filter) => (
            <Chip
              key={filter.key}
              pressed={search.state?.includes(filter.key) ?? false}
              onClick={() => update({ state: toggleIn(search.state, filter.key) })}
            >
              {filter.label}
            </Chip>
          ))}
        </ChipGroup>

        <ChipGroup label="Started by">
          {RUN_KIND_FILTER_KEYS.map((kind) => (
            <Chip
              key={kind}
              pressed={search.kind?.includes(kind) ?? false}
              onClick={() => update({ kind: toggleIn(search.kind, kind) })}
            >
              {kind === "builder.test" ? "Tests" : launchKindDisplay(kind).shortLabel}
            </Chip>
          ))}
        </ChipGroup>

        {search.parent && (
          <RemovableChip
            label={`Started in: ${parentTitle ?? "a chat"}`}
            onRemove={() => update({ parent: undefined })}
          />
        )}
        {search.trigger && (
          <RemovableChip
            label={`Automation: ${triggerName ?? "selected automation"}`}
            onRemove={() => update({ trigger: undefined })}
          />
        )}
        {search.workflow?.map((workflow) => (
          <RemovableChip
            key={workflow}
            label={`Workflow: ${getWorkflowDisplayName(workflow, true)}`}
            onRemove={() => update({ workflow: search.workflow?.filter((w) => w !== workflow) })}
          />
        ))}

        {active && (
          <button
            type="button"
            onClick={() => {
              setQuery("");
              setSearch({ allProjects: search.allProjects, group: search.group });
            }}
            className="ml-1 rounded-sm text-xs font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
            aria-label="Clear filters"
          >
            Clear
          </button>
        )}
      </div>
    </div>
  );
}

function compact(search: RunsSearch): RunsSearch {
  const out: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(search)) {
    if (value === undefined || value === "" || (Array.isArray(value) && value.length === 0)) continue;
    out[key] = value;
  }
  return out as RunsSearch;
}

function ChipGroup({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div role="group" aria-label={label} className="flex flex-wrap items-center gap-1">
      <span className="mr-0.5 text-xs text-muted-foreground" aria-hidden="true">
        {label}
      </span>
      {children}
    </div>
  );
}

function Chip({ pressed, onClick, children }: { pressed: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      aria-pressed={pressed}
      onClick={onClick}
      className={cn(
        "inline-flex h-7 items-center rounded-full border px-2.5 text-xs font-medium transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
        pressed
          ? "border-primary/50 bg-primary/10 text-foreground"
          : "border-border bg-background text-muted-foreground hover:bg-muted/50 hover:text-foreground",
      )}
    >
      {children}
    </button>
  );
}

function RemovableChip({ label, onRemove }: { label: string; onRemove: () => void }) {
  return (
    <span className="inline-flex h-7 items-center gap-1 rounded-full border border-primary/50 bg-primary/10 pl-2.5 pr-1 text-xs font-medium text-foreground">
      {label}
      <button
        type="button"
        onClick={onRemove}
        aria-label={`Remove filter ${label}`}
        className="inline-flex h-5 w-5 items-center justify-center rounded-full text-muted-foreground hover:bg-muted/60 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      >
        <X className="h-3 w-3" aria-hidden="true" />
      </button>
    </span>
  );
}
