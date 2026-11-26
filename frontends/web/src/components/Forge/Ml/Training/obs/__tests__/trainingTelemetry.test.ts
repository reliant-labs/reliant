// Copyright (c) 2026 Reliant Labs

/**
 * The training telemetry fixtures (fixtures/shop/model/ml/trainingTelemetry.ts):
 * series continuous across attempts and sized for a chart, every span joined
 * to its run, rank, node and GPU, and every flamegraph a speedscope file the
 * Observability flamegraph companion can read.
 */

import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { topFunctions } from "@/components/Forge/Observability/ProfilesView";
import {
  GetMlProfileRequestSchema,
  GetMlTraceRequestSchema,
  ListMlLogsRequestSchema,
  ListMlProfilesRequestSchema,
  ListMlTracesRequestSchema,
  MlAxis,
  MlCollection,
  MlSubjectKind,
  QueryMlHistogramsRequestSchema,
  QueryMlSeriesRequestSchema,
  type MlSubject,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { trainingTelemetryAnswers as T, TRAINING_TELEMETRY_IDS } from "@/fixtures/shop/model/ml/trainingTelemetry";
import { FINDINGS, PROFILES, RUNS, STRAGGLER_RANK, TRACES, V3_ATTEMPTS } from "@/fixtures/shop/model/ml/world";
import { v3TrainLoss } from "@/fixtures/shop/model/ml/curves";

const NOW = Date.UTC(2026, 10, 26, 3, 0);
const now = () => NOW;
const v3 = RUNS.v3.id;

const subject = (kind: MlSubjectKind, id: string): Pick<MlSubject, "kind" | "id" | "label"> => ({ kind, id, label: id });
const series = (s: ReturnType<typeof subject>, keys: string[], extra: { axis?: MlAxis; groupBy?: string; maxPoints?: number } = {}) =>
  T.queryMlSeries!(create(QueryMlSeriesRequestSchema, { orgId: "o", subject: s, keys, axis: extra.axis ?? MlAxis.STEP, groupBy: extra.groupBy, maxPoints: extra.maxPoints }), now)!.series!;

describe("training series", () => {
  it("a phase answers one loss series per attempt, continuous across attempts, ~200–400 points each overall", () => {
    const out = series(subject(MlSubjectKind.PHASE, `${v3}/sft`), ["train/loss"]);
    expect(out.map((s) => s.attempt)).toEqual([1, 2, 3]);
    const sft = V3_ATTEMPTS.filter((a) => a.phase === "sft");
    out.forEach((s, i) => {
      expect(s.x![0]).toBe(sft[i].startStep);
      expect(s.x![s.x!.length - 1]).toBe(sft[i].endStep);
      expect(s.subject?.id).toBe(`${v3}/sft/${i + 1}`);
    });
    // a2 resumes at a1's last checkpoint (12,000) — before a1 ended (12,400): the redone steps exist on both.
    expect(out[1].x![0]).toBeLessThan(out[0].x![out[0].x!.length - 1]);
    const total = out.reduce((n, s) => n + s.x!.length, 0);
    expect(total).toBeGreaterThan(200);
    expect(total).toBeLessThan(500);
    // The numbers are curves.ts's.
    expect(out[2].y![out[2].y!.length - 1]).toBeCloseTo(v3TrainLoss("sft", 41_200), 6);
  });

  it("the run reads its current phase; an attempt only itself; val loss every 500", () => {
    expect(series(subject(MlSubjectKind.RUN, v3), ["train/loss"])).toHaveLength(3);
    const a3 = series(subject(MlSubjectKind.ATTEMPT, `${v3}/sft/3`), ["val/loss"]);
    expect(a3).toHaveLength(1);
    expect(a3[0].x!.every((x) => x % 500 === 0)).toBe(true);
  });

  it("step time has p50 and p95, memory p5/p50/p95; evals are one line of the periodic checkpoints", () => {
    const a3 = subject(MlSubjectKind.ATTEMPT, `${v3}/sft/3`);
    expect(series(a3, ["timers/step_ms"]).map((s) => s.stat)).toEqual(["p50", "p95"]);
    expect(series(a3, ["memory/peak_gb"]).map((s) => s.stat)).toEqual(["p5", "p50", "p95"]);
    const evals = series(subject(MlSubjectKind.PHASE, `${v3}/sft`), ["eval/*"]);
    expect(evals).toHaveLength(8);
    expect(evals[0].x).toEqual(Array.from({ length: 20 }, (_, i) => (i + 1) * 2000));
    // eval-periodic answers the same scores (they are of SFT's checkpoints).
    const periodic = series(subject(MlSubjectKind.PHASE, `${v3}/eval-periodic`), ["eval/*"]);
    expect(periodic.map((s) => s.y)).toEqual(evals.map((s) => s.y));
  });

  it("not applicable and not collected are never zeros", () => {
    const p = subject(MlSubjectKind.PHASE, `${v3}/sft`);
    expect(series(p, ["stability/loss_scale"])[0].collection).toBe(MlCollection.NOT_APPLICABLE);
    expect(series(p, ["moe/*"]).every((s) => s.collection === MlCollection.NOT_APPLICABLE)).toBe(true);
    const rl = series(subject(MlSubjectKind.PHASE, `${v3}/rl`), ["rl/*"]);
    expect(rl.every((s) => s.collection === MlCollection.NOT_COLLECTED && s.missingSource === "phase has not started")).toBe(true);
    const reranker = series(subject(MlSubjectKind.RUN, RUNS.reranker.id), ["train/loss", "system/gpu_util"]);
    expect(reranker[0]).toMatchObject({ collection: MlCollection.NOT_COLLECTED, fixFindingId: FINDINGS.trainingUntracked });
    expect(reranker[1].collection).toBe(MlCollection.COLLECTED);
  });

  it("system series group by rank (64) and node (8); rank 37 runs hot and slowed", () => {
    const a3 = subject(MlSubjectKind.ATTEMPT, `${v3}/sft/3`);
    const byRank = series(a3, ["system/sm_active"], { axis: MlAxis.TIME, groupBy: "rank", maxPoints: 48 });
    expect(byRank).toHaveLength(64);
    expect(byRank[37].subject?.id).toBe(`${v3}/sft/3/37`);
    const last = (i: number) => byRank[i].y![byRank[i].y!.length - 1];
    // The throttled GPU's SMs are active less of the time (training.ts v3Ranks: 0.69 against 0.77).
    expect(last(STRAGGLER_RANK)).toBeLessThan(last(0) - 0.05);
    expect(series(a3, ["system/gpu_temp_c"], { axis: MlAxis.TIME, groupBy: "node" })).toHaveLength(8);
    const slow = series(subject(MlSubjectKind.RANK, `${v3}/sft/3/37`), ["system/clock_event_hw_slowdown"], { axis: MlAxis.TIME });
    expect(slow[0].y![slow[0].y!.length - 1]).toBeGreaterThan(0.5);
  });

  it("does not answer for subjects it does not own", () => {
    expect(T.queryMlSeries!(create(QueryMlSeriesRequestSchema, { orgId: "o", subject: subject(MlSubjectKind.NODE, "c/n"), keys: ["x"] }), now)).toBeUndefined();
  });
});

describe("histograms and logs", () => {
  it("attempt 3's step time compares against attempt 2", () => {
    const h = T.queryMlHistograms!(create(QueryMlHistogramsRequestSchema, { orgId: "o", subject: subject(MlSubjectKind.ATTEMPT, `${v3}/sft/3`), keys: ["train.step_time"] }), now)!.histograms![0];
    expect(h.compareLabel).toBe("attempt 2");
    expect(h.p95!).toBeGreaterThan(h.p50!);
  });

  it("logs are rank-tagged, carry the training id, and highlight the failures", () => {
    const { lines } = T.listMlLogs!(create(ListMlLogsRequestSchema, { orgId: "o", subject: subject(MlSubjectKind.PHASE, `${v3}/sft`) }), now)!;
    expect(lines!.length).toBeGreaterThan(100);
    expect(lines!.every((l) => l.runId === v3 && l.phaseKey === "sft" && (l.attempt ?? 0) > 0)).toBe(true);
    expect(lines!.some((l) => l.message?.includes("Watchdog caught collective operation timeout"))).toBe(true);
    expect(lines!.some((l) => l.message?.includes("NVRM: Xid") && l.highlight === "xid")).toBe(true);
    expect(lines!.some((l) => l.traceId === TRACES.slowStep)).toBe(true);
    const errors = T.listMlLogs!(create(ListMlLogsRequestSchema, { orgId: "o", subject: subject(MlSubjectKind.PHASE, `${v3}/sft`), minLevel: "error" }), now)!.lines!;
    expect(errors.every((l) => l.level === "error")).toBe(true);
    const v1 = T.listMlLogs!(create(ListMlLogsRequestSchema, { orgId: "o", subject: subject(MlSubjectKind.RUN, RUNS.v1.id), highlightsOnly: true }), now)!.lines!;
    expect(v1.some((l) => l.message === "Bus error (core dumped)")).toBe(true);
  });
});

describe("traces", () => {
  const ids = [TRACES.slowStep, TRACES.normalStep, TRACES.checkpoint, TRACES.dataPipeline, TRACES.podStart, TRAINING_TELEMETRY_IDS.hangTrace, TRAINING_TELEMETRY_IDS.xidTrace];

  it.each(ids)("every span of %s carries the run, phase and attempt; every rank span its node, GPU and pod", (traceId) => {
    const t = T.getMlTrace!(create(GetMlTraceRequestSchema, { orgId: "o", traceId }), now)!;
    expect(t.spans!.length).toBeGreaterThan(3);
    const ids = new Set(t.spans!.map((s) => s.spanId));
    for (const s of t.spans!) {
      expect(s.runId).toBe(v3);
      expect(s.phaseKey).toBe("sft");
      expect(s.attempt).toBeGreaterThan(0);
      if (s.parentSpanId) expect(ids.has(s.parentSpanId)).toBe(true);
      if ((s.rank ?? -1) >= 0) {
        expect(s.nodeId).toMatch(/\//);
        expect(s.deviceUuid).toMatch(/^GPU-/);
        expect(s.pod).toBeTruthy();
      }
    }
  });

  it("the slow step shows rank 37 late and the others waiting in all_reduce", () => {
    const t = T.getMlTrace!(create(GetMlTraceRequestSchema, { orgId: "o", traceId: TRACES.slowStep }), now)!;
    const ar = t.spans!.filter((s) => s.name === "nccl:all_reduce");
    const of = (r: number) => ar.find((s) => s.rank === r)!.durationMs!;
    expect(of(0)).toBeGreaterThan(of(STRAGGLER_RANK) * 10);
    expect(t.spans!.some((s) => s.name === "dcgm.sample" && s.rank === STRAGGLER_RANK && s.attributes?.DCGM_FI_DEV_CLOCK_EVENT_REASONS?.includes("HW_SLOWDOWN"))).toBe(true);
  });

  it("the attempt lists its slow steps by duration; an unknown id is not ours", () => {
    const { traces } = T.listMlTraces!(create(ListMlTracesRequestSchema, { orgId: "o", subject: subject(MlSubjectKind.ATTEMPT, `${v3}/sft/3`) }), now)!;
    expect(traces!.length).toBeGreaterThanOrEqual(10);
    const d = traces!.map((t) => t.durationMs!);
    expect([...d].sort((a, b) => b - a)).toEqual(d);
    expect(T.getMlTrace!(create(GetMlTraceRequestSchema, { orgId: "o", traceId: TRACES.slowRequest }), now)).toBeUndefined();
    expect(T.listMlTraces!(create(ListMlTracesRequestSchema, { orgId: "o", subject: subject(MlSubjectKind.RUN, RUNS.reranker.id) }), now)!.missingFindingId).toBe(FINDINGS.trainingUntracked);
  });
});

describe("profiles", () => {
  it("lists the attempt's captures; the flight recorder belongs to attempt 1", () => {
    const list = (id: string) => T.listMlProfiles!(create(ListMlProfilesRequestSchema, { orgId: "o", subject: subject(MlSubjectKind.ATTEMPT, id) }), now)!.profiles!.map((p) => p.id);
    expect(list(`${v3}/sft/3`)).toEqual(expect.arrayContaining([PROFILES.kineto, PROFILES.nsight, PROFILES.memory, PROFILES.cpu, PROFILES.gpu]));
    expect(list(`${v3}/sft/1`)).toEqual([PROFILES.flightRecorder]);
  });

  it.each([PROFILES.kineto, PROFILES.cpu, PROFILES.gpu, PROFILES.memory])("%s's flamegraph parses and has top functions", (id) => {
    const p = T.getMlProfile!(create(GetMlProfileRequestSchema, { orgId: "o", profileId: id }), now)!.profile!;
    expect(() => JSON.parse(p.flamegraphJson!)).not.toThrow();
    expect(topFunctions(p.flamegraphJson!).rows.length).toBeGreaterThan(3);
  });

  it("the flight recorder dump marks rank 37 mismatched", () => {
    const p = T.getMlProfile!(create(GetMlProfileRequestSchema, { orgId: "o", profileId: PROFILES.flightRecorder }), now)!.profile!;
    expect(p.collectives).toHaveLength(64);
    expect(p.collectives!.filter((c) => c.mismatched).map((c) => c.rank)).toEqual([STRAGGLER_RANK]);
  });
});
