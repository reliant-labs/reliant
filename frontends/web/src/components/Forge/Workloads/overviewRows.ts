// Copyright (c) 2026 Reliant Labs

/**
 * THE OVERVIEW'S ONE SERVICES LIST — pure, so its rules are tested alone.
 *
 * Everything the app runs, one row each, built from the reads the Overview
 * already holds:
 *
 *   service   a long-running workload (Deployment, StatefulSet, DaemonSet):
 *             the server's health verdict, its restarts and last exit
 *   job       a Job or CronJob: its latest run's result and why it failed
 *   process   a process that sends telemetry (service.name) with no workload
 *             behind it: what the telemetry alone says (an alert firing,
 *             errors elevated, or no workload data)
 *
 * Every row carries the request side where there is one (rate, errors, p95,
 * error logs, alerts firing), joined by name and environment from the one
 * GetServiceOverview answer. CPU, memory and pods are the workload page's.
 *
 * This is what replaced the separate Processes table: nothing it alone
 * showed is lost (the telemetry-only processes, error logs, alerts firing).
 */

import type { AlertInstance } from "@/services/controlPlane/observability/alertingTypes";
import type { ServiceHealth, Workload } from "@/services/controlPlane/observability/types";
import type { StatusTone } from "@/components/forge-ui/status";

import { ENV_ALL } from "../Console/envScope";
import { serviceRows as processRows, type ServiceRow as ProcessRow } from "../Observability/Home/homeModel";
import { HEALTH_STATUS, isJobKind, JOB_STATUS, KIND_LABEL, needsAttention, type JobRow, type ServiceRow } from "./workloadModel";

/** The kind filter's values for the merged list: the workload kinds, a job, and a telemetry-only process. */
export type RowKind = "deployment" | "statefulset" | "daemonset" | "job" | "cronjob" | "process" | "unknown";

interface Common {
  /** Unique across the list. */
  key: string;
  name: string;
  /** The environment's name ("" when the scope is one environment and the telemetry names none). */
  envName: string;
  kind: RowKind;
  /** The kind's word ("Deployment", "CronJob", "Process"); "" when unknown. */
  kindLabel: string;
  tone: StatusTone;
  /** The status word beside the dot. */
  status: string;
  /** Request telemetry for the row's name in its environment, when there is any. */
  health: ServiceHealth | undefined;
  alertsFiring: number;
  /** Needs someone: a crash loop, a degraded workload, a failed latest run, a firing alert or elevated errors. */
  attention: boolean;
}

export type OverviewRow =
  | (Common & { type: "service"; service: ServiceRow })
  | (Common & { type: "job"; job: JobRow })
  | (Common & { type: "process"; process: ProcessRow });

const RANK: Record<StatusTone, number> = { danger: 0, warning: 1, success: 2, neutral: 3 };
const TYPE_RANK: Record<OverviewRow["type"], number> = { service: 0, job: 1, process: 2 };

/** Worst first: danger, then warning, then a telemetry signal, then the rest; services before jobs before processes; then by name. */
export function byRowAttention(a: OverviewRow, b: OverviewRow): number {
  const rank = (row: OverviewRow) => (row.type === "process" && row.attention ? 1.5 : RANK[row.tone]);
  return rank(a) - rank(b) || TYPE_RANK[a.type] - TYPE_RANK[b.type] || a.name.localeCompare(b.name) || a.envName.localeCompare(b.envName);
}

export interface OverviewRowsInput {
  /** The Services rows already cut to scope (workloadModel.serviceRows). */
  services: readonly ServiceRow[];
  /** The latest run of each job in scope (workloadModel.jobRows). */
  jobs: readonly JobRow[];
  /** Every workload the project reported, for a job's kind and the telemetry join. */
  workloads: readonly Workload[];
  /** GetServiceOverview's answer for the scope; grouped by environment when the scope is every environment. */
  telemetry: readonly ServiceHealth[];
  /** The firing alerts of the project's environments. */
  alerts: readonly AlertInstance[];
  /** The scope: an environment name or ENV_ALL. */
  env: string;
  envNameOf: (envId: string) => string | undefined;
  /** The workload read failed: telemetry-only rows say their health is unavailable rather than "No workload data". */
  workloadsUnread?: boolean;
}

