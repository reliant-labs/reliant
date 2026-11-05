import React, { useEffect, useRef, useState } from "react";

/**
 * FilterBar: the one toolbar a list filters with. A search box first, then
 * chips, then "Clear filters", all on one 32px row:
 *
 *   <FilterBar label="Filter services" active={n} onClear={clear}>
 *     <FilterSearch value={q} onChange={setQ} placeholder="Filter services" />
 *     <FilterChip on={attention} count={2} onClick={toggle}>Needs attention</FilterChip>
 *     <FilterMenu label="Kind" value={kind} options={[{ value, label, count }]} onChange={setKind} />
 *   </FilterBar>
 *
 * WHY ITS OWN COMPONENT. Each list drew its own chips, copy-pasted, with
 * a muted label and an active state that was only a darker grey border.
 * Readers could not tell a set filter from an unset one. Here:
 *
 * - the label is ink, not muted: a filter is a control, not a caption;
 * - a SET filter is filled with the primary tint (border, background and
 *   text), so the state reads at a glance;
 * - a chip can say how many rows it keeps (`count`), and a menu's options say
 *   theirs, so the reader knows what a click will do before making it;
 * - "Clear filters" names how many filters are set.
 *
 * Sizes match the search input (h-8), so the bar never wraps a control onto a
 * half line. Self-contained, like the rest of forge-ui: no app hooks, no icon
 * library.
 */

const BASE =
  "inline-flex h-8 shrink-0 items-center gap-1.5 whitespace-nowrap rounded-md border px-2.5 text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

/** The two states: an unset filter is a plain control, a set one is filled with the primary tint. */
export function filterChipClass(on: boolean): string {
  return `${BASE} ${on ? "border-primary/50 bg-primary/10 font-medium text-primary-ink" : "border-border bg-surface text-ink hover:bg-ink/5"}`;
}

function Count({ value, on }: { value: number; on: boolean }) {
  return (
    <span
      aria-hidden="true"
      className={`min-w-[1.25rem] rounded-full px-1.5 text-center text-2xs leading-4 tabular-nums ${on ? "bg-primary/15 text-primary-ink" : "bg-ink/5 text-ink-muted"}`}
    >
      {value.toLocaleString()}
    </span>
  );
}

const Chevron = () => (
  <svg viewBox="0 0 12 12" className="h-3 w-3 opacity-70" aria-hidden="true">
    <path d="M3 4.5 6 7.5 9 4.5" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
);

const Cross = () => (
  <svg viewBox="0 0 12 12" className="h-3 w-3" aria-hidden="true">
    <path d="M3.5 3.5 8.5 8.5M8.5 3.5 3.5 8.5" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
  </svg>
);

export interface FilterBarProps {
  /** What the bar filters, for the group's accessible name ("Filter services"). */
  label: string;
  /** How many filters are set; above zero, a "Clear filters" button ends the bar. */
  active?: number;
  onClear?: () => void;
  /** Right-aligned at the end of the row: a group-by, a time range. */
  trailing?: React.ReactNode;
  className?: string;
  testId?: string;
  children: React.ReactNode;
}

export function FilterBar({ label, active = 0, onClear, trailing, className, testId, children }: FilterBarProps) {
  return (
    <div role="group" aria-label={label} className={`flex flex-wrap items-center gap-2 ${className ?? ""}`} data-testid={testId}>
      {children}
      {active > 0 && onClear && (
        <button
          type="button"
          onClick={onClear}
          className="inline-flex h-8 items-center gap-1 rounded-md px-2 text-xs font-medium text-primary-ink hover:bg-primary/10 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          data-testid={testId ? `${testId}-clear` : undefined}
        >
          Clear filters
          <span className="tabular-nums text-ink-muted">· {active}</span>
        </button>
      )}
      {trailing && <div className="ml-auto flex flex-wrap items-center gap-2">{trailing}</div>}
    </div>
  );
}

export interface FilterSearchProps {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  /** The accessible name; default the placeholder. */
  label?: string;
  testId?: string;
  className?: string;
}

