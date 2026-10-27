// Copyright (c) 2025 Reliant Labs

/**
 * THE HEALTH STRIP — requests · error rate · p95 · alerts firing for a
 * telemetry scope, each with the change against the previous window and a
 * sparkline carrying the scope's release markers.
 *
 * Shared by the Observability home and the project Overview (the KPI tiles
 * with release markers), so the two pages show the same four figures from
 * the same four reads: react-query dedupes them when both are mounted.
 */

import { useMemo } from "react";

import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import { useAlertHistory, useAlerts, usePanelQuery } from "@/hooks/observability-queries";
import type { ObsScope, PanelResult } from "@/services/controlPlane/observability/types";
import { keepsLastResult } from "@/services/controlPlane/observability/quota";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { UPlotChart, type ChartMarker } from "../charts/UPlotChart";
import { alertBands, rangeMs } from "../Dashboards/dashboardModel";
import { unionTimestamps } from "../Dashboards/PanelBody";
import { formatAgo, formatCount, formatDuration, formatPercent } from "../format";
import type { ObsEnvironment } from "../obsPlace";
import { useReleaseMarkers } from "../releaseMarkers";
import { firingCounts, ratioSeries, redPanel, seriesValues } from "./homeModel";

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
  const numberPanel = useMemo(() => redPanel("number"), []);
  const linePanel = useMemo(() => redPanel("line"), []);
  const totals = usePanelQuery(scope, range, numberPanel, []);
  const previous = usePanelQuery(scope, range, numberPanel, [], [], { offsetKey: `period:${range}`, offsetMs: () => rangeMs(range) });
  const trend = usePanelQuery(scope, range, linePanel, []);

  const scopeEnvIds = useMemo(() => (envId ? [envId] : environments.map((candidate) => candidate.id)), [envId, environments]);
  const firing = (alerts.data ?? []).filter((a) => a.state === "firing" && scopeEnvIds.includes(a.envId));
  const bands = alertBands(history.data ?? [], scopeEnvIds, Date.now());

  return (
    <Strip
      totals={scope ? totals : { data: undefined, isLoading: true, error: null }}
      previous={previous.data}
      trend={trend}
      markers={releases.markers}
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
  const failed = !!totals.error && !keepsLastResult(totals.error, totals.data);

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
