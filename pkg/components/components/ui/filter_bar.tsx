import { useState } from "react";

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
}

export default function FilterBar({
  filters,
  values: controlledValues,
  defaultValues,
  onFilterChange,
  searchPlaceholder = "Search...",
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
    commit({});
  }

  const activeFilters: ActiveFilter[] = Object.entries(values)
    .filter(([, v]) => v)
    .map(([key, value]) => {
      const def = filters.find((f) => f.key === key);
      const displayValue = def?.options?.find((o) => o.value === value)?.label ?? value;
      return { key, label: def?.label ?? key, value, displayValue };
    });

  const searchFilter = filters.find((f) => f.type === "search");
  const selectFilters = filters.filter((f) => f.type === "select");

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-3">
        {/* Search */}
        {searchFilter && (
          <div className="relative min-w-[200px] flex-1">
            <svg
              className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-ink-subtle"
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
            <input
              type="text"
              value={values[searchFilter.key] ?? ""}
              onChange={(e) => updateFilter(searchFilter.key, e.target.value)}
              placeholder={searchFilter.placeholder ?? searchPlaceholder}
              className="w-full rounded-lg border border-border-strong bg-surface py-2 pl-9 pr-3 text-sm placeholder:text-ink-subtle focus:border-accent-border focus:outline-none focus:ring-1 focus:ring-accent"
            />
          </div>
        )}

        {/* Select Filters */}
        {selectFilters.map((filter) => (
          <select
            key={filter.key}
            value={values[filter.key] ?? ""}
            onChange={(e) => updateFilter(filter.key, e.target.value)}
            className="rounded-lg border border-border-strong bg-surface px-3 py-2 text-sm focus:border-accent-border focus:outline-none focus:ring-1 focus:ring-accent"
          >
            <option value="">{filter.label}</option>
            {filter.options?.map((opt) => (
              <option key={opt.value} value={opt.value}>
                {opt.label}
              </option>
            ))}
          </select>
        ))}
      </div>

      {/* Active Filter Chips */}
      {activeFilters.length > 0 && (
        <div className="flex flex-wrap items-center gap-2">
          {activeFilters.map((f) => (
            <span
              key={f.key}
              className="inline-flex items-center gap-1 rounded-full bg-accent-surface py-1 pl-3 pr-1.5 text-xs font-medium text-accent-ink"
            >
              {f.label}: {f.displayValue}
              <button
                onClick={() => removeFilter(f.key)}
                aria-label={`Remove ${f.label} filter`}
                className="flex h-4 w-4 items-center justify-center rounded-full transition hover:bg-accent-surface"
              >
                <svg className="h-3 w-3" fill="none" stroke="currentColor" viewBox="0 0 24 24">
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
            onClick={clearAll}
            className="text-xs font-medium text-ink-muted transition hover:text-ink-muted"
          >
            Clear all
          </button>
        </div>
      )}
    </div>
  );
}
