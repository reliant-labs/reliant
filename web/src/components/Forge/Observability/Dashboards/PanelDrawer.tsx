// Copyright (c) 2025 Reliant Labs

/**
 * The add / edit panel drawer (decision DB): a visual builder over forge's
 * panel spec, the spec itself as JSON, and a live preview of the panel over
 * the dashboard's scope, window and service — the same PanelBody the grid
 * draws, so what the preview shows is what the dashboard will.
 *
 *   Builder   visualization (time series, stat, table, funnel, issues,
 *             sessions, rows); signal; series with an aggregation preset
 *             (count, p50 / p95 / p99, avg, max, sum, distinct), a field and
 *             "errors only"; a ratio (A / B as a percentage); filters;
 *             group-by (up to three); funnel steps, window and journey;
 *             unit; width.
 *   Spec      the panel exactly as forge stores it, editable. The server
 *             compiles it into its query (same engine and limits as alert
 *             rules); the console never sends SQL.
 *
 * Saving writes a new version of the workspace dashboard. A read-only
 * dashboard opens the drawer in view mode: the spec and the preview, no
 * save.
 */

import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { Plus, Trash2, X } from "lucide-react";

import { usePanelQuery } from "@/hooks/observability-queries";
import { useEscapeLayer } from "@/hooks/useEscapeLayer";
import type { ObsScope, Panel, Predicate, SeriesIntent, Unit } from "@/services/controlPlane/observability/types";
import type { ObsEnvironment } from "../obsPlace";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { RANGE_LABELS } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { FIELD_SUGGESTIONS, METRIC_TYPES, OP_LABELS, OPS, panelProblems, UNITS } from "../obsdashSpec";
import { GRID_SPANS, spanOf, widthOfSpan } from "./dashboardModel";
import {
  addFilter,
  addSeries,
  AGG_PRESET_LABELS,
  followUnit,
  AGG_PRESETS,
  countsOnlyErrors,
  panelJson,
  parsePanelJson,
  presetOf,
  removeFilter,
  removeSeries,
  suggestedUnit,
  updateFilter,
  updateSeries,
  VISUALIZATIONS,
  withErrorsOnly,
  withKind,
  withPreset,
  withRatio,
  withSignal,
  type AggPreset,
  type Visualization,
} from "./panelBuilder";
import { AlertFromPanelDialog, CreateAlertButton } from "./AlertFromPanelDialog";
import { unsupportedReason } from "./alertSnippet";
import { PanelBody } from "./PanelBody";
import { BUTTON_PRIMARY, BUTTON_SECONDARY, BUTTON_SMALL, ICON_BUTTON } from "./parts";

export type DrawerMode = "add" | "edit" | "view";

export interface PanelDrawerProps {
  mode: DrawerMode;
  panel: Panel;
  dashboardTitle: string;
  scope: ObsScope | null;
  range: RangePreset;
  services: string[];
  /** The project's recorded environments, and the one the dashboard looks at, for "Create alert". */
  environments: readonly ObsEnvironment[];
  env: string;
  /** The scope, in words, for the preview's caption. */
  scopeLabel: string;
  /** The workspace version being edited (Save writes the next one). */
  version: number;
  /** The environments the dashboard is about, in words (D1: a write names where it lands). */
  envScope: string;
  saving: boolean;
  error: ReactNode;
  onCancel: () => void;
  onSave: (panel: Panel) => void;
}

const UNIT_LABELS: Record<Unit, string> = { "": "none", count: "count", ms: "milliseconds", bytes: "bytes", percent: "percent", s: "seconds" };
const INPUT =
  "h-8 w-full min-w-0 rounded-md border border-border bg-surface px-2.5 text-sm text-ink placeholder:text-ink-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-accent disabled:opacity-60";

function useDebounced<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setSettled(value), ms);
    return () => window.clearTimeout(timer);
  }, [value, ms]);
  return settled;
}

