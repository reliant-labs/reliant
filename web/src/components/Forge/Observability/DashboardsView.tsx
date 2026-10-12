// Copyright (c) 2025 Reliant Labs

/**
 * Dashboards: forge's dashboard spec (obsdash, `forge.dashboards/v1`) drawn
 * with uPlot — per-rpc RED panels by default, the environment's own spec once
 * a release ships one.
 *
 * The spec describes WHAT to show, each panel a query intent over OTel field
 * names. There is no "run this panel" RPC, so panelPlan.ts translates each
 * intent into the RPCs that can answer it, and says so when none can. This
 * file only draws the plan.
 */

import { useMemo } from "react";
import { ArrowRight, Info } from "lucide-react";

import Card, { CardHeader } from "@/components/forge-ui/card";
import { Button } from "@/components/ui/Button";
import { useDashboards, useMetricQueries, useServiceOverview, useTraceRed } from "@/hooks/observability-queries";
import type {
  Dashboard,
  EnvScope,
  Panel,
  Series,
  ServiceHealth,
  TraceRed,
  TraceSummary,
} from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { UPlotChart } from "./charts/UPlotChart";
import { formatCount, formatDuration, formatPercent, formatRate, formatValue } from "./format";
import { ServiceSelect } from "./LogsView";
import type { ObsNavigate } from "./obsSearch";
import { ObsError, ObsLoadingRows } from "./ObsStates";
import { planPanel, type LabelledMeasure, type Measure, type PanelPlan } from "./panelPlan";

const SERVICE_FIELD = "service.name";
/** The newest traces a per-rpc table is grouped from. ListTraces' page cap. */
const RPC_SAMPLE = 500;

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
  const plan = useMemo(() => planPanel(panel, serviceFilter), [panel, serviceFilter]);
  const width = panel.width || (panel.kind === "number" ? 6 : 24);
  return (
    <Card
      padding="md"
      className="min-w-0 lg:[grid-column:span_var(--panel-span)/span_var(--panel-span)]"
      style={{ ["--panel-span" as string]: String(Math.min(Math.max(width, 1), 24)) }}
      data-testid={`obs-panel-${panel.id}`}
      data-plan={plan.kind}
    >
      <CardHeader title={panel.title} description={panel.description} />
      <PlanBody scope={scope} range={range} panel={panel} plan={plan} navigate={navigate} />
    </Card>
  );
}

function PlanBody({
  scope,
  range,
  panel,
  plan,
  navigate,
}: {
  scope: EnvScope;
  range: RangePreset;
  panel: Panel;
  plan: PanelPlan;
  navigate: ObsNavigate;
}) {
  switch (plan.kind) {
    case "overview":
      return <OverviewPlan scope={scope} range={range} panel={panel} plan={plan} />;
    case "red":
      return <RedPlan scope={scope} range={range} plan={plan} />;
    case "rpcTable":
      return <RpcTablePlan scope={scope} range={range} panel={panel} plan={plan} />;
    case "metric":
      return <MetricPlan scope={scope} range={range} panel={panel} plan={plan} />;
    case "link":
      return <LinkPlan plan={plan} navigate={navigate} />;
    case "none":
      return (
        <div className="space-y-2" data-testid="obs-panel-unsupported">
          <p className="flex items-start gap-2 text-xs text-muted-foreground">
            <Info className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
            {plan.reason}
          </p>
          <Button
            variant="ghost"
            size="xs"
            rightIcon={<ArrowRight className="h-3 w-3" />}
            onClick={() => navigate({ view: plan.to, service: plan.service, q: undefined })}
          >
            Open {plan.to === "logs" ? "Logs" : "Traces"}
          </Button>
        </div>
      );
  }
}

// ── Plan renderers ───────────────────────────────────────────────────────────

function PanelLoading() {
  return <div className="h-16 animate-pulse rounded bg-border/40 motion-reduce:animate-none" aria-busy="true" aria-label="Loading" />;
}

function PanelFailed() {
  return (
    <p className="text-xs text-destructive" role="alert" data-testid="obs-panel-error">
      This panel couldn&apos;t be loaded.
    </p>
  );
}

function PanelEmpty() {
  return (
    <p className="py-4 text-center text-xs text-muted-foreground" data-testid="obs-panel-empty">
      No data in this window
    </p>
  );
}

function Note({ children }: { children: string | undefined }) {
  if (!children) return null;
  return <p className="mt-2 text-2xs text-muted-foreground">{children}</p>;
}

