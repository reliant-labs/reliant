import { useState, type ReactNode } from "react";

import Input from "./input";
import Select from "./select";

interface FilterOption {
  label: string;
  value: string;
}

interface FilterDef {
  key: string;
  label: string;
  type: "select" | "search";
  options?: FilterOption[];
  placeholder?: string;
}

interface ActiveFilter {
  key: string;
  label: string;
  value: string;
  displayValue: string;
}

/**
 * FilterBar — CONTROLLED or uncontrolled, the same pair as Tabs and for the
 * same reason.
 *
 * Pass `values` and the parent owns them; omit it and the bar keeps its own.
 * Generated list pages hold their filters in the URL via
 * `useTypedSearchParams`, and a bar that owns its state cannot follow that
 * convention: a deep link, a refresh, or Back leaves the chips showing one
 * thing and the query fetching another.
 *
 *   const [params, setParams] = useTypedSearchParams(schema);
 *   <FilterBar filters={defs} values={params} onFilterChange={setParams} />
 *
 * The controls are the library's own Input and Select, so restyling those
 * restyles the bar. Both render at `sm`, the one 32px field (input.tsx's
 * header): h-8, rounded-md, the strong border, the surface fill, text-sm and
 * the accent focus ring. The search icon is 14px, 10px in from the edge, and
 * the text starts 32px in, so the search box and the selects read as one
 * family. Size controls you pass in `children` to `sm` too (inputSize="sm",
 * selectSize="sm") so the row stays one height.
 *
 * The row WRAPS: on a phone each facet drops to its own line instead of
 * pushing the page sideways. Controls the bar cannot model (an entity
 * picker, a number range) go in `children` and sit in the same row; pass `onClearAll` and `hasOtherFilters` so "Clear all" resets them too.
 */
interface FilterBarProps {
  filters: FilterDef[];
  /**
   * Controlled filter values. When set, the bar renders exactly these and
   * never self-updates — every edit goes out through `onFilterChange` and
   * comes back as new props.
   */
  values?: Record<string, string>;
  /** Initial values when uncontrolled. Ignored if `values` is set. */
  defaultValues?: Record<string, string>;
  onFilterChange: (filters: Record<string, string>) => void;
  searchPlaceholder?: string;
  /** Extra controls rendered in the same wrapping row, after the selects. */
  children?: ReactNode;
  /**
   * Replaces "Clear all"'s default (clearing this bar's own values) — pass it
   * when `children` hold filters of their own, so one click resets every one.
   */
  onClearAll?: () => void;
  /** True when a filter in `children` is set: shows "Clear all" with no chip. */
  hasOtherFilters?: boolean;
}

export default function FilterBar({
  filters,
  values: controlledValues,
  defaultValues,
  onFilterChange,
  searchPlaceholder = "Search...",
  children,
  onClearAll,
  hasOtherFilters = false,
}: FilterBarProps) {
  const [uncontrolled, setUncontrolled] = useState<Record<string, string>>(defaultValues ?? {});
  const controlled = controlledValues !== undefined;
  const values = controlledValues ?? uncontrolled;

  function commit(next: Record<string, string>) {
    if (!controlled) setUncontrolled(next);
    onFilterChange(next);
  }

  function updateFilter(key: string, value: string) {
    const next = { ...values, [key]: value };
    if (!value) delete next[key];
    commit(next);
  }

  function removeFilter(key: string) {
    const next = { ...values };
    delete next[key];
    commit(next);
  }

  function clearAll() {
    if (onClearAll) {
      if (!controlled) setUncontrolled({});
      onClearAll();
      return;
    }
    commit({});
  }

  const activeFilters: ActiveFilter[] = Object.entries(values)
    .filter(([key, v]) => v && filters.some((f) => f.key === key))
    .map(([key, value]) => {
      const def = filters.find((f) => f.key === key);
      const displayValue = def?.options?.find((o) => o.value === value)?.label ?? value;
      return { key, label: def?.label ?? key, value, displayValue };
    });

  const searchFilter = filters.find((f) => f.type === "search");
  const selectFilters = filters.filter((f) => f.type === "select");
  const showClear = activeFilters.length > 0 || hasOtherFilters;

  return (
    <div className="space-y-2">
      <div className="flex flex-wrap items-center gap-2">
        {/* Search */}
        {searchFilter && (
          <div className="relative w-full min-w-0 sm:w-64">
            <svg
              aria-hidden="true"
              className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-subtle"
              fill="none"
              stroke="currentColor"
              viewBox="0 0 24 24"
            >
              <path
                strokeLinecap="round"
                strokeLinejoin="round"
                strokeWidth={2}
                d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"
              />
            </svg>
            <Input
              type="search"
              value={values[searchFilter.key] ?? ""}
              onChange={(e) => updateFilter(searchFilter.key, e.target.value)}
              placeholder={searchFilter.placeholder ?? searchPlaceholder}
              aria-label={searchFilter.label}
              inputSize="sm"
              className="pl-8"
            />
          </div>
        )}

        {/* Select Filters */}
        {selectFilters.map((filter) => (
          <div key={filter.key} className="w-full min-w-0 sm:w-auto">
            <Select
              value={values[filter.key] ?? ""}
              onChange={(e) => updateFilter(filter.key, e.target.value)}
              aria-label={`Filter by ${filter.label.toLowerCase()}`}
              selectSize="sm"
            >
              <option value="">{`All ${filter.label.toLowerCase()}`}</option>
              {filter.options?.map((opt) => (
                <option key={opt.value} value={opt.value}>
                  {opt.label}
                </option>
              ))}
            </Select>
          </div>
        ))}

        {children}
      </div>

      {/* Active Filter Chips */}
      {showClear && (
        <div className="flex flex-wrap items-center gap-2">
          {activeFilters.map((f) => (
            <span
              key={f.key}
              className="inline-flex items-center gap-1 rounded-full bg-accent-surface py-1 pl-3 pr-1.5 text-xs font-medium text-accent-ink"
            >
              {f.label}: {f.displayValue}
              <button
                type="button"
                onClick={() => removeFilter(f.key)}
                aria-label={`Remove ${f.label} filter`}
                className="flex h-4 w-4 items-center justify-center rounded-full transition hover:bg-accent-surface focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
              >
                <svg
                  aria-hidden="true"
                  className="h-3 w-3"
                  fill="none"
                  stroke="currentColor"
                  viewBox="0 0 24 24"
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth={2}
                    d="M6 18L18 6M6 6l12 12"
                  />
                </svg>
              </button>
            </span>
          ))}
          <button
            type="button"
            onClick={clearAll}
            className="rounded text-xs font-medium text-ink-muted transition hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
          >
            Clear all
          </button>
        </div>
      )}
    </div>
  );
}