export function PanelDrawer({ mode, panel: initial, dashboardTitle, scope, range, services, environments, env, scopeLabel, version, envScope, saving, error, onCancel, onSave }: PanelDrawerProps) {
  const [draft, setDraft] = useState<Panel>(initial);
  const [tab, setTab] = useState<"builder" | "spec">(mode === "view" ? "spec" : "builder");
  const escape = useEscapeLayer(true, onCancel);
  const problems = useMemo(() => panelProblems(draft), [draft]);
  const settled = useDebounced(draft, 300);
  const previewable = panelProblems(settled).length === 0;
  const preview = usePanelQuery(scope, range, settled, services, [], { enabled: previewable });
  const readOnly = mode === "view";
  const titleId = useId();
  const [alerting, setAlerting] = useState(false);
  // A panel that can never be an alert (a heatmap, a funnel) still opens the dialog, which says why.
  const incomplete = problems.length > 0 && unsupportedReason(draft) === null;

  return (
    <div className="fixed inset-0 z-[60] flex justify-end" data-testid="panel-drawer" data-mode={mode}>
      <div className="absolute inset-0 bg-black/40" aria-hidden="true" onClick={onCancel} />
      <aside
        {...escape}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className="relative flex h-full w-full max-w-[600px] flex-col border-l border-border bg-surface shadow-xl"
      >
        <header className="flex items-start justify-between gap-3 border-b border-border px-5 pb-3 pt-4">
          <div className="min-w-0">
            <p className="truncate text-xs text-ink-muted">
              {dashboardTitle} · {mode === "add" ? "Add panel" : mode === "edit" ? "Edit panel" : "View panel"}
            </p>
            <h2 id={titleId} className="truncate text-base font-semibold text-ink">
              {draft.title || "Untitled panel"}
            </h2>
          </div>
          <button type="button" onClick={onCancel} aria-label="Close" className={ICON_BUTTON}>
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        </header>
        <div role="tablist" aria-label="Panel editor" className="flex gap-4 border-b border-border px-5">
          {(readOnly ? (["spec"] as const) : (["builder", "spec"] as const)).map((id) => (
            <button
              key={id}
              type="button"
              role="tab"
              aria-selected={tab === id}
              onClick={() => setTab(id)}
              className={cn(
                "-mb-px h-9 border-b-2 text-sm transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent",
                tab === id ? "border-ink font-medium text-ink" : "border-transparent text-ink-muted hover:text-ink"
              )}
              data-testid={`panel-drawer-tab-${id}`}
            >
              {id === "builder" ? "Builder" : "Spec"}
            </button>
          ))}
        </div>

        <div className="flex-1 space-y-5 overflow-y-auto px-5 py-4">
          <section aria-label="Live preview" className="space-y-1.5">
            <div className="flex items-center justify-between gap-2">
              <p className="text-xs text-ink-muted">
                Live preview · {scopeLabel} · {RANGE_LABELS[range].toLowerCase()}
              </p>
              <CreateAlertButton
                onClick={() => setAlerting(true)}
                disabled={incomplete}
                title={incomplete ? "Finish the panel first: an alert is written from a complete panel." : "Write this panel's query as a KCL alert rule"}
              />
            </div>
            <div className="rounded-lg border border-border/60 bg-surface-sunken p-3" data-testid="panel-drawer-preview">
              <p className="mb-2 truncate text-sm font-medium text-ink">{draft.title || "Untitled panel"}</p>
              {previewable ? (
                <PanelBody panel={settled} query={preview} height={140} />
              ) : (
                <p className="text-xs text-ink-muted">The preview runs once the panel is complete.</p>
              )}
            </div>
          </section>

          {tab === "builder" ? <Builder draft={draft} setDraft={setDraft} /> : <SpecTab draft={draft} setDraft={setDraft} readOnly={readOnly} />}

          {!readOnly && problems.length > 0 && (
            <ul className="space-y-1 rounded-md border border-border px-3 py-2 text-xs text-warning-ink" data-testid="panel-drawer-problems" role="status">
              {problems.map((problem) => (
                <li key={problem}>{problem}</li>
              ))}
            </ul>
          )}
          {error}
        </div>

        <footer className="flex flex-wrap items-center justify-end gap-2 border-t border-border px-5 py-3">
          <span className="mr-auto text-xs text-ink-muted">
            {readOnly ? "Declared in your repo. Save a copy of the dashboard to change it." : `Saves a new version of the dashboard for ${envScope}. Promote to code to declare it in the repo.`}
          </span>
          <button type="button" className={BUTTON_SECONDARY} onClick={onCancel}>
            {readOnly ? "Close" : "Cancel"}
          </button>
          {!readOnly && (
            <button
              type="button"
              className={BUTTON_PRIMARY}
              disabled={saving || problems.length > 0}
              onClick={() => onSave(draft)}
              data-testid="panel-drawer-save"
            >
              {saving ? "Saving…" : `Save v${version} → v${version + 1}`}
            </button>
          )}
        </footer>
      </aside>
      {alerting && <AlertFromPanelDialog panel={draft} dashboardTitle={dashboardTitle} environments={environments} env={env} services={services} onClose={() => setAlerting(false)} />}
    </div>
  );
}