export function formatMeasure(measure: Measure, value: number | null | undefined): string {
  if (value === null || value === undefined || !Number.isFinite(value)) return "—";
  switch (measure) {
    case "rate":
      return formatRate(value);
    case "errorRate":
      return formatPercent(value);
    case "p50":
    case "p95":
    case "p99":
      return formatDuration(value);
    default:
      return formatCount(value);
  }
}

function NumberGrid({ items }: { items: { label: string; value: string; alert?: boolean }[] }) {
  const many = items.length > 1;
  return (
    <dl className={cn("grid gap-3", many ? "grid-cols-3" : "grid-cols-1")} data-testid="obs-panel-number">
      {items.map((item) => (
        <div key={item.label}>
          <dt className={many ? "text-xs font-medium uppercase tracking-wide text-muted-foreground" : "sr-only"}>{item.label}</dt>
          <dd className={cn("font-semibold tabular-nums", many ? "text-lg" : "text-2xl", item.alert ? "text-destructive" : "text-foreground")}>
            {item.value}
          </dd>
        </div>
      ))}
    </dl>
  );
}

/** One measure over a set of services. Percentiles do not add, so several services report the slowest. */
export function overviewMeasure(services: ServiceHealth[], measure: Measure): number | null {
  if (services.length === 0) return null;
  const sum = (pick: (s: ServiceHealth) => number) => services.reduce((acc, s) => acc + pick(s), 0);
  switch (measure) {
    case "requests":
      return sum((s) => s.requestCount);
    case "rate":
      return sum((s) => s.requestRate);
    case "errors":
      return sum((s) => s.errorCount);
    case "errorRate": {
      const requests = sum((s) => s.requestCount);
      return requests ? sum((s) => s.errorCount) / requests : null;
    }
    case "p50":
      return Math.max(...services.map((s) => s.p50Ms));
    case "p95":
      return Math.max(...services.map((s) => s.p95Ms));
    case "p99":
      return Math.max(...services.map((s) => s.p99Ms));
  }
}

/** One measure per trend bucket, summed over services that share the overview's buckets. */
export function overviewTrend(services: ServiceHealth[], measure: Measure, step: number): { timestamps: number[]; values: (number | null)[] } {
  const timestamps = unionTimestamps(services);
  const total = (pick: (s: ServiceHealth) => (number | null)[]) =>
    timestamps.map((t) =>
      services.reduce((acc, s) => {
        const index = s.timestamps.indexOf(t);
        return acc + (index >= 0 ? (pick(s)[index] ?? 0) : 0);
      }, 0)
    );
  const requests = total((s) => s.requestTrend);
  const errors = total((s) => s.errorTrend);
  const values =
    measure === "errors"
      ? errors
      : measure === "errorRate"
        ? requests.map((r, i) => (r ? errors[i] / r : null))
        : measure === "rate"
          ? requests.map((r) => r / step)
          : requests;
  return { timestamps, values };
}

function OverviewPlan({
  scope,
  range,
  panel,
  plan,
}: {
  scope: EnvScope;
  range: RangePreset;
  panel: Panel;
  plan: Extract<PanelPlan, { kind: "overview" }>;
}) {
  const overview = useServiceOverview(scope, range, plan.services);
  if (overview.isLoading) return <PanelLoading />;
  if (overview.error) return <PanelFailed />;
  const services = (overview.data?.services ?? []).filter((s) => s.requestCount > 0);
  if (services.length === 0) return <PanelEmpty />;
  const step = overview.data?.trendStepSeconds ?? 60;

  if (plan.shape === "table") {
    return (
      <MeasureTable
        caption={panel.title}
        groupLabel="service"
        measures={plan.measures}
        rows={services.map((s) => ({ key: s.name, values: plan.measures.map((m) => overviewMeasure([s], m.measure)) }))}
      />
    );
  }
  if (plan.shape === "number") {
    const slowest = services.length > 1 && plan.measures.some((m) => m.measure.startsWith("p"));
    return (
      <>
        <NumberGrid
          items={plan.measures.map((m) => ({
            label: m.label,
            value: formatMeasure(m.measure, overviewMeasure(services, m.measure)),
          }))}
        />
        <Note>{plan.note ?? (slowest ? "Percentiles show the slowest service." : undefined)}</Note>
      </>
    );
  }
  // Lines: one per service when grouped, else one per measure over all of them.
  const measure = plan.measures[0].measure;
  const lines = plan.byService
    ? services.map((s) => ({ label: s.name, ...overviewTrend([s], measure, step) }))
    : plan.measures.map((m) => ({ label: m.label, ...overviewTrend(services, m.measure, step) }));
  const timestamps = unionTimestamps(lines);
  return (
    <UPlotChart
      height={Math.max((panel.height || 7) * 28 - 72, 120)}
      timestamps={timestamps}
      series={lines.map((l) => ({ label: l.label, values: alignTo(timestamps, l) }))}
      unit={measure === "errorRate" ? "percent" : "count"}
      label={panel.title}
    />
  );
}

