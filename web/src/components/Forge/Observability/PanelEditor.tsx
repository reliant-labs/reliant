// Copyright (c) 2025 Reliant Labs

/**
 * Edit one dashboard panel's query in place: what it aggregates, over which
 * spans, logs or metrics, filtered and grouped how, drawn as what.
 *
 * The result goes into the URL (`edit`), not to a server — there are no
 * server-side dashboards (owner decision 2). The panel on the dashboard
 * re-runs with the edit through the same QueryPanel every panel uses, so what
 * the reader sees is exactly what "Save as code" will write.
 *
 * The form covers a panel's common shape. Anything it does not show (a
 * per-series filter, table columns) is kept as it was; the JSON tab edits
 * the whole panel for the rest.
 */

import { useId, useState } from "react";
import { Plus, Trash2 } from "lucide-react";

import { Button } from "@/components/ui/Button";
import { Modal } from "@/components/ui/Modal";
import type { Panel, Predicate, SeriesIntent, Unit } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { attributeLabel } from "./attributes";
import { AGG_LABELS, AGGS, FIELD_SUGGESTIONS, OP_LABELS, OPS, PANEL_KINDS, panelProblems, SIGNALS, UNITS } from "./obsdashSpec";

const FIELD =
  "h-8 w-full rounded-md border border-border bg-background px-2 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";
const LABEL = "mb-1 block text-xs font-medium text-muted-foreground";

const UNIT_LABELS: Record<Unit, string> = { "": "none", count: "count", ms: "milliseconds", bytes: "bytes", percent: "percent (0–1)" };
const KIND_LABELS: Record<string, string> = { line: "Line chart", number: "Number", table: "Table", search: "Rows (opens Logs or Traces)" };
const WIDTHS = [6, 8, 12, 16, 24];

export interface PanelEditorProps {
  open: boolean;
  panel: Panel;
  /** "Add panel" rather than editing one the dashboard has. */
  isNew: boolean;
  /** The page's in-place group-by, which replaces the panel's own while it is set. */
  pageGroupBy?: string[];
  onApply: (panel: Panel) => void;
  onClose: () => void;
}

