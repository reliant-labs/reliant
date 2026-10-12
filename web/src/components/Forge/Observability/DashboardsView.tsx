// Copyright (c) 2025 Reliant Labs

/**
 * Dashboards: forge's dashboard spec (obsdash, `forge.dashboards/v1`) drawn
 * with uPlot — per-rpc RED panels by default, the environment's own spec once
 * a release ships one.
 *
 * The spec describes WHAT to show, each panel a query intent over OTel field
 * names. QueryPanel compiles that intent server-side and returns the panel's
 * data — lines, a number, or table rows — so this file only draws. A "search"
 * panel lists rows rather than aggregating them, which is what the Logs and
 * Traces views do, so it renders as a way into them. A panel the server
 * cannot compile (an unknown field, operator or aggregation) answers
 * InvalidArgument, and says so in place.
 */

import { ArrowRight, Info } from "lucide-react";

import Card, { CardHeader } from "@/components/forge-ui/card";
import { Button } from "@/components/ui/Button";
import { useDashboards, usePanelQuery } from "@/hooks/observability-queries";
import { obsErrorKind, obsErrorMessage } from "@/services/controlPlane/observability/errors";
import type { Dashboard, EnvScope, Panel, PanelResult, Predicate, Series, Unit } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { UPlotChart } from "./charts/UPlotChart";
import { formatValue } from "./format";
import { ServiceSelect } from "./LogsView";
import type { ObsNavigate } from "./obsSearch";
import { ObsError, ObsLoadingRows } from "./ObsStates";

const SERVICE_FIELD = "service.name";

/** The dashboard a bare link opens: the service's own when one is named, else the first (forge's overview). */
export function pickDashboard(dashboards: Dashboard[], requested: string | undefined, service: string | undefined): Dashboard | undefined {
  return (
    dashboards.find((d) => d.id === requested) ??
    (service ? dashboards.find((d) => d.id === `service-${service}`) : undefined) ??
    dashboards[0]
  );
}