function redValue(red: TraceRed, measure: Measure): number {
  switch (measure) {
    case "requests":
      return red.traceCount;
    case "rate":
      return red.ratePerSecond;
    case "errors":
      return red.errorTraceCount;
    case "errorRate":
      return red.errorRate;
    case "p50":
      return red.p50Ms;
    case "p95":
      return red.p95Ms;
    case "p99":
      return red.p99Ms;
  }
}

function RedPlan({ scope, range, plan }: { scope: EnvScope; range: RangePreset; plan: Extract<PanelPlan, { kind: "red" }> }) {
  const result = useTraceRed(scope, range, plan.scope, 1);
  if (result.isLoading) return <PanelLoading />;
  if (result.error) return <PanelFailed />;
  const red = result.data?.red;
  if (!red || red.traceCount === 0) return <PanelEmpty />;
  return (
    <>
      <NumberGrid
        items={plan.measures.map((m) => ({
          label: m.label,
          value: formatMeasure(m.measure, redValue(red, m.measure)),
          alert: m.measure === "errorRate" && red.errorRate >= 0.05,
        }))}
      />
      <Note>{plan.note}</Note>
    </>
  );
}

/** Nearest-rank percentile over a sorted array. */
function percentile(sorted: number[], p: number): number | null {
  if (sorted.length === 0) return null;
  return sorted[Math.min(sorted.length - 1, Math.max(0, Math.ceil(p * sorted.length) - 1))];
}

/** Per-root-operation RED from a sample of traces — what the per-rpc table shows. */
export function groupByRootOperation(traces: TraceSummary[], measures: LabelledMeasure[], windowSeconds: number) {
  const groups = new Map<string, TraceSummary[]>();
  for (const trace of traces) {
    const key = trace.rootOperation || "(unnamed)";
    const group = groups.get(key);
    if (group) group.push(trace);
    else groups.set(key, [trace]);
  }
  return [...groups.entries()]
    .map(([key, group]) => {
      const durations = group.map((t) => t.durationMs).sort((a, b) => a - b);
      const errors = group.filter((t) => t.errorCount > 0).length;
      const value = (measure: Measure): number | null => {
        switch (measure) {
          case "requests":
            return group.length;
          case "rate":
            return windowSeconds > 0 ? group.length / windowSeconds : null;
          case "errors":
            return errors;
          case "errorRate":
            return errors / group.length;
          case "p50":
            return percentile(durations, 0.5);
          case "p95":
            return percentile(durations, 0.95);
          case "p99":
            return percentile(durations, 0.99);
        }
      };
      return { key, values: measures.map((m) => value(m.measure)), count: group.length };
    })
    .sort((a, b) => b.count - a.count);
}

function RpcTablePlan({
  scope,
  range,
  panel,
  plan,
}: {
  scope: EnvScope;
  range: RangePreset;
  panel: Panel;
  plan: Extract<PanelPlan, { kind: "rpcTable" }>;
}) {
  const result = useTraceRed(scope, range, plan.scope, RPC_SAMPLE);
  if (result.isLoading) return <PanelLoading />;
  if (result.error) return <PanelFailed />;
  const traces = result.data?.traces ?? [];
  if (traces.length === 0) return <PanelEmpty />;
  const newest = Math.max(...traces.map((t) => t.startMs));
  const oldest = Math.min(...traces.map((t) => t.startMs));
  const rows = groupByRootOperation(traces, plan.measures, Math.max((newest - oldest) / 1000, 1));
  const sampled = traces.length >= RPC_SAMPLE;
  return (
    <>
      <MeasureTable caption={panel.title} groupLabel="rpc" measures={plan.measures} rows={rows} />
      <Note>
        {sampled
          ? `Grouped by root operation from the ${RPC_SAMPLE} newest traces in the window.`
          : `Grouped by root operation from all ${traces.length} traces in the window.`}
      </Note>
    </>
  );
}

