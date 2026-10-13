// Copyright (c) 2025 Reliant Labs

/**
 * Dashboards: forge's dashboard spec (obsdash, `forge.dashboards/v1`) drawn
 * with uPlot — per-rpc RED panels by default, the environment's own spec once
 * a release ships one (contract K4, through the same GetDashboards).
 *
 * The spec describes WHAT to show, each panel a query intent over OTel field
 * names. QueryPanel compiles that intent server-side and returns the panel's
 * data — lines, a number, or table rows — so this file only draws. A "search"
 * panel lists rows rather than aggregating them, which is what the Logs and
 * Traces views do, so it renders as a way into them. A panel the server
 * cannot compile (an unknown field, operator or aggregation) answers
 * InvalidArgument, and says so in place.
 *
 * ── SEMI-DYNAMIC (owner decision 2) ─────────────────────────────────────────
 *
 * Without saving anything, a reader can change the filters, the group-by and
 * the window (the scope bar), and edit, hide or add panels (PanelEditor). All
 * of it lives in the URL, so the changed dashboard is a link. "Save as code"
 * turns what is on screen into the file to commit (SaveAsCodeDialog); there is
 * no server-side dashboard store.
 */

import { useMemo, useState } from "react";
import { ArrowRight, EyeOff, FileCode2, Info, Pencil, Plus, RotateCcw, Undo2 } from "lucide-react";

import Card, { CardHeader } from "@/components/forge-ui/card";
import { Button } from "@/components/ui/Button";
import { useAttributeValues, useDashboards, usePanelQuery } from "@/hooks/observability-queries";
import { obsErrorKind, obsErrorMessage } from "@/services/controlPlane/observability/errors";
import type { Dashboard, DashboardFilter, ObsScope, Panel, PanelResult, Predicate, Series, Unit } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { attributeLabel, ENV_KEY, isAttributeKeyShaped } from "./attributes";
import { ObsChartMarkersProvider, UPlotChart } from "./charts/UPlotChart";
import { formatValue } from "./format";
import { ServiceSelect } from "./LogsView";
import { applyEdits, editKey, editsFor, newPanelId, type PanelEdits } from "./obsdashSpec";
import { filterParam, groupLabel, scopeFiltersOf, type FilterToken } from "./obsFilters";
import type { ObsNavigate } from "./obsSearch";
import { ObsError, ObsLoadingRows } from "./ObsStates";
import { PanelEditor } from "./PanelEditor";
import { useReleaseMarkers, type ReleaseMarkers } from "./releaseMarkers";
import { SaveAsCodeDialog } from "./SaveAsCodeDialog";

const SERVICE_FIELD = "service.name";
const NO_KEYS: string[] = [];
const NO_TOKENS: FilterToken[] = [];

/** The dashboard a bare link opens: the service's own when one is named, else the first (forge's overview). */
export function pickDashboard(dashboards: Dashboard[], requested: string | undefined, service: string | undefined): Dashboard | undefined {
  return (
    dashboards.find((d) => d.id === requested) ??
    (service ? dashboards.find((d) => d.id === `service-${service}`) : undefined) ??
    dashboards[0]
  );
}

type Editing = { panel: Panel; isNew: boolean } | null;

