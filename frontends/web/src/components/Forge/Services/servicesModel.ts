// Copyright (c) 2026 Reliant Labs

/**
 * THE SERVICES PAGE'S ROWS — pure, so its rules are tested alone (prototype).
 *
 * One row per thing the app runs, in the scope, from reads the console
 * already makes:
 *
 *   workload   ListWorkloads: a Deployment, StatefulSet, DaemonSet or CronJob,
 *              with the server's health verdict, ready/desired, CPU and memory
 *              (each container's worst replica, summed over the containers),
 *              and the request side joined from GetServiceOverview by name.
 *   job        a Job seen only in its runs (ListWorkloads.jobs): its latest run.
 *   site       a static deployment GetStatus reports (tier `static`).
 *   database   a managed database GetStatus reports (tier `database`).
 *
 * The kind is the Kubernetes kind, said as it is: the control plane does not
 * report forge's service/worker distinction, so a worker is a "Deployment"
 * that serves no requests.
 */

import type { StatusTone } from "@/components/forge-ui/status";
import type { MetricCatalogEntry } from "@/services/controlPlane/observability/metricsTypes";
import type { Panel, ServiceHealth, Workload, WorkloadContainer, WorkloadJob } from "@/services/controlPlane/observability/types";
import type { ForgeHostedWorkload } from "@/services/forge/topology";

import { ENV_ALL } from "../Console/envScope";
import { ENV_KEY, SERVICE_KEY } from "../Observability/attributes";
import { defaultSeriesFor } from "../Observability/Metrics/model/explorerState";
import { HEALTH_STATUS, JOB_STATUS, KIND_LABEL, isJobKind, jobRows, jobStatusOf, latestRunsByWorkload } from "../Workloads/workloadModel";

export interface ContainerFigure {
  container: string;
  /** Cores, the worst replica's latest; null when not reported. */
  cpu: number | null;
  /** Bytes (working set), the worst replica's latest; null when not reported. */
  memory: number | null;
}

interface RowBase {
  key: string;
  name: string;
  envName: string;
  /** The word beside the name: "Deployment", "CronJob", "Static site", "Managed Postgres". */
  kindLabel: string;
  tone: StatusTone;
  status: string;
}

export type ServicesRow =
  | (RowBase & {
      type: "workload";
      workload: Workload;
      /** Ready of desired; "—" for a Job or CronJob, which has no replica claim. */
      ready: string;
      short: boolean;
      /** A Job or CronJob's latest run in the window; null for anything else, or with no run in the window. */
      lastRun: WorkloadJob | null;
      cpu: number | null;
      memory: number | null;
      containers: ContainerFigure[];
      health: ServiceHealth | undefined;
      release: string;
    })
  | (RowBase & { type: "job"; latest: WorkloadJob; release: string })
  | (RowBase & { type: "site"; url: string; release: string })
  | (RowBase & { type: "database"; release: string });

export interface ServicesEnv {
  name: string;
  release: string;
  preview?: boolean;
}

export interface ServicesRowsInput {
  workloads: readonly Workload[];
  jobs: readonly WorkloadJob[];
  telemetry: readonly ServiceHealth[];
  /** GetStatus per environment name, then deployment name. */
  hosted: ReadonlyMap<string, ReadonlyMap<string, ForgeHostedWorkload>>;
  envs: readonly ServicesEnv[];
  /** An environment name or ENV_ALL. */
  env: string;
}

/** A sum that is null only when every part is. */
function sumOf(values: readonly (number | null)[]): number | null {
  return values.some((v) => v !== null) ? values.reduce<number>((acc, v) => acc + (v ?? 0), 0) : null;
}

export function containerFigures(containers: readonly WorkloadContainer[]): ContainerFigure[] {
  return containers.map((c) => ({ container: c.container, cpu: c.cpu?.latest ?? null, memory: c.memory?.latest ?? null }));
}