function MeasureTable({
  caption,
  groupLabel,
  measures,
  rows,
}: {
  caption: string;
  groupLabel: string;
  measures: LabelledMeasure[];
  rows: { key: string; values: (number | null)[] }[];
}) {
  return (
    <div className="max-h-80 overflow-auto">
      <table className="w-full text-xs" data-testid="obs-panel-table">
        <caption className="sr-only">{caption}</caption>
        <thead>
          <tr className="border-b border-border text-left text-muted-foreground">
            <th scope="col" className="py-1 pr-3 font-medium">
              {groupLabel}
            </th>
            {measures.map((m) => (
              <th key={m.label} scope="col" className="py-1 pr-3 text-right font-medium">
                {m.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.key} className="border-b border-border/50 last:border-b-0">
              <th scope="row" className="max-w-[24rem] truncate py-1 pr-3 text-left font-mono font-normal text-foreground">
                {row.key}
              </th>
              {measures.map((m, index) => (
                <td key={m.label} className="py-1 pr-3 text-right tabular-nums text-foreground">
                  {formatMeasure(m.measure, row.values[index])}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function MetricPlan({
  scope,
  range,
  panel,
  plan,
}: {
  scope: EnvScope;
  range: RangePreset;
  panel: Panel;
  plan: Extract<PanelPlan, { kind: "metric" }>;
}) {
  const results = useMetricQueries(scope, range, plan.queries, plan.shape === "line" ? 0 : "window");
  if (results.some((r) => r.isLoading)) return <PanelLoading />;
  if (results.some((r) => r.error)) return <PanelFailed />;
  // Each query's series, labelled by the panel series it answers.
  const series: Series[] = results.flatMap((r, index) =>
    (r.data?.series ?? []).map((s) => ({
      ...s,
      label: Object.keys(s.group).length ? `${s.label} — ${plan.queries[index].label}` : plan.queries[index].label,
    }))
  );
  if (!series.some((s) => s.values.some((v) => v !== null))) return <PanelEmpty />;

  if (plan.shape === "number") {
    return <NumberGrid items={series.slice(0, 3).map((s) => ({ label: s.label, value: formatValue(lastValue(s), panel.unit) }))} />;
  }
  if (plan.shape === "table") {
    return (
      <div className="max-h-80 overflow-auto">
        <table className="w-full text-xs" data-testid="obs-panel-table">
          <caption className="sr-only">{panel.title}</caption>
          <tbody>
            {series.map((s) => (
              <tr key={s.label} className="border-b border-border/50 last:border-b-0">
                <th scope="row" className="truncate py-1 pr-3 text-left font-mono font-normal text-foreground">
                  {s.label}
                </th>
                <td className="py-1 text-right tabular-nums text-foreground">{formatValue(lastValue(s), panel.unit)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    );
  }
  const timestamps = unionTimestamps(series);
  return (
    <UPlotChart
      height={Math.max((panel.height || 7) * 28 - 72, 120)}
      timestamps={timestamps}
      series={series.map((s) => ({ label: s.label, values: alignTo(timestamps, s) }))}
      unit={panel.unit}
      label={panel.title}
    />
  );
}

function LinkPlan({ plan, navigate }: { plan: Extract<PanelPlan, { kind: "link" }>; navigate: ObsNavigate }) {
  const toLogs = plan.to === "logs";
  return (
    <div className="flex items-center justify-between gap-3">
      <p className="text-xs text-muted-foreground">
        {toLogs ? "Log records" : "Spans"}
        {plan.service ? (
          <>
            {" "}
            from <span className="font-mono text-foreground">{plan.service}</span>
          </>
        ) : null}
        {plan.errorsOnly ? ", errors only" : ""}.
      </p>
      <Button
        variant="outline"
        size="sm"
        rightIcon={<ArrowRight className="h-3.5 w-3.5" />}
        onClick={() =>
          toLogs
            ? navigate({ view: "logs", service: plan.service, severity: plan.errorsOnly ? "error,fatal" : undefined, q: undefined })
            : navigate({ view: "traces", service: plan.service, errors: plan.errorsOnly || undefined, q: undefined })
        }
      >
        Open in {toLogs ? "Logs" : "Traces"}
      </Button>
    </div>
  );
}

function lastValue(series: Series | undefined): number | null {
  if (!series) return null;
  for (let i = series.values.length - 1; i >= 0; i -= 1) {
    const value = series.values[i];
    if (value !== null && Number.isFinite(value)) return value;
  }
  return null;
}

/** Every bucket any series has — a grouped query's series can each skip buckets they had no rows in. */
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
