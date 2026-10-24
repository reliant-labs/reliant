// Copyright (c) 2025 Reliant Labs

/**
 * The query, built: one row per dashspec series (A…J) — its metric, an
 * aggregation the metric's kind allows (allowedAggs: a counter has no
 * quantile, an up-down counter no rate), a percentile for a quantile, and its
 * own filters — then the panel-level filters, group-by (at most three keys),
 * an optional formula over the rows, the chart kind and, for a table or bar,
 * how many groups to keep.
 *
 * Every edit is a new panel handed up; the page debounces before it asks.
 * The formula is checked here with the dashboards' own parser
 * (Dashboards/formula.ts), so a typo says what is wrong before any query runs.
 */

import { useId, useState } from "react";
import { Plus, Sigma, X } from "lucide-react";

import { useAttributeValues } from "@/hooks/observability-queries";
import { useDebounce } from "@/hooks/useDebounce";
import { cn } from "@/lib/utils";
import type { MetricAttribute, MetricCatalogEntry } from "@/services/controlPlane/observability/metricsTypes";
import type { ObsScope, Panel, Predicate, SeriesIntent } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

import { parseFormula } from "../Dashboards/formula";
import { BUTTON_SMALL, Segmented } from "../Dashboards/parts";
import { allowedAggs, defaultSeriesFor, type ChartKind } from "./model/explorerState";
import { seriesLabel } from "./model/metricSemantics";
import { attributeField, fieldLabel } from "./seriesView";

/** Most series a panel holds (dashspec), so most rows. */
export const MAX_SERIES = 10;
/** Most group-by keys a metrics panel takes (obsquery MaxMetricGroupBy). */
export const MAX_GROUP_BY = 3;

const AGG_LABELS: Record<string, string> = {
  rate: "rate /s",
  increase: "increase",
  sum: "sum",
  avg: "avg",
  min: "min",
  max: "max",
  last: "last",
  count: "count",
  quantile: "percentile",
};

const QUANTILES = [0.5, 0.75, 0.9, 0.95, 0.99, 0.999];

export function quantileLabel(q: number): string {
  return `p${Math.round(q * 1000) / 10}`;
}

/** The chart kinds the builder offers. An area is drawn as a line today, so it is not offered as a separate choice. */
export const CHART_OPTIONS: { value: ChartKind; label: string }[] = [
  { value: "line", label: "Line" },
  { value: "bar", label: "Bar" },
  { value: "number", label: "Number" },
  { value: "table", label: "Table" },
];

const TOP_OPTIONS = [5, 10, 20, 50, 100];

/** Why the formula cannot run, or null when it can (or there is none). */
export function formulaProblem(panel: Pick<Panel, "formula" | "series">): string | null {
  const formula = panel.formula?.trim();
  if (!formula) return null;
  try {
    parseFormula(formula, panel.series.length);
    return null;
  } catch (error) {
    return (error as Error).message;
  }
}

export interface QueryBuilderProps {
  panel: Panel;
  chart: ChartKind;
  catalog: readonly MetricCatalogEntry[];
  /** The open metric's attributes: what filters and group-by can name. */
  attributes: readonly MetricAttribute[];
  scope: ObsScope | null;
  range: RangePreset;
  onPanel: (next: Panel) => void;
  onChart: (next: ChartKind) => void;
}

const SELECT =
  "h-7 min-w-0 rounded-md border border-border bg-background px-1.5 text-xs text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-accent";