export function DashboardsView({
  scope,
  subject,
  range,
  dashboardId,
  service,
  groupBy = NO_KEYS,
  filterTokens = NO_TOKENS,
  edits,
  navigate,
}: {
  scope: ObsScope;
  subject: string;
  range: RangePreset;
  dashboardId: string | undefined;
  service: string | undefined;
  groupBy?: string[];
  filterTokens?: FilterToken[];
  edits?: PanelEdits;
  navigate: ObsNavigate;
}) {
  const spec = useDashboards(scope);
  const [editing, setEditing] = useState<Editing>(null);
  const [saving, setSaving] = useState(false);
  const dashboards = spec.data?.dashboards ?? [];
  const base = pickDashboard(dashboards, dashboardId, service);
  const shown = useMemo(() => (base ? applyEdits(base, edits) : undefined), [base, edits]);
  const scopeFilters = useMemo(() => scopeFiltersOf(filterTokens), [filterTokens]);
  const releaseAnnotation = shown?.annotations?.find((a) => a.source === "releases");
  const releases = useReleaseMarkers(scope, range, !!releaseAnnotation);

  if (spec.isLoading) return <ObsLoadingRows testId="obs-dashboards-loading" />;
  if (spec.error) {
    return <ObsError error={spec.error} subject={subject} onRetry={() => void spec.refetch()} retrying={spec.isFetching} />;
  }
  if (!base || !shown) {
    return (
      <p
        role="status"
        data-testid="obs-dashboards-empty"
        className="rounded-lg border border-dashed border-border bg-background px-4 py-5 text-sm text-muted-foreground"
      >
        No dashboards yet. They are built from the services that have sent telemetry in the last 7 days, so they appear
        once a service has run.
      </p>
    );
  }

  const mine = editsFor(edits, base.id);
  const hasServiceFilter = base.filters.some((filter) => filter.field === SERVICE_FIELD);
  // A service's own dashboard is already scoped to it; the filter would only narrow it to nothing.
  const serviceFilter = hasServiceFilter && base.id !== `service-${service}` ? service : undefined;

  /** One panel's edit into the URL. A push, so Back undoes it. */
  const setEdit = (panelId: string, panel: Panel | null | undefined) => {
    const next: PanelEdits = { ...(edits ?? {}) };
    if (panel === undefined) delete next[editKey(base.id, panelId)];
    else next[editKey(base.id, panelId)] = panel;
    navigate({ edit: Object.keys(next).length ? next : undefined });
  };
  const discard = () => {
    const next: PanelEdits = Object.fromEntries(Object.entries(edits ?? {}).filter(([key]) => !key.startsWith(`${base.id}/`)));
    navigate({ edit: Object.keys(next).length ? next : undefined });
  };
  const changed = mine.size > 0 || filterTokens.length > 0 || groupBy.length > 0;
  const hidden = [...mine.values()].filter((panel) => panel === null).length;

  return (
    <div className="space-y-4" data-testid="obs-dashboards">
      <div className="flex flex-wrap items-center gap-2">
        <label htmlFor="obs-dashboard-picker" className="sr-only">
          Dashboard
        </label>
        <select
          id="obs-dashboard-picker"
          value={base.id}
          onChange={(event) => navigate({ dashboard: event.target.value }, { replace: true })}
          className="h-8 rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          data-testid="obs-dashboard-picker"
        >
          {dashboards.map((d) => (
            <option key={d.id} value={d.id}>
              {d.title}
              {editsFor(edits, d.id).size > 0 ? " (edited)" : ""}
            </option>
          ))}
        </select>
        {hasServiceFilter && (
          <ServiceSelect
            scope={scope}
            range={range}
            value={service}
            onChange={(next) => navigate({ service: next }, { replace: true })}
            id="obs-dashboard-service"
          />
        )}
        {base.filters
          .filter((f) => f.field !== SERVICE_FIELD && f.field !== ENV_KEY)
          .map((f) => (
            <DashboardFilterSelect key={f.id} scope={scope} range={range} filter={f} tokens={filterTokens} navigate={navigate} />
          ))}
        <DashboardOrigin specSource={spec.data?.source ?? ""} dashboard={base} />
        <div className="ml-auto flex flex-wrap gap-2">
          <Button
            variant="outline"
            size="sm"
            leftIcon={<Plus className="h-3.5 w-3.5" />}
            onClick={() =>
              setEditing({
                isNew: true,
                panel: { id: newPanelId(shown), title: "New panel", kind: "line", signal: "traces", series: [{ label: "requests", agg: "count" }], width: 12, height: 7 },
              })
            }
            data-testid="obs-add-panel"
          >
            Add panel
          </Button>
          <Button variant="outline" size="sm" leftIcon={<FileCode2 className="h-3.5 w-3.5" />} onClick={() => setSaving(true)} data-testid="obs-save-as-code-open">
            Save as code
          </Button>
        </div>
        {shown.description && <p className="w-full text-xs text-muted-foreground">{shown.description}</p>}
      </div>

      {spec.data?.source === "synthesized" && (
        <p className="rounded-lg border border-border/60 bg-background px-3 py-2 text-xs text-muted-foreground" data-testid="obs-dashboards-synthesized">
          <span className="font-medium text-foreground">Built from the services seen in the last 7 days.</span> Dashboards ride in a
          release: forge&apos;s generated set and your own files in <code className="font-mono">deploy/observability/dashboards/</code>.
          None of the environments in view has a recorded release bundle — nothing was promoted yet, the release predates
          dashboards in the bundle, or its bundle was not recorded (a failed push to the registry) — so these stand in until
          one is.
        </p>
      )}

      {releaseAnnotation && <ReleaseMarkersNote label={releaseAnnotation.label || "Releases"} releases={releases} />}

      {changed && (
        <div
          className="flex flex-wrap items-center gap-x-3 gap-y-1 rounded-lg border border-border/60 bg-background px-3 py-2 text-xs"
          role="status"
          data-testid="obs-dashboard-changed"
        >
          <span className="font-medium text-foreground">Changed in place</span>
          <span className="text-muted-foreground">
            {[
              mine.size - hidden > 0 ? `${mine.size - hidden} panel${mine.size - hidden === 1 ? "" : "s"} edited` : "",
              hidden > 0 ? `${hidden} hidden` : "",
              filterTokens.length ? `${filterTokens.length} filter${filterTokens.length === 1 ? "" : "s"}` : "",
              groupBy.length ? `grouped by ${groupBy.map(attributeLabel).join(", ")}` : "",
            ]
              .filter(Boolean)
              .join(" · ")}
            . This page&apos;s link carries it; Save as code keeps it.
          </span>
          {mine.size > 0 && (
            <Button variant="ghost" size="xs" leftIcon={<Undo2 className="h-3 w-3" />} onClick={discard} data-testid="obs-discard-edits" className="ml-auto">
              Discard panel edits
            </Button>
          )}
        </div>
      )}

      <ObsChartMarkersProvider markers={releases.markers}>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-[repeat(24,minmax(0,1fr))]">
        {shown.panels.map((panel) => (
          <PanelCard
            key={`${base.id}/${panel.id}`}
            scope={scope}
            range={range}
            panel={panel}
            edited={mine.has(panel.id)}
            added={!base.panels.some((p) => p.id === panel.id)}
            groupBy={groupBy}
            serviceFilter={serviceFilter}
            navigate={navigate}
            onEdit={() => setEditing({ panel, isNew: false })}
            onHide={() => setEdit(panel.id, base.panels.some((p) => p.id === panel.id) ? null : undefined)}
            onReset={() => setEdit(panel.id, undefined)}
          />
        ))}
      </div>
      </ObsChartMarkersProvider>
      {editing && (
        <PanelEditor
          open
          panel={editing.panel}
          isNew={editing.isNew}
          pageGroupBy={groupBy}
          onClose={() => setEditing(null)}
          onApply={(panel) => {
            // An Apply that changed nothing is not an edit: it must not mark the panel "Edited".
            if (editing.isNew || JSON.stringify(panel) !== JSON.stringify(editing.panel)) setEdit(panel.id, panel);
            setEditing(null);
          }}
        />
      )}
      {saving && (
        <SaveAsCodeDialog
          open
          onClose={() => setSaving(false)}
          project={spec.data?.project ?? ""}
          dashboard={shown}
          filters={scopeFilters}
          groupBy={groupBy}
        />
      )}
    </div>
  );
}

