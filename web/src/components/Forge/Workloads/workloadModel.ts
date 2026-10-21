// Copyright (c) 2025 Reliant Labs

/**
 * The Overview's and the workload page's model: pure functions over what
 * ObservabilityService.ListWorkloads / GetWorkload return, so every rule the
 * pages apply is one testable line rather than a branch in JSX.
 *
 * THE HEALTH VERDICT IS THE SERVER'S. Workload.health is computed once, on the
 * control plane, from the deploy ledger, the replica counts and the restarts
 * (WorkloadHealth in the proto). Nothing here re-derives it from restarts or
 * events: the list, the detail page, the environment strip and "Needs
 * attention" all read the same field, so they cannot disagree.
 */

import type { StatusTone } from "@/components/forge-ui/status";
import type { AlertInstance } from "@/services/controlPlane/observability/alertingTypes";
import type {
  DeclaredWorkload,
  ResourceUsage,
  Workload,
  WorkloadContainer,
  WorkloadHealth,
  WorkloadJob,
  WorkloadKind,
  WorkloadPod,
} from "@/services/controlPlane/observability/types";

import { ENV_ALL } from "../Console/envScope";
import { alertService } from "../Observability/Home/homeModel";
import { HEALTH_STATUS } from "./health";

export { HEALTH_STATUS };

// ── Vocabulary ──────────────────────────────────────────────────────────────

/** The workload page's tabs, in order (the URL's `tab`). */
export const WORKLOAD_TABS = ["overview", "logs", "traces", "profiles", "events", "history", "config"] as const;
export type WorkloadTab = (typeof WORKLOAD_TABS)[number];

/** The Overview's filters, as the URL spells them. */
export const OVERVIEW_STATUS_FILTERS = ["attention"] as const;
export const OVERVIEW_KIND_FILTERS = ["deployment", "statefulset", "daemonset"] as const;

/** Worst first: the order "Needs attention" and a status filter rank by. */
const HEALTH_RANK: Record<WorkloadHealth, number> = {
  crash_looping: 0,
  degraded: 1,
  pending: 2,
  unknown: 3,
  suspended: 4,
  healthy: 5,
};

export const KIND_LABEL: Record<WorkloadKind, string> = {
  deployment: "Deployment",
  statefulset: "StatefulSet",
  daemonset: "DaemonSet",
  cronjob: "CronJob",
  job: "Job",
  unknown: "",
};

/** A Job or CronJob runs to completion: it belongs in the Jobs table, never in Services. */
export function isJobKind(kind: WorkloadKind): boolean {
  return kind === "job" || kind === "cronjob";
}

/** What someone should look at: a degraded or crash-looping workload. Pending is a rollout in flight, not a fault. */
export function needsAttention(workload: Pick<Workload, "health">): boolean {
  return workload.health === "degraded" || workload.health === "crash_looping";
}

// ── Figures ─────────────────────────────────────────────────────────────────

/** "2/3" ready of desired, "2" when nothing says how many are wanted, "—" when nothing is known. */
export function replicasText(workload: Pick<Workload, "readyReplicas" | "desiredReplicas">): string {
  const { readyReplicas: ready, desiredReplicas: desired } = workload;
  if (desired !== null) return `${ready ?? 0}/${desired}`;
  return ready !== null ? String(ready) : "—";
}

/** Whether fewer replicas are ready than wanted. */
export function replicasShort(workload: Pick<Workload, "readyReplicas" | "desiredReplicas">): boolean {
  return workload.desiredReplicas !== null && (workload.readyReplicas ?? 0) < workload.desiredReplicas;
}

/** The most recent termination across the containers: the reason the restarts column names. */
export function lastExitOf(workload: Pick<Workload, "containers">): { reason: string; atMs: number | null } | null {
  let best: WorkloadContainer | null = null;
  for (const c of workload.containers) {
    if (!c.lastTerminatedReason) continue;
    if (!best || (c.lastTerminatedAtMs ?? 0) > (best.lastTerminatedAtMs ?? 0)) best = c;
  }
  return best ? { reason: best.lastTerminatedReason, atMs: best.lastTerminatedAtMs } : null;
}

/**
 * The container closest to its limit for one resource — the one a meter in a
 * one-row-per-workload table should show. A container with no limit ranks
 * below any that has one.
 */
