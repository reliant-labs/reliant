// Copyright (c) 2025 Reliant Labs

/**
 * Before and after one promotion: did the release make things better or
 * worse?
 *
 * Two equal windows either side of the moment a release was promoted into an
 * environment, asked the same questions — requests, errors and p95 latency per
 * service; which `service.version` served the traffic; CPU and memory per
 * workload — and read side by side. Equal lengths, so a rate and a percentile
 * compare like for like; and neither side reaches past a neighbouring
 * promotion or the present, so each holds one release's traffic
 * (deliveryScope.ts comparisonWindows).
 *
 * The promotion is in the URL as environment + moment + id, and found again in
 * the ledger, so a comparison is a link: "look what v1.8.3 did to checkout".
 */

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowLeft, ArrowRight, GitCommitHorizontal, Undo2 } from "lucide-react";

import Card from "@/components/forge-ui/card";
import { Button } from "@/components/ui/Button";
import { obsKeys, useReleases } from "@/hooks/observability-queries";
import type { Promotion } from "@/services/controlPlane/observability/deliveryTypes";
import { obsRetry } from "@/services/controlPlane/observability/errors";
import { useObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { ObsScope, Panel, PanelResult, TimeWindow } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import {
  changeText,
  COMPARE_WINDOW_MS,
  COMPARE_WINDOWS,
  comparisonWindows,
  formatSpan,
  movement,
  neighboursOf,
  shortCommit,
  type CompareRef,
  type CompareWindow,
  type ComparisonWindows,
  type Movement,
} from "./deliveryScope";
import { DeliveryError } from "./DeliveryStates";
import { formatAgo, formatBytes, formatCores, formatDuration, formatPercent, formatRate, formatWhen } from "./format";
import type { ObsNavigate } from "./obsSearch";
import { ObsError, ObsLoadingRows } from "./ObsStates";

const SERVICE = "service.name";
const VERSION = "resource.attr.service.version";
const WORKLOAD = "resource.attr.k8s.workload.name";
const SERVER_SPANS = [{ field: "span.kind", op: "eq", value: "server" }];

/** The questions each side is asked: forge's own Releases and Workloads intents, as tables. */
export const COMPARE_PANELS: Record<"red" | "versions" | "resources", Panel> = {
  red: {
    id: "compare-red",
    title: "Requests, errors and latency",
    kind: "table",
    signal: "traces",
    where: SERVER_SPANS,
    series: [
      { label: "requests", agg: "count" },
      { label: "errors", agg: "count", where: [{ field: "span.status", op: "eq", value: "error" }] },
      { label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" },
    ],
    groupBy: [SERVICE],
    unit: "ms",
  },
  versions: {
    id: "compare-versions",
    title: "Versions serving",
    kind: "table",
    signal: "traces",
    where: SERVER_SPANS,
    series: [{ label: "requests", agg: "count" }],
    groupBy: [SERVICE, VERSION],
  },
  resources: {
    id: "compare-resources",
    title: "Resource usage",
    kind: "table",
    signal: "metrics",
    series: [
      { label: "cpu", agg: "avg", field: "container.cpu.usage", metricType: "gauge" },
      { label: "memory", agg: "avg", field: "container.memory.working_set", metricType: "gauge" },
    ],
    groupBy: [WORKLOAD],
  },
};

/** One side's figures for one service. */
export interface ServiceSide {
  rate: number | null;
  errorRate: number | null;
  p95Ms: number | null;
  /** Versions that served it, busiest first, with their share of requests. */
  versions: { version: string; share: number }[];
}

export interface ServiceComparison {
  service: string;
  before: ServiceSide;
  after: ServiceSide;
}

const EMPTY_SIDE: ServiceSide = { rate: null, errorRate: null, p95Ms: null, versions: [] };

function column(result: PanelResult | undefined, label: string): number {
  return result ? result.columns.indexOf(label) : -1;
}

/** One side of the RED + versions tables, per service. */
export function serviceSides(red: PanelResult | undefined, versions: PanelResult | undefined, lengthMs: number): Map<string, ServiceSide> {
  const out = new Map<string, ServiceSide>();
  const seconds = lengthMs / 1000;
  const [req, err, p95] = ["requests", "errors", "p95"].map((label) => column(red, label));
  for (const row of red?.rows ?? []) {
    const name = row.group[SERVICE] ?? "";
    const requests = req >= 0 ? (row.values[req] ?? 0) : 0;
    const errors = err >= 0 ? (row.values[err] ?? 0) : 0;
    out.set(name, {
      rate: seconds > 0 ? requests / seconds : null,
      errorRate: requests > 0 ? errors / requests : null,
      p95Ms: p95 >= 0 ? (row.values[p95] ?? null) : null,
      versions: [],
    });
  }
  const vreq = column(versions, "requests");
  const byService = new Map<string, { version: string; requests: number }[]>();
  for (const row of versions?.rows ?? []) {
    const name = row.group[SERVICE] ?? "";
    const list = byService.get(name) ?? [];
    list.push({ version: row.group[VERSION] || "(none)", requests: vreq >= 0 ? (row.values[vreq] ?? 0) : 0 });
    byService.set(name, list);
  }
  for (const [name, list] of byService) {
    const total = list.reduce((sum, v) => sum + v.requests, 0);
    const side = out.get(name) ?? { ...EMPTY_SIDE };
    side.versions = list
      .filter((v) => v.requests > 0)
      .sort((a, b) => b.requests - a.requests)
      .map((v) => ({ version: v.version, share: total > 0 ? v.requests / total : 0 }));
    out.set(name, side);
  }
  return out;
}

/** Both sides, per service, busiest service first. */
export function compareServices(before: Map<string, ServiceSide>, after: Map<string, ServiceSide>): ServiceComparison[] {
  const names = [...new Set([...before.keys(), ...after.keys()])];
  return names
    .map((service) => ({ service, before: before.get(service) ?? EMPTY_SIDE, after: after.get(service) ?? EMPTY_SIDE }))
    .sort((a, b) => (b.after.rate ?? b.before.rate ?? 0) - (a.after.rate ?? a.before.rate ?? 0) || a.service.localeCompare(b.service));
}

export interface WorkloadComparison {
  workload: string;
  cpu: [number | null, number | null];
  memory: [number | null, number | null];
}

export function compareWorkloads(before: PanelResult | undefined, after: PanelResult | undefined): WorkloadComparison[] {
  const read = (result: PanelResult | undefined) => {
    const [cpu, memory] = ["cpu", "memory"].map((label) => column(result, label));
    return new Map(
      (result?.rows ?? []).map((row) => [row.group[WORKLOAD] ?? "", { cpu: cpu >= 0 ? row.values[cpu] : null, memory: memory >= 0 ? row.values[memory] : null }])
    );
  };
  const b = read(before);
  const a = read(after);
  return [...new Set([...b.keys(), ...a.keys()])].sort().map((workload) => ({
    workload,
    cpu: [b.get(workload)?.cpu ?? null, a.get(workload)?.cpu ?? null],
    memory: [b.get(workload)?.memory ?? null, a.get(workload)?.memory ?? null],
  }));
}

function usePanelOver(scope: ObsScope, panel: Panel, side: "before" | "after", window: TimeWindow | null) {
  const api = useObservabilityApi();
  return useQuery({
    queryKey: [...obsKeys.all(scope), "compare", panel.id, side, window?.startMs ?? 0, window?.endMs ?? 0] as const,
    queryFn: () =>
      api.queryPanel({ ...scope, window: window!, panel: { ...panel, id: `${panel.id}-${side}` }, services: [], stepSeconds: 0, groupBy: panel.groupBy }),
    enabled: !!window,
    staleTime: 5 * 60_000,
    retry: obsRetry,
  });
}

export interface ReleaseComparisonProps {
  /** The telemetry scope on screen; the comparison narrows it to the promotion's environment. */
  scope: ObsScope;
  subject: string;
  target: CompareRef;
  width: CompareWindow;
  /** The view the reader came from, for the way back. */
  backLabel: string;
  onBack: () => void;
  navigate: ObsNavigate;
}

export function ReleaseComparison({ scope, subject, target, width, backLabel, onBack, navigate }: ReleaseComparisonProps) {
  // One clock per comparison, so the windows do not slide while it is open.
  const [now] = useState(() => Date.now());
  const delivery = useMemo(() => ({ orgId: scope.orgId, projectId: scope.projectId, envIds: [target.envId] }), [scope.orgId, scope.projectId, target.envId]);
  // The promotion itself, the one before it (what it replaced, and where the
  // before side must stop) and the one after it (where the after side must).
  const day = COMPARE_WINDOW_MS["24h"];
  const ledger = useReleases(
    delivery,
    `around:${target.promotionId}`,
    () => ({ startMs: target.promotedAtMs - 30 * day, endMs: Math.min(Date.now(), target.promotedAtMs + day + 60_000) }),
    { limit: 2000 }
  );
  const promotions = ledger.data?.promotions ?? [];
  const promotion =
    promotions.find((p) => p.promotionId === target.promotionId) ??
    promotions.find((p) => p.envId === target.envId && Math.abs(p.promotedAtMs - target.promotedAtMs) < 1000) ??
    null;
  const { previous, next } = promotion ? neighboursOf(promotion, promotions) : { previous: null, next: null };
  const windows = promotion
    ? comparisonWindows(promotion.promotedAtMs, COMPARE_WINDOW_MS[width], now, { previousMs: previous?.promotedAtMs, nextMs: next?.promotedAtMs })
    : null;
  // The environment filter is the promotion's; any other filter (a service, a route) still narrows.
  const telemetry = useMemo<ObsScope>(
    () => ({ ...scope, envIds: [target.envId], filters: scope.filters.filter((f) => f.key !== "deployment.environment.name" && f.key !== "reliant.env.id") }),
    [scope, target.envId]
  );
  const ready = windows && !windows.tooShort ? windows : null;
  const redBefore = usePanelOver(telemetry, COMPARE_PANELS.red, "before", ready?.before ?? null);
  const redAfter = usePanelOver(telemetry, COMPARE_PANELS.red, "after", ready?.after ?? null);
  const verBefore = usePanelOver(telemetry, COMPARE_PANELS.versions, "before", ready?.before ?? null);
  const verAfter = usePanelOver(telemetry, COMPARE_PANELS.versions, "after", ready?.after ?? null);
  const resBefore = usePanelOver(telemetry, COMPARE_PANELS.resources, "before", ready?.before ?? null);
  const resAfter = usePanelOver(telemetry, COMPARE_PANELS.resources, "after", ready?.after ?? null);

  const back = (
    <Button variant="ghost" size="sm" leftIcon={<ArrowLeft className="h-3.5 w-3.5" />} onClick={onBack} data-testid="obs-compare-back">
      Back to {backLabel}
    </Button>
  );

  if (ledger.isLoading) {
    return (
      <div className="space-y-3" data-testid="obs-compare" data-state="loading">
        {back}
        <ObsLoadingRows testId="obs-compare-loading" rows={4} />
      </div>
    );
  }
  if (ledger.error) {
    return (
      <div className="space-y-3" data-testid="obs-compare" data-state="error">
        {back}
        <DeliveryError error={ledger.error} subject={subject} onRetry={() => void ledger.refetch()} retrying={ledger.isFetching} />
      </div>
    );
  }
  if (!promotion || !windows) {
    return (
      <div className="space-y-3" data-testid="obs-compare" data-state="not-found">
        {back}
        <p role="status" className="rounded-lg border border-dashed border-border bg-background px-4 py-5 text-sm text-muted-foreground" data-testid="obs-compare-not-found">
          That promotion isn&apos;t in the release ledger any more, or isn&apos;t one you can see from here. Pick a release from the list to
          compare it.
        </p>
      </div>
    );
  }

  const red = compareServices(
    serviceSides(redBefore.data, verBefore.data, windows.lengthMs),
    serviceSides(redAfter.data, verAfter.data, windows.lengthMs)
  );
  const workloads = compareWorkloads(resBefore.data, resAfter.data);
  const telemetryError = redBefore.error ?? redAfter.error ?? verBefore.error ?? verAfter.error;
  const resourceError = resBefore.error ?? resAfter.error;
  const loading = redBefore.isLoading || redAfter.isLoading || verBefore.isLoading || verAfter.isLoading;

  return (
    <div className="space-y-4" data-testid="obs-compare" data-state="ready" data-window={width}>
      {back}
      <ComparisonHeader promotion={promotion} previous={previous} />
      <div className="flex flex-wrap items-center gap-3">
        <div role="group" aria-label="Window on each side" className="inline-flex rounded-lg border border-border bg-background p-0.5">
          {COMPARE_WINDOWS.map((w) => (
            <button
              key={w}
              type="button"
              aria-pressed={w === width}
              onClick={() => navigate({ cmpWindow: w === "30m" ? undefined : w }, { replace: true })}
              data-testid={`obs-compare-window-${w}`}
              className={cn(
                "rounded-md px-2.5 py-1 text-xs transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                w === width ? "bg-card font-medium text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground"
              )}
            >
              {w}
            </button>
          ))}
        </div>
        <WindowsLine windows={windows} width={width} previous={previous} next={next} promotion={promotion} />
      </div>

      {windows.tooShort ? (
        <p role="status" className="rounded-lg border border-dashed border-border bg-background px-4 py-5 text-sm text-muted-foreground" data-testid="obs-compare-too-short">
          {windows.clampedBy === "now"
            ? `Promoted ${formatSpan(now - promotion.promotedAtMs)} ago: there isn't a minute of traffic on each side yet. Come back shortly.`
            : `Another promotion came within a minute of this one, so neither side holds a minute of one release's traffic.`}
        </p>
      ) : (
        <>
          <Card padding="none" data-testid="obs-compare-red">
            <SectionTitle title="Requests, errors and latency" note={`Server spans in ${promotion.envName}, per service, over ${formatSpan(windows.lengthMs)} on each side.`} />
            {loading ? (
              <div className="p-4">
                <ObsLoadingRows rows={3} testId="obs-compare-red-loading" />
              </div>
            ) : telemetryError ? (
              <div className="p-4">
                <ObsError error={telemetryError} subject={subject} />
              </div>
            ) : red.length === 0 ? (
              <p className="px-4 py-5 text-sm text-muted-foreground" data-testid="obs-compare-red-empty">
                No server traffic in either window.
              </p>
            ) : (
              <RedTable rows={red} />
            )}
          </Card>
          <Card padding="none" data-testid="obs-compare-resources">
            <SectionTitle title="Resource usage" note="Average CPU and working-set memory per container, per workload." />
            {resBefore.isLoading || resAfter.isLoading ? (
              <div className="p-4">
                <ObsLoadingRows rows={2} testId="obs-compare-resources-loading" />
              </div>
            ) : resourceError ? (
              <div className="p-4">
                <ObsError error={resourceError} subject={subject} />
              </div>
            ) : workloads.length === 0 ? (
              <p className="px-4 py-5 text-sm text-muted-foreground" data-testid="obs-compare-resources-empty">
                No container metrics in either window: the environment&apos;s workloads report none, or none ran.
              </p>
            ) : (
              <ResourceTable rows={workloads} />
            )}
          </Card>
        </>
      )}
    </div>
  );
}

function ComparisonHeader({ promotion: p, previous }: { promotion: Promotion; previous: Promotion | null }) {
  const rollback = p.kind === "rollback";
  const version = p.version || p.releaseId.slice(0, 12);
  return (
    <div className="space-y-1" data-testid="obs-compare-header">
      <h3 className="flex flex-wrap items-center gap-2 text-base font-semibold text-foreground">
        {rollback && <Undo2 className="h-4 w-4 text-warning" aria-hidden="true" />}
        {rollback ? "Before and after the rollback to " : "Before and after "}
        <span className="font-mono">{version}</span> in <span className="font-mono">{p.envName}</span>
      </h3>
      <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
        <span title={formatWhen(p.promotedAtMs)}>
          {rollback ? "Rolled back" : "Promoted"} {formatWhen(p.promotedAtMs)} ({formatAgo(p.promotedAtMs)})
        </span>
        {p.promotedBy && <span>· by {p.promotedBy}</span>}
        <span className="inline-flex items-center gap-1 font-mono" title={p.gitCommit || "No commit recorded"}>
          · <GitCommitHorizontal className="h-3.5 w-3.5" aria-hidden="true" />
          {p.gitCommit ? shortCommit(p.gitCommit) : "no commit recorded"}
          {p.gitTag && p.gitTag !== p.version ? ` (${p.gitTag})` : ""}
        </span>
        {previous && (
          <span data-testid="obs-compare-replaced">
            · replaced <span className="font-mono text-foreground">{previous.version || previous.releaseId.slice(0, 12)}</span>
          </span>
        )}
      </p>
    </div>
  );
}

function WindowsLine({
  windows,
  width,
  previous,
  next,
  promotion,
}: {
  windows: ComparisonWindows;
  width: CompareWindow;
  previous: Promotion | null;
  next: Promotion | null;
  promotion: Promotion;
}) {
  const time = (ms: number) => formatWhen(ms);
  const span = formatSpan(windows.lengthMs);
  const why =
    windows.clampedBy === "now"
      ? `Only ${span} has passed since the promotion, so each side is ${span} rather than ${width}.`
      : windows.clampedBy === "next" && next
        ? `${next.version || "Another release"} was promoted ${formatSpan(next.promotedAtMs - promotion.promotedAtMs)} later, so each side stops at ${span} to hold this release's traffic only.`
        : windows.clampedBy === "previous" && previous
          ? `${previous.version || "The release before"} had been promoted only ${formatSpan(promotion.promotedAtMs - previous.promotedAtMs)} earlier, so each side is ${span}.`
          : "";
  return (
    <div className="text-xs text-muted-foreground" data-testid="obs-compare-windows" data-clamped={windows.clampedBy ?? "none"}>
      <p>
        <span className="font-medium text-foreground">Before</span> {time(windows.before.startMs)} – {time(windows.before.endMs)} ·{" "}
        <span className="font-medium text-foreground">After</span> {time(windows.after.startMs)} – {time(windows.after.endMs)}
      </p>
      {why && <p data-testid="obs-compare-clamped">{why}</p>}
    </div>
  );
}

function SectionTitle({ title, note }: { title: string; note: string }) {
  return (
    <div className="border-b border-border/60 px-4 py-3">
      <h4 className="text-sm font-medium text-foreground">{title}</h4>
      <p className="text-xs text-muted-foreground">{note}</p>
    </div>
  );
}

const MOVE_TONE: Record<Movement, string> = {
  "up-bad": "text-destructive",
  "down-bad": "text-destructive",
  "up-good": "text-success",
  "down-good": "text-success",
  flat: "text-muted-foreground",
  none: "text-muted-foreground",
};

const MOVE_WORD: Record<Movement, string> = {
  "up-bad": "worse",
  "down-bad": "worse",
  "up-good": "better",
  "down-good": "better",
  flat: "about the same",
  none: "",
};

function Change({
  before,
  after,
  format,
  higherIsWorse,
  kind = "relative",
  testId,
}: {
  before: number | null;
  after: number | null;
  format: (v: number) => string;
  higherIsWorse: boolean | null;
  kind?: "relative" | "points";
  testId?: string;
}) {
  const move = movement(before, after, higherIsWorse, kind === "points" ? 0.25 : 0.1);
  const text = changeText(before, after, kind);
  return (
    <span className="inline-flex flex-wrap items-baseline gap-x-1.5 tabular-nums" data-testid={testId} data-movement={move}>
      <span className="text-muted-foreground">{before == null ? "—" : format(before)}</span>
      <ArrowRight className="h-3 w-3 self-center text-muted-foreground" aria-label="to" />
      <span className="font-medium text-foreground">{after == null ? "—" : format(after)}</span>
      {text && (
        <span className={cn("text-2xs font-medium", MOVE_TONE[move])} title={MOVE_WORD[move]}>
          {text}
          {higherIsWorse !== null && MOVE_WORD[move] && move !== "flat" ? <span className="sr-only"> ({MOVE_WORD[move]})</span> : null}
        </span>
      )}
    </span>
  );
}

function Versions({ versions }: { versions: ServiceSide["versions"] }) {
  if (versions.length === 0) return <span className="text-muted-foreground">—</span>;
  return (
    <span className="flex flex-wrap gap-1">
      {versions.slice(0, 3).map((v) => (
        <span key={v.version} className="rounded bg-background px-1.5 py-px font-mono text-2xs text-foreground ring-1 ring-inset ring-border/60">
          {v.version}
          {versions.length > 1 ? <span className="ml-1 text-muted-foreground">{formatPercent(v.share)}</span> : null}
        </span>
      ))}
    </span>
  );
}

function RedTable({ rows }: { rows: ServiceComparison[] }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <caption className="sr-only">Each service before and after the promotion</caption>
        <thead>
          <tr className="border-b border-border text-left text-xs font-medium uppercase tracking-wide text-muted-foreground">
            <th scope="col" className="px-4 py-2 font-medium">Service</th>
            <th scope="col" className="px-4 py-2 font-medium">Requests</th>
            <th scope="col" className="px-4 py-2 font-medium">Error rate</th>
            <th scope="col" className="px-4 py-2 font-medium">p95 latency</th>
            <th scope="col" className="px-4 py-2 font-medium">Versions before</th>
            <th scope="col" className="px-4 py-2 font-medium">Versions after</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.service} className="border-b border-border/50 align-top last:border-b-0" data-testid={`obs-compare-service-${r.service}`}>
              <th scope="row" className="px-4 py-2 text-left font-mono text-xs font-medium text-foreground">
                {r.service || "(unnamed)"}
              </th>
              <td className="px-4 py-2 text-xs">
                <Change before={r.before.rate} after={r.after.rate} format={formatRate} higherIsWorse={null} />
              </td>
              <td className="px-4 py-2 text-xs">
                <Change before={r.before.errorRate} after={r.after.errorRate} format={formatPercent} higherIsWorse kind="points" testId={`obs-compare-errors-${r.service}`} />
              </td>
              <td className="px-4 py-2 text-xs">
                <Change before={r.before.p95Ms} after={r.after.p95Ms} format={formatDuration} higherIsWorse testId={`obs-compare-p95-${r.service}`} />
              </td>
              <td className="px-4 py-2 text-xs">
                <Versions versions={r.before.versions} />
              </td>
              <td className="px-4 py-2 text-xs" data-testid={`obs-compare-versions-after-${r.service}`}>
                <Versions versions={r.after.versions} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ResourceTable({ rows }: { rows: WorkloadComparison[] }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <caption className="sr-only">Each workload's resource use before and after the promotion</caption>
        <thead>
          <tr className="border-b border-border text-left text-xs font-medium uppercase tracking-wide text-muted-foreground">
            <th scope="col" className="px-4 py-2 font-medium">Workload</th>
            <th scope="col" className="px-4 py-2 font-medium">CPU</th>
            <th scope="col" className="px-4 py-2 font-medium">Memory</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.workload} className="border-b border-border/50 last:border-b-0" data-testid={`obs-compare-workload-${r.workload}`}>
              <th scope="row" className="px-4 py-2 text-left font-mono text-xs font-medium text-foreground">
                {r.workload || "(unnamed)"}
              </th>
              <td className="px-4 py-2 text-xs">
                <Change before={r.cpu[0]} after={r.cpu[1]} format={formatCores} higherIsWorse />
              </td>
              <td className="px-4 py-2 text-xs">
                <Change before={r.memory[0]} after={r.memory[1]} format={formatBytes} higherIsWorse testId={`obs-compare-memory-${r.workload}`} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
