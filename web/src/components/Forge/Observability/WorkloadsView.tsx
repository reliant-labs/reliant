// Copyright (c) 2025 Reliant Labs

/**
 * Workloads: customer metadata (owner decision 4, contract K2) — what the node
 * agent records about the customer's own workloads.
 *
 * The list answers "is anything unhealthy?" per workload: the image and
 * version running, restarts and the last reason a container exited
 * (OOMKilled is the one that matters most), CPU and memory against their
 * limits, and the latest warning event. A workload opens into its detail:
 * resource use over time, the rollouts seen, and the Kubernetes events.
 *
 * A workload name is the comparison key: on the project area the detail
 * draws the same workload in every environment in scope on the same axes,
 * which is how "staging uses twice the memory prod does" is seen.
 *
 * "Customer metadata" is the product's term for this data. "Infrastructure"
 * — nodes and the cluster itself — is the platform's, and never shown here.
 */

import { useMemo, useState } from "react";
import { AlertTriangle, ArrowLeft, Boxes } from "lucide-react";

import Card, { CardHeader } from "@/components/forge-ui/card";
import { Button } from "@/components/ui/Button";
import { useWorkload, useWorkloads } from "@/hooks/observability-queries";
import type {
  ObsScope,
  ResourceUsage,
  Workload,
  WorkloadContainer,
  WorkloadDetail,
  WorkloadEvent,
  WorkloadMetric,
} from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { UPlotChart, useInheritedChartMarkers, type ChartMarker } from "./charts/UPlotChart";
import { formatAgo, formatBytes, formatCores, formatCount, formatPercent, formatTimestamp, type ValueUnit } from "./format";
import type { ObsNavigate } from "./obsSearch";
import { ObsError, ObsLoadingRows, ObsNoCustomerMetadata } from "./ObsStates";

/** Exit reasons that mean the container was killed, not that it finished. */
const FATAL_REASONS = new Set(["OOMKilled", "Error", "ContainerCannotRun", "DeadlineExceeded"]);

/** Event reasons a reader is looking for when something is wrong. */
const ALARMING_EVENTS = new Set([
  "BackOff",
  "CrashLoopBackOff",
  "Failed",
  "FailedScheduling",
  "FailedMount",
  "ErrImagePull",
  "ImagePullBackOff",
  "Evicted",
  "OOMKilling",
  "Unhealthy",
  "Killing",
]);

/** At or above this share of a limit, usage reads as a risk. */
export const USAGE_WARN = 0.8;
export const USAGE_ALERT = 0.95;

export function WorkloadsView({
  scope,
  subject,
  range,
  service,
  workload,
  container,
  showEnv,
  navigate,
}: {
  scope: ObsScope;
  subject: string;
  range: RangePreset;
  service: string | undefined;
  workload: string | undefined;
  container: string | undefined;
  /** The project area: a column, and per-env series, for the environment. */
  showEnv: boolean;
  navigate: ObsNavigate;
}) {
  const list = useWorkloads(scope, range, service ? [service] : []);

  if (list.isLoading) return <ObsLoadingRows testId="obs-workloads-loading" />;
  if (list.error && !list.data) {
    return <ObsError error={list.error} subject={subject} onRetry={() => void list.refetch()} retrying={list.isFetching} />;
  }
  const workloads = list.data?.workloads ?? [];
  if (workload) {
    return (
      <WorkloadDetailView
        scope={scope}
        subject={subject}
        range={range}
        name={workload}
        container={container}
        rows={workloads.filter((w) => w.workload === workload)}
        showEnv={showEnv}
        navigate={navigate}
      />
    );
  }
  if (workloads.length === 0) {
    const filtered = scope.filters.length > 0 || !!service;
    return (
      <ObsNoCustomerMetadata
        range={range}
        filtered={filtered}
        onWiden={() => navigate({ range: "7d" })}
        onClearFilters={() => navigate({ f: undefined, service: undefined })}
      />
    );
  }
  return (
    <div className="space-y-3" data-testid="obs-workloads">
      <WorkloadSummary workloads={workloads} />
      <Card padding="none">
        <div className="overflow-x-auto">
          <table className="w-full text-sm">
            <caption className="sr-only">
              Workloads in {subject}: image, restarts, CPU and memory against limits, and the latest warning, over the selected window
            </caption>
            <thead>
              <tr className="border-b border-border text-left text-xs font-medium uppercase tracking-wide text-muted-foreground">
                <th scope="col" className="px-4 py-2 font-medium">Workload</th>
                {showEnv && <th scope="col" className="px-4 py-2 font-medium">Environment</th>}
                <th scope="col" className="px-4 py-2 font-medium">Image</th>
                <th scope="col" className="px-4 py-2 font-medium">Restarts</th>
                <th scope="col" className="px-4 py-2 font-medium">CPU</th>
                <th scope="col" className="px-4 py-2 font-medium">Memory</th>
                <th scope="col" className="px-4 py-2 font-medium">Latest warning</th>
              </tr>
            </thead>
            <tbody>
              {workloads.map((w) => (
                <WorkloadRow key={`${w.envId}/${w.namespace}/${w.workload}`} workload={w} showEnv={showEnv} onOpen={() => navigate({ workload: w.workload, container: undefined })} />
              ))}
            </tbody>
          </table>
        </div>
      </Card>
      {list.data?.truncated && <p className="text-xs text-muted-foreground">Only the first workloads are listed. Narrow with a filter to see the rest.</p>}
    </div>
  );
}

