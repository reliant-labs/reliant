// Copyright (c) 2026 Reliant Labs

/**
 * How much a detail page (one issue, one trace) shows unasked. The console
 * opens every panel; a host may fold the less-used ones behind their title
 * instead (`folded`, by panel id) and set the triage controls on one row
 * (`inlineTriage`). Nothing provides it by default, so the console's detail
 * pages are unchanged; the led console does (Console/ledConsole.tsx).
 *
 * Panel ids: issue-discussion, issue-events, issue-release, issue-versions,
 * issue-related, issue-history, trace-toolbar (an open trace's list filters),
 * trace-related, session-details.
 */

import { createContext, useContext, type ReactNode } from "react";
import { ChevronRight } from "lucide-react";

export interface DetailDensity {
  /** Panels drawn folded, by id: only their title shows until opened. */
  folded?: readonly string[];
  /** An issue's status and assignee controls on one row. */
  inlineTriage?: boolean;
}

const DetailDensityContext = createContext<DetailDensity>({});

export function DetailDensityProvider({ value, children }: { value: DetailDensity; children: ReactNode }) {
  return <DetailDensityContext.Provider value={value}>{children}</DetailDensityContext.Provider>;
}

export function useDetailDensity(): DetailDensity {
  return useContext(DetailDensityContext);
}

/**
 * A less-used panel: as it is, unless the host folds `id`; then a one-line
 * disclosure with its title (and `hint`), and the panel under it once opened.
 */
export function Foldable({ id, title, hint, children }: { id: string; title: string; hint?: string; children: ReactNode }) {
  const { folded } = useDetailDensity();
  if (!folded?.includes(id)) return <>{children}</>;
  return (
    <details className="group/fold" data-testid={`fold-${id}`}>
      <summary className="flex min-h-9 cursor-pointer list-none items-center gap-2 rounded-lg border border-border bg-surface px-3 text-sm text-ink hover:bg-ink/[0.03] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
        <ChevronRight className="h-4 w-4 shrink-0 text-ink-muted transition-transform group-open/fold:rotate-90" aria-hidden="true" />
        <span className="font-medium">{title}</span>
        {hint && <span className="min-w-0 truncate text-xs text-ink-muted">{hint}</span>}
      </summary>
      <div className="mt-2">{children}</div>
    </details>
  );
}