/** The telemetry row for `name` in `envName`: grouped by environment name when the scope is every environment. */
export function telemetryFor(telemetry: readonly ServiceHealth[], name: string, envName: string): ServiceHealth | undefined {
  return telemetry.find((t) => t.name === name && (t.group[ENV_KEY] === undefined || t.group[ENV_KEY] === envName));
}

const VERDICT: Record<string, { tone: StatusTone; label: string }> = {
  converged: { tone: "success", label: "Live" },
  converging: { tone: "neutral", label: "Deploying" },
  diverged: { tone: "warning", label: "Drifted" },
  degraded: { tone: "danger", label: "Degraded" },
};

export function verdictStatus(hosted: ForgeHostedWorkload): { tone: StatusTone; label: string } {
  return VERDICT[hosted.verdict ?? ""] ?? { tone: "neutral", label: "Unknown" };
}

/** Worst first, then workloads before jobs, sites and the database, then by name and environment. */
const TONE_RANK: Record<StatusTone, number> = { danger: 0, warning: 1, neutral: 2, success: 3 };
const TYPE_RANK: Record<ServicesRow["type"], number> = { workload: 0, job: 1, site: 2, database: 3 };

export function servicesRows({ workloads, jobs, telemetry, hosted, envs, env }: ServicesRowsInput): ServicesRow[] {
  const inScope = (envName: string) => (env === ENV_ALL ? !envs.find((e) => e.name === envName)?.preview : envName === env);
  const releaseOf = (envName: string) => envs.find((e) => e.name === envName)?.release ?? "";
  const rows: ServicesRow[] = [];
  const seen = new Set<string>();
  const latestRuns = latestRunsByWorkload(jobs);
  for (const w of workloads) {
    if (!inScope(w.envName)) continue;
    const key = `${w.envName}/${w.workload}`;
    seen.add(key);
    // A Job or CronJob is judged on its runs, never on ready of desired: no
    // pod running after a completed run is its normal state, not a shortfall.
    const runs = isJobKind(w.kind);
    const lastRun = runs ? (latestRuns.get(key) ?? null) : null;
    const status = runs ? jobStatusOf(w, lastRun) : HEALTH_STATUS[w.health];
    const containers = containerFigures(w.containers);
    rows.push({
      type: "workload",
      key: `w:${key}`,
      name: w.workload,
      envName: w.envName,
      kindLabel: KIND_LABEL[w.kind] || "Workload",
      tone: status.tone,
      status: status.label,
      workload: w,
      ready: runs ? "—" : w.desiredReplicas !== null ? `${w.readyReplicas ?? 0}/${w.desiredReplicas}` : w.readyReplicas !== null ? String(w.readyReplicas) : "—",
      short: !runs && w.desiredReplicas !== null && (w.readyReplicas ?? 0) < w.desiredReplicas,
      lastRun,
      cpu: sumOf(containers.map((c) => c.cpu)),
      memory: sumOf(containers.map((c) => c.memory)),
      containers,
      health: telemetryFor(telemetry, w.workload, w.envName),
      release: w.releaseVersion || releaseOf(w.envName),
    });
  }
  for (const job of jobRows(jobs, ENV_ALL)) {
    if (!inScope(job.envName) || seen.has(`${job.envName}/${job.workload}`)) continue;
    const status = JOB_STATUS[job.latest.result];
    rows.push({
      type: "job",
      key: `j:${job.key}`,
      name: job.workload,
      envName: job.envName,
      kindLabel: "Job",
      tone: status.tone,
      status: status.label,
      latest: job.latest,
      release: job.latest.releaseVersion || releaseOf(job.envName),
    });
  }
  for (const [envName, byName] of hosted) {
    if (!inScope(envName)) continue;
    for (const deployment of byName.values()) {
      if (!deployment.name) continue;
      const verdict = verdictStatus(deployment);
      if (deployment.tier === "static") {
        rows.push({
          type: "site",
          key: `s:${envName}/${deployment.name}`,
          name: deployment.name,
          envName,
          kindLabel: "Static site",
          tone: verdict.tone,
          status: verdict.label,
          url: deployment.url || (deployment.hostname ? `https://${deployment.hostname}` : ""),
          release: releaseOf(envName),
        });
      } else if (deployment.tier === "database") {
        rows.push({
          type: "database",
          key: `d:${envName}/${deployment.name}`,
          name: deployment.name,
          envName,
          kindLabel: "Managed Postgres",
          tone: verdict.tone,
          status: verdict.label === "Live" ? "Provisioned" : verdict.label,
          release: releaseOf(envName),
        });
      }
    }
  }
  return rows.sort(
    (a, b) => TONE_RANK[a.tone] - TONE_RANK[b.tone] || TYPE_RANK[a.type] - TYPE_RANK[b.type] || a.name.localeCompare(b.name) || a.envName.localeCompare(b.envName)
  );
}

