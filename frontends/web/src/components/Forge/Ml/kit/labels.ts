// Copyright (c) 2026 Reliant Labs

/**
 * The contract's enums as words and tones, once. A tone is what the dot is:
 * danger, warning, success, info (running / live: blue), or neutral. Status
 * colour is a dot, an icon or a count, never a fill (web-styling).
 *
 * An enum value this build does not know (a newer server) reads "unknown",
 * never a guess.
 */

import {
  MlAttemptEndReason,
  MlCollection,
  MlComponentState,
  MlCostCategory,
  MlEventKind,
  MlFindingCategory,
  MlFindingState,
  MlHealth,
  MlImpactUnit,
  MlPendingReason,
  MlPhaseKind,
  MlPhaseState,
  MlReleaseVerdict,
  MlRunBadge,
  MlRunState,
  MlSeverity,
  MlSubjectKind,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

export type MlTone = "danger" | "warning" | "success" | "info" | "neutral";

export interface ToneLabel {
  label: string;
  tone: MlTone;
}

function table<E extends number>(rows: Partial<Record<E, ToneLabel>>): (value: E) => ToneLabel {
  return (value) => rows[value] ?? { label: "unknown", tone: "neutral" };
}

export const severityOf = table<MlSeverity>({
  [MlSeverity.CRITICAL]: { label: "Critical", tone: "danger" },
  [MlSeverity.WARNING]: { label: "Warning", tone: "warning" },
  [MlSeverity.INFO]: { label: "Info", tone: "neutral" },
});

/** Worst first: a sort key for severities. */
export const severityRank = (s: MlSeverity): number => (s === MlSeverity.CRITICAL ? 0 : s === MlSeverity.WARNING ? 1 : s === MlSeverity.INFO ? 2 : 3);

export const healthOf = table<MlHealth>({
  [MlHealth.HEALTHY]: { label: "Healthy", tone: "success" },
  [MlHealth.DEGRADED]: { label: "Degraded", tone: "warning" },
  [MlHealth.FAULTY]: { label: "Faulty", tone: "danger" },
  [MlHealth.UNKNOWN]: { label: "Unknown", tone: "neutral" },
});

export const runStateOf = table<MlRunState>({
  [MlRunState.QUEUED]: { label: "Queued", tone: "neutral" },
  [MlRunState.RUNNING]: { label: "Running", tone: "info" },
  [MlRunState.DEGRADED]: { label: "Degraded", tone: "warning" },
  [MlRunState.PAUSED]: { label: "Paused", tone: "neutral" },
  [MlRunState.SUCCEEDED]: { label: "Succeeded", tone: "success" },
  [MlRunState.FAILED]: { label: "Failed", tone: "danger" },
  [MlRunState.CANCELLED]: { label: "Cancelled", tone: "neutral" },
  [MlRunState.UNKNOWN]: { label: "Unknown", tone: "neutral" },
});

export const phaseStateOf = table<MlPhaseState>({
  [MlPhaseState.PENDING]: { label: "Pending", tone: "neutral" },
  [MlPhaseState.QUEUED]: { label: "Queued", tone: "neutral" },
  [MlPhaseState.ADMITTED]: { label: "Admitted", tone: "neutral" },
  [MlPhaseState.STARTING]: { label: "Starting", tone: "info" },
  [MlPhaseState.RUNNING]: { label: "Running", tone: "info" },
  [MlPhaseState.RESTARTING]: { label: "Restarting", tone: "warning" },
  [MlPhaseState.SUCCEEDED]: { label: "Succeeded", tone: "success" },
  [MlPhaseState.FAILED]: { label: "Failed", tone: "danger" },
  [MlPhaseState.CANCELLED]: { label: "Cancelled", tone: "neutral" },
  [MlPhaseState.SKIPPED]: { label: "Skipped", tone: "neutral" },
});

export const phaseKindOf = table<MlPhaseKind>({
  [MlPhaseKind.PRETRAIN]: { label: "Pre-training", tone: "neutral" },
  [MlPhaseKind.MIDTRAIN]: { label: "Mid-training", tone: "neutral" },
  [MlPhaseKind.SFT]: { label: "SFT", tone: "neutral" },
  [MlPhaseKind.DPO]: { label: "DPO", tone: "neutral" },
  [MlPhaseKind.RL]: { label: "RL", tone: "neutral" },
  [MlPhaseKind.DISTILL]: { label: "Distillation", tone: "neutral" },
  [MlPhaseKind.QUANTIZE]: { label: "Quantization", tone: "neutral" },
  [MlPhaseKind.EVAL]: { label: "Eval", tone: "neutral" },
  [MlPhaseKind.CUSTOM]: { label: "Custom", tone: "neutral" },
});

export const endReasonOf = table<MlAttemptEndReason>({
  [MlAttemptEndReason.COMPLETED]: { label: "Completed", tone: "success" },
  [MlAttemptEndReason.PREEMPTED_BY_QUEUE]: { label: "Preempted (queue)", tone: "warning" },
  [MlAttemptEndReason.PREEMPTED_BY_SCHEDULER]: { label: "Preempted (scheduler)", tone: "warning" },
  [MlAttemptEndReason.SPOT_RECLAIMED]: { label: "Spot reclaimed", tone: "warning" },
  [MlAttemptEndReason.NODE_FAILURE]: { label: "Node failure", tone: "danger" },
  [MlAttemptEndReason.GPU_ERROR]: { label: "GPU error", tone: "danger" },
  [MlAttemptEndReason.NCCL_TIMEOUT]: { label: "NCCL timeout", tone: "danger" },
  [MlAttemptEndReason.HANG]: { label: "Hang", tone: "danger" },
  [MlAttemptEndReason.OOM_GPU]: { label: "GPU OOM", tone: "danger" },
  [MlAttemptEndReason.OOM_HOST]: { label: "Host OOM", tone: "danger" },
  [MlAttemptEndReason.USER_CODE_ERROR]: { label: "Code error", tone: "danger" },
  [MlAttemptEndReason.PODS_READY_TIMEOUT]: { label: "Pods ready timeout", tone: "warning" },
  [MlAttemptEndReason.REWIND]: { label: "Rewind", tone: "warning" },
  [MlAttemptEndReason.ELASTIC_RESIZE]: { label: "Elastic resize", tone: "neutral" },
  [MlAttemptEndReason.CANCELLED]: { label: "Cancelled", tone: "neutral" },
  [MlAttemptEndReason.UNKNOWN]: { label: "Unknown", tone: "neutral" },
  [MlAttemptEndReason.UNSPECIFIED]: { label: "Running", tone: "info" },
});

export const runBadgeOf = table<MlRunBadge>({
  [MlRunBadge.STRAGGLER]: { label: "Straggler", tone: "warning" },
  [MlRunBadge.STALLED]: { label: "Stalled", tone: "danger" },
  [MlRunBadge.LOSS_SPIKE]: { label: "Loss spike", tone: "warning" },
  [MlRunBadge.NAN]: { label: "NaN", tone: "danger" },
  [MlRunBadge.RESTART_STORM]: { label: "Restart storm", tone: "danger" },
  [MlRunBadge.BAD_DEVICE]: { label: "Bad device", tone: "danger" },
  [MlRunBadge.UNTRACKED]: { label: "Untracked", tone: "neutral" },
});

export const verdictOf = table<MlReleaseVerdict>({
  [MlReleaseVerdict.PASS]: { label: "Pass", tone: "success" },
  [MlReleaseVerdict.REGRESSED]: { label: "Regressed", tone: "danger" },
  [MlReleaseVerdict.NOT_PROVEN]: { label: "Not proven", tone: "warning" },
  [MlReleaseVerdict.PENDING]: { label: "Pending", tone: "info" },
});

export const componentStateOf = table<MlComponentState>({
  [MlComponentState.ADOPTED]: { label: "Adopted", tone: "success" },
  [MlComponentState.INSTALLED]: { label: "Installed by forge", tone: "success" },
  [MlComponentState.MISSING]: { label: "Missing", tone: "danger" },
  [MlComponentState.PARTIAL]: { label: "Partial", tone: "warning" },
  [MlComponentState.NOT_APPLICABLE]: { label: "Not applicable", tone: "neutral" },
});

export const findingCategoryOf = table<MlFindingCategory>({
  [MlFindingCategory.HEALTH]: { label: "Health", tone: "neutral" },
  [MlFindingCategory.WASTE]: { label: "Waste", tone: "neutral" },
  [MlFindingCategory.SCHEDULING]: { label: "Scheduling", tone: "neutral" },
  [MlFindingCategory.STARTUP]: { label: "Startup", tone: "neutral" },
  [MlFindingCategory.TRAINING_EFFICIENCY]: { label: "Training efficiency", tone: "neutral" },
  [MlFindingCategory.INFERENCE]: { label: "Inference", tone: "neutral" },
  [MlFindingCategory.COVERAGE]: { label: "Telemetry coverage", tone: "neutral" },
  [MlFindingCategory.RELIABILITY]: { label: "Reliability", tone: "neutral" },
});

export const findingStateOf = table<MlFindingState>({
  [MlFindingState.OPEN]: { label: "Open", tone: "warning" },
  [MlFindingState.ASSIGNED]: { label: "Assigned", tone: "info" },
  [MlFindingState.FIX_PROPOSED]: { label: "Fix proposed", tone: "info" },
  [MlFindingState.FIXED]: { label: "Fixed", tone: "success" },
  [MlFindingState.DISMISSED]: { label: "Dismissed", tone: "neutral" },
  [MlFindingState.SNOOZED]: { label: "Snoozed", tone: "neutral" },
});

export const impactUnitOf = table<MlImpactUnit>({
  [MlImpactUnit.USD_PER_DAY]: { label: "$/day", tone: "neutral" },
  [MlImpactUnit.GPU_HOURS_PER_DAY]: { label: "GPU-h/day", tone: "neutral" },
  [MlImpactUnit.RUN_HOURS_AT_RISK]: { label: "run-hours at risk", tone: "neutral" },
  [MlImpactUnit.LATENCY_MS]: { label: "ms p95", tone: "neutral" },
  [MlImpactUnit.BLIND_SPOT]: { label: "blind spot", tone: "neutral" },
});

export const costCategoryOf = table<MlCostCategory>({
  [MlCostCategory.PRODUCTIVE]: { label: "Productive", tone: "success" },
  [MlCostCategory.IDLE_ALLOCATED]: { label: "Idle (allocated)", tone: "warning" },
  [MlCostCategory.QUEUED_HOLDING]: { label: "Queued, holding", tone: "warning" },
  [MlCostCategory.STARTUP]: { label: "Startup", tone: "neutral" },
  [MlCostCategory.RESTART_LOST]: { label: "Lost to restarts", tone: "danger" },
  [MlCostCategory.CHECKPOINT]: { label: "Checkpointing", tone: "neutral" },
  [MlCostCategory.STALLED]: { label: "Stalled", tone: "danger" },
  [MlCostCategory.UNALLOCATED]: { label: "Unallocated", tone: "neutral" },
});

export const pendingReasonOf = table<MlPendingReason>({
  [MlPendingReason.INSUFFICIENT_QUOTA]: { label: "Insufficient quota", tone: "warning" },
  [MlPendingReason.NO_CAPACITY]: { label: "No capacity", tone: "warning" },
  [MlPendingReason.WAITING_FOR_PREEMPTION]: { label: "Waiting for preemption", tone: "neutral" },
  [MlPendingReason.ADMISSION_CHECK]: { label: "Admission check", tone: "neutral" },
  [MlPendingReason.UNTOLERATED_TAINT]: { label: "Untolerated taint", tone: "danger" },
  [MlPendingReason.NODE_PROVISIONING]: { label: "Node provisioning", tone: "neutral" },
  [MlPendingReason.TOPOLOGY]: { label: "Topology", tone: "neutral" },
  [MlPendingReason.INACTIVE_QUEUE]: { label: "Inactive queue", tone: "danger" },
});

export const collectionOf = table<MlCollection>({
  [MlCollection.COLLECTED]: { label: "Collected", tone: "success" },
  [MlCollection.NOT_COLLECTED]: { label: "Not collected", tone: "neutral" },
  [MlCollection.NOT_APPLICABLE]: { label: "Not applicable", tone: "neutral" },
});

/** An event kind as a short word, and the tone of its marker on a chart. */
export const eventKindOf = table<MlEventKind>({
  [MlEventKind.LAUNCH]: { label: "Launch", tone: "info" },
  [MlEventKind.QUEUE_ADMITTED]: { label: "Admitted", tone: "neutral" },
  [MlEventKind.QUEUE_EVICTED]: { label: "Evicted", tone: "warning" },
  [MlEventKind.PREEMPTION]: { label: "Preemption", tone: "warning" },
  [MlEventKind.SCHEDULING_FAILED]: { label: "Scheduling failed", tone: "warning" },
  [MlEventKind.IMAGE_PULL]: { label: "Image pull", tone: "neutral" },
  [MlEventKind.POD_STARTED]: { label: "Pod started", tone: "neutral" },
  [MlEventKind.RESTART]: { label: "Restart", tone: "warning" },
  [MlEventKind.CHECKPOINT]: { label: "Checkpoint", tone: "neutral" },
  [MlEventKind.XID]: { label: "Xid", tone: "danger" },
  [MlEventKind.ECC]: { label: "ECC", tone: "danger" },
  [MlEventKind.ROW_REMAP]: { label: "Row remap", tone: "warning" },
  [MlEventKind.THROTTLE]: { label: "Throttle", tone: "warning" },
  [MlEventKind.NPD_CONDITION]: { label: "NPD", tone: "warning" },
  [MlEventKind.NODE_NOT_READY]: { label: "Node NotReady", tone: "danger" },
  [MlEventKind.NCCL_TIMEOUT]: { label: "NCCL timeout", tone: "danger" },
  [MlEventKind.HANG]: { label: "Hang", tone: "danger" },
  [MlEventKind.OOM]: { label: "OOM", tone: "danger" },
  [MlEventKind.LOSS_SPIKE]: { label: "Loss spike", tone: "warning" },
  [MlEventKind.NAN]: { label: "NaN", tone: "danger" },
  [MlEventKind.EVAL]: { label: "Eval", tone: "neutral" },
  [MlEventKind.RELEASE]: { label: "Release", tone: "info" },
  [MlEventKind.ALIAS_MOVE]: { label: "Alias move", tone: "info" },
  [MlEventKind.CHANGES_RAMP]: { label: "Changes ramp", tone: "info" },
  [MlEventKind.FINDING_OPENED]: { label: "Finding", tone: "warning" },
  [MlEventKind.REWIND]: { label: "Rewind", tone: "warning" },
  [MlEventKind.PROFILE_CAPTURE]: { label: "Profile", tone: "neutral" },
  [MlEventKind.LINK_FLAP]: { label: "Link flap", tone: "warning" },
  [MlEventKind.DCGM_DIAG]: { label: "DCGM diag", tone: "neutral" },
  [MlEventKind.SCALE]: { label: "Scale", tone: "neutral" },
  [MlEventKind.CANCEL]: { label: "Cancel", tone: "neutral" },
  [MlEventKind.COMPLETED]: { label: "Completed", tone: "success" },
  [MlEventKind.STRAGGLER]: { label: "Straggler", tone: "warning" },
});

export const subjectKindOf = table<MlSubjectKind>({
  [MlSubjectKind.ORG]: { label: "org", tone: "neutral" },
  [MlSubjectKind.CLUSTER]: { label: "cluster", tone: "neutral" },
  [MlSubjectKind.POOL]: { label: "pool", tone: "neutral" },
  [MlSubjectKind.NODE]: { label: "node", tone: "neutral" },
  [MlSubjectKind.DEVICE]: { label: "GPU", tone: "neutral" },
  [MlSubjectKind.EXPERIMENT]: { label: "experiment", tone: "neutral" },
  [MlSubjectKind.RUN]: { label: "run", tone: "neutral" },
  [MlSubjectKind.PHASE]: { label: "phase", tone: "neutral" },
  [MlSubjectKind.ATTEMPT]: { label: "attempt", tone: "neutral" },
  [MlSubjectKind.RANK]: { label: "rank", tone: "neutral" },
  [MlSubjectKind.CHECKPOINT]: { label: "checkpoint", tone: "neutral" },
  [MlSubjectKind.DATASET]: { label: "dataset", tone: "neutral" },
  [MlSubjectKind.MODEL]: { label: "model", tone: "neutral" },
  [MlSubjectKind.MODEL_VERSION]: { label: "model version", tone: "neutral" },
  [MlSubjectKind.EVAL_RUN]: { label: "eval run", tone: "neutral" },
  [MlSubjectKind.INFERENCE_SERVICE]: { label: "service", tone: "neutral" },
  [MlSubjectKind.REVISION]: { label: "revision", tone: "neutral" },
  [MlSubjectKind.REPLICA]: { label: "replica", tone: "neutral" },
  [MlSubjectKind.QUEUE]: { label: "queue", tone: "neutral" },
  [MlSubjectKind.WORKLOAD]: { label: "workload", tone: "neutral" },
  [MlSubjectKind.POD]: { label: "pod", tone: "neutral" },
  [MlSubjectKind.RELEASE]: { label: "release", tone: "neutral" },
  [MlSubjectKind.FINDING]: { label: "finding", tone: "neutral" },
});

/** The CSS custom property holding a tone's colour (for charts and Gantt bars). */
export const TONE_VAR: Record<MlTone, string> = {
  danger: "--destructive",
  warning: "--warning",
  success: "--success",
  info: "--info",
  neutral: "--muted-foreground",
};

/** Tailwind classes for a tone's dot. */
export const TONE_DOT: Record<MlTone, string> = {
  danger: "bg-danger",
  warning: "bg-warning",
  success: "bg-success",
  info: "bg-info",
  neutral: "bg-ink-muted/50",
};

/** Tailwind classes for a tone's text (only where the word itself is the status). */
export const TONE_TEXT: Record<MlTone, string> = {
  danger: "text-danger-ink",
  warning: "text-warning-ink",
  success: "text-ink",
  info: "text-ink",
  neutral: "text-ink-muted",
};
