// Copyright (c) 2026 Reliant Labs

/**
 * The ML telemetry vocabulary: every subject kind a telemetry read names
 * round-trips through the attributes its telemetry carries (as QueryScope
 * filters and as alert labels), metric and event names are what the
 * conventions say, and an event query names exactly its kinds.
 */

import { describe, expect, it } from "vitest";

import { MlEventKind, MlSubjectKind } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import {
  alertMatches,
  eventKindOfName,
  eventKindsOfQuery,
  eventQuery,
  filterKey,
  mlEventName,
  mlMetricName,
  subjectAlertLabels,
  subjectFilters,
  subjectOfAlertLabels,
  subjectOfFilters,
  type SubjectRef,
} from "../telemetry";

const SUBJECTS: SubjectRef[] = [
  { kind: MlSubjectKind.RUN, id: "run_v3" },
  { kind: MlSubjectKind.PHASE, id: "run_v3/sft" },
  { kind: MlSubjectKind.ATTEMPT, id: "run_v3/sft/3" },
  { kind: MlSubjectKind.RANK, id: "run_v3/sft/3/37" },
  { kind: MlSubjectKind.CHECKPOINT, id: "run_v3/sft/40000" },
  { kind: MlSubjectKind.NODE, id: "gke-train/gke-train-h100-c8dw" },
  { kind: MlSubjectKind.POOL, id: "gke-train/h100-train" },
  { kind: MlSubjectKind.QUEUE, id: "gke-train/team-research" },
  { kind: MlSubjectKind.DEVICE, id: "GPU-0a1b2c3d-0000-1111-2222-333344445555" },
  { kind: MlSubjectKind.INFERENCE_SERVICE, id: "support-assistant-prod" },
  { kind: MlSubjectKind.REVISION, id: "support-assistant-prod/v1.8.3+fp8" },
  { kind: MlSubjectKind.REPLICA, id: "support-assistant-prod/support-assistant-prod-vllm-1" },
  { kind: MlSubjectKind.WORKLOAD, id: "eval-release-gate-v7" },
  { kind: MlSubjectKind.MODEL_VERSION, id: "support-assistant/v3" },
  { kind: MlSubjectKind.MODEL, id: "support-assistant" },
  { kind: MlSubjectKind.EXPERIMENT, id: "exp_support" },
  { kind: MlSubjectKind.CLUSTER, id: "gke-train" },
];

describe("ML subjects as telemetry attributes", () => {
  it.each(SUBJECTS.map((s) => [MlSubjectKind[s.kind], s] as const))("%s round-trips through filters and alert labels", (_, s) => {
    for (const signal of ["metrics", "logs", "traces"] as const) expect(subjectOfFilters(subjectFilters(s, signal))).toEqual(s);
    expect(subjectOfAlertLabels(subjectAlertLabels(s))).toEqual(s);
  });

  it("filters by the conventional keys: allowlisted bare, the GPU as DCGM's metric label, the rest as resource attributes", () => {
    expect(subjectFilters({ kind: MlSubjectKind.RANK, id: "r/sft/3/37" }, "metrics").map((f) => f.key)).toEqual(["resource.attr.ml.run.id", "resource.attr.ml.phase", "resource.attr.ml.attempt", "resource.attr.ml.rank"]);
    expect(subjectFilters({ kind: MlSubjectKind.NODE, id: "c/n1" }, "logs")).toEqual([
      { key: "k8s.cluster.name", op: "eq", value: "c" },
      { key: "k8s.node.name", op: "eq", value: "n1" },
    ]);
    expect(filterKey("UUID", "metrics")).toBe("metric.attr.UUID");
    expect(filterKey("UUID", "logs")).toBe("log.attr.UUID");
    expect(subjectFilters({ kind: MlSubjectKind.ORG, id: "" }, "metrics")).toEqual([]);
    expect(subjectOfFilters([])).toBeUndefined();
  });

  it("matches an alert on the subject or under it; the org matches every ML alert and nothing else", () => {
    const rankAlert = subjectAlertLabels({ kind: MlSubjectKind.RANK, id: "run_v3/sft/3/37" });
    expect(alertMatches(rankAlert, { kind: MlSubjectKind.RUN, id: "run_v3" })).toBe(true);
    expect(alertMatches(rankAlert, { kind: MlSubjectKind.RUN, id: "run_v2" })).toBe(false);
    expect(alertMatches(rankAlert, { kind: MlSubjectKind.ORG, id: "" })).toBe(true);
    expect(alertMatches({ alertname: "OrdersErrorRateHigh", service: "api" }, { kind: MlSubjectKind.ORG, id: "" })).toBe(false);
  });
});

describe("metric and event names", () => {
  it("names a key's metric by the ml.* convention; a family by its prefix", () => {
    expect(mlMetricName("train/loss")).toBe("ml.train.loss");
    expect(mlMetricName("gpu.sm_active")).toBe("ml.gpu.sm_active");
    expect(mlMetricName("eval/*")).toBe("ml.eval");
  });

  it("names an event kind ml.<kind>, and an event query its kinds", () => {
    expect(mlEventName(MlEventKind.NCCL_TIMEOUT)).toBe("ml.nccl_timeout");
    expect(eventKindOfName("ml.nccl_timeout")).toBe(MlEventKind.NCCL_TIMEOUT);
    expect(eventKindOfName("k8s.pod.started")).toBe(MlEventKind.UNSPECIFIED);
    expect(eventKindsOfQuery(eventQuery())).toEqual([]);
    expect(eventKindsOfQuery(eventQuery([MlEventKind.XID, MlEventKind.CHECKPOINT]))).toEqual([MlEventKind.XID, MlEventKind.CHECKPOINT]);
    expect(eventKindsOfQuery("timeout")).toBeNull();
  });
});