/** A Job or CronJob row's latest run in the window (a workload's own, or a job's seen only in its runs); null for anything else. */
export function runOfRow(row: ServicesRow): WorkloadJob | null {
  if (row.type === "job") return row.latest;
  return row.type === "workload" ? row.lastRun : null;
}

/** Whether a row is a Job or CronJob: what has no replica claim and is judged on its runs. */
export function runsToCompletion(row: ServicesRow): boolean {
  return row.type === "job" || (row.type === "workload" && isJobKind(row.workload.kind));
}

/** The database deployments GetStatus reports, by environment: what the Services page's database row and page read. */
export function databasesOf(hosted: ReadonlyMap<string, ReadonlyMap<string, ForgeHostedWorkload>>, name?: string): { envName: string; deployment: ForgeHostedWorkload }[] {
  const out: { envName: string; deployment: ForgeHostedWorkload }[] = [];
  for (const [envName, byName] of hosted) {
    for (const deployment of byName.values()) {
      if (deployment.tier === "database" && (!name || deployment.name === name)) out.push({ envName, deployment });
    }
  }
  return out;
}

/** The static deployments named `name`, by environment. */
export function sitesOf(hosted: ReadonlyMap<string, ReadonlyMap<string, ForgeHostedWorkload>>, name: string): { envName: string; deployment: ForgeHostedWorkload }[] {
  const out: { envName: string; deployment: ForgeHostedWorkload }[] = [];
  for (const [envName, byName] of hosted) {
    const deployment = byName.get(name);
    if (deployment?.tier === "static") out.push({ envName, deployment });
  }
  return out;
}

/** Sum per-container series sharing timestamps into one (the workload's), by bucket. */
export function sumSeries(series: readonly { timestamps: number[]; values: (number | null)[] }[]): { timestamps: number[]; values: (number | null)[] } | null {
  if (series.length === 0) return null;
  const longest = [...series].sort((a, b) => b.timestamps.length - a.timestamps.length)[0]!;
  const index = series.map((s) => new Map(s.timestamps.map((t, i) => [t, s.values[i] ?? null])));
  return { timestamps: longest.timestamps, values: longest.timestamps.map((t) => sumOf(index.map((m) => m.get(t) ?? null))) };
}

/** The app's own metrics: declared in its schema, or sent with no forge or infrastructure prefix. */
export function customMetrics(metrics: readonly MetricCatalogEntry[], service?: string): MetricCatalogEntry[] {
  return metrics.filter((m) => (m.group === "declared" || m.group === "other") && (!service || m.services.length === 0 || m.services.includes(service)));
}

export function metricPanel(entry: MetricCatalogEntry, service?: string): Panel {
  return {
    id: `custom-metric-${entry.name}`,
    title: entry.name,
    kind: "line",
    signal: "metrics",
    series: [defaultSeriesFor(entry, "A")],
    where: service ? [{ field: SERVICE_KEY, op: "eq", value: service }] : undefined,
  };
}