export function overviewRows(input: OverviewRowsInput): OverviewRow[] {
  const { services, jobs, workloads, telemetry, alerts, env, envNameOf, workloadsUnread = false } = input;
  const all = env === ENV_ALL;
  const scoped = all ? workloads : workloads.filter((w) => w.envName === env);
  const scopedAlerts = all ? alerts : alerts.filter((a) => envNameOf(a.envId) === env);
  // homeModel's join gives each telemetry row its workload and its firing alerts, by name and environment.
  const joined = processRows(telemetry, scoped, scopedAlerts, envNameOf);
  // An ungrouped (one-environment) telemetry row names no environment: it is the scope's.
  const envOf = (row: ProcessRow) => row.env ?? (all ? "" : env);
  const byKey = new Map(joined.map((row) => [`${envOf(row)}/${row.name}`, row] as const));
  const telemetryOf = (name: string, envName: string) => byKey.get(`${envName}/${name}`);

  const rows: OverviewRow[] = [];
  const claimed = new Set<string>();

  for (const service of services) {
    const key = `${service.envName}/${service.workload}`;
    claimed.add(key);
    const t = telemetryOf(service.workload, service.envName);
    const verdict = service.unobserved ? { tone: "neutral" as const, label: "Unobserved" } : HEALTH_STATUS[service.health];
    const alertsFiring = t?.alertsFiring ?? 0;
    rows.push({
      type: "service",
      key: `service/${key}`,
      name: service.workload,
      envName: service.envName,
      kind: service.kind,
      kindLabel: service.unobserved ? "" : KIND_LABEL[service.kind],
      tone: verdict.tone,
      status: verdict.label,
      health: t?.health,
      alertsFiring,
      attention: !service.unobserved && (needsAttention(service) || alertsFiring > 0),
      service,
    });
  }

  for (const job of jobs) {
    const key = `${job.envName}/${job.workload}`;
    claimed.add(key);
    const t = telemetryOf(job.workload, job.envName);
    const declared = workloads.find((w) => w.envName === job.envName && w.workload === job.workload && isJobKind(w.kind));
    const kind: RowKind = declared?.kind === "cronjob" ? "cronjob" : "job";
    const result = JOB_STATUS[job.latest.result];
    rows.push({
      type: "job",
      key: `job/${key}`,
      name: job.workload,
      envName: job.envName,
      kind,
      kindLabel: KIND_LABEL[kind],
      tone: result.tone,
      status: result.label,
      health: t?.health,
      alertsFiring: t?.alertsFiring ?? 0,
      attention: job.latest.result === "failed" || (t?.alertsFiring ?? 0) > 0,
      job,
    });
  }

  for (const process of joined) {
    const envName = envOf(process);
    // A telemetry row with a workload is that service's or job's request side: already on its row.
    if (process.workload || claimed.has(`${envName}/${process.name}`)) continue;
    rows.push({
      type: "process",
      key: `process/${envName}/${process.name}`,
      name: process.name,
      envName,
      kind: "process",
      kindLabel: "Process",
      tone: process.tone,
      // Without the workload read, "No workload data" would be a claim about the workloads: say what is true.
      status: workloadsUnread && !process.signal ? "Health unavailable" : process.status,
      health: process.health,
      alertsFiring: process.alertsFiring,
      attention: !!process.signal,
      process,
    });
  }

  return rows.sort(byRowAttention);
}

export interface RowFilters {
  q?: string;
  status?: "attention";
  kind?: string;
}

export function filterRows(rows: readonly OverviewRow[], filters: RowFilters): OverviewRow[] {
  const q = filters.q?.trim().toLowerCase() ?? "";
  const release = (row: OverviewRow) =>
    row.type === "service" ? row.service.releaseVersion : row.type === "job" ? row.job.latest.releaseVersion : "";
  return rows.filter(
    (row) =>
      (!q || row.name.toLowerCase().includes(q) || release(row).toLowerCase().includes(q)) &&
      (filters.status !== "attention" || row.attention) &&
      (!filters.kind || row.kind === filters.kind)
  );
}

/** The kinds present among `rows`, in a fixed order, for the kind filter's options. */
export function rowKindsOf(rows: readonly OverviewRow[]): RowKind[] {
  const order: RowKind[] = ["deployment", "statefulset", "daemonset", "job", "cronjob", "process"];
  const present = new Set(rows.map((row) => row.kind));
  return order.filter((kind) => present.has(kind));
}

export const ROW_KIND_LABEL: Record<RowKind, string> = { ...KIND_LABEL, process: "Process" };
