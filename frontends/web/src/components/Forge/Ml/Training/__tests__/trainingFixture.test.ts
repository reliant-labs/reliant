// Copyright (c) 2026 Reliant Labs

/**
 * The Training fixture (fixtures/shop/model/ml/training.ts) against the
 * world's story: the hero run's attempts are V3_ATTEMPTS exactly, each
 * recovery's stages add up to the gap it covers, every cost adds up, and
 * every checkpoint id an attempt, recovery or event names resolves.
 */

import { create, toBinary } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import {
  GetMlRunResponseSchema,
  ListMlAlertsResponseSchema,
  ListMlEventsResponseSchema,
  ListMlExperimentsResponseSchema,
  ListMlRunsResponseSchema,
  MlCheckpointStatus,
  MlEventKind,
  MlRunState,
  MlSubjectKind,
  type GetMlRunResponse,
  type ListMlAlertsRequest,
  type ListMlEventsRequest,
  type ListMlExperimentsRequest,
  type ListMlRunsRequest,
  type GetMlRunRequest,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { timestampMs } from "@bufbuild/protobuf/wkt";

import { recoveryGapSeconds, trainingAnswers, V3_RECOVERIES, V3_SPECS } from "@/fixtures/shop/model/ml/training";
import { ALERTS, attemptId, BAD_NODE, checkpointId, EXPERIMENTS, HOT_NODE, RUNS, SPARE_NODE, V3_ATTEMPTS } from "@/fixtures/shop/model/ml/world";

const NOW = Date.UTC(2026, 10, 26, 3, 0);
const now = () => NOW;
const ms = (t: { seconds: bigint; nanos: number } | undefined) => (t ? timestampMs(t as never) - NOW : null);

function getRun(run: string): GetMlRunResponse {
  const answer = trainingAnswers.getMlRun!({ orgId: "o", run } as GetMlRunRequest, now);
  expect(answer).toBeDefined();
  return create(GetMlRunResponseSchema, answer!);
}

describe("training fixture: the hero run", () => {
  const v3 = getRun(RUNS.v3.id);
  const run = v3.run!;

  it("answers by id and by name", () => {
    expect(getRun(RUNS.v3.name).run?.id).toBe(RUNS.v3.id);
    expect(trainingAnswers.getMlRun!({ orgId: "o", run: "nope" } as GetMlRunRequest, now)).toBeUndefined();
  });

  it("has every attempt of V3_ATTEMPTS, with its times and steps", () => {
    for (const def of V3_ATTEMPTS) {
      const a = run.phases.find((p) => p.key === def.phase)!.attempts.find((x) => x.n === def.n)!;
      expect(a, `${def.phase} a${def.n}`).toBeDefined();
      expect(ms(a.queuedAt)).toBeCloseTo(def.queuedAt, -3);
      expect(ms(a.runningAt)).toBeCloseTo(def.runningAt, -3);
      if (def.endedAt === null) expect(a.endedAt).toBeUndefined();
      else expect(ms(a.endedAt)).toBeCloseTo(def.endedAt, -3);
      expect(Number(a.startStep)).toBe(def.startStep);
      expect(Number(a.endStep)).toBe(def.endStep);
      const lost = def.end === "completed" || def.end === "running" ? 0 : def.endStep - def.lastCheckpointStep;
      expect(Number(a.stepsLost)).toBe(lost);
      expect(a.worldSize).toBe(64);
      expect(a.deviceUuids).toHaveLength(64);
      expect(a.workload?.name).toBe(`${RUNS.v3.name}-${def.phase}-a${def.n}`);
    }
  });

  it("recovers each failure in stages that add up to the gap between attempts", () => {
    for (const [key, r] of Object.entries(V3_RECOVERIES)) {
      const [phase, n] = key.split("/");
      const next = V3_SPECS.find((a) => a.phase === phase && a.n === Number(n))!;
      const prev = V3_SPECS.find((a) => a.phase === phase && a.n === Number(n) - 1)!;
      expect(recoveryGapSeconds(r), key).toBe((next.runningAt - prev.endedAt!) / 1000);
      expect(r.stages.map(([s]) => s)).toEqual(["detect", "drain", "reschedule", "image pull", "NCCL init", "checkpoint load", "first step"]);
    }
    const a3 = run.phases.find((p) => p.key === "sft")!.attempts.find((a) => a.n === 3)!;
    expect(a3.recovery?.automatic).toBe(true);
    expect(a3.recovery?.alertId).toBe(ALERTS.xidFatal);
    expect(a3.recovery?.hotSpareNodeId).toContain(SPARE_NODE);
    expect(a3.recovery?.cordonedNodeIds[0]).toContain(BAD_NODE);
    // (recovery seconds + 750 steps × 10 s) × 64 GPUs.
    expect(a3.recovery!.gpuHoursLost).toBeCloseTo(((a3.recovery!.totalSeconds + 750 * 10) * 64) / 3600, 6);
    expect(a3.recovery!.usdLost).toBeCloseTo(a3.recovery!.gpuHoursLost * 2.85, 6);
  });

  it("adds up its costs: attempts → phases → run → cost cells", () => {
    let sum = 0;
    for (const p of run.phases) {
      const attempts = p.attempts.reduce((s, a) => s + (a.cost?.gpuHours ?? 0), 0);
      expect(p.cost?.gpuHours ?? 0).toBeCloseTo(attempts, 6);
      for (const c of p.cost?.byCategory ?? []) expect(c.gpuHours, `${p.key} ${c.category}`).toBeGreaterThanOrEqual(0);
      sum += p.cost?.gpuHours ?? 0;
    }
    expect(run.cost!.gpuHours).toBeCloseTo(sum, 6);
    const cells = v3.cost!.cells;
    expect(cells.reduce((s, c) => s + c.gpuHours, 0)).toBeCloseTo(run.cost!.gpuHours, 6);
    expect(cells.reduce((s, c) => s + c.usd, 0)).toBeCloseTo(run.cost!.usd, 6);
    expect(run.cost!.byCategory.reduce((s, c) => s + c.usd, 0)).toBeCloseTo(run.cost!.usd, 6);
    expect(run.goodput).toBeGreaterThan(0.6);
    expect(run.goodput).toBeLessThan(0.95);
  });

  it("names only checkpoints it has, and resumes only from durable ones", () => {
    const byId = new Map(v3.checkpoints.map((c) => [c.id, c]));
    for (const c of v3.checkpoints) {
      expect(c.runId).toBe(RUNS.v3.id);
      expect(c.id).toBe(checkpointId(RUNS.v3.id, c.phaseKey, Number(c.step)));
    }
    for (const p of run.phases)
      for (const a of p.attempts) {
        for (const id of [a.resumedFromCheckpointId, a.recovery?.resumedFromCheckpointId].filter(Boolean)) {
          expect(byId.get(id!)?.status, id).toBe(MlCheckpointStatus.DURABLE);
        }
      }
    expect(byId.get(checkpointId(RUNS.v3.id, "sft", 32750))).toBeUndefined();
    expect(byId.get(checkpointId(RUNS.v3.id, "sft", 12400))).toBeUndefined();
    expect(byId.get(checkpointId(RUNS.v3.id, "pretrain", 6400))?.status).toBe(MlCheckpointStatus.PARTIAL);
    expect(byId.get(checkpointId(RUNS.v3.id, "sft", 40000))?.evalRunIds).toHaveLength(2);
  });

  it("has 64 ranks on sft attempt 3, rank 37 the straggler on the hot node", () => {
    const a3 = run.phases.find((p) => p.key === "sft")!.attempts.find((a) => a.n === 3)!;
    expect(a3.ranks).toHaveLength(64);
    const r37 = a3.ranks.find((r) => r.rank === 37)!;
    expect(r37.straggler).toBe(true);
    expect(r37.nodeId).toContain(HOT_NODE);
    expect([r37.dp, r37.tp, r37.localRank]).toEqual([4, 5, 5]);
    const other = a3.ranks.find((r) => r.rank === 3)!;
    expect(r37.computeMsP50 / other.computeMsP50).toBeCloseTo(1.18, 1);
    expect(other.commExposedMsP50).toBeGreaterThan(r37.commExposedMsP50);
    expect(a3.ranks.some((r) => r.nodeId.includes(BAD_NODE))).toBe(false);
  });

  it("has a step breakdown that sums to the step", () => {
    const share = v3.stepBreakdown.reduce((s, x) => s + x.share, 0);
    expect(share).toBeCloseTo(1, 6);
  });
});

describe("training fixture: the wire", () => {
  it("encodes every run's detail and the list to binary (ints are ints)", () => {
    const runs = create(ListMlRunsResponseSchema, trainingAnswers.listMlRuns!({ orgId: "o" } as ListMlRunsRequest, now)!);
    expect(() => toBinary(ListMlRunsResponseSchema, runs)).not.toThrow();
    for (const r of runs.runs) expect(() => toBinary(GetMlRunResponseSchema, getRun(r.id)), r.name).not.toThrow();
    const ev = create(ListMlEventsResponseSchema, trainingAnswers.listMlEvents!({ orgId: "o", subject: { kind: MlSubjectKind.RUN, id: RUNS.v3.id, label: "" }, kinds: [] } as unknown as ListMlEventsRequest, now)!);
    expect(() => toBinary(ListMlEventsResponseSchema, ev)).not.toThrow();
  });
});

describe("training fixture: lists, events, alerts", () => {
  it("lists every run and both experiments", () => {
    const runs = create(ListMlRunsResponseSchema, trainingAnswers.listMlRuns!({ orgId: "o" } as ListMlRunsRequest, now)!).runs;
    expect(runs.map((r) => r.id).sort()).toEqual(Object.values(RUNS).map((r) => r.id).sort());
    expect(runs.every((r) => r.phases.every((p) => p.attempts.every((a) => a.ranks.length === 0)))).toBe(true);
    const exps = create(ListMlExperimentsResponseSchema, trainingAnswers.listMlExperiments!({ orgId: "o" } as ListMlExperimentsRequest, now)!).experiments;
    expect(exps.map((e) => e.id)).toEqual([EXPERIMENTS.support.id, EXPERIMENTS.reranker.id]);
    expect(exps[0].bestRunId).toBe(RUNS.v3.id);
    expect(runs.find((r) => r.id === RUNS.dpo.id)?.state).toBe(MlRunState.QUEUED);
  });

  const events = (kind: MlSubjectKind, id: string) =>
    create(ListMlEventsResponseSchema, trainingAnswers.listMlEvents!({ orgId: "o", subject: { kind, id, label: "" }, kinds: [] } as unknown as ListMlEventsRequest, now) ?? {}).events;

  it("scopes events to the subject, newest first", () => {
    const a2 = events(MlSubjectKind.ATTEMPT, attemptId(RUNS.v3.id, "sft", 2));
    expect(a2.some((e) => e.kind === MlEventKind.XID)).toBe(true);
    expect(a2.every((e) => e.kind !== MlEventKind.STRAGGLER)).toBe(true);
    const all = events(MlSubjectKind.RUN, RUNS.v3.id);
    const times = all.map((e) => timestampMs(e.at!));
    expect([...times].sort((a, b) => b - a)).toEqual(times);
    expect(events(MlSubjectKind.CHECKPOINT, checkpointId(RUNS.v3.id, "sft", 40000)).some((e) => e.kind === MlEventKind.EVAL)).toBe(true);
    expect(events(MlSubjectKind.NODE, "x")).toHaveLength(0);
    expect(events(MlSubjectKind.ORG, "").length).toBeGreaterThan(5);
  });

  it("lists the run's alert rules, and only training rules for the org", () => {
    const alerts = (kind: MlSubjectKind, id: string) => create(ListMlAlertsResponseSchema, trainingAnswers.listMlAlerts!({ orgId: "o", subject: { kind, id, label: "" } } as unknown as ListMlAlertsRequest, now) ?? {}).alerts;
    expect(alerts(MlSubjectKind.RUN, RUNS.v3.id).map((a) => a.id)).toContain(ALERTS.xidFatal);
    const org = alerts(MlSubjectKind.ORG, "").map((a) => a.id);
    expect(org).not.toContain(ALERTS.xidFatal);
    expect(org).not.toContain(ALERTS.gpuThermal);
    expect(org).toContain(ALERTS.straggler);
    expect(alerts(MlSubjectKind.RUN, RUNS.v3.id).find((a) => a.id === ALERTS.lossSpike)?.autoActionEnabled).toBe(false);
  });
});
