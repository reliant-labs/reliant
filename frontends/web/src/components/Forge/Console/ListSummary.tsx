// Copyright (c) 2026 Reliant Labs

/**
 * One sentence above a list that says what is in it ("1 open issue in the
 * last hour: 1 new"), for a relaxed console (forge-ui TableDensityProvider;
 * the public demo's tidied console). The console's own density shows none:
 * its tables and their filters say it.
 */

import { useState, type ReactNode } from "react";
import { SlidersHorizontal } from "lucide-react";

import { useTableDensity } from "@/components/forge-ui/data_table";
import { RANGE_LABELS, type RangePreset } from "@/services/controlPlane/observability/window";

export function ListSummary({ children, testId }: { children: ReactNode; testId?: string }) {
  if (useTableDensity() !== "relaxed") return null;
  return (
    <p className="text-base leading-relaxed text-ink" data-testid={testId ?? "list-summary"}>
      {children}
    </p>
  );
}

/**
 * A relaxed list keeps its first-rank filters in view and folds the rest
 * (user id, browser, OS, build…) behind "More filters". `active`: one of
 * the folded filters is set, so they show regardless. At the console's own
 * density every filter shows and there is no button.
 */
export function useMoreFilters(active: boolean): { show: boolean; button: ReactNode } {
  const relaxed = useTableDensity() === "relaxed";
  const [open, setOpen] = useState(false);
  if (!relaxed) return { show: true, button: null };
  const show = open || active;
  return {
    show,
    button: (
      <button
        type="button"
        aria-expanded={show}
        onClick={() => setOpen(!show)}
        className="inline-flex h-8 items-center gap-1.5 rounded-md border border-border bg-surface px-3 text-sm font-medium text-ink transition-colors hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        data-testid="more-filters"
      >
        <SlidersHorizontal className="h-3.5 w-3.5 text-ink-muted" aria-hidden="true" />
        {show ? "Fewer filters" : "More filters"}
      </button>
    ),
  };
}

/** "in the last hour", for a summary sentence. */
export function inRange(range: RangePreset): string {
  return `in the ${RANGE_LABELS[range].replace(/^Last /, "last ").toLowerCase()}`;
}

/** "1 issue", "3 issues". */
export function plural(count: number, noun: string, many = `${noun}s`): string {
  return `${count.toLocaleString()} ${count === 1 ? noun : many}`;
}