export function QueryBuilder({ panel, chart, catalog, attributes, scope, range, onPanel, onChart }: QueryBuilderProps) {
  const entryOf = (name: string | undefined) => catalog.find((m) => m.name === name);
  const [formulaOpen, setFormulaOpen] = useState(!!panel.formula);
  const formulaId = useId();
  const problem = formulaProblem(panel);
  const fields = attributes.map(attributeField);

  const setSeries = (series: SeriesIntent[]) => onPanel({ ...panel, series: series.map((s, i) => ({ ...s, label: seriesLabel(i) })) });
  const replaceRow = (index: number, next: SeriesIntent) => setSeries(panel.series.map((s, i) => (i === index ? next : s)));
  const addRow = () => {
    const last = entryOf(panel.series[panel.series.length - 1]?.field);
    setSeries([...panel.series, last ? defaultSeriesFor(last, seriesLabel(panel.series.length)) : { label: seriesLabel(panel.series.length), agg: "" }]);
  };

  return (
    <div className="space-y-3" data-testid="metrics-builder">
      <ol className="space-y-2" aria-label="Queries">
        {panel.series.map((series, index) => (
          <SeriesRow
            key={index}
            series={series}
            entry={entryOf(series.field)}
            catalog={catalog}
            fields={fields}
            scope={scope}
            range={range}
            onChange={(next) => replaceRow(index, next)}
            onRemove={panel.series.length > 1 ? () => setSeries(panel.series.filter((_, i) => i !== index)) : undefined}
          />
        ))}
      </ol>

      {(formulaOpen || panel.formula) && (
        <div className="flex flex-wrap items-start gap-2" data-testid="metrics-formula-row">
          <span className="inline-flex h-7 w-6 items-center justify-center rounded bg-ink/5 font-mono text-xs text-ink" aria-hidden="true">
            ƒ
          </span>
          <div className="min-w-0 flex-1">
            <label htmlFor={formulaId} className="sr-only">
              Formula over the queries
            </label>
            <input
              id={formulaId}
              value={panel.formula ?? ""}
              onChange={(event) => onPanel({ ...panel, formula: event.target.value || undefined })}
              placeholder="B / A * 100"
              spellCheck={false}
              aria-invalid={!!problem}
              aria-describedby={problem ? `${formulaId}-problem` : undefined}
              className={cn(SELECT, "h-7 w-full font-mono", problem && "border-danger-ink/60")}
              data-testid="metrics-formula"
            />
            {problem ? (
              <p id={`${formulaId}-problem`} className="mt-1 text-xs text-danger-ink" data-testid="metrics-formula-problem">
                {problem}
              </p>
            ) : (
              <p className="mt-1 text-xs text-ink-muted">A is the first query, B the second. Only the formula is drawn.</p>
            )}
          </div>
          <button
            type="button"
            className={ICON}
            aria-label="Remove the formula"
            onClick={() => {
              setFormulaOpen(false);
              onPanel({ ...panel, formula: undefined });
            }}
          >
            <X className="h-3.5 w-3.5" aria-hidden="true" />
          </button>
        </div>
      )}

      <div className="flex flex-wrap items-center gap-2">
        <button type="button" className={BUTTON_SMALL} onClick={addRow} disabled={panel.series.length >= MAX_SERIES} data-testid="metrics-add-query">
          <Plus className="h-3 w-3" aria-hidden="true" />
          Query
        </button>
        {!formulaOpen && !panel.formula && (
          <button type="button" className={BUTTON_SMALL} onClick={() => setFormulaOpen(true)} disabled={panel.series.length === 0} data-testid="metrics-add-formula">
            <Sigma className="h-3 w-3" aria-hidden="true" />
            Formula
          </button>
        )}
      </div>

      <div className="space-y-2 border-t border-border/70 pt-3">
        <WhereEditor
          label="Where"
          where={panel.where ?? []}
          fields={fields}
          scope={scope}
          range={range}
          onChange={(where) => onPanel({ ...panel, where: where.length ? where : undefined })}
          testId="metrics-where"
        />
        <GroupByEditor fields={fields} groupBy={panel.groupBy ?? []} onChange={(groupBy) => onPanel({ ...panel, groupBy: groupBy.length ? groupBy : undefined })} />
      </div>

      <div className="flex flex-wrap items-center gap-2 border-t border-border/70 pt-3">
        <Segmented label="Chart" value={chart === "area" ? "line" : chart} options={CHART_OPTIONS} onChange={onChart} testId="metrics-chart-kind" />
        {(chart === "table" || chart === "bar") && (
          <label className="inline-flex items-center gap-1.5 text-xs text-ink-muted">
            Top
            <select
              value={panel.limit ?? ""}
              onChange={(event) => onPanel({ ...panel, limit: event.target.value ? Number(event.target.value) : undefined })}
              className={SELECT}
              data-testid="metrics-top"
            >
              <option value="">all</option>
              {TOP_OPTIONS.map((n) => (
                <option key={n} value={n}>
                  {n}
                </option>
              ))}
            </select>
          </label>
        )}
      </div>
    </div>
  );
}

const ICON =
  "inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-ink-muted transition-colors hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent";