function PanelCard({
  scope,
  range,
  panel,
  edited,
  added,
  groupBy,
  serviceFilter,
  navigate,
  onEdit,
  onHide,
  onReset,
}: {
  scope: ObsScope;
  range: RangePreset;
  panel: Panel;
  edited: boolean;
  added: boolean;
  groupBy: string[];
  serviceFilter: string | undefined;
  navigate: ObsNavigate;
  onEdit: () => void;
  onHide: () => void;
  onReset: () => void;
}) {
  const width = panel.width || (panel.kind === "number" ? 6 : 24);
  // The in-place group-by replaces an aggregating panel's own; a search panel lists rows and is not grouped.
  const effectiveGroupBy = panel.kind === "search" ? [] : groupBy;
  return (
    <Card
      padding="md"
      className="group/panel min-w-0 lg:[grid-column:span_var(--panel-span)/span_var(--panel-span)]"
      style={{ ["--panel-span" as string]: String(Math.min(Math.max(width, 1), 24)) }}
      data-testid={`obs-panel-${panel.id}`}
      data-kind={panel.kind}
      data-edited={edited || undefined}
    >
      <CardHeader
        title={panel.title}
        description={panel.description}
        actions={
          <div className="flex items-center gap-0.5">
            {(edited || added) && (
              <span className="mr-1 rounded border border-border px-1 py-px text-2xs uppercase tracking-wide text-muted-foreground" data-testid="obs-panel-edited">
                {added ? "Added" : "Edited"}
              </span>
            )}
            <div className="flex items-center gap-0.5 opacity-60 transition-opacity focus-within:opacity-100 group-hover/panel:opacity-100">
              <PanelAction label={`Edit ${panel.title}`} onClick={onEdit} testId={`obs-panel-edit-${panel.id}`}>
                <Pencil className="h-3.5 w-3.5" />
              </PanelAction>
              {edited && !added && (
                <PanelAction label={`Reset ${panel.title} to the dashboard's own`} onClick={onReset} testId={`obs-panel-reset-${panel.id}`}>
                  <RotateCcw className="h-3.5 w-3.5" />
                </PanelAction>
              )}
              <PanelAction label={added ? `Remove ${panel.title}` : `Hide ${panel.title}`} onClick={onHide} testId={`obs-panel-hide-${panel.id}`}>
                <EyeOff className="h-3.5 w-3.5" />
              </PanelAction>
            </div>
          </div>
        }
      />
      {panel.kind === "search" ? (
        <SearchLink panel={panel} serviceFilter={serviceFilter} navigate={navigate} />
      ) : (
        <PanelData scope={scope} range={range} panel={panel} groupBy={effectiveGroupBy} serviceFilter={serviceFilter} navigate={navigate} />
      )}
    </Card>
  );
}