// ── Builder ─────────────────────────────────────────────────────────────────

function Row({ label, children, htmlFor }: { label: string; children: ReactNode; htmlFor?: string }) {
  return (
    <div className="grid grid-cols-[6.5rem_minmax(0,1fr)] items-start gap-3">
      {htmlFor ? (
        <label htmlFor={htmlFor} className="pt-2 text-xs font-medium text-ink-muted">
          {label}
        </label>
      ) : (
        <span className="pt-2 text-xs font-medium text-ink-muted">{label}</span>
      )}
      <div className="min-w-0">{children}</div>
    </div>
  );
}

function Choice<T extends string>({ label, value, options, onChange, testId }: { label: string; value: T; options: { value: T; label: string }[]; onChange: (value: T) => void; testId?: string }) {
  return (
    <div role="radiogroup" aria-label={label} className="flex flex-wrap gap-1" data-testid={testId}>
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          role="radio"
          aria-checked={value === option.value}
          onClick={() => onChange(option.value)}
          data-value={option.value}
          className={cn(
            "h-7 rounded-md border px-2.5 text-xs transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent",
            value === option.value ? "border-ink/40 bg-ink/8 font-medium text-ink" : "border-border bg-surface text-ink-muted hover:text-ink"
          )}
        >
          {option.label}
        </button>
      ))}
    </div>
  );
}