function SeriesRow({
  series,
  entry,
  catalog,
  fields,
  scope,
  range,
  onChange,
  onRemove,
}: {
  series: SeriesIntent;
  entry?: MetricCatalogEntry;
  catalog: readonly MetricCatalogEntry[];
  fields: string[];
  scope: ObsScope | null;
  range: RangePreset;
  onChange: (next: SeriesIntent) => void;
  onRemove?: () => void;
}) {
  const id = useId();
  const aggs = entry ? allowedAggs(entry) : series.agg ? [series.agg] : [];
  const options = catalog.filter((m) => m.queryable);
  const unlisted = series.field && !options.some((m) => m.name === series.field);
  const quantile = series.quantile ?? 0.95;
  return (
    <li className="space-y-1.5" data-testid={`metrics-query-${series.label}`} data-label={series.label}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="inline-flex h-7 w-6 items-center justify-center rounded bg-ink/5 font-mono text-xs font-medium text-ink" aria-hidden="true">
          {series.label}
        </span>
        <label htmlFor={`${id}-metric`} className="sr-only">
          Query {series.label} metric
        </label>
        <select
          id={`${id}-metric`}
          value={series.field ?? ""}
          onChange={(event) => {
            const next = catalog.find((m) => m.name === event.target.value);
            if (next) onChange({ ...defaultSeriesFor(next, series.label), where: series.where });
          }}
          className={cn(SELECT, "max-w-[18rem] flex-1 font-mono")}
          data-testid={`metrics-query-${series.label}-metric`}
        >
          {!series.field && <option value="">Pick a metric</option>}
          {unlisted && <option value={series.field}>{series.field}</option>}
          {options.map((m) => (
            <option key={`${m.name}/${m.kind}`} value={m.name}>
              {m.name}
            </option>
          ))}
        </select>
        <label htmlFor={`${id}-agg`} className="sr-only">
          Query {series.label} aggregation
        </label>
        <select
          id={`${id}-agg`}
          value={series.agg}
          onChange={(event) => {
            const agg = event.target.value;
            onChange({ ...series, agg, quantile: agg === "quantile" ? quantile : undefined });
          }}
          disabled={aggs.length === 0}
          className={SELECT}
          data-testid={`metrics-query-${series.label}-agg`}
        >
          {aggs.map((agg) => (
            <option key={agg} value={agg}>
              {AGG_LABELS[agg] ?? agg}
            </option>
          ))}
        </select>
        {series.agg === "quantile" && (
          <>
            <label htmlFor={`${id}-q`} className="sr-only">
              Query {series.label} percentile
            </label>
            <select
              id={`${id}-q`}
              value={String(quantile)}
              onChange={(event) => onChange({ ...series, quantile: Number(event.target.value) })}
              className={SELECT}
              data-testid={`metrics-query-${series.label}-quantile`}
            >
              {(QUANTILES.includes(quantile) ? QUANTILES : [...QUANTILES, quantile].sort((a, b) => a - b)).map((q) => (
                <option key={q} value={String(q)}>
                  {quantileLabel(q)}
                </option>
              ))}
            </select>
          </>
        )}
        {onRemove && (
          <button type="button" className={ICON} onClick={onRemove} aria-label={`Remove query ${series.label}`} data-testid={`metrics-query-${series.label}-remove`}>
            <X className="h-3.5 w-3.5" aria-hidden="true" />
          </button>
        )}
      </div>
      <div className="pl-8">
        <WhereEditor
          label={`Where (${series.label})`}
          compact
          where={series.where ?? []}
          fields={fields}
          scope={scope}
          range={range}
          onChange={(where) => onChange({ ...series, where: where.length ? where : undefined })}
          testId={`metrics-query-${series.label}-where`}
        />
      </div>
    </li>
  );
}

const OPS: { value: string; label: string }[] = [
  { value: "eq", label: "=" },
  { value: "neq", label: "≠" },
];

function predicateText(p: Predicate): string {
  const op = OPS.find((o) => o.value === p.op)?.label ?? p.op;
  return `${fieldLabel(p.field)} ${op} ${p.value ?? (p.values ?? []).join(", ")}`;
}