function PanelAction({ label, onClick, testId, children }: { label: string; onClick: () => void; testId: string; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      title={label}
      className="rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      data-testid={testId}
    >
      {children}
    </button>
  );
}

function PanelData({
  scope,
  range,
  panel,
  groupBy,
  serviceFilter,
  navigate,
}: {
  scope: ObsScope;
  range: RangePreset;
  panel: Panel;
  groupBy: string[];
  serviceFilter: string | undefined;
  navigate: ObsNavigate;
}) {
  const result = usePanelQuery(scope, range, panel, serviceFilter ? [serviceFilter] : [], groupBy);
  if (result.isLoading) {
    return <div className="h-16 animate-pulse rounded bg-border/40 motion-reduce:animate-none" aria-busy="true" aria-label="Loading" />;
  }
  if (result.error) {
    if (obsErrorKind(result.error) === "invalid") {
      // The server could not compile this panel's intent: say what, and offer the view that can show the rows.
      const to = panel.signal === "logs" ? "logs" : "traces";
      return (
        <div className="space-y-2" data-testid="obs-panel-unsupported">
          <p className="flex items-start gap-2 text-xs text-muted-foreground">
            <Info className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            <span>
              This panel can&apos;t be computed yet: <span className="font-mono">{obsErrorMessage(result.error)}</span>
            </span>
          </p>
          <Button
            variant="ghost"
            size="xs"
            rightIcon={<ArrowRight className="h-3 w-3" />}
            onClick={() => navigate({ view: to, service: serviceFilter ?? serviceOf(panel.where ?? []), q: undefined })}
          >
            Open {to === "logs" ? "Logs" : "Traces"}
          </Button>
        </div>
      );
    }
    return (
      <p className="text-xs text-destructive" role="alert" data-testid="obs-panel-error">
        This panel couldn&apos;t be loaded.
      </p>
    );
  }
  const data = result.data;
  if (!data || !hasData(data)) {
    return (
      <p className="py-4 text-center text-xs text-muted-foreground" data-testid="obs-panel-empty">
        No data in this window
      </p>
    );
  }
  const unit = unitOf(panel, data);
  const keys = groupBy.length ? groupBy : (panel.groupBy ?? []);
  const truncated = data.truncated ? (
    <p className="mt-2 text-2xs text-muted-foreground">Only the largest series are shown.</p>
  ) : null;

  if (panel.kind === "number") {
    return <NumberGrid items={data.series.map((s) => ({ label: seriesLabel(s, panel, keys), value: formatValue(s.value, unit) }))} />;
  }
  if (panel.kind === "table") {
    return (
      <>
        <PanelTable panel={panel} result={data} groupFields={keys} />
        {truncated}
      </>
    );
  }
  const timestamps = unionTimestamps(data.series);
  return (
    <>
      <UPlotChart
        height={Math.max((panel.height || 7) * 28 - 72, 120)}
        timestamps={timestamps}
        series={data.series.map((s) => ({ label: seriesLabel(s, panel, keys), values: alignTo(timestamps, s) }))}
        unit={unit}
        label={panel.title}
      />
      {truncated}
    </>
  );
}

