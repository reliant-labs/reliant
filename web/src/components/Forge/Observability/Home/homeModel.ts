// Copyright (c) 2025 Reliant Labs

/**
 * The pure parts of the Observability home: which services need attention,
 * in what order, and the figures of its four-cell strip. No React, so the
 * ranking rule — the one thing the page exists to get right — is tested on
 * its own.
 */

import type { AlertInstance } from "@/services/controlPlane/observability/alertingTypes";
import type { Panel, ServiceHealth, Workload } from "@/services/controlPlane/observability/types";

import { ENV_KEY } from "../attributes";
import type { ChartBand } from "../charts/UPlotChart";

/** An error rate at or above this needs attention. */
export const ERROR_RATE_ATTENTION = 0.05;
/** Restarts in the window at or above this read as crash-looping. */
const CRASH_RESTARTS = 3;

export type HealthTone = "danger" | "warning" | "success" | "neutral";

export interface ServiceRow {
  name: string;
  /** The environment's name, when the scope is every environment. */
  env: string | null;
  health: ServiceHealth;
  workload: Workload | null;
  tone: HealthTone;
  /** The word beside the dot: Crash-looping, Degraded, Healthy, No workload data. */
  status: string;
  /** Why, in a few words, when it is not healthy. */
  reason: string | null;
  alertsFiring: number;
}

/** The labels an alert names its service by, in the order the rule packs use them. */
export function alertService(alert: Pick<AlertInstance, "labels">): string | undefined {
  const l = alert.labels;
  return l.service ?? l.service_name ?? l["service.name"] ?? l.workload ?? l.job;
}

/**
 * One row per service (per environment, when every environment is in
 * scope), with a status derived from what the page has: crash-looping when
 * its containers restarted repeatedly; degraded when its error rate is high,
 * it restarted, Kubernetes warned about it, or an alert about it is firing;
 * healthy otherwise. Reliant's own operator telemetry is not a service of
 * the project and is left out.
 */
export function serviceRows(
  services: readonly ServiceHealth[],
  workloads: readonly Workload[],
  alerts: readonly AlertInstance[],
  envNameOf: (id: string) => string | undefined
): ServiceRow[] {
  return services
    .filter((s) => !s.operatorData)
    .map((health) => {
      const env = health.group[ENV_KEY] ?? null;
      const workload = workloads.find((w) => w.workload === health.name && (env === null || w.envName === env)) ?? null;
      const firing = alerts.filter(
        (a) => a.state === "firing" && alertService(a) === health.name && (env === null || envNameOf(a.envId) === env)
      ).length;
      const restarts = workload?.restartsInWindow ?? 0;
      const lastReason = workload?.containers.find((c) => c.lastTerminatedReason)?.lastTerminatedReason ?? "";
      let tone: HealthTone = workload || health.requestCount > 0 ? "success" : "neutral";
      let status = workload || health.requestCount > 0 ? "Healthy" : "No workload data";
      let reason: string | null = null;
      if (restarts >= CRASH_RESTARTS) {
        tone = "danger";
        status = "Crash-looping";
        reason = `${restarts} restarts${lastReason ? ` · ${lastReason}` : ""}`;
      } else if (health.errorRate >= ERROR_RATE_ATTENTION || restarts > 0 || workload?.latestWarning || firing > 0) {
        tone = "warning";
        status = "Degraded";
        reason =
          firing > 0
            ? `${firing} alert${firing === 1 ? "" : "s"} firing`
            : health.errorRate >= ERROR_RATE_ATTENTION
              ? "high error rate"
              : restarts > 0
                ? `${restarts} restart${restarts === 1 ? "" : "s"}${lastReason ? ` · ${lastReason}` : ""}`
                : (workload?.latestWarning?.reason ?? null);
      }
      return { name: health.name, env, health, workload, tone, status, reason, alertsFiring: firing };
    });
}

const RANK: Record<HealthTone, number> = { danger: 0, warning: 1, success: 2, neutral: 3 };

/** What needs attention first: the worst status, then the most firing alerts, then the highest error rate, then the busiest. */
export function byAttention(a: ServiceRow, b: ServiceRow): number {
  return (
    RANK[a.tone] - RANK[b.tone] ||
    b.alertsFiring - a.alertsFiring ||
    b.health.errorRate - a.health.errorRate ||
    b.health.requestCount - a.health.requestCount ||
    a.name.localeCompare(b.name)
  );
}

// ── The strip ────────────────────────────────────────────────────────────────

const SERVER_SPANS = [{ field: "span.kind", op: "eq", value: "server" }];

/**
 * The strip's figures, as one forge panel: requests, failed requests and p95
 * over the project's server spans. Drawn as a NUMBER panel (the window's
 * totals, and again for the previous window) and as a LINE panel (the
 * sparklines, one bucket grid for all three).
 */
export function redPanel(kind: "number" | "line"): Panel {
  return {
    id: `observability-home-${kind}`,
    title: "Requests, errors and p95",
    kind,
    signal: "traces",
    where: SERVER_SPANS,
    series: [
      { label: "requests", agg: "count" },
      { label: "errors", agg: "count", where: [{ field: "span.status", op: "eq", value: "error" }] },
      { label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" },
    ],
  };
}

/** A series' values by label from a panel answer. */
export function seriesValues(series: readonly { label: string; values: (number | null)[] }[], label: string): (number | null)[] {
  return series.find((s) => s.label === label)?.values ?? [];
}

/** errors / requests per bucket; null where a bucket had no requests. */
export function ratioSeries(errors: readonly (number | null)[], requests: readonly (number | null)[]): (number | null)[] {
  return requests.map((r, i) => (r ? (errors[i] ?? 0) / r : null));
}

/**
 * How many alerts were firing in each bucket, from their firing bands — the
 * Firing alerts cell's sparkline.
 */
export function firingCounts(timestamps: readonly number[], bands: readonly ChartBand[]): number[] {
  const step = timestamps.length > 1 ? timestamps[1] - timestamps[0] : 60;
  return timestamps.map((t) => bands.filter((b) => b.startSec < t + step && b.endSec > t).length);
}
