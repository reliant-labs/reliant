// Copyright (c) 2025 Reliant Labs

/**
 * One metric, described (GetMetric): what it is (kind, unit, temporality,
 * export interval, stability, who sends it), what it says it measures, and its
 * attributes with how many values each takes — the cardinality a group-by
 * would multiply. Each attribute can be grouped by or broken down; a key with
 * too many values to group by safely is marked, and still allowed (the series
 * cap keeps the heaviest).
 *
 * NotFound is not a failure here: it is the answer "nothing in this window".
 */

import { Clock, Layers, LayoutGrid } from "lucide-react";

import Tag from "@/components/forge-ui/tag";
import Button from "@/components/forge-ui/button";
import type { MetricCatalogEntry, MetricDetail as MetricDetailModel } from "@/services/controlPlane/observability/metricsTypes";
import { RANGE_LABELS, type RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { formatCount } from "../format";
import { ObsError, ObsLoadingRows } from "../ObsStates";
import { attributeField, isNoDataInWindow, kindLabel, orderedAttributes, unitLabel } from "./seriesView";

export interface MetricDetailProps {
  name: string;
  /** The catalog's entry, when the catalog lists it: shown while the detail loads, and when the window has none. */
  entry?: MetricCatalogEntry;
  detail: MetricDetailModel | undefined;
  isLoading: boolean;
  error: unknown;
  onRetry: () => void;
  range: RangePreset;
  onWiden?: () => void;
  /** The panel's group-by fields, so an attribute already grouped says so. */
  groupBy: readonly string[];
  onGroupBy: (field: string) => void;
  breakdownKey?: string;
  onBreakdown: (field: string) => void;
  subject: string;
}

export function MetricDetail(props: MetricDetailProps) {
  const { name, entry, detail, isLoading, error, range } = props;
  const info = detail?.info ?? entry;
  return (
    <section aria-labelledby="metric-detail-heading" className="space-y-3" data-testid="metric-detail" data-metric={name}>
      <div className="min-w-0">
        <h2 id="metric-detail-heading" className="truncate font-mono text-sm font-medium text-ink" title={name}>
          {name}
        </h2>
        {info && (
          <p className="mt-0.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-ink-muted">
            <span>{kindLabel(info)}</span>
            <span aria-hidden="true">·</span>
            <span>{unitLabel(info.unit)}</span>
            {info.temporality !== "unknown" && (
              <>
                <span aria-hidden="true">·</span>
                <span>{info.temporality}</span>
              </>
            )}
            {detail && detail.intervalSeconds > 0 && (
              <>
                <span aria-hidden="true">·</span>
                <span title="Estimated export interval: the smallest useful step">every {detail.intervalSeconds}s</span>
              </>
            )}
            {info.stability && <Tag tone={info.stability === "deprecated" ? "warning" : "neutral"}>{info.stability}</Tag>}
          </p>
        )}
        {info?.description && <p className="mt-2 text-xs text-ink">{info.description}</p>}
        {info && info.services.length > 0 && (
          <p className="mt-1 text-xs text-ink-muted">
            Sent by {info.services.join(", ")}
            {info.serviceCount > info.services.length ? ` and ${info.serviceCount - info.services.length} more` : ""}
          </p>
        )}
        {detail?.mixedTemporality && (
          <p className="mt-1 text-xs text-warning-ink">Some services send it as deltas and others cumulatively; the explorer reads each correctly, but compare them with care.</p>
        )}
      </div>

      {isLoading ? (
        <ObsLoadingRows rows={4} testId="metric-detail-loading" />
      ) : isNoDataInWindow(error) ? (
        <NoDataInWindow name={name} entry={entry} range={range} onWiden={props.onWiden} />
      ) : error ? (
        <ObsError error={error} subject={props.subject} onRetry={props.onRetry} />
      ) : detail ? (
        <Attributes {...props} detail={detail} />
      ) : null}
    </section>
  );
}

function NoDataInWindow({ name, entry, range, onWiden }: { name: string; entry?: MetricCatalogEntry; range: RangePreset; onWiden?: () => void }) {
  const window = RANGE_LABELS[range].toLowerCase();
  return (
    <div className="flex items-start gap-3 rounded-lg border border-dashed border-border bg-background px-3 py-3" role="status" data-testid="metric-no-data">
      <Clock className="mt-0.5 h-4 w-4 shrink-0 text-ink-muted" aria-hidden="true" />
      <div className="min-w-0 space-y-1.5 text-xs">
        <p className="font-medium text-ink">
          No data for <span className="font-mono">{name}</span> in the {window}
        </p>
        <p className="text-ink-muted">
          {entry?.declared && !entry.seen
            ? "The release declares it, but nothing has sent it. A code path that never ran, or a name that differs from the code's, looks like this."
            : "A metric whose service was idle, or deployed after the window began, looks like this."}
        </p>
        {onWiden && range !== "7d" && (
          <Button variant="ghost" size="sm" onClick={onWiden} data-testid="metric-no-data-widen">
            Look at the last 7 days
          </Button>
        )}
      </div>
    </div>
  );
}

function Attributes({ detail, groupBy, onGroupBy, breakdownKey, onBreakdown }: MetricDetailProps & { detail: MetricDetailModel }) {
  const attributes = orderedAttributes(detail.attributes, detail.suggestedBreakdowns);
  if (attributes.length === 0) {
    return <p className="text-xs text-ink-muted">It carries no attributes: one series per service.</p>;
  }
  return (
    <div>
      <h3 className="mb-1.5 text-xs font-medium text-ink">Attributes</h3>
      <ul className="divide-y divide-border/70 rounded-md border border-border/60 bg-background" data-testid="metric-attributes">
        {attributes.map((attribute) => {
          const field = attributeField(attribute);
          const grouped = groupBy.includes(field);
          const suggested = detail.suggestedBreakdowns.includes(attribute.key);
          return (
            <li key={`${attribute.resource ? "r" : "m"}:${attribute.key}`} className="px-2.5 py-2" data-attribute={attribute.key}>
              <div className="flex items-center gap-2 text-xs">
                <span className="min-w-0 flex-1 truncate font-mono text-ink" title={field}>
                  {attribute.key}
                </span>
                <span className="shrink-0 tabular-nums text-ink-muted" title="Estimated distinct values">
                  ~{formatCount(attribute.distinctEstimate)}
                </span>
              </div>
              <div className="mt-1 flex flex-wrap items-center gap-1.5">
                {attribute.resource && <Tag title="A resource attribute: set per process, not per data point">resource</Tag>}
                {attribute.highCardinality && (
                  <Tag tone="warning" title="Grouping by it could exceed the series cap; only the heaviest series would be kept">
                    high cardinality
                  </Tag>
                )}
                {suggested && !attribute.highCardinality && <Tag title="A good key to break this metric down by">suggested</Tag>}
                {attribute.enumerated.length > 0 && (
                  <span className="truncate text-2xs text-ink-muted" title={attribute.enumerated.join(", ")}>
                    {attribute.enumerated.slice(0, 4).join(" · ")}
                    {attribute.enumerated.length > 4 ? " …" : ""}
                  </span>
                )}
                <span className="ml-auto inline-flex gap-1">
                  <button
                    type="button"
                    onClick={() => onGroupBy(field)}
                    disabled={grouped || groupBy.length >= 3}
                    title={grouped ? "Already grouped by it" : groupBy.length >= 3 ? "A panel groups by at most three keys" : `Group the query by ${attribute.key}`}
                    className={ATTR_BUTTON}
                    aria-label={`Group by ${attribute.key}`}
                    data-testid={`metric-attr-group-${attribute.key}`}
                  >
                    <Layers className="h-3 w-3" aria-hidden="true" />
                    Group
                  </button>
                  <button
                    type="button"
                    onClick={() => onBreakdown(field)}
                    aria-pressed={breakdownKey === field}
                    title={`One small chart per value of ${attribute.key}`}
                    className={cn(ATTR_BUTTON, breakdownKey === field && "bg-ink/8 text-ink")}
                    aria-label={`Break down by ${attribute.key}`}
                    data-testid={`metric-attr-breakdown-${attribute.key}`}
                  >
                    <LayoutGrid className="h-3 w-3" aria-hidden="true" />
                    Break down
                  </button>
                </span>
              </div>
            </li>
          );
        })}
      </ul>
    </div>
  );
}

const ATTR_BUTTON =
  "inline-flex h-6 items-center gap-1 rounded px-1.5 text-2xs text-ink-muted transition-colors hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent disabled:cursor-not-allowed disabled:opacity-50";