// ── The list ─────────────────────────────────────────────────────────────────

function WorkloadSummary({ workloads }: { workloads: Workload[] }) {
  const restarts = workloads.reduce((sum, w) => sum + w.restartsInWindow, 0);
  const oom = workloads.filter((w) => w.containers.some((c) => c.lastTerminatedReason === "OOMKilled")).length;
  const warnings = workloads.filter((w) => w.latestWarning).length;
  const facts: [string, string, boolean][] = [
    ["Workloads", formatCount(workloads.length), false],
    ["Restarts", formatCount(restarts), restarts > 0],
    ["Out of memory", formatCount(oom), oom > 0],
    ["With warnings", formatCount(warnings), warnings > 0],
  ];
  return (
    <dl className="grid grid-cols-2 gap-3 rounded-lg border border-border bg-card px-4 py-3 sm:grid-cols-4" aria-label="Summary of the workloads" data-testid="obs-workloads-summary">
      {facts.map(([label, value, alert]) => (
        <div key={label}>
          <dt className="text-xs font-medium uppercase tracking-wide text-muted-foreground">{label}</dt>
          <dd className={cn("text-sm font-semibold tabular-nums", alert ? "text-destructive" : "text-foreground")}>{value}</dd>
        </div>
      ))}
    </dl>
  );
}

function WorkloadRow({ workload, showEnv, onOpen }: { workload: Workload; showEnv: boolean; onOpen: () => void }) {
  return (
    <tr className="border-b border-border/60 align-top last:border-b-0 hover:bg-muted/50" data-testid={`obs-workload-${workload.envName}-${workload.workload}`}>
      <th scope="row" className="px-4 py-2 text-left font-normal">
        <button
          type="button"
          onClick={onOpen}
          className="rounded-sm font-mono text-sm text-foreground underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {workload.workload}
        </button>
        <span className="block text-2xs text-muted-foreground">
          {[workload.serviceVersion, workload.namespace, `${workload.replicasObserved} replica${workload.replicasObserved === 1 ? "" : "s"}`].filter(Boolean).join(" · ")}
        </span>
      </th>
      {showEnv && <td className="px-4 py-2 font-mono text-xs text-foreground">{workload.envName || workload.envId}</td>}
      <td className="max-w-[18rem] px-4 py-2">
        <Stack containers={workload.containers}>
          {(c) => (
            <span className="block truncate font-mono text-xs text-foreground" title={`${c.container}: ${c.imageName}:${c.imageTag}`}>
              {imageRef(c)}
            </span>
          )}
        </Stack>
      </td>
      <td className="px-4 py-2">
        <Stack containers={workload.containers}>{(c) => <Restarts container={c} />}</Stack>
      </td>
      <td className="px-4 py-2">
        <Stack containers={workload.containers}>{(c) => <UsageBar usage={c.cpu} format={formatCores} what={`${c.container} CPU`} />}</Stack>
      </td>
      <td className="px-4 py-2">
        <Stack containers={workload.containers}>{(c) => <UsageBar usage={c.memory} format={formatBytes} what={`${c.container} memory`} />}</Stack>
      </td>
      <td className="max-w-[16rem] px-4 py-2">
        {workload.latestWarning ? <EventGist event={workload.latestWarning} /> : <span className="text-xs text-muted-foreground">None</span>}
      </td>
    </tr>
  );
}