export function PanelEditor({ open, panel: initial, isNew, pageGroupBy = [], onApply, onClose }: PanelEditorProps) {
  const [panel, setPanel] = useState<Panel>(initial);
  const [mode, setMode] = useState<"form" | "json">("form");
  const [json, setJson] = useState("");
  const [jsonError, setJsonError] = useState<string | null>(null);
  const [problems, setProblems] = useState<string[]>([]);
  const ids = { title: useId(), kind: useId(), signal: useId(), unit: useId(), width: useId(), groupBy: useId(), fields: useId(), json: useId() };

  const update = (patch: Partial<Panel>) => setPanel((prev) => ({ ...prev, ...patch }));
  const updateSeries = (index: number, patch: Partial<SeriesIntent>) =>
    update({ series: panel.series.map((s, i) => (i === index ? { ...s, ...patch } : s)) });
  const updateWhere = (index: number, patch: Partial<Predicate>) =>
    update({ where: (panel.where ?? []).map((p, i) => (i === index ? { ...p, ...patch } : p)) });

  /** The panel the current tab describes, or null when its JSON does not parse. */
  const current = (): Panel | null => {
    if (mode === "form") return panel;
    try {
      const parsed = JSON.parse(json) as Panel;
      if (!parsed || typeof parsed !== "object" || !Array.isArray(parsed.series)) throw new Error("A panel is an object with a series array.");
      setJsonError(null);
      return { ...parsed, id: initial.id };
    } catch (error) {
      setJsonError(error instanceof Error ? error.message : "Not valid JSON.");
      return null;
    }
  };

  const switchMode = (next: "form" | "json") => {
    if (next === mode) return;
    if (next === "json") {
      setJson(JSON.stringify(panel, null, 2));
      setJsonError(null);
      setMode("json");
      return;
    }
    const parsed = current();
    if (parsed) {
      setPanel(parsed);
      setMode("form");
    }
  };

  const apply = () => {
    const candidate = current();
    if (!candidate) return;
    // Drop the blank rows the form leaves; keep an absent list absent so an untouched panel compares equal.
    const cleaned: Panel = {
      ...candidate,
      where: candidate.where ? candidate.where.filter((p) => p.field.trim()) : undefined,
      groupBy: candidate.groupBy ? candidate.groupBy.filter(Boolean) : undefined,
    };
    if (!cleaned.where) delete cleaned.where;
    if (!cleaned.groupBy) delete cleaned.groupBy;
    const found = panelProblems(cleaned);
    setProblems(found);
    if (found.length === 0) onApply(cleaned);
  };

  const signal = (SIGNALS as readonly string[]).includes(panel.signal) ? (panel.signal as (typeof SIGNALS)[number]) : "traces";

  return (
    <Modal isOpen={open} onClose={onClose} title={isNew ? "Add a panel" : `Edit "${initial.title}"`} size="lg">
      <div className="space-y-4" data-testid="obs-panel-editor">
        <p className="text-xs text-muted-foreground">
          Changes apply to this page only and are kept in its link. Use <span className="font-medium text-foreground">Save as code</span> to
          keep them in your repo.
        </p>
        <div role="tablist" aria-label="Edit as" className="inline-flex rounded-lg border border-border bg-background p-0.5">
          {(["form", "json"] as const).map((m) => (
            <button
              key={m}
              type="button"
              role="tab"
              aria-selected={mode === m}
              onClick={() => switchMode(m)}
              className={cn(
                "rounded-md px-3 py-1 text-sm focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                mode === m ? "bg-card font-medium text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground"
              )}
              data-testid={`obs-editor-mode-${m}`}
            >
              {m === "form" ? "Query" : "JSON"}
            </button>
          ))}
        </div>

        {mode === "json" ? (
          <div>
            <label htmlFor={ids.json} className={LABEL}>
              The panel, in forge&apos;s dashboard spec (forge.dashboards/v1)
            </label>
            <textarea
              id={ids.json}
              value={json}
              onChange={(event) => setJson(event.target.value)}
              spellCheck={false}
              rows={18}
              className="w-full rounded-md border border-border bg-background p-2 font-mono text-xs text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              data-testid="obs-editor-json"
            />
            {jsonError && (
              <p role="alert" className="mt-1 text-xs text-destructive">
                {jsonError}
              </p>
            )}
          </div>
        ) : (
          <>
            <datalist id={ids.fields}>
              {FIELD_SUGGESTIONS[signal].map((field) => (
                <option key={field} value={field} />
              ))}
            </datalist>
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="sm:col-span-2">
                <label htmlFor={ids.title} className={LABEL}>
                  Title
                </label>
                <input id={ids.title} value={panel.title} onChange={(e) => update({ title: e.target.value })} className={FIELD} data-testid="obs-editor-title" />
              </div>
              <div>
                <label htmlFor={ids.signal} className={LABEL}>
                  Reads
                </label>
                <select id={ids.signal} value={panel.signal} onChange={(e) => update({ signal: e.target.value })} className={FIELD} data-testid="obs-editor-signal">
                  {SIGNALS.map((s) => (
                    <option key={s} value={s}>
                      {s}
                    </option>
                  ))}
                </select>
              </div>
              <div>
                <label htmlFor={ids.kind} className={LABEL}>
                  Drawn as
                </label>
                <select id={ids.kind} value={panel.kind} onChange={(e) => update({ kind: e.target.value })} className={FIELD} data-testid="obs-editor-kind">
                  {PANEL_KINDS.map((k) => (
                    <option key={k} value={k}>
                      {KIND_LABELS[k]}
                    </option>
                  ))}
                </select>
              </div>
            </div>

            <fieldset className="space-y-2">
              <legend className="text-sm font-medium text-foreground">Series</legend>
              {panel.series.map((series, index) => (
                <div key={index} className="grid grid-cols-[1fr_1fr_1.4fr_auto] items-end gap-2 rounded-md border border-border/60 bg-background p-2" data-testid="obs-editor-series">
                  <div>
                    <label className={LABEL} htmlFor={`${ids.title}-s${index}-label`}>
                      Label
                    </label>
                    <input id={`${ids.title}-s${index}-label`} value={series.label} onChange={(e) => updateSeries(index, { label: e.target.value })} className={FIELD} />
                  </div>
                  <div>
                    <label className={LABEL} htmlFor={`${ids.title}-s${index}-agg`}>
                      Aggregate
                    </label>
                    <select
                      id={`${ids.title}-s${index}-agg`}
                      value={series.agg}
                      onChange={(e) => updateSeries(index, { agg: e.target.value, quantile: e.target.value === "quantile" ? (series.quantile ?? 0.95) : undefined })}
                      className={FIELD}
                      data-testid="obs-editor-agg"
                    >
                      {AGGS.map((agg) => (
                        <option key={agg} value={agg}>
                          {AGG_LABELS[agg]}
                        </option>
                      ))}
                    </select>
                  </div>
                  <div className="flex gap-2">
                    {series.agg !== "count" && (
                      <div className="min-w-0 flex-1">
                        <label className={LABEL} htmlFor={`${ids.title}-s${index}-field`}>
                          {signal === "metrics" ? "Metric" : "Field"}
                        </label>
                        <input
                          id={`${ids.title}-s${index}-field`}
                          list={ids.fields}
                          value={series.field ?? ""}
                          onChange={(e) => updateSeries(index, { field: e.target.value || undefined })}
                          placeholder={signal === "metrics" ? "orders_created" : "span.duration"}
                          className={cn(FIELD, "font-mono")}
                          data-testid="obs-editor-field"
                        />
                      </div>
                    )}
                    {series.agg === "quantile" && (
                      <div className="w-20">
                        <label className={LABEL} htmlFor={`${ids.title}-s${index}-q`}>
                          Quantile
                        </label>
                        <input
                          id={`${ids.title}-s${index}-q`}
                          type="number"
                          step="0.01"
                          min="0.01"
                          max="0.999"
                          value={series.quantile ?? ""}
                          onChange={(e) => updateSeries(index, { quantile: e.target.value === "" ? undefined : Number(e.target.value) })}
                          className={FIELD}
                        />
                      </div>
                    )}
                  </div>
                  <IconButton label={`Remove series ${series.label || index + 1}`} onClick={() => update({ series: panel.series.filter((_, i) => i !== index) })} />
                </div>
              ))}
              <div className="flex flex-wrap items-center gap-3">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => update({ series: [...panel.series, { label: `series ${panel.series.length + 1}`, agg: "count" }] })}
                  leftIcon={<Plus className="h-3.5 w-3.5" />}
                  data-testid="obs-editor-add-series"
                >
                  Add series
                </Button>
                {panel.kind === "line" && panel.series.length === 2 && (
                  <label className="inline-flex items-center gap-2 text-xs text-foreground">
                    <input type="checkbox" checked={!!panel.ratio} onChange={(e) => update({ ratio: e.target.checked || undefined })} className="h-3.5 w-3.5 accent-[hsl(var(--primary))]" />
                    Plot the first divided by the second (a ratio, like an error rate)
                  </label>
                )}
              </div>
            </fieldset>

            <fieldset className="space-y-2">
              <legend className="text-sm font-medium text-foreground">Only where</legend>
              {(panel.where ?? []).map((p, index) => (
                <div key={index} className="grid grid-cols-[1.4fr_1fr_1.4fr_auto] items-end gap-2" data-testid="obs-editor-where">
                  <div>
                    <label className="sr-only" htmlFor={`${ids.title}-w${index}-field`}>
                      Field
                    </label>
                    <input
                      id={`${ids.title}-w${index}-field`}
                      list={ids.fields}
                      value={p.field}
                      onChange={(e) => updateWhere(index, { field: e.target.value })}
                      placeholder="span.name"
                      className={cn(FIELD, "font-mono")}
                    />
                  </div>
                  <div>
                    <label className="sr-only" htmlFor={`${ids.title}-w${index}-op`}>
                      Operator
                    </label>
                    <select id={`${ids.title}-w${index}-op`} value={p.op} onChange={(e) => updateWhere(index, { op: e.target.value })} className={FIELD}>
                      {OPS.map((op) => (
                        <option key={op} value={op}>
                          {OP_LABELS[op]}
                        </option>
                      ))}
                    </select>
                  </div>
                  <div>
                    <label className="sr-only" htmlFor={`${ids.title}-w${index}-value`}>
                      Value
                    </label>
                    {p.op === "exists" ? (
                      <span className="block h-8" />
                    ) : (
                      <input
                        id={`${ids.title}-w${index}-value`}
                        value={p.op === "in" || p.op === "not_in" ? (p.values ?? []).join(", ") : (p.value ?? "")}
                        onChange={(e) =>
                          p.op === "in" || p.op === "not_in"
                            ? updateWhere(index, { values: e.target.value.split(",").map((v) => v.trim()).filter(Boolean), value: undefined })
                            : updateWhere(index, { value: e.target.value, values: undefined })
                        }
                        placeholder={p.op === "in" || p.op === "not_in" ? "a, b, c" : "value"}
                        className={cn(FIELD, "font-mono")}
                      />
                    )}
                  </div>
                  <IconButton label={`Remove condition ${p.field || index + 1}`} onClick={() => update({ where: (panel.where ?? []).filter((_, i) => i !== index) })} />
                </div>
              ))}
              <Button
                variant="outline"
                size="sm"
                onClick={() => update({ where: [...(panel.where ?? []), { field: "", op: "eq", value: "" }] })}
                leftIcon={<Plus className="h-3.5 w-3.5" />}
                data-testid="obs-editor-add-where"
              >
                Add condition
              </Button>
            </fieldset>

            <div className="grid gap-3 sm:grid-cols-3">
              <div>
                <label htmlFor={ids.groupBy} className={LABEL}>
                  Group by (up to 3, comma-separated)
                </label>
                <input
                  id={ids.groupBy}
                  value={(panel.groupBy ?? []).join(", ")}
                  onChange={(e) => update({ groupBy: e.target.value.split(",").map((v) => v.trim()) })}
                  placeholder="service.name"
                  className={cn(FIELD, "font-mono")}
                  data-testid="obs-editor-group-by"
                />
                {pageGroupBy.length > 0 && panel.kind !== "search" && (
                  <p className="mt-1 text-2xs text-muted-foreground" data-testid="obs-editor-group-by-overridden">
                    While the page is grouped by {pageGroupBy.map(attributeLabel).join(", ")}, that replaces this.
                  </p>
                )}
              </div>
              <div>
                <label htmlFor={ids.unit} className={LABEL}>
                  Unit
                </label>
                <select id={ids.unit} value={panel.unit ?? ""} onChange={(e) => update({ unit: (e.target.value || undefined) as Unit | undefined })} className={FIELD}>
                  {UNITS.map((u) => (
                    <option key={u} value={u}>
                      {UNIT_LABELS[u]}
                    </option>
                  ))}
                </select>
              </div>
              <div>
                <label htmlFor={ids.width} className={LABEL}>
                  Width
                </label>
                <select id={ids.width} value={panel.width || (panel.kind === "number" ? 6 : 24)} onChange={(e) => update({ width: Number(e.target.value) })} className={FIELD}>
                  {WIDTHS.map((w) => (
                    <option key={w} value={w}>
                      {w === 24 ? "Full width" : `${w} of 24 columns`}
                    </option>
                  ))}
                </select>
              </div>
            </div>
          </>
        )}

        {problems.length > 0 && (
          <ul role="alert" className="space-y-0.5 rounded-md border border-destructive/40 bg-background px-3 py-2 text-xs text-destructive" data-testid="obs-editor-problems">
            {problems.map((problem) => (
              <li key={problem}>{problem}</li>
            ))}
          </ul>
        )}

        <div className="flex justify-end gap-2 border-t border-border/60 pt-4">
          <Button variant="ghost" size="sm" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" size="sm" onClick={apply} data-testid="obs-editor-apply">
            {isNew ? "Add panel" : "Apply"}
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function IconButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      title={label}
      className="mb-0.5 rounded p-1.5 text-muted-foreground hover:bg-muted hover:text-destructive focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
    </button>
  );
}
