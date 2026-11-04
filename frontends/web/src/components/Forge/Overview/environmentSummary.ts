// Copyright (c) 2025 Reliant Labs

/**
 * What the Environments page says about one environment, as plain data: the
 * pure half of EnvironmentCards. Every fact is a read the console already
 * makes elsewhere, joined here so the page and its tests agree:
 *
 *   release     GetLiveView (the roster): the release bound now and who put it there.
 *   health      a placed env: the platform's GetStatus verdict. Otherwise the
 *               node agent's telemetry (ListWorkloads), cut exactly as the
 *               Overview's environment strip cuts it (stripCell).
 *   workloads   ready / total of its long-running workloads: telemetry first,
 *               GetStatus's observations where telemetry has none.
 *   last deploy ListDeployAttempts: the newest run of `forge env deploy` for it.
 *   obs check   GetReleaseHealth on the current promotion: what `forge obs
 *               check <env>` judges.
 *   drift       the roster's own verdict on running vs declared.
 *
 * An unread fact is said to be unread; it never reads as healthy.
 */

import { verdictTone } from "@/components/Apps/appParts";
import type { StatusTone } from "@/components/forge-ui/status";
import type { ReleaseHealth } from "@/services/controlPlane/observability/deliveryTypes";
import type { Workload } from "@/services/controlPlane/observability/types";
import type { CloudEnvStatus } from "@/services/forge/cloudEnvs";
import type { DeployRun } from "@/services/forge/deployAttempts";
import { driftLine, isPlacedKind, type LiveEnv } from "@/services/forge/live";
import { verdictLabel } from "@/services/forge/topology";

import { isJobKind, replicasShort, stripCell, type JobRow, type StripEnv } from "../Workloads/workloadModel";

/** A read that may not have answered: what each fact is built from. */
export interface Read<T> {
  data: T | undefined;
  loading: boolean;
  failed: boolean;
}

export interface Fact {
  tone: StatusTone;
  label: string;
  /** One more line of context, or "". */
  detail: string;
}

export interface WorkloadCount {
  ready: number;
  total: number;
  /** Where the count came from. */
  source: "telemetry" | "platform";
}

export interface EnvSummaryInput {
  env: LiveEnv;
  /** GetStatus, for a placed environment only. */
  status: Read<CloudEnvStatus>;
  /** The project's ListWorkloads answer (shared with the Overview). */
  workloads: Read<{ workloads: Workload[]; jobs: JobRow[] }>;
  /** The newest deploy runs of the project, newest first. */
  runs: Read<DeployRun[]>;
  /** GetReleaseHealth on the env's current promotion; absent when nothing is promoted. */
  check: Read<ReleaseHealth> | null;
  /** The workloads its last render declares, by name (a self-managed env with no telemetry is "Unobserved", not "No data"). */
  declared: string[];
}

export interface EnvSummary {
  env: LiveEnv;
  health: Fact;
  workloads: WorkloadCount | null;
  lastDeploy: DeployRun | null;
  check: Fact | null;
  drift: Fact;
}

const UNREAD = (label: string): Fact => ({ tone: "neutral", label, detail: "" });

export function summarizeEnvironment(input: EnvSummaryInput): EnvSummary {
  const { env } = input;
  const placed = isPlacedKind(env.kind);
  const mine = (input.workloads.data?.workloads ?? []).filter((w) => w.envName === env.name && !isJobKind(w.kind));
  return {
    env,
    health: healthOf(input, placed),
    workloads: workloadCount(mine, placed ? input.status.data : undefined),
    lastDeploy: (input.runs.data ?? []).find((run) => run.env === env.name) ?? null,
    check: checkOf(input.check, env),
    drift: driftOf(env),
  };
}

function healthOf(input: EnvSummaryInput, placed: boolean): Fact {
  if (placed) {
    if (input.status.loading && !input.status.data) return UNREAD("Checking");
    if (!input.status.data) return UNREAD(input.status.failed ? "Not read" : "Not reported");
    const verdict = input.status.data.verdict;
    return { tone: verdictTone(verdict), label: verdictLabel(verdict), detail: "" };
  }
  if (input.workloads.loading && !input.workloads.data) return UNREAD("Checking");
  if (!input.workloads.data) return UNREAD(input.workloads.failed ? "Not read" : "Not reported");
  const strip: StripEnv = {
    id: input.env.id,
    name: input.env.name,
    release: input.env.release,
    promotedBy: "",
    preview: input.env.kind === "preview",
    placed,
    declared: input.declared,
  };
  const cell = stripCell(strip, input.workloads.data.workloads, input.workloads.data.jobs);
  return { tone: cell.tone, label: cell.label, detail: "" };
}

/**
 * Ready over total long-running workloads. Telemetry (the node agent) when it
 * reports any for the environment; otherwise what the platform observed of a
 * placed one's backends; otherwise nothing is known.
 */
export function workloadCount(mine: readonly Workload[], status: CloudEnvStatus | undefined): WorkloadCount | null {
  if (mine.length > 0) {
    const ready = mine.filter((w) => w.health === "healthy" || (w.health !== "crash_looping" && w.health !== "degraded" && w.desiredReplicas !== null && !replicasShort(w))).length;
    return { ready, total: mine.length, source: "telemetry" };
  }
  const backends = (status?.workloads ?? []).filter((w) => (w.tier ?? "backend") === "backend");
  if (backends.length > 0) {
    return { ready: backends.filter((w) => w.observed_state === "ready").length, total: backends.length, source: "platform" };
  }
  return null;
}

/** The obs check's words: `forge obs check` exits 0 / 1 / 2 / 8 on the same verdict. */
function checkOf(check: Read<ReleaseHealth> | null, env: LiveEnv): Fact | null {
  if (!check || env.release === "") return null;
  if (check.loading && !check.data) return UNREAD("Checking");
  if (!check.data) return { tone: "neutral", label: "Not read", detail: check.failed ? "The release's health could not be read." : "" };
  const h = check.data;
  if (!h.afterComplete && h.verdict !== "fail") {
    return { tone: "neutral", label: "Still watching", detail: "The window after this release has not finished yet." };
  }
  switch (h.verdict) {
    case "pass":
      return { tone: "success", label: "Proven good", detail: "No regression after this release." };
    case "fail":
      return { tone: "danger", label: "Regressed", detail: `${h.findings.filter((f) => f.policy === "fail").length || 1} finding(s) crossed a fail threshold.` };
    case "inconclusive":
      return { tone: "warning", label: "Not proven", detail: "Too little to judge: not a pass." };
    default:
      return { tone: "neutral", label: "No verdict", detail: "" };
  }
}

function driftOf(env: LiveEnv): Fact {
  const line = driftLine(env.drift);
  switch (env.drift.state) {
    case "in_sync":
      return { tone: "success", label: line, detail: "" };
    case "drifted":
      return { tone: "warning", label: line, detail: env.driftDetail };
    case "unknown":
      return { tone: "neutral", label: line, detail: env.driftDetail };
    default:
      return { tone: "neutral", label: "Not reported", detail: "Nothing has compared what runs with what was declared." };
  }
}
