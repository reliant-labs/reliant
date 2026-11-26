// Copyright (c) 2025 Reliant Labs

/**
 * THE HEALTH STRIP — the project Overview's headline: Requests as the one
 * big number, with its window and its change against the previous window,
 * then one chart of it; error rate, p95 and alerts firing beside it as a
 * quiet row of smaller figures, each with its own change.
 *
 * The chart marks only the LATEST release in the window ("did the last deploy
 * move this?"), and shades the stretches an alert was firing. The workload
 * page's full charts carry every marker.
 */

import { useMemo } from "react";
import { ArrowDownRight, ArrowUpRight, Minus } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { useAlertHistory, useAlerts, usePanelQuery } from "@/hooks/observability-queries";
import type { ObsScope, PanelResult } from "@/services/controlPlane/observability/types";
import { keepsLastResult } from "@/services/controlPlane/observability/quota";
import { RANGE_LABELS, type RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { UPlotChart, type ChartBand, type ChartMarker } from "../charts/UPlotChart";
import { alertBands, rangeMs } from "../Dashboards/dashboardModel";
import { unionTimestamps } from "../Dashboards/PanelBody";
import { formatAgo, formatCount, formatDuration, formatPercent } from "../format";
import type { ObsEnvironment } from "../obsPlace";
import { useReleaseMarkers } from "../releaseMarkers";
import { firingCounts, redPanel, seriesValues } from "./homeModel";

export interface HealthStripProps {
  /** Null until the org is known (or when the scope names an unrecorded environment): the cells show their loading state. */
  scope: ObsScope | null;
  range: RangePreset;
  environments: readonly ObsEnvironment[];
  /** The one environment in scope, or null for every environment. */
  envId: string | null;
  alertsHref: string;
}

export function HealthStrip({ scope, range, environments, envId, alertsHref }: HealthStripProps) {
  const orgId = scope?.orgId ?? null;
  const alerts = useAlerts(orgId, envId ?? undefined);
  const history = useAlertHistory(orgId, range, envId ?? undefined);
  const releases = useReleaseMarkers(scope, range, environments, !!scope);
  const latestMarker = useMemo(() => {
    const newest = [...releases.markers].sort((a, b) => b.timeSec - a.timeSec)[0];
    return newest ? [newest] : [];
  }, [releases.markers]);
  const numberPanel = useMemo(() => redPanel("number"), []);
  const linePanel = useMemo(() => redPanel("line"), []);
  const totals = usePanelQuery(scope, range, numberPanel, []);
  const previous = usePanelQuery(scope, range, numberPanel, [], [], { offset: { endsAgoMs: rangeMs(range) } });
  const trend = usePanelQuery(scope, range, linePanel, []);

  const scopeEnvIds = useMemo(() => (envId ? [envId] : environments.map((candidate) => candidate.id)), [envId, environments]);
  const firing = (alerts.data ?? []).filter((a) => a.state === "firing" && scopeEnvIds.includes(a.envId));
  const bands = alertBands(history.data ?? [], scopeEnvIds, Date.now());

  return (
    <Strip
      totals={scope ? totals : { data: undefined, isLoading: true, error: null }}
      previous={previous.data}
      trend={trend}
      markers={latestMarker}
      firing={firing.length}
      firingDetail={firing[0] ? `${firing[0].rule}${firing[0].firingAtMs ? ` · ${formatAgo(firing[0].firingAtMs).replace(" ago", "")}` : ""}` : null}
      firingCounts={(timestamps) => firingCounts(timestamps, bands)}
      range={range}
      alertsHref={alertsHref}
    />
  );
}

interface Query<T> {
  data: T | undefined;
  isLoading: boolean;
  error: unknown;
}

function valueOf(result: PanelResult | undefined, label: string): number | null {
  return result?.series.find((s) => s.label === label)?.value ?? null;
}

/** A figure's change against the previous window: its words, and which way it went. */
export interface Change {
  text: string;
  /** Null when there was no change worth a direction. */
  direction: "up" | "down" | null;
}

function change(now: number | null, before: number | null, format: (delta: number) => string, label: string): Change | null {
  if (now === null || before === null) return null;
  const delta = now - before;
  // A change too small to show at the figure's precision ("+0pt") is no change.
  const shown = format(Math.abs(delta));
  if (Math.abs(delta) < 1e-9 || shown === format(0)) return { text: `no change vs ${label}`, direction: null };
  return { text: `${delta > 0 ? "+" : "−"}${shown} vs ${label}`, direction: delta > 0 ? "up" : "down" };
}

/**
 * Whether a direction is good or bad news for the figure: requests going up
 * is neither, so it stays muted; an error rate or a latency going up is bad.
 */
type Sense = "neutral" | "lower-is-better";

function ChangeLine({ change: delta, sense, testId }: { change: Change | null; sense: Sense; testId?: string }) {
  if (!delta) return <div className="min-h-4 text-meta text-ink-muted" data-testid={testId}> </div>;
  const Icon = delta.direction === "up" ? ArrowUpRight : delta.direction === "down" ? ArrowDownRight : Minus;
  const tone =
    sense === "neutral" || delta.direction === null
      ? "text-ink-muted"
      : delta.direction === "up"
        ? "text-danger-ink"
        : "text-success-ink";
  return (
    <div className={cn("flex min-h-4 items-center gap-1 text-meta tabular-nums", tone)} data-testid={testId}>
      <Icon className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
      <span className="truncate">{delta.text}</span>
    </div>
  );
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
  const label = `previous ${range === "30d" ? "30 days" : range === "7d" ? "week" : range === "24h" ? "day" : range}`;
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
  const loading = totals.isLoading;
  const failed = !!totals.error && !keepsLastResult(totals.error, totals.data);
  const charted = timestamps.length > 1 && trendRequests.some((value) => !!value);
  const windowWords = RANGE_LABELS[range];
  // Alerts history is drawn as a shaded stretch under the requests: when they fired, against what was asked.
  const alertCounts = countsOf(timestamps);

  return (
    // The headline: one number, its window and its change, then one chart.
    // The other figures are a quiet row beside it, not a wall of equal tiles.
    <section className="min-w-0 space-y-5" aria-label={`Requests, errors and latency, ${windowWords.toLowerCase()}`} data-testid="observability-strip">
      <div className="flex flex-wrap items-end justify-between gap-x-12 gap-y-6">
        <div className="min-w-0" data-testid="observability-cell-requests">
          <div className="flex items-baseline gap-2 text-ui">
            <span className="font-medium text-ink">Requests</span>
            <span className="text-meta text-ink-muted">{windowWords}</span>
          </div>
          {loading ? (
            <div className="mt-1 h-10 w-40 animate-pulse rounded-md bg-ink/5 motion-reduce:animate-none" aria-busy="true" aria-label="Loading Requests" />
          ) : (
            <div className="mt-1 text-figure tabular-nums text-ink">{failed ? "—" : formatCount(requests ?? 0)}</div>
          )}
          <ChangeLine
            change={requests !== null && prevRequests ? change(requests, prevRequests, (d) => formatPercent(d / prevRequests), label) : null}
            sense="neutral"
          />
        </div>
        <dl className="flex flex-wrap gap-x-10 gap-y-5">
          <Figure
            label="Error rate"
            figure={loading ? null : failed ? "—" : rate === null ? "—" : formatPercent(rate)}
            tone={rate !== null && rate >= 0.05 ? "warning" : undefined}
            change={change(rate, prevRate, (d) => `${formatCount(d * 100)}pt`, label)}
          />
          <Figure
            label="p95 latency"
            figure={loading ? null : failed ? "—" : p95 === null ? "—" : formatDuration(p95)}
            change={change(p95, prevP95, (d) => formatDuration(d), label)}
          />
          <Figure
            label="Alerts firing"
            figure={String(firing)}
            tone={firing > 0 ? "warning" : undefined}
            detail={firingDetail ?? "None in this scope"}
            href={alertsHref}
          />
        </dl>
      </div>
      <div className={charted ? "h-[180px]" : "h-24"} data-testid="observability-strip-chart">
        {charted ? (
          <UPlotChart
            height={180}
            area="fill"
            legend={false}
            timestamps={timestamps}
            series={[{ label: "Requests", values: trendRequests, color: "--chart-1" }]}
            unit="count"
            label={`Requests over the ${windowWords.toLowerCase()}`}
            markers={markers}
            bands={alertCounts.some((count) => count > 0) ? firingBands(timestamps, alertCounts) : undefined}
          />
        ) : (
          // An empty window still has a shape: a flat baseline and the sentence that says why.
          <div className="flex h-full flex-col justify-end" data-testid="observability-strip-chart-empty">
            {!loading && (
              <p className="pb-3 text-ui text-ink-muted">{failed ? "Requests could not be read right now." : `No requests traced in the ${windowWords.toLowerCase()}.`}</p>
            )}
            <div className="border-b border-border" aria-hidden="true" />
          </div>
        )}
      </div>
    </section>
  );
}

/** The stretches where an alert was firing, as chart bands. */
function firingBands(timestamps: number[], counts: number[]): ChartBand[] {
  const bands: ChartBand[] = [];
  let start: number | null = null;
  timestamps.forEach((t, i) => {
    if (counts[i] > 0 && start === null) start = t;
    if ((counts[i] === 0 || i === timestamps.length - 1) && start !== null) {
      bands.push({ startSec: start, endSec: counts[i] > 0 ? t : timestamps[i - 1] ?? t, label: "Alert firing", tone: "warning" });
      start = null;
    }
  });
  return bands;
}

/** A secondary figure: smaller, unboxed, its change (or what it is about) under it. */
function Figure({
  label,
  figure,
  change: delta,
  detail,
  tone,
  href,
}: {
  label: string;
  /** Null while loading. */
  figure: string | null;
  change?: Change | null;
  detail?: string;
  tone?: "warning";
  href?: string;
}) {
  const body = (
    <>
      <dt className="text-meta text-ink-muted">{label}</dt>
      <dd>
        {figure === null ? (
          <div className="mt-1 h-7 w-16 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-busy="true" aria-label={`Loading ${label}`} />
        ) : (
          <div className={cn("mt-0.5 text-2xl font-semibold tabular-nums", tone === "warning" ? "text-warning-ink" : "text-ink")}>{figure}</div>
        )}
        {detail !== undefined ? (
          <div className="max-w-[14rem] truncate text-meta text-ink-muted">{detail}</div>
        ) : (
          <ChangeLine change={delta ?? null} sense="lower-is-better" />
        )}
      </dd>
    </>
  );
  const testId = `observability-cell-${label.toLowerCase().replace(/\s+/g, "-")}`;
  return href ? (
    <Link
      href={href}
      className="no-color -m-2 block min-w-0 rounded-lg p-2 hover:bg-ink/5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      data-testid={testId}
    >
      {body}
    </Link>
  ) : (
    <div className="min-w-0" data-testid={testId}>
      {body}
    </div>
  );
}