function Builder({ draft, setDraft: set }: { draft: Panel; setDraft: (panel: Panel) => void }) {
  const id = useId();
  // Every change but an explicit unit pick lets the unit follow what the panel measures.
  const setDraft = (next: Panel) => set(next.unit !== draft.unit ? next : followUnit(draft, next));
  const kind = draft.kind as Visualization;
  const aggregates = kind === "line" || kind === "number" || kind === "table";
  const signals = FIELD_SUGGESTIONS[(draft.signal as keyof typeof FIELD_SUGGESTIONS) ?? "traces"] ?? FIELD_SUGGESTIONS.traces;
  const fieldsId = `${id}-fields`;
  return (
    <div className="space-y-4" data-testid="panel-builder">
      <datalist id={fieldsId}>
        {signals.map((field) => (
          <option key={field} value={field} />
        ))}
      </datalist>
      <Row label="Title" htmlFor={`${id}-title`}>
        <input id={`${id}-title`} value={draft.title} onChange={(e) => setDraft({ ...draft, title: e.target.value })} className={INPUT} data-testid="panel-builder-title" />
      </Row>
      <Row label="Description" htmlFor={`${id}-description`}>
        <input
          id={`${id}-description`}
          value={draft.description ?? ""}
          onChange={(e) => setDraft({ ...draft, description: e.target.value || undefined })}
          placeholder="Optional: what it shows, and why it matters"
          className={INPUT}
        />
      </Row>
      <Row label="Visualization">
        <Choice label="Visualization" value={kind} options={VISUALIZATIONS.map((v) => ({ value: v.kind, label: v.label }))} onChange={(next) => setDraft(withKind(draft, next))} testId="panel-builder-kind" />
      </Row>

      {(aggregates || kind === "search") && (
        <Row label="Signal">
          <Choice
            label="Signal"
            value={draft.signal}
            options={(kind === "search" ? ["traces", "logs"] : ["traces", "logs", "metrics"]).map((s) => ({ value: s, label: s === "traces" ? "Traces · spans" : s === "logs" ? "Logs" : "Metrics" }))}
            onChange={(next) => setDraft(withSignal(draft, next))}
            testId="panel-builder-signal"
          />
        </Row>
      )}

      {aggregates && (
        <>
          <div className="space-y-2">
            {draft.series.map((series, index) => (
              <SeriesEditor
                key={index}
                index={index}
                series={series}
                signal={draft.signal}
                fieldsId={fieldsId}
                removable={draft.series.length > 1}
                onChange={(next) => setDraft(updateSeries(draft, index, next))}
                onRemove={() => setDraft(removeSeries(draft, index))}
              />
            ))}
            <div className="flex flex-wrap items-center gap-3">
              {draft.series.length < 10 && (
                <button type="button" className={BUTTON_SMALL} onClick={() => setDraft(addSeries(draft))} data-testid="panel-builder-add-series">
                  <Plus className="h-3 w-3" aria-hidden="true" />
                  Series
                </button>
              )}
              {kind === "line" && draft.signal !== "metrics" && (
                <label className="inline-flex items-center gap-2 text-xs text-ink">
                  <input type="checkbox" checked={!!draft.ratio} onChange={(e) => setDraft(withRatio(draft, e.target.checked))} data-testid="panel-builder-ratio" />
                  Plot A / B as a percentage
                </label>
              )}
            </div>
          </div>
        </>
      )}

      {kind === "funnel" && <FunnelEditor draft={draft} setDraft={setDraft} />}
      {kind === "sessions" && (
        <Row label="Sessions">
          <label className="inline-flex items-center gap-2 pt-1.5 text-sm text-ink">
            <input type="checkbox" checked={!!draft.hasError} onChange={(e) => setDraft({ ...draft, hasError: e.target.checked || undefined })} data-testid="panel-builder-has-error" />
            Only sessions that hit an error
          </label>
        </Row>
      )}
      {kind === "search" && (
        <Row label="Columns" htmlFor={`${id}-columns`}>
          <input
            id={`${id}-columns`}
            value={(draft.columns ?? []).join(", ")}
            onChange={(e) => setDraft({ ...draft, columns: e.target.value.split(",").map((c) => c.trim()).filter(Boolean) })}
            className={cn(INPUT, "font-mono")}
          />
        </Row>
      )}

      <Row label="Filters">
        <div className="space-y-2">
          {(draft.where ?? []).map((predicate, index) => (
            <FilterRow key={index} predicate={predicate} fieldsId={fieldsId} onChange={(next) => setDraft(updateFilter(draft, index, next))} onRemove={() => setDraft(removeFilter(draft, index))} />
          ))}
          <button type="button" className={BUTTON_SMALL} onClick={() => setDraft(addFilter(draft))} data-testid="panel-builder-add-filter">
            <Plus className="h-3 w-3" aria-hidden="true" />
            Filter
          </button>
        </div>
      </Row>

      {(kind === "line" || kind === "table" || kind === "number") && (
        <Row label="Group by">
          <div className="space-y-2">
            {(draft.groupBy ?? []).map((field, index) => (
              <div key={index} className="flex items-center gap-2">
                <input
                  aria-label={`Group by ${index + 1}`}
                  list={fieldsId}
                  value={field}
                  onChange={(e) => setDraft({ ...draft, groupBy: (draft.groupBy ?? []).map((f, i) => (i === index ? e.target.value : f)) })}
                  className={cn(INPUT, "font-mono")}
                  data-testid={`panel-builder-group-${index}`}
                />
                <button
                  type="button"
                  aria-label={`Remove group by ${field || index + 1}`}
                  className={ICON_BUTTON}
                  onClick={() => {
                    const next = (draft.groupBy ?? []).filter((_, i) => i !== index);
                    setDraft({ ...draft, groupBy: next.length ? next : undefined });
                  }}
                >
                  <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                </button>
              </div>
            ))}
            {(draft.groupBy ?? []).length < 3 && (
              <button type="button" className={BUTTON_SMALL} onClick={() => setDraft({ ...draft, groupBy: [...(draft.groupBy ?? []), ""] })} data-testid="panel-builder-add-group">
                <Plus className="h-3 w-3" aria-hidden="true" />
                Group by
              </button>
            )}
            {kind !== "number" && <p className="text-xs text-ink-muted">One {kind === "line" ? "line" : "row"} per value; the largest {kind === "line" ? "50" : "200"} are kept.</p>}
          </div>
        </Row>
      )}

      {aggregates && (
        <Row label="Unit" htmlFor={`${id}-unit`}>
          <select
            id={`${id}-unit`}
            value={draft.unit ?? ""}
            onChange={(e) => set({ ...draft, unit: (e.target.value || undefined) as Unit | undefined })}
            className={cn(INPUT, "w-auto")}
          >
            {UNITS.map((unit) => (
              <option key={unit} value={unit}>
                {unit === "" ? `auto${suggestedUnit(draft) ? ` (${UNIT_LABELS[suggestedUnit(draft)]})` : ""}` : UNIT_LABELS[unit]}
              </option>
            ))}
          </select>
        </Row>
      )}

      <Row label="Width">
        <Choice
          label="Width"
          value={String(spanOf(draft))}
          options={Array.from({ length: GRID_SPANS }, (_, i) => ({ value: String(i + 1), label: `${i + 1}/${GRID_SPANS}` }))}
          onChange={(next) => setDraft({ ...draft, width: widthOfSpan(Number(next)) })}
          testId="panel-builder-width"
        />
      </Row>
    </div>
  );
}