/** The bar's search box: the same height as a chip, filled while it holds text. */
export function FilterSearch({ value, onChange, placeholder, label, testId, className }: FilterSearchProps) {
  return (
    <label
      className={`flex h-8 min-w-56 items-center gap-1.5 rounded-md border bg-surface px-2.5 text-ink-muted focus-within:ring-2 focus-within:ring-ring ${value ? "border-primary/50" : "border-border"} ${className ?? ""}`}
    >
      <svg viewBox="0 0 16 16" className="h-3.5 w-3.5 shrink-0" aria-hidden="true">
        <circle cx="7" cy="7" r="4.5" fill="none" stroke="currentColor" strokeWidth="1.5" />
        <path d="m10.5 10.5 3 3" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
      </svg>
      <span className="sr-only">{label ?? placeholder}</span>
      <input
        value={value}
        onChange={(event) => onChange(event.target.value)}
        placeholder={placeholder}
        className="min-w-0 flex-1 bg-transparent text-sm text-ink placeholder:text-ink-muted focus:outline-none"
        data-testid={testId}
      />
    </label>
  );
}

export interface FilterChipProps {
  on: boolean;
  onClick: () => void;
  /** How many rows the filter keeps; shown as a badge. */
  count?: number;
  title?: string;
  testId?: string;
  children: React.ReactNode;
}

/** A toggle filter. */
export function FilterChip({ on, onClick, count, title, testId, children }: FilterChipProps) {
  return (
    <button type="button" onClick={onClick} aria-pressed={on} title={title} data-testid={testId} className={filterChipClass(on)}>
      {children}
      {count !== undefined && <Count value={count} on={on} />}
    </button>
  );
}

export interface FilterMenuOption {
  value: string;
  label: string;
  /** How many rows the option keeps. */
  count?: number;
}

export interface FilterMenuProps {
  /** The dimension: "Kind", "Author". Unset, the chip reads it alone; set, "Kind: StatefulSet". */
  label: string;
  value?: string;
  options: FilterMenuOption[];
  onChange: (value: string | undefined) => void;
  testId?: string;
}

/** A pick-one filter: a chip that opens its options. A set menu clears with its ×. */
export function FilterMenu({ label, value, options, onChange, testId }: FilterMenuProps) {
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const current = options.find((option) => option.value === value);

  useEffect(() => {
    if (!open) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.stopPropagation();
        setOpen(false);
      }
    };
    const onPointer = (event: PointerEvent) => {
      if (!root.current?.contains(event.target as Node)) setOpen(false);
    };
    document.addEventListener("keydown", onKey, true);
    document.addEventListener("pointerdown", onPointer);
    return () => {
      document.removeEventListener("keydown", onKey, true);
      document.removeEventListener("pointerdown", onPointer);
    };
  }, [open]);

  return (
    <div
      ref={root}
      className="relative inline-flex"
      onBlur={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget as Node | null)) setOpen(false);
      }}
    >
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((prev) => !prev)}
        data-testid={testId}
        className={`${filterChipClass(!!current)} ${current ? "rounded-r-none border-r-0 pr-2" : ""}`}
      >
        {current ? (
          <>
            <span className="font-normal opacity-80">{label}:</span> {current.label}
          </>
        ) : (
          label
        )}
        <Chevron />
      </button>
      {current && (
        <button
          type="button"
          onClick={() => onChange(undefined)}
          aria-label={`Clear ${label.toLowerCase()} filter`}
          className="inline-flex h-8 w-7 items-center justify-center rounded-r-md border border-l-0 border-primary/50 bg-primary/10 text-primary-ink hover:bg-primary/20 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          data-testid={testId ? `${testId}-clear` : undefined}
        >
          <Cross />
        </button>
      )}
      {open && (
        <div role="menu" aria-label={label} className="absolute left-0 top-9 z-50 min-w-48 rounded-lg border border-border bg-surface p-1 shadow-md">
          {options.length === 0 && <div className="px-2 py-1.5 text-xs text-ink-muted">Nothing to filter by</div>}
          {[{ value: "", label: `Any ${label.toLowerCase()}` } as FilterMenuOption, ...options].map((option) => {
            const checked = (option.value || undefined) === value;
            return (
              <button
                key={option.value || "any"}
                type="button"
                role="menuitemradio"
                aria-checked={checked}
                onClick={() => {
                  setOpen(false);
                  onChange(option.value || undefined);
                }}
                className={`flex h-8 w-full items-center justify-between gap-3 rounded-md px-2 text-left text-sm hover:bg-ink/5 ${checked ? "font-medium text-primary-ink" : option.value ? "text-ink" : "text-ink-muted"}`}
              >
                {option.label}
                {option.count !== undefined && (
                  <span aria-hidden="true" className="text-xs tabular-nums text-ink-muted">
                    {option.count.toLocaleString()}
                  </span>
                )}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

export default FilterBar;