export function worstUsage(workload: Pick<Workload, "containers">, resource: "cpu" | "memory"): ResourceUsage | null {
  let best: ResourceUsage | null = null;
  for (const c of workload.containers) {
    const usage = c[resource];
    if (!usage) continue;
    if (!best || (usage.limitUtilizationLatest ?? -1) > (best.limitUtilizationLatest ?? -1)) best = usage;
  }
  return best;
}

/** "41s", "3m 12s", "1h 4m" — a Job's duration. */
export function formatSeconds(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return s % 60 ? `${m}m ${s % 60}s` : `${m}m`;
  const h = Math.floor(m / 60);
  return m % 60 ? `${h}h ${m % 60}m` : `${h}h`;
}

/** "250m", "1 core", "1.5 cores" from millicores; "" for an undeclared 0. */
export function formatMillicores(millicores: number): string {
  if (!millicores) return "";
  if (millicores < 1000) return `${millicores}m`;
  const cores = millicores / 1000;
  return `${Number.isInteger(cores) ? cores : cores.toFixed(1)} ${cores === 1 ? "core" : "cores"}`;
}

/** "512Mi", "1Gi" — Kubernetes' binary units, as the declaration spells them; "" for an undeclared 0. */
export function formatQuantityBytes(bytes: number): string {
  if (!bytes) return "";
  const units = ["", "Ki", "Mi", "Gi", "Ti"];
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1 && Number.isInteger(value / 1024)) {
    value /= 1024;
    unit += 1;
  }
  if (unit === 0 && bytes >= 1024) {
    // Not a whole number of KiB: round to the nearest MiB-ish unit for display.
    while (value >= 1024 && unit < units.length - 1) {
      value /= 1024;
      unit += 1;
    }
    return `${value.toFixed(1)}${units[unit]}`;
  }
  return `${value}${units[unit]}`;
}

/** A pod's age: since it was first seen, to its last sample when it is gone. */
export function podAgeMs(pod: Pick<WorkloadPod, "state" | "firstSeenMs" | "lastSeenMs">, now: number): number | null {
  if (pod.firstSeenMs === null) return null;
  const end = pod.state === "running" ? now : (pod.lastSeenMs ?? now);
  return Math.max(0, end - pod.firstSeenMs);
}