/** One line per container, so a sidecar's figures sit beside the main container's. */
function Stack({ containers, children }: { containers: WorkloadContainer[]; children: (c: WorkloadContainer) => React.ReactNode }) {
  if (containers.length === 0) return <span className="text-xs text-muted-foreground">—</span>;
  return (
    <ul className="space-y-1">
      {containers.map((c) => (
        <li key={c.container} className="min-h-[1.25rem]">
          {children(c)}
        </li>
      ))}
    </ul>
  );
}

/** `ghcr.io/acme/api:1.8.2` → `api:1.8.2`: the registry path is in the title. */
export function imageRef(c: Pick<WorkloadContainer, "imageName" | "imageTag">): string {
  const name = c.imageName.split("/").pop() || c.imageName || "unknown image";
  return c.imageTag ? `${name}:${c.imageTag}` : name;
}

function Restarts({ container }: { container: WorkloadContainer }) {
  const reason = container.lastTerminatedReason;
  return (
    <span className="flex flex-wrap items-center gap-1.5 text-xs">
      <span className={cn("tabular-nums", container.restartsInWindow > 0 ? "font-semibold text-destructive" : "text-muted-foreground")}>
        {formatCount(container.restartsInWindow)}
      </span>
      {reason && <ExitReason reason={reason} atMs={container.lastTerminatedAtMs} />}
    </span>
  );
}

function ExitReason({ reason, atMs }: { reason: string; atMs: number | null }) {
  const fatal = FATAL_REASONS.has(reason);
  return (
    <span
      className={cn(
        "rounded px-1 py-px font-mono text-2xs ring-1 ring-inset",
        fatal ? "bg-destructive/10 text-destructive ring-destructive/30" : "text-muted-foreground ring-border"
      )}
      title={atMs ? `Last exited ${formatTimestamp(atMs)}` : "Last exit reason"}
      data-testid="obs-exit-reason"
    >
      {reason}
    </span>
  );
}

/** Which bound a usage is measured against: the limit, else the request. */
function boundOf(usage: ResourceUsage): { share: number | null; p95Share: number | null; bound: number | null; against: "limit" | "request" | null } {
  if (usage.limitUtilizationLatest !== null || usage.limit !== null) {
    return { share: usage.limitUtilizationLatest, p95Share: usage.limitUtilizationP95, bound: usage.limit, against: "limit" };
  }
  if (usage.requestUtilizationLatest !== null || usage.request !== null) {
    return { share: usage.requestUtilizationLatest, p95Share: usage.requestUtilizationP95, bound: usage.request, against: "request" };
  }
  return { share: null, p95Share: null, bound: null, against: null };
}

export function usageTone(share: number | null): "ok" | "warn" | "alert" {
  if (share === null) return "ok";
  if (share >= USAGE_ALERT) return "alert";
  if (share >= USAGE_WARN) return "warn";
  return "ok";
}

