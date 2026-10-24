// Copyright (c) 2025 Reliant Labs

/**
 * The breakdown: the same query once per value of one attribute, as small
 * multiples — the heaviest BREAKDOWN_TOP values, each its own chart on its own
 * scale. It is the query-less drill (Grafana's "drilldown"): see which value
 * the shape comes from, then keep only it.
 *
 * One request (breakdownPanel: the panel grouped by the key), split per value
 * here; a line panel has no `limit` in dashspec, so the server's series cap
 * applies and the first values it returns are shown (topSeries).
 */

import { Filter, X } from "lucide-react";

import { usePanelQuery } from "@/hooks/observability-queries";
import type { ObsScope, Panel } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

import { UPlotChart } from "../charts/UPlotChart";
import { alignTo, unionTimestamps } from "../Dashboards/PanelBody";
import { BUTTON_SMALL } from "../Dashboards/parts";
import { formatCount, type ValueUnit } from "../format";
import { ObsError, ObsLoadingRows } from "../ObsStates";
import { BREAKDOWN_TOP, breakdownPanel, topSeries } from "./model/explorerState";
import { capNote, fieldLabel, seriesByValue } from "./seriesView";
import { TONES } from "./ExplorerChart";

export interface BreakdownProps {
  /** The explorer's (debounced) query, as a line panel. */
  panel: Panel;
  field: string;
  scope: ObsScope | null;
  range: RangePreset;
  unit: ValueUnit;
  onClose: () => void;
  /** Keep only this value: a filter on the panel. */
  onFilter: (field: string, value: string) => void;
  subject: string;
}

export function Breakdown({ panel, field, scope, range, unit, onClose, onFilter, subject }: BreakdownProps) {
  const query = usePanelQuery(scope, range, breakdownPanel({ ...panel, kind: "line" }, field, BREAKDOWN_TOP), []);
  const groups = query.data ? topSeries(seriesByValue(query.data.series, field), BREAKDOWN_TOP) : [];
  const key = fieldLabel(field);
  const cap = query.data ? capNote(query.data) : null;
  return (
    <section aria-labelledby="metrics-breakdown-heading" className="space-y-3" data-testid="metrics-breakdown" data-field={field}>
      <div className="flex items-center justify-between gap-2">
        <h2 id="metrics-breakdown-heading" className="text-sm font-medium text-ink">
          Breakdown by <span className="font-mono">{key}</span>
        </h2>
        <button type="button" onClick={onClose} className={BUTTON_SMALL} data-testid="metrics-breakdown-close">
          <X className="h-3 w-3" aria-hidden="true" />
          Close
        </button>
      </div>
      {query.isLoading ? (
        <ObsLoadingRows rows={3} testId="metrics-breakdown-loading" />
      ) : query.error ? (
        <ObsError error={query.error} subject={subject} onRetry={() => void query.refetch()} retrying={query.isFetching} />
      ) : groups.length === 0 ? (
        <p className="text-xs text-ink-muted" data-testid="metrics-breakdown-empty">
          No values of {key} in this window.
        </p>
      ) : (
        <>
          <ul className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4" data-testid="metrics-breakdown-grid">
            {groups.map((group) => {
              const timestamps = unionTimestamps(group.series);
              return (
                <li key={group.value} className="min-w-0 rounded-md border border-border/60 bg-background p-2.5" data-value={group.value}>
                  <div className="mb-1 flex items-center justify-between gap-2">
                    <span className="truncate font-mono text-xs text-ink" title={group.value}>
                      {group.value || "(none)"}
                    </span>
                    {group.value && (
                      <button
                        type="button"
                        onClick={() => onFilter(field, group.value)}
                        className="inline-flex h-6 shrink-0 items-center gap-1 rounded px-1.5 text-2xs text-ink-muted hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
                        aria-label={`Keep only ${key} = ${group.value}`}
                        data-testid="metrics-breakdown-filter"
                      >
                        <Filter className="h-3 w-3" aria-hidden="true" />
                        Only this
                      </button>
                    )}
                  </div>
                  <UPlotChart
                    height={88}
                    timestamps={timestamps}
                    series={group.series.map((s, i) => ({ label: s.label, values: alignTo(timestamps, s), tone: TONES[i % TONES.length] }))}
                    unit={unit}
                    label={`${panel.title || "Metrics"} where ${key} is ${group.value || "unset"}`}
                    legend={false}
                  />
                </li>
              );
            })}
          </ul>
          {cap && cap.total > groups.length && (
            <p className="text-xs text-ink-muted">
              The {groups.length} values shown are of {formatCount(cap.total)}. Filter the query to reach the others.
            </p>
          )}
        </>
      )}
    </section>
  );
}