function SeriesEditor({
  index,
  series,
  signal,
  fieldsId,
  removable,
  onChange,
  onRemove,
}: {
  index: number;
  series: SeriesIntent;
  signal: string;
  fieldsId: string;
  removable: boolean;
  onChange: (series: SeriesIntent) => void;
  onRemove: () => void;
}) {
  const id = useId();
  const letter = "ABCDEFGHIJ"[index] ?? String(index + 1);
  const preset = presetOf(series);
  const presets: AggPreset[] = signal === "metrics" ? ["avg", "max", "sum", "p50", "p95", "p99"] : [...AGG_PRESETS];
  return (
    <div className="space-y-2.5 rounded-lg border border-border/60 bg-surface-sunken p-3" data-testid={`panel-builder-series-${index}`}>
      <div className="flex items-center gap-2">
        <span className="inline-flex h-5 w-5 shrink-0 items-center justify-center rounded border border-border text-xs font-medium text-ink" aria-hidden="true">
          {letter}
        </span>
        <label htmlFor={`${id}-label`} className="sr-only">
          Series {letter} label
        </label>
        <input id={`${id}-label`} value={series.label} onChange={(e) => onChange({ ...series, label: e.target.value })} className={cn(INPUT, "h-7")} />
        {removable && (
          <button type="button" aria-label={`Remove series ${letter}`} className={ICON_BUTTON} onClick={onRemove}>
            <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
          </button>
        )}
      </div>
      <Row label="Aggregation">
        <Choice
          label={`Series ${letter} aggregation`}
          value={preset ?? ("" as AggPreset)}
          options={presets.map((p) => ({ value: p, label: AGG_PRESET_LABELS[p] }))}
          onChange={(next) => onChange(withPreset(series, next, signal))}
          testId={`panel-builder-agg-${index}`}
        />
      </Row>
      {series.agg !== "count" && (
        <Row label={signal === "metrics" ? "Metric" : "Field"} htmlFor={`${id}-field`}>
          <input
            id={`${id}-field`}
            list={signal === "metrics" ? undefined : fieldsId}
            value={series.field ?? ""}
            onChange={(e) => onChange({ ...series, field: e.target.value })}
            placeholder={signal === "metrics" ? "http.server.request.duration" : "span.duration"}
            className={cn(INPUT, "font-mono")}
          />
        </Row>
      )}
      {signal === "metrics" && (
        <Row label="Metric type" htmlFor={`${id}-type`}>
          <select id={`${id}-type`} value={series.metricType ?? ""} onChange={(e) => onChange({ ...series, metricType: e.target.value || undefined })} className={cn(INPUT, "w-auto")}>
            <option value="">choose…</option>
            {METRIC_TYPES.map((type) => (
              <option key={type} value={type}>
                {type}
              </option>
            ))}
          </select>
        </Row>
      )}
      {signal !== "metrics" && (
        <label className="ml-[7.25rem] inline-flex items-center gap-2 text-xs text-ink">
          <input type="checkbox" checked={countsOnlyErrors(series)} onChange={(e) => onChange(withErrorsOnly(series, e.target.checked, signal))} data-testid={`panel-builder-errors-${index}`} />
          Errors only ({signal === "logs" ? "log.severity = error" : "span.status = error"})
        </label>
      )}
    </div>
  );
}