/** Latest use as a bar against the limit (or the request), with the window's p95 as a tick. */
export function UsageBar({ usage, format, what }: { usage: ResourceUsage | null; format: (value: number) => string; what: string }) {
  if (!usage) return <span className="text-xs text-muted-foreground">not reported</span>;
  const { share, p95Share, bound, against } = boundOf(usage);
  const tone = usageTone(share);
  const text = bound !== null ? `${format(usage.latest)} of ${format(bound)}` : format(usage.latest);
  const label = against
    ? `${what}: ${text} ${against}${share !== null ? `, ${formatPercent(share)}` : ""}; p95 ${format(usage.p95)}`
    : `${what}: ${text}, no limit or request set; p95 ${format(usage.p95)}`;
  return (
    <span className="block min-w-[8rem]" title={label} data-testid="obs-usage" data-tone={tone}>
      <span className="flex items-baseline justify-between gap-2 text-xs">
        <span className="tabular-nums text-foreground">{text}</span>
        <span className={cn("tabular-nums", tone === "alert" ? "text-destructive" : tone === "warn" ? "text-warning" : "text-muted-foreground")}>
          {share !== null ? formatPercent(share) : against === null ? "no limit" : ""}
        </span>
      </span>
      {share !== null && (
        <span className="relative mt-1 block h-1.5 overflow-hidden rounded-full bg-border/60" aria-hidden="true">
          <span
            className={cn("absolute inset-y-0 left-0 rounded-full", tone === "alert" ? "bg-destructive" : tone === "warn" ? "bg-warning" : "bg-primary")}
            style={{ width: `${Math.min(share, 1) * 100}%` }}
          />
          {p95Share !== null && <span className="absolute inset-y-0 w-0.5 bg-foreground/60" style={{ left: `calc(${Math.min(p95Share, 1) * 100}% - 1px)` }} />}
        </span>
      )}
      <span className="sr-only">{label}</span>
    </span>
  );
}

function EventGist({ event }: { event: WorkloadEvent }) {
  return (
    <span className="block text-xs" title={event.message}>
      <span className="flex items-center gap-1.5">
        <AlertTriangle className="h-3 w-3 shrink-0 text-warning" aria-hidden="true" />
        <span className="font-mono text-foreground">{event.reason}</span>
        {event.count > 1 && <span className="text-muted-foreground">×{formatCount(event.count)}</span>}
        <span className="text-muted-foreground">{formatAgo(event.timeMs)}</span>
      </span>
      <span className="block truncate text-muted-foreground">{event.message}</span>
    </span>
  );
}

// ── The detail ───────────────────────────────────────────────────────────────

interface ChartSpec {
  title: string;
  description: string;
  metrics: WorkloadMetric[];
  unit: ValueUnit;
}

const CHARTS: ChartSpec[] = [
  { title: "CPU", description: "Cores used, the busiest replica per bucket", metrics: ["cpu_usage"], unit: "cores" },
  { title: "CPU against limit and request", description: "Share of the limit (and of the request) in use", metrics: ["cpu_limit_utilization", "cpu_request_utilization"], unit: "percent" },
  { title: "Memory", description: "Working set, the largest replica per bucket", metrics: ["memory_working_set"], unit: "bytes" },
  { title: "Memory against limit and request", description: "Past 100% of the limit the container is OOMKilled", metrics: ["memory_limit_utilization", "memory_request_utilization"], unit: "percent" },
  { title: "Restarts", description: "Container restarts per bucket, all replicas", metrics: ["restarts"], unit: "count" },
];

const METRIC_SUFFIX: Partial<Record<WorkloadMetric, string>> = {
  cpu_limit_utilization: "of limit",
  cpu_request_utilization: "of request",
  memory_limit_utilization: "of limit",
  memory_request_utilization: "of request",
};