/** "5m", "3h", "2d" — an age, without "ago". */
export function formatAge(ms: number): string {
  const minutes = Math.floor(ms / 60_000);
  if (minutes < 1) return "<1m";
  if (minutes < 60) return `${minutes}m`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${hours}h`;
  return `${Math.floor(hours / 24)}d`;
}

// ── Rows ────────────────────────────────────────────────────────────────────

/** The long-running workloads in scope, by name and then environment. */
export function serviceRows(workloads: readonly Workload[], env: string, envOrder: readonly string[]): Workload[] {
  const order = (name: string) => {
    const index = envOrder.indexOf(name);
    return index < 0 ? envOrder.length : index;
  };
  return workloads
    .filter((w) => !isJobKind(w.kind) && (env === ENV_ALL || w.envName === env))
    .sort((a, b) => a.workload.localeCompare(b.workload) || order(a.envName) - order(b.envName));
}

/** One declared Job in one environment: its latest run, and how its recent runs went. */
export interface JobRow {
  key: string;
  envName: string;
  workload: string;
  latest: WorkloadJob;
  runs: number;
  failedRuns: number;
}

/** The latest run per (environment, job), newest first. The list arrives newest first, so the first run seen is the latest. */
export function jobRows(jobs: readonly WorkloadJob[], env: string): JobRow[] {
  const rows = new Map<string, JobRow>();
  for (const job of jobs) {
    if (env !== ENV_ALL && job.envName !== env) continue;
    const key = `${job.envName}/${job.workload}`;
    const row = rows.get(key);
    if (row) {
      row.runs += 1;
      if (job.result === "failed") row.failedRuns += 1;
    } else {
      rows.set(key, { key, envName: job.envName, workload: job.workload, latest: job, runs: 1, failedRuns: job.result === "failed" ? 1 : 0 });
    }
  }
  return [...rows.values()];
}

export const JOB_STATUS: Record<WorkloadJob["result"], { tone: StatusTone; label: string }> = {
  succeeded: { tone: "success", label: "Succeeded" },
  failed: { tone: "danger", label: "Failed" },
  running: { tone: "neutral", label: "Running" },
  unknown: { tone: "neutral", label: "Unknown" },
};

export type StatusFilter = (typeof OVERVIEW_STATUS_FILTERS)[number];
export type KindFilter = (typeof OVERVIEW_KIND_FILTERS)[number];

export interface ServiceFilters {
  q?: string;
  status?: StatusFilter;
  kind?: KindFilter;
}

export function filterServices(rows: readonly Workload[], filters: ServiceFilters): Workload[] {
  const q = filters.q?.trim().toLowerCase() ?? "";
  return rows.filter(
    (w) =>
      (!q || w.workload.toLowerCase().includes(q) || w.releaseVersion.toLowerCase().includes(q)) &&
      (filters.status !== "attention" || needsAttention(w)) &&
      (!filters.kind || w.kind === filters.kind)
  );
}

/** The kinds present among `rows`, for the kind filter's options. */
export function kindsOf(rows: readonly Workload[]): KindFilter[] {
  const kinds = new Set<KindFilter>();
  for (const w of rows) if (w.kind === "deployment" || w.kind === "statefulset" || w.kind === "daemonset") kinds.add(w.kind);
  return [...kinds].sort();
}

// ── Needs attention ─────────────────────────────────────────────────────────

export interface AttentionItem {
  key: string;
  tone: StatusTone;
  kind: "alert" | "workload" | "job";
  /** The workload, job or alert rule named in bold. */
  subject: string;
  /** What is wrong, after the subject: "crash-looping", "failed", "firing". */
  what: string;
  envName: string;
  /** The muted figure on the right: "14 restarts", "BackoffLimitExceeded", "since 52m". */
  detail: string;
  /** For a workload or job, its name (the page it opens); for an alert, null (the Alerts page). */
  workload: string | null;
}

/**
 * What needs someone, worst first: alerts firing, then crash-looping and
 * degraded workloads, then Jobs whose latest run failed. A Job that failed and
 * then succeeded is recovered and is not listed.
 */
export function attentionItems(
  workloads: readonly Workload[],
  jobs: readonly JobRow[],
  firing: readonly AlertInstance[],
  envNameOf: (envId: string) => string | undefined,
  now: number
): AttentionItem[] {
  const items: AttentionItem[] = [];
  for (const alert of firing) {
    const since = alert.firingAtMs ? Math.max(0, now - alert.firingAtMs) : null;
    items.push({
      key: `alert/${alert.envId}/${alert.fingerprint || alert.rule}`,
      tone: "warning",
      kind: "alert",
      subject: alert.rule,
      what: "firing",
      envName: envNameOf(alert.envId) ?? "",
      detail: since === null ? (alertService(alert) ?? "") : `for ${formatAge(since)}`,
      workload: null,
    });
  }
  const unhealthy = workloads.filter((w) => !isJobKind(w.kind) && needsAttention(w));
  unhealthy.sort((a, b) => HEALTH_RANK[a.health] - HEALTH_RANK[b.health] || b.restartsInWindow - a.restartsInWindow || a.workload.localeCompare(b.workload));
  for (const w of unhealthy) {
    const status = HEALTH_STATUS[w.health];
    items.push({
      key: `workload/${w.envName}/${w.workload}`,
      tone: status.tone,
      kind: "workload",
      subject: w.workload,
      what: status.label.toLowerCase(),
      envName: w.envName,
      detail: w.healthReason || (w.restartsInWindow ? `${w.restartsInWindow} restarts` : ""),
      workload: w.workload,
    });
  }
  for (const job of jobs) {
    if (job.latest.result !== "failed") continue;
    items.push({
      key: `job/${job.key}`,
      tone: "danger",
      kind: "job",
      subject: job.workload,
      what: "failed",
      envName: job.envName,
      detail: job.latest.reason || "failed",
      workload: job.workload,
    });
  }
  return items;
}

// ── The environment strip ───────────────────────────────────────────────────

export interface StripEnv {
  id: string;
  name: string;
  /** The release live there, "" when nothing was promoted. */
  release: string;
  promotedAt?: string;
  promotedBy: string;
  preview: boolean;
}

export interface StripCell {
  env: StripEnv;
  tone: StatusTone;
  label: string;
  services: number;
  failedJobs: number;
  /** Nothing reports from it: no workload and no job in the window. */
  silent: boolean;
}

/** One cell per environment: its worst workload, its live release, and its failed jobs. */
export function stripCell(env: StripEnv, workloads: readonly Workload[], jobs: readonly JobRow[]): StripCell {
  const mine = workloads.filter((w) => w.envName === env.name && !isJobKind(w.kind));
  const failing = mine.filter((w) => w.health === "crash_looping").length;
  const degraded = mine.filter((w) => w.health === "degraded").length;
  const failedJobs = jobs.filter((j) => j.envName === env.name && j.latest.result === "failed").length;
  const silent = mine.length === 0 && !jobs.some((j) => j.envName === env.name);
  if (failing) return { env, tone: "danger", label: `${failing} failing`, services: mine.length, failedJobs, silent };
  if (degraded) return { env, tone: "warning", label: `${degraded} degraded`, services: mine.length, failedJobs, silent };
  if (failedJobs) return { env, tone: "danger", label: `${failedJobs} job${failedJobs === 1 ? "" : "s"} failed`, services: mine.length, failedJobs, silent };
  if (silent) return { env, tone: "neutral", label: "No data", services: 0, failedJobs, silent };
  return { env, tone: "success", label: "Healthy", services: mine.length, failedJobs, silent };
}

/** The previews' one shared cell: the worst of them. */
export function previewsCell(previews: readonly StripEnv[], workloads: readonly Workload[], jobs: readonly JobRow[]): StripCell | null {
  if (previews.length === 0) return null;
  const cells = previews.map((env) => stripCell(env, workloads, jobs));
  const rank: Record<StatusTone, number> = { danger: 0, warning: 1, success: 2, neutral: 3 };
  const worst = [...cells].sort((a, b) => rank[a.tone] - rank[b.tone])[0]!;
  const unhealthy = cells.filter((c) => c.tone === "danger" || c.tone === "warning").length;
  return {
    env: { id: "", name: "previews", release: "", promotedBy: "", preview: true },
    tone: worst.tone,
    label: unhealthy ? `${unhealthy} need${unhealthy === 1 ? "s" : ""} attention` : worst.tone === "success" ? "Healthy" : worst.label,
    services: cells.reduce((sum, c) => sum + c.services, 0),
    failedJobs: cells.reduce((sum, c) => sum + c.failedJobs, 0),
    silent: cells.every((c) => c.silent),
  };
}

// ── The declaration ─────────────────────────────────────────────────────────

/**
 * The GitHub repository a declaration was rendered from, as owner/repo, or
 * null when it is not on GitHub or not recorded. Accepts the forms a bundle
 * records: https URLs, `github.com/o/r`, and ssh remotes, with or without .git.
 */
export function githubRepoOf(sourceRepo: string): string | null {
  const match = /github\.com[/:]([\w.-]+)\/([\w.-]+?)(?:\.git)?\/?$/.exec(sourceRepo.trim());
  return match ? `${match[1]}/${match[2]}` : null;
}

/**
 * Where to change the declaration (decision D6: replicas are declared in code,
 * so the console's Scale is "Open PR", never a mutation).
 *
 *   edit   GitHub's web editor on the branch the release was cut from: saving
 *          proposes the change as a pull request. Null when the branch or the
 *          repository is unknown — an editor on a commit cannot open one.
 *   view   the file at the exact commit the bundle was rendered from.
 */
export function declarationLinks(declared: Pick<DeclaredWorkload, "sourceRepo" | "sourceCommit" | "sourcePath">, branch: string | null): { edit: string | null; view: string | null } {
  const repo = githubRepoOf(declared.sourceRepo);
  if (!repo || !declared.sourcePath) return { edit: null, view: null };
  const path = declared.sourcePath.replace(/^\/+/, "");
  return {
    edit: branch ? `https://github.com/${repo}/edit/${encodeURIComponent(branch)}/${path}` : null,
    view: declared.sourceCommit ? `https://github.com/${repo}/blob/${declared.sourceCommit}/${path}` : null,
  };
}
