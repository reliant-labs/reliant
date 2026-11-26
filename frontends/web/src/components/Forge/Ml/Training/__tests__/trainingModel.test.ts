// Copyright (c) 2026 Reliant Labs

import "@/components/Forge/Ml/__tests__/jsdomShims";

import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";

import {
  GetMlRunResponseSchema,
  ListMlExperimentsResponseSchema,
  ListMlRunsResponseSchema,
  MlCostCategory,
  MlEventKind,
  MlEventSchema,
  MlSubjectKind,
  type GetMlRunRequest,
  type ListMlExperimentsRequest,
  type ListMlRunsRequest,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { trainingAnswers } from "@/fixtures/shop/model/ml/training";
import { EXPERIMENTS, RUNS } from "@/fixtures/shop/model/ml/world";

import { mergeSpans, timelineRows } from "../RunTimeline";
import { runScopeOf } from "../runScope";
import {
  checkpointFacts,
  costSegments,
  costTable,
  evalCurves,
  failureCards,
  filterRuns,
  groupRuns,
  listKpis,
  lostToFailures,
  meshGrid,
  mergeEvents,
  miniGantt,
  scopeCheckpoints,
  scopeRanks,
} from "../trainingModel";

const NOW = Date.UTC(2026, 10, 26, 3, 0);
const now = () => NOW;
const detail = create(GetMlRunResponseSchema, trainingAnswers.getMlRun!({ orgId: "o", run: RUNS.v3.name } as GetMlRunRequest, now)!);
const run = detail.run!;
const runs = create(ListMlRunsResponseSchema, trainingAnswers.listMlRuns!({ orgId: "o" } as ListMlRunsRequest, now)!).runs;
const experiments = create(ListMlExperimentsResponseSchema, trainingAnswers.listMlExperiments!({ orgId: "o" } as ListMlExperimentsRequest, now)!).experiments;

describe("the list", () => {
  it("counts active runs, GPUs held now and failures in the week", () => {
    const k = listKpis(runs, NOW);
    expect(k.activeRuns).toBe(2); // v3 (degraded) and sweep trial 3
    expect(k.queuedRuns).toBe(1); // the DPO ablation
    expect(k.gpusInUse).toBe(64 + 8 + 8); // v3 sft a3, the sweep trial, and eval-periodic holding its A100s
    expect(k.failures7d).toBe(3); // v3: preemption, NCCL timeout, Xid (the cancelled trial is not a failure)
    expect(k.restarts7d).toBe(3);
    // 24h: v3 sft a3 ran the whole day on 64 H100s; the sweep and eval-periodic add a little.
    expect(k.gpuHours24h).toBeGreaterThan(24 * 64);
    expect(k.usd24h).toBeGreaterThan(24 * 64 * 2.85);
  });

  it("filters by experiment and searches names, tags and params", () => {
    expect(filterRuns(runs, EXPERIMENTS.reranker.id, undefined).map((r) => r.id)).toEqual([RUNS.reranker.id]);
    expect(filterRuns(runs, undefined, "lr=8e-5").map((r) => r.id)).toEqual([RUNS.sweep4.id]);
    expect(filterRuns(runs, undefined, "maya sweep").length).toBe(4);
  });

  it("groups by experiment in the experiments' order", () => {
    const g = groupRuns(runs, experiments, "experiment");
    expect(g.map((x) => x.label)).toEqual(["support-assistant-8b", "reranker"]);
    expect(groupRuns(runs, experiments, undefined)).toHaveLength(1);
  });

  it("draws a run's attempts across its lifetime, failures marked", () => {
    const segs = miniGantt(run, NOW);
    expect(segs).toHaveLength(6); // 2 pretrain + 3 sft + 1 eval-periodic
    expect(segs.filter((s) => s.failed)).toHaveLength(3);
    expect(segs.find((s) => s.phaseKey === "sft" && s.n === 3)?.running).toBe(true);
    for (const s of segs) {
      expect(s.left).toBeGreaterThanOrEqual(0);
      expect(s.left + s.width).toBeLessThanOrEqual(1.0001);
    }
  });
});

describe("the run detail", () => {
  it("has a failure card per failed attempt, its stages and what it cost", () => {
    const cards = failureCards(run);
    expect(cards.map((c) => `${c.phaseKey}/${c.failed.n}→${c.next.n}`)).toEqual(["pretrain/1→2", "sft/1→2", "sft/2→3"]);
    const xid = cards[2];
    expect(xid.stepsLost).toBe(750);
    expect(xid.stages.map((s) => s.stage)[0]).toBe("detect");
    expect(xid.stages.reduce((s, x) => s + x.seconds, 0)).toBeCloseTo(xid.totalSeconds, 6);
    const lost = lostToFailures(run);
    expect(lost.count).toBe(3);
    expect(lost.gpuHours).toBeCloseTo(cards.reduce((s, c) => s + c.gpuHoursLost, 0), 6);
  });

  it("splits cost into ordered categories that sum to the whole", () => {
    const segs = costSegments(run.cost);
    expect(segs[0].category).toBe(MlCostCategory.PRODUCTIVE);
    expect(segs.reduce((s, x) => s + x.share, 0)).toBeCloseTo(1, 6);
    expect(segs[0].share).toBeCloseTo(run.goodput, 6);
    const t = costTable(detail.cost!.cells, run.phases.map((p) => p.key));
    expect(t.rows.map((r) => r.key)).toEqual(["pretrain/1", "pretrain/2", "sft/1", "sft/2", "sft/3", "eval-periodic/1"]);
    expect(t.total.usd).toBeCloseTo(run.cost!.usd, 6);
  });

  it("knows the last durable checkpoint, the steps at risk and the cadence", () => {
    const sft = run.phases.find((p) => p.key === "sft")!;
    const f = checkpointFacts(detail.checkpoints, sft);
    expect(f.lastDurableStep).toBe(41000);
    expect(f.atRiskSteps).toBe(200);
    expect(f.cadenceSteps).toBe(1000);
    const a2 = sft.attempts.find((a) => a.n === 2)!;
    expect(scopeCheckpoints(detail.checkpoints, sft, a2, true).every((c) => c.attempt === 2 && c.phaseKey === "sft")).toBe(true);
  });

  it("draws eval curves from the evaluated checkpoints, oldest first", () => {
    const curves = evalCurves(detail.checkpoints);
    const mmlu = curves.find((c) => c.task === "mmlu_pro")!;
    expect(mmlu.x[0]).toBe(2000);
    expect(mmlu.x[mmlu.x.length - 1]).toBe(40000);
    expect(mmlu.y[mmlu.y.length - 1]).toBeGreaterThan(mmlu.y[0]);
  });

  it("lays the mesh out dp-major with rank 37 the straggler", () => {
    const sft = run.phases.find((p) => p.key === "sft")!;
    const { attempt, ranks } = scopeRanks(sft, null);
    expect(attempt?.n).toBe(3);
    const grid = meshGrid(8, 8, ranks);
    expect(grid[4][5].rank).toBe(37);
    expect(grid[4][5].straggler).toBe(true);
    expect(grid[4][5].computeRatio!).toBeGreaterThan(1.15);
    expect(grid[0][0].computeRatio!).toBeLessThan(1.02);
  });

  it("scopes to a phase and an attempt from the URL", () => {
    const s = runScopeOf(detail, "sft", 2, undefined);
    expect(s.subject.kind).toBe(MlSubjectKind.ATTEMPT);
    expect(s.subject.id).toBe(`${RUNS.v3.id}/sft/2`);
    expect(runScopeOf(detail, undefined, undefined, undefined).subject.kind).toBe(MlSubjectKind.RUN);
  });
});

describe("the timeline", () => {
  it("folds checkpoint saves into the run around them", () => {
    const a3 = run.phases.find((p) => p.key === "sft")!.attempts.find((a) => a.n === 3)!;
    const merged = mergeSpans(a3.spans);
    expect(merged.some((s) => s.state === "checkpointing")).toBe(false);
    expect(merged.map((s) => s.state)).toEqual(["recovering", "queued", "starting", "running", "stalled"]);
    expect(merged[merged.length - 1].end).toBeNull();
  });

  it("has a row per phase; on the step axis, the steps redone after each failure", () => {
    const time = timelineRows(run, detail.checkpoints, "time", NOW, { phase: null, attempt: null });
    expect(time.rows.map((r) => r.id)).toEqual(["pretrain", "sft", "eval-periodic", "rl"]);
    const step = timelineRows(run, detail.checkpoints, "step", NOW, { phase: null, attempt: null });
    const sft = step.rows.find((r) => r.id === "sft")!;
    const redone = sft.spans.filter((s) => s.state === "recovering");
    expect(redone.map((s) => [s.start, s.end])).toEqual([
      [12000, 12400],
      [32000, 32750],
    ]);
    expect(step.rows.some((r) => r.id === "sft-data")).toBe(true);
    expect(step.rows.find((r) => r.id === "eval-periodic")!.marks!.filter((m) => m.shape === "diamond")).toHaveLength(20);
  });
});

describe("events", () => {
  it("keeps one of the same moment reported by two domains", () => {
    const at = timestampFromMs(NOW - 3600_000);
    const subject = { kind: MlSubjectKind.DEVICE, id: "GPU-x", label: "" };
    const a = create(MlEventSchema, { id: "a", at, kind: MlEventKind.XID, subject });
    const b = create(MlEventSchema, { id: "b", at: timestampFromMs(NOW - 3600_000 + 20_000), kind: MlEventKind.XID, subject });
    const c = create(MlEventSchema, { id: "c", at: timestampFromMs(NOW), kind: MlEventKind.CHECKPOINT, subject });
    expect(mergeEvents([a, b, c]).map((e) => e.id)).toEqual(["c", "b"]);
  });
});
