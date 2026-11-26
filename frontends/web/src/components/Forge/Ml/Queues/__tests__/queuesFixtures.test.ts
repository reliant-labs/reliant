// Copyright (c) 2026 Reliant Labs

/** The Queues fixtures hold together: quota adds up, pending subjects resolve, preemptions match the hero run. */

import { describe, expect, it } from "vitest";

import { MlEarlyAccessService, MlSubjectKind, type GetMlQueuesResponse, type QueryMlSeriesResponse } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { answerMl } from "@/fixtures/shop/handlers/ml";
import { V3_PREEMPTION_GPU_HOURS } from "@/fixtures/shop/model/ml/queues";
import { attemptId, POOLS, QUEUES, RUNS, V3_ATTEMPTS } from "@/fixtures/shop/model/ml/world";

const NOW = Date.UTC(2026, 10, 26, 15, 30);
const now = () => NOW;
const method = (name: string) => MlEarlyAccessService.methods.find((m) => m.localName === name)!;
const ask = <T>(name: string, req: object) => answerMl(method(name), { orgId: "org", ...req }, now) as unknown as T;
const queues = () => ask<GetMlQueuesResponse>("getMlQueues", {});

describe("queues fixtures", () => {
  it("fills cohort ml-h100 exactly: research borrows serving-batch's 16", () => {
    const q = queues();
    const research = q.queues.find((x) => x.id === QUEUES.research.id)!.flavors[0];
    const batch = q.queues.find((x) => x.id === QUEUES.servingBatch.id)!.flavors[0];
    expect(research.used).toBe(research.nominal + research.borrowed);
    expect(research.borrowed).toBe(batch.lendable);
    expect(research.borrowed).toBeLessThanOrEqual(research.borrowingLimit);
    expect(research.nominal + batch.nominal).toBe(POOLS.h100.nodes.length * POOLS.h100.gpusPerNode);
    for (const cq of q.queues) for (const f of cq.flavors) expect(f.used).toBeLessThanOrEqual(f.nominal + f.borrowed);
  });

  it("counts the pending workloads and their GPUs in the summary, and each queue's pending", () => {
    const q = queues();
    expect(q.summary!.pending).toBe(q.pending.length);
    expect(q.summary!.gpusDemanded).toBe(q.pending.reduce((s, p) => s + p.gpus, 0));
    for (const cq of q.queues) expect(cq.pending).toBe(q.pending.filter((p) => p.queueId === cq.id).length);
  });

  it("names a resolvable subject for every pending workload", () => {
    const runIds = new Set<string>(Object.values(RUNS).map((r) => r.id));
    for (const p of queues().pending) {
      const s = p.subject!;
      if (s.kind === MlSubjectKind.ATTEMPT) {
        const [run, phase, n] = s.id.split("/");
        expect(runIds.has(run)).toBe(true);
        expect(phase).toBeTruthy();
        expect(Number(n)).toBeGreaterThan(0);
      } else {
        expect(s.kind).toBe(MlSubjectKind.WORKLOAD);
        expect(s.id).toMatch(/^[a-z0-9][a-z0-9-]*$/);
      }
    }
    expect(queues().pending[0].subject!.id).toBe(attemptId(RUNS.dpo.id, "dpo", 1));
  });

  it("costs v3 pretrain attempt 1's preemption as 400 redone steps on 64 GPUs, at the attempt's end", () => {
    const pre = queues().preemptions.find((p) => p.preempted!.id === attemptId(RUNS.v3.id, "pretrain", 1))!;
    expect(pre.gpuHoursLost).toBeCloseTo(V3_PREEMPTION_GPU_HOURS, 6);
    const a1 = V3_ATTEMPTS.find((a) => a.phase === "pretrain" && a.n === 1)!;
    expect(Number(pre.at!.seconds) * 1000).toBe(NOW + a1.endedAt!);
    expect(queues().summary!.preemptions24h).toBe(2);
  });

  it("pod start shares sum to 1 and the series show demand under a capacity line", () => {
    const q = queues();
    expect(q.podStart!.stages.reduce((s, x) => s + x.share, 0)).toBeCloseTo(1, 6);
    const res = ask<QueryMlSeriesResponse>("queryMlSeries", { subject: { kind: MlSubjectKind.QUEUE, id: QUEUES.research.id }, keys: ["queue.demand_gpus"] });
    const cap = res.series.find((s) => s.stat === "limit")!;
    expect(cap.y.every((v) => v === 64)).toBe(true);
    const admitted = res.series.find((s) => s.group === "admitted")!;
    expect(admitted.y[admitted.y.length - 1]).toBe(64);
  });
});