/** Filters as chips, and a small form to add one: an attribute of the metric, = or ≠, and a value (autocompleted). */
function WhereEditor({
  label,
  where,
  fields,
  scope,
  range,
  onChange,
  compact = false,
  testId,
}: {
  label: string;
  where: Predicate[];
  fields: string[];
  scope: ObsScope | null;
  range: RangePreset;
  onChange: (where: Predicate[]) => void;
  compact?: boolean;
  testId: string;
}) {
  const [adding, setAdding] = useState(false);
  const [field, setField] = useState("");
  const [op, setOp] = useState("eq");
  const [value, setValue] = useState("");
  const id = useId();
  const key = field || fields[0] || "";
  const prefix = useDebounce(value, 200);
  const values = useAttributeValues(adding ? scope : null, range, fieldLabel(key), prefix, "metrics");
  const add = () => {
    if (!key || !value.trim()) return;
    onChange([...where, { field: key, op, value: value.trim() }]);
    setAdding(false);
    setValue("");
  };
  return (
    <div className={cn("flex flex-wrap items-center gap-1.5", compact && "min-h-0")} data-testid={testId} role="group" aria-label={label}>
      {!compact && <span className="w-16 shrink-0 text-xs text-ink-muted">Where</span>}
      {where.map((p, i) => (
        <span key={`${p.field}-${i}`} className="inline-flex h-6 items-center gap-1 rounded-md border border-border bg-background pl-2 text-xs text-ink">
          <span className="font-mono">{predicateText(p)}</span>
          <button
            type="button"
            onClick={() => onChange(where.filter((_, j) => j !== i))}
            className="inline-flex h-5 w-5 items-center justify-center rounded text-ink-muted hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
            aria-label={`Remove filter ${predicateText(p)}`}
          >
            <X className="h-3 w-3" aria-hidden="true" />
          </button>
        </span>
      ))}
      {adding ? (
        <span className="inline-flex flex-wrap items-center gap-1">
          <label htmlFor={`${id}-key`} className="sr-only">
            Attribute
          </label>
          {fields.length > 0 ? (
            <select id={`${id}-key`} value={key} onChange={(event) => setField(event.target.value)} className={cn(SELECT, "font-mono")} data-testid={`${testId}-key`}>
              {fields.map((f) => (
                <option key={f} value={f}>
                  {fieldLabel(f)}
                </option>
              ))}
            </select>
          ) : (
            <input
              id={`${id}-key`}
              value={field}
              onChange={(event) => setField(event.target.value)}
              placeholder="metric.attr.status"
              className={cn(SELECT, "w-40 font-mono")}
              data-testid={`${testId}-key`}
            />
          )}
          <label htmlFor={`${id}-op`} className="sr-only">
            Operator
          </label>
          <select id={`${id}-op`} value={op} onChange={(event) => setOp(event.target.value)} className={SELECT}>
            {OPS.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
          <label htmlFor={`${id}-value`} className="sr-only">
            Value
          </label>
          <input
            id={`${id}-value`}
            value={value}
            list={`${id}-values`}
            onChange={(event) => setValue(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter") {
                event.preventDefault();
                add();
              }
              if (event.key === "Escape") setAdding(false);
            }}
            autoFocus
            className={cn(SELECT, "w-32")}
            data-testid={`${testId}-value`}
          />
          <datalist id={`${id}-values`}>
            {(values.data?.values ?? []).map((v) => (
              <option key={v.value} value={v.value} />
            ))}
          </datalist>
          <button type="button" className={BUTTON_SMALL} onClick={add} disabled={!key || !value.trim()} data-testid={`${testId}-apply`}>
            Add
          </button>
          <button type="button" className={ICON} onClick={() => setAdding(false)} aria-label="Cancel the filter">
            <X className="h-3.5 w-3.5" aria-hidden="true" />
          </button>
        </span>
      ) : (
        <button
          type="button"
          onClick={() => setAdding(true)}
          className="inline-flex h-6 items-center gap-1 rounded-md px-1.5 text-xs text-ink-muted hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
          data-testid={`${testId}-add`}
        >
          <Plus className="h-3 w-3" aria-hidden="true" />
          {compact ? "filter" : "Add filter"}
        </button>
      )}
    </div>
  );
}

function GroupByEditor({ fields, groupBy, onChange }: { fields: string[]; groupBy: string[]; onChange: (groupBy: string[]) => void }) {
  const id = useId();
  const available = fields.filter((f) => !groupBy.includes(f));
  return (
    <div className="flex flex-wrap items-center gap-1.5" role="group" aria-label="Group by" data-testid="metrics-group-by">
      <span className="w-16 shrink-0 text-xs text-ink-muted">Group by</span>
      {groupBy.map((field) => (
        <span key={field} className="inline-flex h-6 items-center gap-1 rounded-md border border-border bg-background pl-2 text-xs font-mono text-ink">
          {fieldLabel(field)}
          <button
            type="button"
            onClick={() => onChange(groupBy.filter((f) => f !== field))}
            className="inline-flex h-5 w-5 items-center justify-center rounded text-ink-muted hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
            aria-label={`Stop grouping by ${fieldLabel(field)}`}
          >
            <X className="h-3 w-3" aria-hidden="true" />
          </button>
        </span>
      ))}
      {groupBy.length < MAX_GROUP_BY && available.length > 0 && (
        <>
          <label htmlFor={id} className="sr-only">
            Add a group-by key
          </label>
          <select
            id={id}
            value=""
            onChange={(event) => event.target.value && onChange([...groupBy, event.target.value])}
            className={cn(SELECT, "font-mono text-ink-muted")}
            data-testid="metrics-group-by-add"
          >
            <option value="">+ key</option>
            {available.map((f) => (
              <option key={f} value={f}>
                {fieldLabel(f)}
              </option>
            ))}
          </select>
        </>
      )}
    </div>
  );
}
