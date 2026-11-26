// Copyright (c) 2026 Reliant Labs

/**
 * The ML Overview's view-model (02-views §2): "is anything burning, where is
 * the money going, what finished?" Pure functions over the generated
 * answers, so the page only draws.
 */

import {
  MlCostCategory,
  MlEventKind,
  MlFindingCategory,
  MlFindingState,
  MlRunState,
  MlSeverity,
  type MlCost,
  type MlEvent,
  type MlFinding,
  type MlInferenceService,
  type MlRun,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { severityRank } from "../kit/labels";

/** Open findings worth a person's attention now, worst money first; telemetry gaps excluded. */
export function needsAttention(findings: readonly MlFinding[], limit = 6): MlFinding[] {
  return findings
    .filter((f) => (f.state === MlFindingState.OPEN || f.state === MlFindingState.ASSIGNED) && f.category !== MlFindingCategory.COVERAGE)
    .sort((a, b) => severityRank(a.severity) - severityRank(b.severity) || impactUsd(b) - impactUsd(a))
    .slice(0, limit);
}

/** A finding's impact in $/day, for sorting (0 when unknown). */
export const impactUsd = (f: MlFinding): number => (f.impact && Number.isFinite(f.impact.usdPerDay) ? f.impact.usdPerDay : 0);

/** Runs that are live (queued, running, degraded, paused), most GPUs first. */
export function activeRuns(runs: readonly MlRun[]): MlRun[] {
  const live = new Set([MlRunState.RUNNING, MlRunState.DEGRADED, MlRunState.QUEUED, MlRunState.PAUSED]);
  return runs.filter((r) => live.has(r.state)).sort((a, b) => b.accelerators - a.accelerators);
}

/** The waste part of a cost: everything but productive time. */
export function wasteUsd(cost: MlCost | undefined): number {
  if (!cost) return 0;
  return cost.byCategory.filter((c) => c.category !== MlCostCategory.PRODUCTIVE).reduce((sum, c) => sum + c.usd, 0);
}

/** Productive share of a cost's GPU-hours, 0–1; NaN without a breakdown. */
export function productiveShare(cost: MlCost | undefined): number {
  if (!cost || cost.gpuHours <= 0 || cost.byCategory.length === 0) return Number.NaN;
  const productive = cost.byCategory.filter((c) => c.category === MlCostCategory.PRODUCTIVE).reduce((sum, c) => sum + c.gpuHours, 0);
  return productive / cost.gpuHours;
}

/** Services missing their SLO target, worst first. */
export function servicesBreachingSlo(services: readonly MlInferenceService[]): MlInferenceService[] {
  return services.filter((s) => s.sloTarget > 0 && s.sloAttainment < s.sloTarget).sort((a, b) => a.sloAttainment - a.sloTarget - (b.sloAttainment - b.sloTarget));
}

/** What changed recently: completions, failures, releases, alias moves, findings; newest first. */
const RECENT_KINDS = new Set([
  MlEventKind.COMPLETED,
  MlEventKind.RELEASE,
  MlEventKind.ALIAS_MOVE,
  MlEventKind.EVAL,
  MlEventKind.XID,
  MlEventKind.NCCL_TIMEOUT,
  MlEventKind.PREEMPTION,
  MlEventKind.RESTART,
  MlEventKind.LAUNCH,
  MlEventKind.FINDING_OPENED,
  MlEventKind.CHANGES_RAMP,
  MlEventKind.CANCEL,
]);

export function recentChanges(events: readonly MlEvent[], limit = 12): MlEvent[] {
  const seen = new Set<string>();
  return events
    .filter((e) => RECENT_KINDS.has(e.kind))
    .filter((e) => (seen.has(e.id) ? false : (seen.add(e.id), true)))
    .sort((a, b) => Number(b.at?.seconds ?? 0n) - Number(a.at?.seconds ?? 0n))
    .slice(0, limit);
}

/** Counts of open findings by severity. */
export function findingCounts(findings: readonly MlFinding[]): { critical: number; warning: number; info: number } {
  const open = findings.filter((f) => f.state === MlFindingState.OPEN || f.state === MlFindingState.ASSIGNED);
  return {
    critical: open.filter((f) => f.severity === MlSeverity.CRITICAL).length,
    warning: open.filter((f) => f.severity === MlSeverity.WARNING).length,
    info: open.filter((f) => f.severity === MlSeverity.INFO).length,
  };
}