function WorkloadDetailView({
  scope,
  subject,
  range,
  name,
  container,
  rows,
  showEnv,
  navigate,
}: {
  scope: ObsScope;
  subject: string;
  range: RangePreset;
  name: string;
  container: string | undefined;
  rows: Workload[];
  showEnv: boolean;
  navigate: ObsNavigate;
}) {
  const detail = useWorkload(scope, range, name, container);
  const envNames = useMemo(() => new Map(rows.map((w) => [w.envId, w.envName || w.envId])), [rows]);
  const containers = useMemo(() => [...new Set(rows.flatMap((w) => w.containers.map((c) => c.container)))].sort(), [rows]);

  return (
    <div className="space-y-4" data-testid="obs-workload-detail">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="ghost"
            size="sm"
            onClick={() => navigate({ workload: undefined, container: undefined })}
            leftIcon={<ArrowLeft className="h-3.5 w-3.5" />}
            data-testid="obs-workload-back"
          >
            All workloads
          </Button>
          <Boxes className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
          <h3 className="font-mono text-sm font-semibold text-foreground">{name}</h3>
          {showEnv && rows.length > 0 && (
            <span className="text-xs text-muted-foreground">in {rows.map((w) => w.envName || w.envId).join(", ")}</span>
          )}
        </div>
        {containers.length > 1 && (
          <>
            <label htmlFor="obs-workload-container" className="sr-only">
              Container
            </label>
            <select
              id="obs-workload-container"
              value={container ?? ""}
              onChange={(event) => navigate({ container: event.target.value || undefined }, { replace: true })}
              className="h-8 rounded-md border border-border bg-background px-2 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              data-testid="obs-workload-container"
            >
              <option value="">Every container</option>
              {containers.map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </select>
          </>
        )}
      </div>

      {rows.length > 0 && <ContainerFacts rows={rows} container={container} showEnv={showEnv} />}

      {detail.isLoading ? (
        <ObsLoadingRows testId="obs-workload-loading" rows={4} />
      ) : detail.error && !detail.data ? (
        <ObsError error={detail.error} subject={subject} onRetry={() => void detail.refetch()} retrying={detail.isFetching} />
      ) : detail.data ? (
        <WorkloadTelemetry detail={detail.data} envNames={envNames} showEnv={showEnv} />
      ) : null}
    </div>
  );
}

function ContainerFacts({ rows, container, showEnv }: { rows: Workload[]; container: string | undefined; showEnv: boolean }) {
  const lines = rows.flatMap((w) => w.containers.filter((c) => !container || c.container === container).map((c) => ({ w, c })));
  return (
    <Card padding="none" data-testid="obs-workload-facts">
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <caption className="sr-only">Containers of this workload: image, restarts and resource use</caption>
          <thead>
            <tr className="border-b border-border text-left text-xs font-medium uppercase tracking-wide text-muted-foreground">
              {showEnv && <th scope="col" className="px-4 py-2 font-medium">Environment</th>}
              <th scope="col" className="px-4 py-2 font-medium">Container</th>
              <th scope="col" className="px-4 py-2 font-medium">Image</th>
              <th scope="col" className="px-4 py-2 font-medium">Restarts</th>
              <th scope="col" className="px-4 py-2 font-medium">CPU</th>
              <th scope="col" className="px-4 py-2 font-medium">Memory</th>
            </tr>
          </thead>
          <tbody>
            {lines.map(({ w, c }) => (
              <tr key={`${w.envId}/${c.container}`} className="border-b border-border/60 align-top last:border-b-0">
                {showEnv && <td className="px-4 py-2 font-mono text-xs">{w.envName || w.envId}</td>}
                <th scope="row" className="px-4 py-2 text-left font-mono text-xs font-normal text-foreground">
                  {c.container}
                </th>
                <td className="max-w-[20rem] px-4 py-2">
                  <span className="block truncate font-mono text-xs text-foreground" title={`${c.imageName}:${c.imageTag}`}>
                    {c.imageName}
                    {c.imageTag ? `:${c.imageTag}` : ""}
                  </span>
                  {w.serviceVersion && <span className="block text-2xs text-muted-foreground">service.version {w.serviceVersion}</span>}
                </td>
                <td className="px-4 py-2">
                  <Restarts container={c} />
                </td>
                <td className="px-4 py-2">
                  <UsageBar usage={c.cpu} format={formatCores} what={`${c.container} CPU`} />
                  <RequestLimit usage={c.cpu} format={formatCores} />
                </td>
                <td className="px-4 py-2">
                  <UsageBar usage={c.memory} format={formatBytes} what={`${c.container} memory`} />
                  <RequestLimit usage={c.memory} format={formatBytes} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </Card>
  );
}

function RequestLimit({ usage, format }: { usage: ResourceUsage | null; format: (value: number) => string }) {
  if (!usage) return null;
  return (
    <span className="mt-0.5 block text-2xs text-muted-foreground">
      p95 {format(usage.p95)} · request {usage.request !== null ? format(usage.request) : "unset"} · limit {usage.limit !== null ? format(usage.limit) : "unset"}
    </span>
  );
}

/** Version changes as chart markers: when each `service.version` was first seen, per environment. */
export function versionMarkers(detail: WorkloadDetail, envNames: Map<string, string>, showEnv: boolean): ChartMarker[] {
  return detail.versionChanges.map((change) => {
    const env = envNames.get(change.envId) ?? change.envId;
    return {
      timeSec: Math.round(change.firstSeenMs / 1000),
      label: showEnv ? `${env} ${change.serviceVersion}` : change.serviceVersion,
      description: `${change.serviceVersion} first seen in ${env}`,
    };
  });
}

function WorkloadTelemetry({ detail, envNames, showEnv }: { detail: WorkloadDetail; envNames: Map<string, string>; showEnv: boolean }) {
  // The workload's own version changes, beside the panel's release promotions.
  const releases = useInheritedChartMarkers();
  const markers = useMemo(() => [...releases, ...versionMarkers(detail, envNames, showEnv)], [releases, detail, envNames, showEnv]);
  const containers = new Set(detail.series.map((s) => s.container));
  const envs = new Set(detail.series.map((s) => s.envId));
  const nameOf = (envId: string, container: string, metric: WorkloadMetric, metricsInChart: number) => {
    const parts: string[] = [];
    if (envs.size > 1 || showEnv) parts.push(envNames.get(envId) ?? envId);
    if (containers.size > 1) parts.push(container);
    if (metricsInChart > 1 && METRIC_SUFFIX[metric]) parts.push(METRIC_SUFFIX[metric]!);
    return parts.join(" · ") || container;
  };

  return (
    <div className="space-y-4">
      <div className="grid gap-4 lg:grid-cols-2">
        {CHARTS.map((chart) => {
          const series = detail.series.filter((s) => chart.metrics.includes(s.metric));
          const timestamps = [...new Set(series.flatMap((s) => s.timestamps))].sort((a, b) => a - b);
          return (
            <Card key={chart.title} padding="md" data-testid={`obs-workload-chart-${chart.metrics[0]}`}>
              <CardHeader title={chart.title} description={chart.description} />
              {series.length === 0 || timestamps.length === 0 ? (
                <p className="py-6 text-center text-xs text-muted-foreground">
                  {chart.metrics[0].includes("utilization")
                    ? "Not reported: the container sets no limit or request, or the node agent does not report utilization."
                    : "No data in this window"}
                </p>
              ) : (
                <UPlotChart
                  height={160}
                  timestamps={timestamps}
                  series={series.map((s) => {
                    const byTime = new Map(s.timestamps.map((t, i) => [t, s.values[i] ?? null]));
                    return { label: nameOf(s.envId, s.container, s.metric, chart.metrics.length), values: timestamps.map((t) => byTime.get(t) ?? null) };
                  })}
                  unit={chart.unit}
                  label={chart.title}
                  markers={markers}
                />
              )}
            </Card>
          );
        })}
        <Rollouts detail={detail} envNames={envNames} showEnv={showEnv} />
      </div>
      <Events events={detail.events} envNames={envNames} showEnv={showEnv} />
      {detail.truncated && <p className="text-xs text-muted-foreground">Some series or events were cut to fit; narrow the window or pick a container.</p>}
    </div>
  );
}

function Rollouts({ detail, envNames, showEnv }: { detail: WorkloadDetail; envNames: Map<string, string>; showEnv: boolean }) {
  const items = [
    ...detail.versionChanges.map((c) => ({ at: c.firstSeenMs, until: c.lastSeenMs, env: c.envId, what: `service.version ${c.serviceVersion}` })),
    ...detail.imageChanges.map((c) => ({ at: c.firstSeenMs, until: c.lastSeenMs, env: c.envId, what: `${c.container}: ${imageRef(c)}` })),
  ].sort((a, b) => b.at - a.at);
  return (
    <Card padding="md" data-testid="obs-workload-rollouts">
      <CardHeader title="Versions and images seen" description="When each was first and last reported in the window" />
      {items.length === 0 ? (
        <p className="py-6 text-center text-xs text-muted-foreground">No version or image reported in this window.</p>
      ) : (
        <ol className="max-h-48 space-y-1.5 overflow-y-auto text-xs">
          {items.map((item, index) => (
            <li key={`${item.env}-${item.what}-${index}`} className="flex items-baseline justify-between gap-3">
              <span className="min-w-0 truncate">
                {showEnv && <span className="mr-1.5 font-mono text-muted-foreground">{envNames.get(item.env) ?? item.env}</span>}
                <span className="font-mono text-foreground">{item.what}</span>
              </span>
              <span className="shrink-0 tabular-nums text-muted-foreground" title={`until ${formatTimestamp(item.until)}`}>
                {formatTimestamp(item.at)}
              </span>
            </li>
          ))}
        </ol>
      )}
    </Card>
  );
}

function Events({ events, envNames, showEnv }: { events: WorkloadEvent[]; envNames: Map<string, string>; showEnv: boolean }) {
  const [warningsOnly, setWarningsOnly] = useState(false);
  const shown = warningsOnly ? events.filter((e) => e.type === "Warning") : events;
  const warnings = events.filter((e) => e.type === "Warning").length;
  return (
    <Card padding="none" data-testid="obs-workload-events">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-4 py-3">
        <div>
          <h4 className="text-sm font-semibold text-foreground">Kubernetes events</h4>
          <p className="text-xs text-muted-foreground">
            {formatCount(events.length)} in the window, {formatCount(warnings)} warning{warnings === 1 ? "" : "s"}. Crash loops, failed pulls and evictions show here first.
          </p>
        </div>
        <Button
          variant={warningsOnly ? "primary" : "outline"}
          size="sm"
          aria-pressed={warningsOnly}
          onClick={() => setWarningsOnly(!warningsOnly)}
          data-testid="obs-events-warnings-only"
        >
          Warnings only
        </Button>
      </div>
      {shown.length === 0 ? (
        <p className="px-4 py-6 text-center text-xs text-muted-foreground">{warningsOnly ? "No warnings in this window." : "No events in this window."}</p>
      ) : (
        <div className="max-h-96 overflow-auto">
          <table className="w-full text-xs">
            <caption className="sr-only">Kubernetes events for this workload, newest first</caption>
            <thead className="sticky top-0 bg-card">
              <tr className="border-b border-border text-left text-muted-foreground">
                <th scope="col" className="px-4 py-1.5 font-medium">When</th>
                {showEnv && <th scope="col" className="px-2 py-1.5 font-medium">Environment</th>}
                <th scope="col" className="px-2 py-1.5 font-medium">Reason</th>
                <th scope="col" className="px-2 py-1.5 font-medium">Object</th>
                <th scope="col" className="px-2 py-1.5 font-medium">Message</th>
                <th scope="col" className="px-4 py-1.5 text-right font-medium">Count</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((event, index) => {
                const alarming = event.type === "Warning" || ALARMING_EVENTS.has(event.reason);
                return (
                  <tr key={`${event.timeMs}-${event.reason}-${index}`} className="border-b border-border/50 align-top last:border-b-0" data-testid="obs-workload-event" data-type={event.type}>
                    <td className="whitespace-nowrap px-4 py-1.5 tabular-nums text-muted-foreground" title={formatTimestamp(event.timeMs)}>
                      {formatAgo(event.timeMs)}
                    </td>
                    {showEnv && <td className="px-2 py-1.5 font-mono">{envNames.get(event.envId) ?? event.envId}</td>}
                    <td className="whitespace-nowrap px-2 py-1.5">
                      <span className={cn("inline-flex items-center gap-1 font-mono", alarming ? "text-destructive" : "text-foreground")}>
                        {alarming && <AlertTriangle className="h-3 w-3" aria-hidden="true" />}
                        {event.reason}
                      </span>
                      <span className="sr-only">, {event.type}</span>
                    </td>
                    <td className="px-2 py-1.5 font-mono text-muted-foreground">
                      {event.objectKind ? `${event.objectKind}/` : ""}
                      {event.objectName}
                      {event.container ? ` (${event.container})` : ""}
                    </td>
                    <td className="max-w-[32rem] px-2 py-1.5 text-foreground">{event.message}</td>
                    <td className="px-4 py-1.5 text-right tabular-nums">{event.count > 1 ? `×${formatCount(event.count)}` : "1"}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Card>
  );
}