function FilterRow({ predicate, fieldsId, onChange, onRemove }: { predicate: Predicate; fieldsId: string; onChange: (p: Predicate) => void; onRemove: () => void }) {
  const multi = predicate.op === "in" || predicate.op === "not_in";
  return (
    <div className="grid grid-cols-[minmax(0,1.2fr)_auto_minmax(0,1fr)_auto] items-center gap-2" data-testid="panel-builder-filter">
      <input aria-label="Field" list={fieldsId} value={predicate.field} onChange={(e) => onChange({ ...predicate, field: e.target.value })} placeholder="service.name" className={cn(INPUT, "font-mono")} />
      <select
        aria-label="Operator"
        value={predicate.op}
        onChange={(e) => {
          const op = e.target.value;
          const toMulti = op === "in" || op === "not_in";
          onChange(toMulti ? { field: predicate.field, op, values: predicate.values ?? (predicate.value ? [predicate.value] : []) } : { field: predicate.field, op, value: op === "exists" ? undefined : (predicate.value ?? predicate.values?.[0] ?? "") });
        }}
        className={cn(INPUT, "w-auto")}
      >
        {OPS.map((op) => (
          <option key={op} value={op}>
            {OP_LABELS[op]}
          </option>
        ))}
      </select>
      {predicate.op === "exists" ? (
        <span />
      ) : (
        <input
          aria-label="Value"
          value={multi ? (predicate.values ?? []).join(", ") : (predicate.value ?? "")}
          onChange={(e) => onChange(multi ? { ...predicate, values: e.target.value.split(",").map((v) => v.trim()).filter(Boolean) } : { ...predicate, value: e.target.value })}
          placeholder={multi ? "a, b, c" : "value"}
          className={INPUT}
        />
      )}
      <button type="button" aria-label={`Remove filter ${predicate.field}`} className={ICON_BUTTON} onClick={onRemove}>
        <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
      </button>
    </div>
  );
}