function hasData(result: PanelResult): boolean {
  if (result.rows.length > 0) return true;
  return result.series.some((s) => s.value !== null || s.values.some((v) => v !== null));
}

const UNITS: readonly string[] = ["", "count", "ms", "bytes", "percent", "s"];

/** The panel's declared unit; the server's when the panel declares none. */
function unitOf(panel: Panel, result: PanelResult): Unit {
  if (panel.unit) return panel.unit;
  return (UNITS.includes(result.unit) ? result.unit : "") as Unit;
}

function NumberGrid({ items }: { items: { label: string; value: string }[] }) {
  const many = items.length > 1;
  // Grouped numbers (one per environment, say) can outnumber three columns.
  return (
    <dl className={cn("grid gap-3", many ? "grid-cols-[repeat(auto-fill,minmax(6rem,1fr))]" : "grid-cols-1")} data-testid="obs-panel-number">
      {items.map((item, index) => (
        <div key={`${item.label}-${index}`}>
          <dt className={many ? "text-xs font-medium uppercase tracking-wide text-muted-foreground" : "sr-only"}>{item.label}</dt>
          <dd className={cn("font-semibold tabular-nums text-foreground", many ? "text-lg" : "text-2xl")}>{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

/** One row per group, one column per series. A latency column is milliseconds whatever the panel's unit. */
function PanelTable({ panel, result, groupFields }: { panel: Panel; result: PanelResult; groupFields: string[] }) {
  const columnUnit = (index: number): Unit => {
    const intent = panel.series[index];
    if (intent?.agg === "quantile" && intent.field === "span.duration") return "ms";
    if (intent?.agg === "count" || intent?.agg === "count_distinct") return "count";
    return panel.unit ?? "";
  };
  return (
    <div className="max-h-80 overflow-auto">
      <table className="w-full text-xs" data-testid="obs-panel-table">
        <caption className="sr-only">{panel.title}</caption>
        <thead>
          <tr className="border-b border-border text-left text-muted-foreground">
            {groupFields.map((field) => (
              <th key={field} scope="col" className="py-1 pr-3 font-medium" title={field}>
                {attributeLabel(field)}
              </th>
            ))}
            {result.columns.map((label) => (
              <th key={label} scope="col" className="py-1 pr-3 text-right font-medium">
                {label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {result.rows.map((row, rowIndex) => (
            <tr key={rowIndex} className="border-b border-border/50 last:border-b-0">
              {groupFields.map((field, fieldIndex) =>
                fieldIndex === 0 ? (
                  <th key={field} scope="row" className="max-w-[24rem] truncate py-1 pr-3 text-left font-mono font-normal text-foreground">
                    {row.group[field] || "—"}
                  </th>
                ) : (
                  <td key={field} className="max-w-[24rem] truncate py-1 pr-3 font-mono text-muted-foreground">
                    {row.group[field] || "—"}
                  </td>
                )
              )}
              {row.values.map((value, index) => (
                <td key={index} className="py-1 pr-3 text-right tabular-nums text-foreground">
                  {formatValue(value, columnUnit(index))}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function serviceOf(where: Predicate[]): string | undefined {
  return where.find((p) => p.field === SERVICE_FIELD && p.op === "eq")?.value;
}

const isErrorPredicate = (p: Predicate) =>
  (p.field === "log.severity" || p.field === "span.status") && p.op === "eq" && (p.value ?? "").toLowerCase() === "error";

/** A search panel lists rows; the Logs and Traces views do that, carrying its service and error scope. */
function SearchLink({ panel, serviceFilter, navigate }: { panel: Panel; serviceFilter: string | undefined; navigate: ObsNavigate }) {
  const where = panel.where ?? [];
  const service = serviceFilter ?? serviceOf(where);
  const errorsOnly = where.some(isErrorPredicate);
  const toLogs = panel.signal === "logs";
  return (
    <div className="flex items-center justify-between gap-3" data-testid="obs-panel-link">
      <p className="text-xs text-muted-foreground">
        {toLogs ? "Log records" : "Spans"}
        {service ? (
          <>
            {" "}
            from <span className="font-mono text-foreground">{service}</span>
          </>
        ) : null}
        {errorsOnly ? ", errors only" : ""}.
      </p>
      <Button
        variant="outline"
        size="sm"
        rightIcon={<ArrowRight className="h-3.5 w-3.5" />}
        onClick={() =>
          toLogs
            ? navigate({ view: "logs", service, severity: errorsOnly ? "error,fatal" : undefined, q: undefined })
            : navigate({ view: "traces", service, errors: errorsOnly || undefined, q: undefined })
        }
      >
        Open in {toLogs ? "Logs" : "Traces"}
      </Button>
    </div>
  );
}

/** "api — requests", "api" for a ratio's per-group series, or the series' own label ungrouped. */
export function seriesLabel(series: Series, panel: Panel, keys: string[] = []): string {
  const group = Object.keys(series.group).length ? groupLabel(series.group, keys.filter((k) => k in series.group)) : "";
  if (!group) return series.label;
  if (series.label === "ratio" || panel.series.length <= 1) return group;
  return `${group} — ${series.label}`;
}

/** Every bucket any series has — a grouped panel's series can each skip buckets they had no rows in. */
export function unionTimestamps(series: { timestamps: number[] }[]): number[] {
  const all = new Set<number>();
  series.forEach((s) => s.timestamps.forEach((t) => all.add(t)));
  return [...all].sort((a, b) => a - b);
}

/** A series on the shared x axis, a missing bucket as a gap (null). */
export function alignTo(timestamps: number[], series: { timestamps: number[]; values: (number | null)[] }): (number | null)[] {
  const byTime = new Map(series.timestamps.map((t, i) => [t, series.values[i] ?? null]));
  return timestamps.map((t) => byTime.get(t) ?? null);
}

/** Where the dashboard on screen came from: forge's generated set, the project's own file, or synthesized. */
function DashboardOrigin({ specSource, dashboard }: { specSource: string; dashboard: Dashboard }) {
  const [label, title] =
    specSource !== "release"
      ? ["Synthesized", "Built from the services seen in the data: no environment in view has a recorded release bundle."]
      : dashboard.source === "user"
        ? ["Yours", "Your project's own file in deploy/observability/dashboards/, from the release that is running."]
        : ["Generated by forge", "forge generated it from your services, and it shipped with the release that is running."];
  return (
    <span
      className="rounded border border-border px-1.5 py-0.5 text-2xs font-medium uppercase tracking-wide text-muted-foreground"
      title={title}
      data-testid="obs-dashboard-origin"
      data-origin={specSource !== "release" ? "synthesized" : (dashboard.source ?? "gen")}
    >
      {label}
    </span>
  );
}

/** The release markers a dashboard declares, said in words: drawn, none in the window, or not available yet. */
function ReleaseMarkersNote({ label, releases }: { label: string; releases: ReleaseMarkers }) {
  const text =
    releases.status === "ready"
      ? releases.count
        ? `${label}: ${releases.count} promotion${releases.count === 1 ? "" : "s"} in this window, marked on every time-series panel.`
        : `${label}: nothing was promoted in this window.`
      : releases.status === "unavailable"
        ? `${label}: this dashboard marks every promotion on its charts. Release history isn't available here yet, so no markers are drawn.`
        : releases.status === "error"
          ? `${label}: the release history couldn't be loaded, so no markers are drawn.`
          : `${label}: loading release history…`;
  return (
    <p className="flex items-center gap-2 text-xs text-muted-foreground" data-testid="obs-release-markers" data-status={releases.status}>
      <span className="inline-block h-3 w-0 border-l border-dashed border-current" aria-hidden="true" />
      {text}
    </p>
  );
}

/**
 * One of a dashboard's own filter dropdowns (DashboardFilter): its values come
 * from the telemetry the filter names (`signal`, empty is traces) and picking
 * one adds it as an attribute filter, so it shows in the scope bar too.
 */
function DashboardFilterSelect({
  scope,
  range,
  filter,
  tokens,
  navigate,
}: {
  scope: ObsScope;
  range: RangePreset;
  filter: DashboardFilter;
  tokens: FilterToken[];
  navigate: ObsNavigate;
}) {
  const key = filter.field.replace(/^(resource|metric|log|span)\.attr\./, "");
  const usable = isAttributeKeyShaped(key);
  const signal = (filter.signal || "traces") as "traces" | "logs" | "metrics";
  const values = useAttributeValues(usable ? scope : null, range, key, "", signal);
  const current = tokens.find((t) => t.key === key && t.op === "eq")?.value ?? "";
  const id = `obs-dashboard-filter-${filter.id}`;
  if (!usable) return null;
  const options = values.data?.values.map((v) => v.value) ?? [];
  if (current && !options.includes(current)) options.unshift(current);
  return (
    <>
      <label htmlFor={id} className="sr-only">
        {filter.label || attributeLabel(key)}
      </label>
      <select
        id={id}
        value={current}
        onChange={(event) => {
          const rest = tokens.filter((t) => !(t.key === key && t.op === "eq"));
          const next = event.target.value ? [...rest, { key, op: "eq" as const, value: event.target.value }] : rest;
          navigate({ f: filterParam(next) }, { replace: true });
        }}
        className="h-8 max-w-[14rem] rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        data-testid={id}
        data-signal={signal}
      >
        <option value="">All {attributeLabel(key).toLowerCase()}s</option>
        {options.map((value) => (
          <option key={value} value={value}>
            {value || "(empty)"}
          </option>
        ))}
      </select>
    </>
  );
}
