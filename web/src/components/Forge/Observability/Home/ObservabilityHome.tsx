// Copyright (c) 2025 Reliant Labs

/**
 * OBSERVABILITY — the home of the area (PROPOSAL §4.6, decision D9: the
 * views are nav entries, so this page has no tab strip of its own).
 *
 *   strip        requests · error rate · p95 · alerts firing, each with the
 *                change against the previous window and a sparkline carrying
 *                the release markers of the scope.
 *   services     sorted by what needs attention: status, alerts firing, error
 *                rate. Each row opens the workload.
 *   right rail   recent releases into the scope, and the ways into the
 *                explorers and dashboards.
 *
 * Everything reads the console scope (one environment, or all of them) and
 * the window in the URL. Nothing here writes.
 */

import { useMemo, type ReactNode } from "react";
import { ArrowRight } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import {
  useAlertHistory,
  useAlerts,
  useObservabilityOrg,
  usePanelQuery,
  useServiceOverview,
  useWorkloads,
} from "@/hooks/observability-queries";
import { obsErrorKind } from "@/services/controlPlane/observability/errors";
import type { ObsScope, PanelResult } from "@/services/controlPlane/observability/types";
import { RANGE_LABELS, RANGE_PRESETS, type RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { ENV_ALL } from "../../Console/envScope";
import { ENV_KEY } from "../attributes";
import { UPlotChart, type ChartMarker } from "../charts/UPlotChart";
import { DashboardHeader } from "../Dashboards/DashboardHeader";
import { alertBands, rangeMs } from "../Dashboards/dashboardModel";
import { unionTimestamps } from "../Dashboards/PanelBody";
import { INK_LINK, VariableSelect } from "../Dashboards/parts";
import { describePromotion, useReleaseMarkers } from "../releaseMarkers";
import { formatAgo, formatCores, formatCount, formatDuration, formatPercent, formatRate } from "../format";
import type { ObsEnvironment } from "../obsPlace";
import type { ObsSearch } from "../obsSearch";
import { ObsError, ObsNotSetUp } from "../ObsStates";
import { UsageBar } from "../WorkloadsView";
import { byAttention, firingCounts, ratioSeries, redPanel, seriesValues, serviceRows, type ServiceRow } from "./homeModel";

export interface ObservabilityHomeHrefs {
  logs: (search?: ObsSearch) => string;
  traces: (search?: ObsSearch) => string;
  profiles: (search?: ObsSearch) => string;
  alerts: string;
  dashboards: string;
  /** The workload's page (the Overview's workload detail), in an environment when one is named. */
  workload: (name: string, env: string | null) => string;
  release: (version: string) => string;
  releases: string;
}

export interface ObservabilityHomeProps {
  project: string;
  /** The console scope: an environment name or "all". */
  env: string;
  environments: readonly ObsEnvironment[];
  range: RangePreset;
  onRange: (range: RangePreset) => void;
  hrefs: ObservabilityHomeHrefs;
  navigateTo: (href: string) => void;
}

const RANGE_WORDS: Record<RangePreset, string> = {
  "15m": "last 15 minutes",
  "1h": "last hour",
  "6h": "last 6 hours",
  "24h": "last 24 hours",
  "7d": "last 7 days",
};

export function ObservabilityHome({ project, env, environments, range, onRange, hrefs, navigateTo }: ObservabilityHomeProps) {
  const org = useObservabilityOrg();
  const orgId = org.orgId;
  const envId = env === ENV_ALL ? null : (environments.find((candidate) => candidate.name === env)?.id ?? null);
  const unrecorded = env !== ENV_ALL && !envId;
  const scope = useMemo<ObsScope | null>(
    () => (orgId && !unrecorded ? { orgId, projectId: project, envIds: envId ? [envId] : [], filters: [] } : null),
    [orgId, project, envId, unrecorded]
  );
  const groupBy = useMemo(() => (env === ENV_ALL ? [ENV_KEY] : []), [env]);

  const overview = useServiceOverview(scope, range, [], groupBy);
  const workloads = useWorkloads(scope, range);
  const alerts = useAlerts(orgId, envId ?? undefined);
  const history = useAlertHistory(orgId, range, envId ?? undefined);
  const releases = useReleaseMarkers(scope, range, environments, !!scope);
  const numberPanel = useMemo(() => redPanel("number"), []);
  const linePanel = useMemo(() => redPanel("line"), []);
  const totals = usePanelQuery(scope, range, numberPanel, []);
  const previous = usePanelQuery(scope, range, numberPanel, [], [], { offsetKey: `period:${range}`, offsetMs: () => rangeMs(range) });
  const trend = usePanelQuery(scope, range, linePanel, []);

  const scopeEnvIds = useMemo(() => (envId ? [envId] : environments.map((candidate) => candidate.id)), [envId, environments]);
  const firing = (alerts.data ?? []).filter((a) => a.state === "firing" && scopeEnvIds.includes(a.envId));
  const rows = useMemo(
    () => serviceRows(overview.data?.services ?? [], workloads.data?.workloads ?? [], alerts.data ?? [], (id) => environments.find((e) => e.id === id)?.name).sort(byAttention),
    [overview.data, workloads.data, alerts.data, environments]
  );

  const meta = (
    <>
      {env === ENV_ALL ? <span>All environments</span> : <Tag>{env}</Tag>}
      <span>{RANGE_WORDS[range]}</span>
      <span>Dashed lines are releases</span>
    </>
  );
  const header = (
    <DashboardHeader
      title="Observability"
      meta={meta}
      actions={
        <VariableSelect
          id="observability-range"
          label="time"
          value={range}
          options={RANGE_PRESETS.map((preset) => ({ value: preset, label: RANGE_LABELS[preset] }))}
          onChange={(value) => onRange(value as RangePreset)}
        />
      }
      testId="observability-home-header"
    />
  );

  if (unrecorded) {
    return (
      <div className="space-y-6" data-testid="observability-home">
        {header}
        <p className="rounded-lg border border-dashed border-border bg-surface px-4 py-5 text-sm text-ink-muted" data-testid="observability-unrecorded-env">
          Reliant has no record of <span className="font-medium text-ink">{env}</span>, so no telemetry is filed under it. Once it is deployed through Reliant, its logs,
          traces and metrics appear here.
        </p>
      </div>
    );
  }
  if (!scope && !org.isLoading) {
    return (
      <div className="space-y-6" data-testid="observability-home">
        {header}
        {org.error ? <ObsError error={org.error} subject={project} onRetry={() => void org.refetch()} /> : <ObsNotSetUp subject={project} />}
      </div>
    );
  }
  if (overview.error && obsErrorKind(overview.error) === "not-set-up") {
    return (
      <div className="space-y-6" data-testid="observability-home">
        {header}
        <ObsNotSetUp subject={project} />
      </div>
    );
  }

  const markers = releases.markers;
  const bands = alertBands(history.data ?? [], scopeEnvIds, Date.now());

  return (
    <div className="space-y-4" data-testid="observability-home">
      {header}
      <Strip
        totals={totals}
        previous={previous.data}
        trend={trend}
        markers={markers}
        firing={firing.length}
        firingDetail={firing[0] ? `${firing[0].rule}${firing[0].firingAtMs ? ` · ${formatAgo(firing[0].firingAtMs).replace(" ago", "")}` : ""}` : null}
        firingCounts={(timestamps) => firingCounts(timestamps, bands)}
        range={range}
        alertsHref={hrefs.alerts}
      />
      <div className="grid grid-cols-1 gap-4 xl:grid-cols-[minmax(0,1fr)_340px]">
        <Card padding="none" className="min-w-0 overflow-hidden" data-testid="observability-services">
          <div className="px-4 pt-3">
            <h2 className="text-sm font-medium text-ink">Services</h2>
            <p className="text-xs text-ink-muted">Sorted by what needs attention first.</p>
          </div>
          <ServicesTable
            rows={rows}
            loading={overview.isLoading}
            error={overview.error}
            onRetry={() => void overview.refetch()}
            showEnv={env === ENV_ALL}
            range={range}
            project={project}
            workloadHref={hrefs.workload}
            navigateTo={navigateTo}
          />
        </Card>
        <div className="space-y-4">
          <RailPanel title="Recent releases" testId="observability-recent-releases">
            {releases.status === "loading" ? (
              <div className="h-20 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" />
            ) : releases.promotions.length === 0 ? (
              <p className="text-xs text-ink-muted">
                Nothing was promoted {env === ENV_ALL ? "" : `to ${env} `}in the {RANGE_WORDS[range]}.{" "}
                <Link href={hrefs.releases} className={INK_LINK}>
                  All releases
                </Link>
              </p>
            ) : (
              <ol className="space-y-3">
                {releases.promotions.slice(0, 4).map((p, index) => (
                  <li key={p.promotionId} className="relative pl-5 text-xs">
                    <span
                      aria-hidden="true"
                      className={cn("absolute left-0.5 top-1 h-2 w-2 rounded-full border", index === 0 ? "border-success bg-success" : "border-ink-muted bg-surface")}
                    />
                    <Link href={hrefs.release(p.version)} className={`${INK_LINK} text-sm`} title={describePromotion(p)}>
                      {p.version || p.releaseId.slice(0, 12)}
                    </Link>
                    <span className="block text-ink-muted">
                      {p.kind === "rollback" ? "rolled back " : ""}
                      {p.envName} · {formatAgo(p.promotedAtMs)}
                      {p.promotedBy ? ` · ${p.promotedBy}` : ""}
                    </span>
                  </li>
                ))}
              </ol>
            )}
          </RailPanel>
          <RailPanel title="Explore" testId="observability-explore">
            <nav className="flex flex-col" aria-label="Explore">
              {(
                [
                  ["Search logs", hrefs.logs({ range })],
                  ["Find slow traces", hrefs.traces({ range, minDuration: "1s" })],
                  ["Compare profiles", hrefs.profiles({ range })],
                  ["Your dashboards", hrefs.dashboards],
                ] as const
              ).map(([label, href]) => (
                <Link key={label} href={href} className="no-color flex h-8 items-center justify-between rounded-md px-2 -mx-2 text-sm text-ink hover:bg-ink/5">
                  {label}
                  <ArrowRight className="h-3.5 w-3.5 text-ink-muted" aria-hidden="true" />
                </Link>
              ))}
            </nav>
          </RailPanel>
        </div>
      </div>
    </div>
  );
}

function RailPanel({ title, children, testId }: { title: string; children: ReactNode; testId: string }) {
  return (
    <Card padding="none" data-testid={testId}>
      <h2 className="px-4 pt-3 text-sm font-medium text-ink">{title}</h2>
      <div className="p-4 pt-3">{children}</div>
    </Card>
  );
}

// ── The strip ───────────────────────────────────────────────────────────────

interface Query<T> {
  data: T | undefined;
  isLoading: boolean;
  error: unknown;
}

function valueOf(result: PanelResult | undefined, label: string): number | null {
  return result?.series.find((s) => s.label === label)?.value ?? null;
}

function change(now: number | null, before: number | null, format: (delta: number) => string, label: string): string | null {
  if (now === null || before === null) return null;
  const delta = now - before;
  if (Math.abs(delta) < 1e-9) return `no change vs ${label}`;
  return `${delta > 0 ? "+" : "−"}${format(Math.abs(delta))} vs ${label}`;
}

function Strip({
  totals,
  previous,
  trend,
  markers,
  firing,
  firingDetail,
  firingCounts: countsOf,
  range,
  alertsHref,
}: {
  totals: Query<PanelResult>;
  previous: PanelResult | undefined;
  trend: Query<PanelResult>;
  markers: readonly ChartMarker[];
  firing: number;
  firingDetail: string | null;
  firingCounts: (timestamps: number[]) => number[];
  range: RangePreset;
  alertsHref: string;
}) {
  const label = `previous ${range === "7d" ? "week" : range === "24h" ? "day" : range}`;
  const requests = valueOf(totals.data, "requests");
  const errors = valueOf(totals.data, "errors");
  const p95 = valueOf(totals.data, "p95");
  const prevRequests = valueOf(previous, "requests");
  const prevErrors = valueOf(previous, "errors");
  const prevP95 = valueOf(previous, "p95");
  const rate = requests ? (errors ?? 0) / requests : null;
  const prevRate = prevRequests ? (prevErrors ?? 0) / prevRequests : null;

  const timestamps = trend.data ? unionTimestamps(trend.data.series) : [];
  const trendRequests = trend.data ? seriesValues(trend.data.series, "requests") : [];
  const trendErrors = trend.data ? seriesValues(trend.data.series, "errors") : [];
  const trendP95 = trend.data ? seriesValues(trend.data.series, "p95") : [];
  const loading = totals.isLoading;
  const failed = !!totals.error;

  return (
    <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4" data-testid="observability-strip">
      <Cell
        label="Requests"
        figure={loading ? null : failed ? "—" : formatCount(requests ?? 0)}
        detail={requests !== null && prevRequests ? change(requests, prevRequests, (d) => formatPercent(d / prevRequests), label) : null}
        spark={{ timestamps, values: trendRequests, unit: "count", tone: "primary" }}
        markers={markers}
      />
      <Cell
        label="Error rate"
        figure={loading ? null : failed ? "—" : rate === null ? "—" : formatPercent(rate)}
        tone={rate !== null && rate >= 0.05 ? "warning" : undefined}
        detail={change(rate, prevRate, (d) => `${formatCount(d * 100)}pt`, label)}
        spark={{ timestamps, values: ratioSeries(trendErrors, trendRequests), unit: "percent", tone: "destructive" }}
        markers={markers}
      />
      <Cell
        label="p95 latency"
        figure={loading ? null : failed ? "—" : p95 === null ? "—" : formatDuration(p95)}
        detail={change(p95, prevP95, (d) => formatDuration(d), label)}
        spark={{ timestamps, values: trendP95, unit: "ms", tone: "secondary" }}
        markers={markers}
      />
      <Cell
        label="Alerts firing"
        figure={String(firing)}
        tone={firing > 0 ? "warning" : undefined}
        detail={firingDetail ?? "none in this scope"}
        spark={{ timestamps, values: countsOf(timestamps), unit: "count", tone: "warning" }}
        markers={markers}
        href={alertsHref}
      />
    </div>
  );
}

function Cell({
  label,
  figure,
  detail,
  spark,
  markers,
  tone,
  href,
}: {
  label: string;
  /** Null while loading. */
  figure: string | null;
  detail: string | null;
  spark: { timestamps: number[]; values: (number | null)[]; unit: "count" | "percent" | "ms"; tone: "primary" | "destructive" | "secondary" | "warning" };
  markers: readonly ChartMarker[];
  tone?: "warning";
  href?: string;
}) {
  const body = (
    <>
      <div className="text-xs text-ink-muted">{label}</div>
      {figure === null ? (
        <div className="mt-1 h-7 w-20 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-busy="true" aria-label={`Loading ${label}`} />
      ) : (
        <div className={cn("mt-0.5 text-xl font-semibold tabular-nums", tone === "warning" ? "text-warning-ink" : "text-ink")}>{figure}</div>
      )}
      <div className="min-h-4 truncate text-xs text-ink-muted">{detail ?? " "}</div>
      <div className="mt-2 h-9">
        {spark.timestamps.length > 1 && (
          <UPlotChart
            compact
            height={36}
            timestamps={spark.timestamps}
            series={[{ label, values: spark.values, tone: spark.tone }]}
            unit={spark.unit}
            label={`${label} over the window`}
            markers={markers}
          />
        )}
      </div>
    </>
  );
  return (
    <Card padding="none" className="min-w-0 px-4 pb-3 pt-3" data-testid={`observability-cell-${label.toLowerCase().replace(/\s+/g, "-")}`}>
      {href ? (
        <Link href={href} className="no-color block">
          {body}
        </Link>
      ) : (
        body
      )}
    </Card>
  );
}

// ── Services ────────────────────────────────────────────────────────────────

function ServicesTable({
  rows,
  loading,
  error,
  onRetry,
  showEnv,
  range,
  project,
  workloadHref,
  navigateTo,
}: {
  rows: ServiceRow[];
  loading: boolean;
  error: unknown;
  onRetry: () => void;
  showEnv: boolean;
  range: RangePreset;
  project: string;
  workloadHref: (name: string, env: string | null) => string;
  navigateTo: (href: string) => void;
}) {
  if (loading) {
    return (
      <div className="mt-2 space-y-px" aria-busy="true" aria-label="Loading services">
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="h-10 animate-pulse border-t border-border/70 bg-ink/5 motion-reduce:animate-none" />
        ))}
      </div>
    );
  }
  if (error) return <div className="p-4"><ObsError error={error} subject={project} onRetry={onRetry} /></div>;
  if (rows.length === 0) {
    return (
      <p className="px-4 pb-6 pt-4 text-sm text-ink-muted" data-testid="observability-no-services">
        No service sent telemetry in the {RANGE_WORDS[range]}. Services built with forge send it on their own once they run; an idle one looks exactly like
        this.
      </p>
    );
  }
  return (
    <table className="mt-2 w-full text-sm">
      <caption className="sr-only">Services, sorted by what needs attention</caption>
      <thead>
        <tr className="border-b border-border text-left text-xs text-ink-muted">
          <th scope="col" className="h-9 px-4 font-medium">
            Name
          </th>
          {showEnv && (
            <th scope="col" className="h-9 px-4 font-medium">
              Environment
            </th>
          )}
          <th scope="col" className="h-9 px-4 font-medium">
            Status
          </th>
          <th scope="col" className="h-9 px-4 text-right font-medium">
            Req/s
          </th>
          <th scope="col" className="h-9 px-4 text-right font-medium">
            Errors
          </th>
          <th scope="col" className="h-9 px-4 text-right font-medium">
            p95
          </th>
          <th scope="col" className="hidden h-9 px-4 font-medium lg:table-cell">
            CPU
          </th>
          <th scope="col" className="h-9 px-4 text-right font-medium">
            Alerts
          </th>
        </tr>
      </thead>
      <tbody>
        {rows.map((row) => {
          const href = workloadHref(row.name, row.env);
          const cpu = row.workload?.containers[0]?.cpu ?? null;
          return (
            <tr
              key={`${row.name}/${row.env ?? ""}`}
              className="cursor-pointer border-b border-border/70 last:border-b-0 hover:bg-ink/5"
              onClick={(event) => {
                if ((event.target as HTMLElement).closest("a, button")) return;
                navigateTo(href);
              }}
              data-testid={`observability-service-${row.name}${row.env ? `-${row.env}` : ""}`}
              data-tone={row.tone}
            >
              <td className="h-10 px-4">
                <Link href={href} className="no-color font-medium text-ink">
                  {row.name}
                </Link>
              </td>
              {showEnv && (
                <td className="h-10 px-4">
                  <Tag>{row.env ?? "—"}</Tag>
                </td>
              )}
              <td className="h-10 px-4">
                <span className="inline-flex items-center gap-2" title={row.reason ?? undefined}>
                  <Status tone={row.tone} label={row.status} />
                  {row.reason && <span className="hidden truncate text-xs text-ink-muted 2xl:inline">{row.reason}</span>}
                </span>
              </td>
              <td className="h-10 px-4 text-right tabular-nums text-ink">{formatRate(row.health.requestRate)}</td>
              <td className={cn("h-10 px-4 text-right tabular-nums", row.health.errorRate >= 0.05 ? "text-warning-ink" : "text-ink")}>
                {row.health.requestCount ? formatPercent(row.health.errorRate) : "—"}
              </td>
              <td className="h-10 px-4 text-right tabular-nums text-ink">{row.health.requestCount ? formatDuration(row.health.p95Ms) : "—"}</td>
              <td className="hidden h-10 w-40 px-4 lg:table-cell">
                {cpu ? <UsageBar usage={cpu} format={formatCores} what="CPU" /> : <span className="text-xs text-ink-muted">—</span>}
              </td>
              <td className="h-10 px-4 text-right tabular-nums">
                {row.alertsFiring > 0 ? <span className="text-warning-ink">{row.alertsFiring} firing</span> : <span className="text-ink-muted">·</span>}
              </td>
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}

