// Copyright (c) 2026 Reliant Labs

/**
 * The ML pages' words and orderings, pure (so they are tested without a
 * render): a digest shortened for a table, a health flag as a sentence
 * ("Xid 79 on GPU-5"), each state's tone and label, findings worst first.
 */

import type { StatusTone } from "@/components/forge-ui/status";
import type {
  MlComponentState,
  MlFinding,
  MlFindingSeverity,
  MlHealthFlag,
  MlNodeHealth,
  MlReleaseVerdict,
  MlRun,
  MlRunState,
} from "@/services/controlPlane/ml/types";

/** `sha256:abcdef0123…` → `sha256:abcdef01…`; anything else as it is. */
export function shortDigest(digest: string): string {
  const match = /^([a-z0-9]+):([0-9a-f]+)$/i.exec(digest);
  if (!match || match[2].length <= 12) return digest;
  return `${match[1]}:${match[2].slice(0, 12)}…`;
}

const FLAG_KIND_WORDS: Record<string, string> = {
  xid: "Xid",
  ecc_dbe: "Uncorrectable ECC error",
  row_remap_failure: "Row remap failure",
  condition: "Condition",
  allocatable_below_capacity: "Allocatable below capacity",
};

/** A health flag in words: "Xid 79 on GPU-5", "Condition GPUUnhealthy". */
export function flagWords(flag: MlHealthFlag): string {
  const kind = FLAG_KIND_WORDS[flag.kind] ?? flag.kind;
  const code = flag.code ? ` ${flag.code}` : "";
  const device = flag.device ? ` on ${flag.device}` : "";
  return `${kind}${code}${device}`;
}

export const NODE_HEALTH: Record<MlNodeHealth, { tone: StatusTone; label: string }> = {
  healthy: { tone: "success", label: "Healthy" },
  degraded: { tone: "warning", label: "Degraded" },
  bad: { tone: "danger", label: "Faulty" },
  unknown: { tone: "neutral", label: "Unknown" },
};

export const COMPONENT_STATE: Record<MlComponentState, { tone: StatusTone; label: string; note: string }> = {
  adopted: { tone: "success", label: "Adopted", note: "Found in the cluster; forge reads it and never changes it" },
  installed: { tone: "success", label: "Installed", note: "Not found, so forge installed it from a pinned upstream chart" },
  missing: { tone: "warning", label: "Missing", note: "Not found and not installed" },
  unknown: { tone: "neutral", label: "Unknown", note: "" },
};

export const SEVERITY: Record<MlFindingSeverity, { tone: StatusTone; label: string; rank: number }> = {
  critical: { tone: "danger", label: "Critical", rank: 0 },
  warning: { tone: "warning", label: "Warning", rank: 1 },
  info: { tone: "neutral", label: "Info", rank: 2 },
  unknown: { tone: "neutral", label: "Unknown", rank: 3 },
};

/** Worst first, then most recently seen: the order the server promises, kept if a client re-sorts. */
export function findingsWorstFirst(findings: readonly MlFinding[]): MlFinding[] {
  return [...findings].sort((a, b) => SEVERITY[a.severity].rank - SEVERITY[b.severity].rank || (b.lastSeenAtMs ?? 0) - (a.lastSeenAtMs ?? 0));
}

export const RUN_STATE: Record<MlRunState, { tone: StatusTone; label: string; busy?: boolean }> = {
  queued: { tone: "neutral", label: "Queued" },
  running: { tone: "neutral", label: "Running", busy: true },
  succeeded: { tone: "success", label: "Succeeded" },
  failed: { tone: "danger", label: "Failed" },
  cancelled: { tone: "neutral", label: "Cancelled" },
  unknown: { tone: "neutral", label: "Unknown" },
};

export const VERDICT: Record<MlReleaseVerdict, { tone: StatusTone; label: string }> = {
  pass: { tone: "success", label: "Pass" },
  fail: { tone: "danger", label: "Fail" },
  inconclusive: { tone: "warning", label: "Not proven" },
  unknown: { tone: "neutral", label: "Not checked" },
};

/** How long a run ran: to its finish, or to `now` while it runs; null before it started. */
export function runDurationMs(run: MlRun, now: number): number | null {
  if (run.startedAtMs === null) return null;
  const end = run.finishedAtMs ?? (run.state === "running" ? now : null);
  return end === null ? null : Math.max(0, end - run.startedAtMs);
}

/** A run's loss curve (the first series named loss), for its sparkline; null when it logged none. */
export function lossSeries(run: MlRun): MlRun["metrics"][number] | null {
  return run.metrics.find((m) => m.name === "loss" || m.name.endsWith("/loss")) ?? null;
}

export const percent = (value: number) => `${Math.round(value)}%`;

/** Seconds as a stage's time: "850 ms", "4.2 s", "3 min 10 s". */
export function seconds(value: number): string {
  if (value < 1) return `${Math.round(value * 1000)} ms`;
  if (value < 60) return `${value.toFixed(value < 10 ? 1 : 0)} s`;
  const minutes = Math.floor(value / 60);
  const rest = Math.round(value % 60);
  return rest ? `${minutes} min ${rest} s` : `${minutes} min`;
}

export const millis = (value: number) => (value >= 1000 ? `${(value / 1000).toFixed(1)} s` : `${Math.round(value)} ms`);

/** An eval metric's value: fractions to three places, anything larger to two. */
export function metricValue(value: number): string {
  return Math.abs(value) < 1 ? value.toFixed(3) : value.toFixed(2);
}
