// Copyright (c) 2025 Reliant Labs

/**
 * The pure parts of the Observability home: which services need attention,
 * in what order, and the figures of its four-cell strip. No React, so the
 * ranking rule — the one thing the page exists to get right — is tested on
 * its own.
 */

import type { AlertInstance } from "@/services/controlPlane/observability/alertingTypes";
import type { Panel, ServiceHealth, Workload } from "@/services/controlPlane/observability/types";

import { HEALTH_STATUS } from "../../Workloads/health";
import { ENV_KEY } from "../attributes";
import type { ChartBand } from "../charts/UPlotChart";

/** An error rate at or above this needs attention. */
export const ERROR_RATE_ATTENTION = 0.05;

export type HealthTone = "danger" | "warning" | "success" | "neutral";

export interface ServiceRow {
  name: string;
  /** The environment's name, when the scope is every environment. */
  env: string | null;
  health: ServiceHealth;
  workload: Workload | null;
  tone: HealthTone;
  /**
   * The word beside the dot: the workload's server verdict (Crash-looping,
   * Degraded, Healthy…), or — with no workload telemetry — the signal alone
   * (Alert firing, Errors elevated) or No workload data, never a verdict.
   */
  status: string;
  /** Why, in a few words, when it is not healthy. */
  reason: string | null;
  alertsFiring: number;
  /** No verdict, but an alert firing or a high error rate: ranked with the rows that need attention. */
  signal?: boolean;
}

/** The labels an alert names its service by, in the order the rule packs use them. */
export function alertService(alert: Pick<AlertInstance, "labels">): string | undefined {
  const l = alert.labels;
  return l.service ?? l.service_name ?? l["service.name"] ?? l.workload ?? l.job;
}

/**
 * One row per service (per environment, when every environment is in
 * scope). Its status is its workload's server-side verdict; a service with no
 * workload telemetry is degraded when its error rate is high or an alert about
 * it is firing, healthy otherwise. Reliant's own operator telemetry is not a
 * service of the project and is left out.
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
      const signal =
        firing > 0 ? `${firing} alert${firing === 1 ? "" : "s"} firing` : health.errorRate >= ERROR_RATE_ATTENTION ? "high error rate" : null;
      // A workload's status word is the server's verdict (WorkloadHealth), the
      // one the Overview and the workload page show, so the pages cannot
      // disagree. Alerts and the error rate are the request side of it: they
      // rank the row (byAttention) and say why, without inventing a verdict.
      if (workload) {
        const verdict = HEALTH_STATUS[workload.health];
        return {
          name: health.name,
          env,
          health,
          workload,
          tone: verdict.tone,
          status: verdict.label,
          reason: workload.healthReason || signal,
          alertsFiring: firing,
        };
      }
      // No workload telemetry (a service outside Kubernetes, or before the
      // node agent reports): there is no verdict, and none is invented. The
      // row says the SIGNAL it has, in words the server's verdict never uses
      // and in the neutral tone, so it cannot be read as "Degraded". It still
      // ranks among the rows that need looking at (byAttention).
      const status = firing > 0 ? `${firing === 1 ? "Alert" : "Alerts"} firing` : health.errorRate >= ERROR_RATE_ATTENTION ? "Errors elevated" : "No workload data";
      return { name: health.name, env, health, workload, tone: "neutral", status, reason: signal, alertsFiring: firing, signal: !!signal };
    });
}

const RANK: Record<HealthTone, number> = { danger: 0, warning: 1, success: 2, neutral: 3 };
/** A row with no verdict but a firing alert or a high error rate ranks with the degraded ones, after them. */
const rankOf = (row: ServiceRow) => (row.signal ? 1.5 : RANK[row.tone]);

/** What needs attention first: the worst status, then the most firing alerts, then the highest error rate, then the busiest. */
export function byAttention(a: ServiceRow, b: ServiceRow): number {
  return (
    rankOf(a) - rankOf(b) ||
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