export function DashboardsView({
  scope,
  envName,
  range,
  dashboardId,
  service,
  navigate,
}: {
  scope: EnvScope;
  envName: string;
  range: RangePreset;
  dashboardId: string | undefined;
  service: string | undefined;
  navigate: ObsNavigate;
}) {
  const spec = useDashboards(scope);

  if (spec.isLoading) return <ObsLoadingRows testId="obs-dashboards-loading" />;
  if (spec.error) {
    return <ObsError error={spec.error} envName={envName} onRetry={() => void spec.refetch()} retrying={spec.isFetching} />;
  }
  const dashboards = spec.data?.dashboards ?? [];
  if (dashboards.length === 0) {
    return (
      <p
        role="status"
        data-testid="obs-dashboards-empty"
        className="rounded-lg border border-dashed border-border bg-background px-4 py-5 text-sm text-muted-foreground"
      >
        No dashboards yet. They are built from the services this environment has sent telemetry from in the last 7 days,
        so they appear once a service has run.
      </p>
    );
  }

  const dashboard = pickDashboard(dashboards, dashboardId, service)!;
  const hasServiceFilter = dashboard.filters.some((filter) => filter.field === SERVICE_FIELD);
  // A service's own dashboard is already scoped to it; the filter would only narrow it to nothing.
  const serviceFilter = hasServiceFilter && dashboard.id !== `service-${service}` ? service : undefined;

  return (
    <div className="space-y-4" data-testid="obs-dashboards">
      <div className="flex flex-wrap items-center gap-2">
        <label htmlFor="obs-dashboard-picker" className="sr-only">
          Dashboard
        </label>
        <select
          id="obs-dashboard-picker"
          value={dashboard.id}
          onChange={(event) => navigate({ dashboard: event.target.value }, { replace: true })}
          className="h-8 rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          data-testid="obs-dashboard-picker"
        >
          {dashboards.map((d) => (
            <option key={d.id} value={d.id}>
              {d.title}
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
        {spec.data?.source === "synthesized" && (
          <span className="text-xs text-muted-foreground" data-testid="obs-dashboards-synthesized">
            Built from the services seen in the last 7 days.
          </span>
        )}
        {dashboard.description && <p className="w-full text-xs text-muted-foreground">{dashboard.description}</p>}
      </div>
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-[repeat(24,minmax(0,1fr))]">
        {dashboard.panels.map((panel) => (
          <PanelCard
            key={`${dashboard.id}/${panel.id}`}
            scope={scope}
            range={range}
            panel={panel}
            serviceFilter={serviceFilter}
            navigate={navigate}
          />
        ))}
      </div>
    </div>
  );
}

function PanelCard({
  scope,
  range,
  panel,
  serviceFilter,
  navigate,
}: {
  scope: EnvScope;
  range: RangePreset;
  panel: Panel;
  serviceFilter: string | undefined;
  navigate: ObsNavigate;
}) {
  const width = panel.width || (panel.kind === "number" ? 6 : 24);
  return (
    <Card
      padding="md"
      className="min-w-0 lg:[grid-column:span_var(--panel-span)/span_var(--panel-span)]"
      style={{ ["--panel-span" as string]: String(Math.min(Math.max(width, 1), 24)) }}
      data-testid={`obs-panel-${panel.id}`}
      data-kind={panel.kind}
    >
      <CardHeader title={panel.title} description={panel.description} />
      {panel.kind === "search" ? (
        <SearchLink panel={panel} serviceFilter={serviceFilter} navigate={navigate} />
      ) : (
        <PanelData scope={scope} range={range} panel={panel} serviceFilter={serviceFilter} navigate={navigate} />
      )}
    </Card>
  );
}

function PanelData({
  scope,
  range,
  panel,
  serviceFilter,
  navigate,
}: {
  scope: EnvScope;
  range: RangePreset;
  panel: Panel;
  serviceFilter: string | undefined;
  navigate: ObsNavigate;
}) {
  const result = usePanelQuery(scope, range, panel, serviceFilter ? [serviceFilter] : []);
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
  const truncated = data.truncated ? (
    <p className="mt-2 text-2xs text-muted-foreground">Only the largest series are shown.</p>
  ) : null;

  if (panel.kind === "number") {
    return <NumberGrid items={data.series.map((s) => ({ label: s.label, value: formatValue(s.value, unit) }))} />;
  }
  if (panel.kind === "table") {
    return (
      <>
        <PanelTable panel={panel} result={data} />
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
        series={data.series.map((s) => ({ label: seriesLabel(s, panel), values: alignTo(timestamps, s) }))}
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

const UNITS: readonly string[] = ["", "count", "ms", "bytes", "percent"];

/** The panel's declared unit; the server's when the panel declares none. */
function unitOf(panel: Panel, result: PanelResult): Unit {
  if (panel.unit) return panel.unit;
  return (UNITS.includes(result.unit) ? result.unit : "") as Unit;
}

function NumberGrid({ items }: { items: { label: string; value: string }[] }) {
  const many = items.length > 1;
  return (
    <dl className={cn("grid gap-3", many ? "grid-cols-3" : "grid-cols-1")} data-testid="obs-panel-number">
      {items.map((item) => (
        <div key={item.label}>
          <dt className={many ? "text-xs font-medium uppercase tracking-wide text-muted-foreground" : "sr-only"}>{item.label}</dt>
          <dd className={cn("font-semibold tabular-nums text-foreground", many ? "text-lg" : "text-2xl")}>{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}

/** One row per group, one column per series. A latency column is milliseconds whatever the panel's unit. */
function PanelTable({ panel, result }: { panel: Panel; result: PanelResult }) {
  const groupFields = panel.groupBy ?? [];
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
              <th key={field} scope="col" className="py-1 pr-3 font-medium">
                {field}
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
function seriesLabel(series: Series, panel: Panel): string {
  const group = Object.values(series.group).filter(Boolean).join(" · ");
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
