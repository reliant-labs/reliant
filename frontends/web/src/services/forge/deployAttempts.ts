// Copyright (c) 2025 Reliant Labs

/**
 * DEPLOYS: one record per `forge env deploy` invocation, from a terminal, CI
 * or the console's Deploy dialog (which runs forge on the daemon). The
 * control plane keeps them (DeployService.ListDeployAttempts); forge writes
 * them.
 *
 * A deploy is the RUN, not what is running. A release is the artifact and a
 * promotion the intent; a deploy is the attempt that cut one and wrote the
 * other — or broke before it could, which is the case nothing else records:
 * a build that failed, a plan the guard refused. Whether its release then
 * RUNS is the promotion's rollout, which the Releases page shows.
 *
 * No React here; the pure helpers decide what the Overview's banner says.
 */

import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";

import { DeployAttemptState, type DeployAttempt } from "@/gen/controlplane/controlplane/v1/deploy_pb";
import { DeployService } from "@/gen/controlplane/services/deploy/v1/deploy_pb";

import { getControlPlaneClient } from "../controlPlane/client";

export type DeployRunState = "running" | "succeeded" | "failed" | "queued" | "cancelled" | "abandoned" | "unknown";

export interface DeployRun {
  id: string;
  env: string;
  /** apply | plan | rollback. A `plan` run builds and plans and writes no promotion. */
  mode: string;
  /** terminal | ci | console, as forge reported it. */
  source: string;
  /** The release it ships; "" when it failed before choosing one. */
  release: string;
  /** The promotion it wrote; "" when it wrote none. */
  promotionId: string;
  startedByUserId: string;
  startedByActor: string;
  /** The phase it is in, or was in when it ended. */
  phase: string;
  phases: { name: string; at?: string }[];
  state: DeployRunState;
  /** For a failed (or abandoned) run: where it stopped. */
  failedPhase: string;
  error: string;
  exitCode: number | null;
  runUrl: string;
  startedAt?: string;
  finishedAt?: string;
  hasLog: boolean;
  logTruncated: boolean;
  hasReport: boolean;
}

export interface DeployRunDetail {
  run: DeployRun;
  log: string;
  report: string;
}

const STATES: Record<number, DeployRunState> = {
  [DeployAttemptState.RUNNING]: "running",
  [DeployAttemptState.SUCCEEDED]: "succeeded",
  [DeployAttemptState.FAILED]: "failed",
  [DeployAttemptState.QUEUED]: "queued",
  [DeployAttemptState.CANCELLED]: "cancelled",
  [DeployAttemptState.ABANDONED]: "abandoned",
};

function toISO(ts: Timestamp | undefined): string | undefined {
  return ts ? timestampDate(ts).toISOString() : undefined;
}

export function toDeployRun(msg: DeployAttempt): DeployRun {
  return {
    id: msg.id,
    env: msg.environmentName,
    mode: msg.mode,
    source: msg.source,
    release: msg.releaseVersion,
    promotionId: msg.promotionId,
    startedByUserId: msg.startedByUserId,
    startedByActor: msg.startedByActor,
    phase: msg.phase,
    phases: msg.phases.map((p) => ({ name: p.name, at: toISO(p.startedAt) })),
    // An unrecognised state is `unknown`, never a success: a newer control
    // plane's verdict must not read as green.
    state: STATES[msg.state] ?? "unknown",
    failedPhase: msg.failedPhase,
    error: msg.errorMessage,
    exitCode: msg.exitCode ?? null,
    runUrl: msg.run?.url ?? "",
    startedAt: toISO(msg.startedAt),
    finishedAt: toISO(msg.finishedAt),
    hasLog: msg.hasLog,
    logTruncated: msg.logTruncated,
    hasReport: msg.hasReport,
  };
}

function client() {
  return getControlPlaneClient(DeployService);
}

/** How many deploys the Releases page lists. */
export const DEPLOY_RUN_LIST_LIMIT = 25;

/** A project's deploys, newest first; one environment's when `env` is set. */
export async function listDeployRuns(project: string, env?: string, limit = DEPLOY_RUN_LIST_LIMIT): Promise<DeployRun[]> {
  const res = await client().listDeployAttempts({ project, environmentName: env, limit });
  return res.attempts.map(toDeployRun);
}

/** One deploy with the tail of its output and its `--json` report. */
export async function getDeployRun(id: string): Promise<DeployRunDetail> {
  const res = await client().getDeployAttempt({ id });
  if (!res.attempt) throw new Error(`deploy ${id} came back empty`);
  return { run: toDeployRun(res.attempt), log: res.log, report: res.reportJson };
}

// ── What the Overview says about them ───────────────────────────────────────

/** How long a failed deploy keeps its place above the environment strip. */
export const FAILED_DEPLOY_SHOWN_MS = 24 * 60 * 60_000;

/**
 * The deploys worth interrupting the Overview for: per environment, the
 * newest deploy, when it is still running or it failed (or went silent)
 * within the last day — and no later deploy to that environment succeeded,
 * which the "newest" rule already says. Newest first.
 */
export function deploysNeedingAttention(runs: readonly DeployRun[], now: number = Date.now()): DeployRun[] {
  const newest = new Map<string, DeployRun>();
  for (const run of runs) {
    // Runs arrive newest first; the first one per environment is its latest.
    if (!newest.has(run.env)) newest.set(run.env, run);
  }
  return [...newest.values()].filter((run) => {
    if (run.state === "running") return true;
    if (run.state !== "failed" && run.state !== "abandoned") return false;
    const at = Date.parse(run.finishedAt ?? run.startedAt ?? "");
    return Number.isFinite(at) && now - at <= FAILED_DEPLOY_SHOWN_MS;
  });
}

const PHASE_WORDS: Record<string, string> = {
  start: "starting",
  preflight: "checking capacity",
  build: "building",
  plan: "planning",
  promote: "promoting",
  rollout: "rolling out",
};

/** "building", "rolling out" — a phase as a reader says it; an unknown one as it is. */
export function phaseWords(phase: string): string {
  return PHASE_WORDS[phase] ?? phase;
}

/** One line for a deploy: "Deploy to prod failed while building", "Deploying 2026… to prod: rolling out". */
export function deployHeadline(run: DeployRun, short: (version: string) => string = (v) => v): string {
  const what = run.mode === "plan" ? "Plan" : run.mode === "rollback" ? "Rollback" : "Deploy";
  const release = run.release ? ` ${short(run.release)}` : "";
  switch (run.state) {
    case "running":
      return `${what}${release} to ${run.env}: ${phaseWords(run.phase)}`;
    case "failed":
      return `${what}${release} to ${run.env} failed${run.failedPhase ? ` while ${phaseWords(run.failedPhase)}` : ""}`;
    case "abandoned":
      return `${what}${release} to ${run.env} stopped reporting${run.failedPhase ? ` while ${phaseWords(run.failedPhase)}` : ""}`;
    case "queued":
      return `${what}${release} to ${run.env} is queued`;
    case "cancelled":
      return `${what}${release} to ${run.env} was cancelled`;
    case "succeeded":
      if (run.mode === "plan") return `Planned${release} for ${run.env}`;
      if (run.mode === "rollback") return `Rolled back ${run.env}${run.release ? ` to ${short(run.release)}` : ""}`;
      return `Deployed${release} to ${run.env}`;
    default:
      return `${what}${release} to ${run.env}`;
  }
}