function FunnelEditor({ draft, setDraft }: { draft: Panel; setDraft: (panel: Panel) => void }) {
  const id = useId();
  const funnel = draft.funnel ?? { steps: ["", ""] };
  const set = (next: Partial<NonNullable<Panel["funnel"]>>) => setDraft({ ...draft, funnel: { ...funnel, ...next } });
  return (
    <>
      <Row label="Steps">
        <ol className="space-y-2" data-testid="panel-builder-funnel">
          {funnel.steps.map((step, index) => (
            <li key={index} className="flex items-center gap-2">
              <span className="w-4 text-right text-xs tabular-nums text-ink-muted" aria-hidden="true">
                {index + 1}
              </span>
              <input
                aria-label={`Step ${index + 1} event`}
                value={step}
                onChange={(e) => set({ steps: funnel.steps.map((s, i) => (i === index ? e.target.value : s)) })}
                placeholder="the event name passed to track()"
                className={cn(INPUT, "font-mono")}
              />
              {funnel.steps.length > 2 && (
                <button type="button" aria-label={`Remove step ${index + 1}`} className={ICON_BUTTON} onClick={() => set({ steps: funnel.steps.filter((_, i) => i !== index) })}>
                  <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
                </button>
              )}
            </li>
          ))}
          {funnel.steps.length < 10 && (
            <li>
              <button type="button" className={BUTTON_SMALL} onClick={() => set({ steps: [...funnel.steps, ""] })} data-testid="panel-builder-add-step">
                <Plus className="h-3 w-3" aria-hidden="true" />
                Step
              </button>
            </li>
          )}
        </ol>
      </Row>
      <Row label="Within" htmlFor={`${id}-window`}>
        <input id={`${id}-window`} value={funnel.window ?? ""} onChange={(e) => set({ window: e.target.value || undefined })} placeholder="1h" className={cn(INPUT, "w-28 font-mono")} />
      </Row>
      <Row label="Count per">
        <Choice label="Count per" value={funnel.by || "session"} options={[{ value: "session", label: "Session" }, { value: "user", label: "Signed-in user" }]} onChange={(by) => set({ by })} />
      </Row>
    </>
  );
}

// ── Spec ────────────────────────────────────────────────────────────────────

function SpecTab({ draft, setDraft, readOnly }: { draft: Panel; setDraft: (panel: Panel) => void; readOnly: boolean }) {
  const [text, setText] = useState(() => panelJson(draft));
  const [problem, setProblem] = useState<string | null>(null);
  // The draft this tab set itself: re-formatting it would move the caret mid-edit.
  const fromSpec = useRef<Panel | null>(null);
  useEffect(() => {
    if (draft !== fromSpec.current) setText(panelJson(draft));
  }, [draft]);
  return (
    <div className="space-y-2" data-testid="panel-spec">
      <p className="text-xs text-ink-muted">
        The panel exactly as forge stores it. The server compiles it into its query, with the same engine and limits as alert rules; the console never sends
        SQL.
      </p>
      <textarea
        aria-label="Panel spec (JSON)"
        value={text}
        readOnly={readOnly}
        spellCheck={false}
        onChange={(e) => {
          setText(e.target.value);
          const parsed = parsePanelJson(e.target.value);
          if ("problem" in parsed) setProblem(parsed.problem);
          else {
            setProblem(null);
            fromSpec.current = parsed.panel;
            setDraft(parsed.panel);
          }
        }}
        rows={18}
        className="block w-full resize-y rounded-md border border-border/60 bg-surface-sunken p-3 font-mono text-xs leading-relaxed text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-accent"
        data-testid="panel-spec-json"
      />
      {problem && (
        <p className="text-xs text-danger-ink" role="alert" data-testid="panel-spec-problem">
          {problem}
        </p>
      )}
    </div>
  );
}
