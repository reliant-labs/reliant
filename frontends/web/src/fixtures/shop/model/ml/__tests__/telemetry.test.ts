// Copyright (c) 2026 Reliant Labs

/**
 * The ML world served through ObservabilityService (../telemetry.ts): a
 * read scoped by a subject's attributes answers that subject's metrics,
 * logs, traces and flamegraph; a read naming no ML subject is shop's.
 */

import { describe, expect, it } from "vitest";

import { MlCollection, MlEventKind, MlSubjectKind } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { PROFILES, RUNS, STRAGGLER_RANK, TRACES } from "../world";
import { mlObs } from "./mlObs";

const NOW = Date.UTC(2026, 10, 26, 3, 0);
const v3 = RUNS.v3.id;
const obs = mlObs(NOW);
const attempt3 = { kind: MlSubjectKind.ATTEMPT, id: `${v3}/sft/3` };

describe("ML telemetry over ObservabilityService", () => {
  it("answers a phase's loss per attempt on the time axis, and a missing family as absent, never zeros", async () => {
    const loss = await obs.series({ kind: MlSubjectKind.PHASE, id: `${v3}/sft` }, ["train/loss"], { groupBy: "attempt" });
    expect(loss.map((s) => s.group)).toEqual(["attempt 1", "attempt 2", "attempt 3"]);
    expect(loss.map((s) => s.attempt)).toEqual([1, 2, 3]);
    // Time axis: epoch seconds, inside the window.
    expect(loss[2].x[loss[2].x.length - 1]).toBeLessThanOrEqual(NOW / 1000);
    expect(loss[0].x[0]).toBeLessThan(loss[2].x[0]);
    const moe = await obs.series({ kind: MlSubjectKind.PHASE, id: `${v3}/sft` }, ["moe/*"]);
    expect(moe[0].collection).not.toBe(MlCollection.COLLECTED);
  });

  it("answers a family split by key, and a rank-grouped heatmap's 64 rows", async () => {
    const evals = await obs.series({ kind: MlSubjectKind.PHASE, id: `${v3}/sft` }, ["eval/*"]);
    expect(evals).toHaveLength(8);
    expect(evals.every((s) => s.key.startsWith("eval/"))).toBe(true);
    const byRank = await obs.series(attempt3, ["system/sm_active"], { groupBy: "rank" });
    expect(byRank).toHaveLength(64);
    expect(byRank.map((s) => s.group)).toContain(`rank ${STRAGGLER_RANK}`);
  });

  it("reads percentiles when asked: step time p50 under p95", async () => {
    const [p50, p95] = await obs.series(attempt3, ["timers/step_ms"], { stats: ["p50", "p95"] });
    expect([p50.stat, p95.stat]).toEqual(["p50", "p95"]);
    expect(p95.y[p95.y.length - 1]).toBeGreaterThan(p50.y[p50.y.length - 1]);
  });

  it("serves a run's pod logs with its ml.* attributes, and its events as event.name records", async () => {
    const logs = await obs.logs({ kind: MlSubjectKind.PHASE, id: `${v3}/sft` });
    expect(logs.length).toBeGreaterThan(50);
    expect(logs.every((l) => l.resource["ml.run.id"] === v3 && l.resource["ml.phase"] === "sft")).toBe(true);
    const xid = await obs.logs({ kind: MlSubjectKind.PHASE, id: `${v3}/sft` }, "attr.ml.log.highlight:xid");
    expect(xid.length).toBeGreaterThan(0);
    expect(xid.every((l) => l.attributes["ml.log.highlight"] === "xid")).toBe(true);
    const events = await obs.events({ kind: MlSubjectKind.ATTEMPT, id: `${v3}/sft/2` }, [MlEventKind.XID]);
    expect(events.length).toBeGreaterThan(0);
    expect(events.every((e) => e.kind === MlEventKind.XID)).toBe(true);
  });

  it("lists an attempt's kept traces and opens them with their join keys", async () => {
    const traces = await obs.traces(attempt3);
    expect(traces.map((t) => t.traceId)).toContain(TRACES.slowStep);
    const slow = await obs.trace(TRACES.slowStep);
    const late = slow.spans.find((s) => s.name === "nccl:all_reduce" && s.attributes["ml.rank"] === String(STRAGGLER_RANK));
    expect(late?.attributes["ml.run.id"]).toBe(v3);
    expect(late?.attributes.UUID).toMatch(/^GPU-/);
  });

  it("draws the trainer's CPU and GPU flamegraphs from the attempt's captures", async () => {
    const cpu = await obs.api.getFlamegraph({ ...obs.scope(attempt3, "traces"), window: obs.window, service: "trainer", profileType: "cpu" });
    expect(cpu.stackCount).toBeGreaterThan(0);
    expect(cpu.availableProfileTypes).toEqual(["cpu", "gpu"]);
    expect(() => JSON.parse(cpu.speedscopeJson)).not.toThrow();
    expect(PROFILES.cpu).toBeTruthy();
  });

  it("leaves a read naming no ML subject to shop", async () => {
    const shop = await obs.api.searchLogs({ orgId: "org", envIds: [], filters: [{ key: "service.name", op: "eq", value: "api" }], window: obs.window, query: "", services: [], severities: [], limit: 10 });
    expect(shop.records.every((r) => !r.resource["ml.run.id"])).toBe(true);
  });
});
