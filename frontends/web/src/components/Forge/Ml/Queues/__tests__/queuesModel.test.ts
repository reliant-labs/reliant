// Copyright (c) 2026 Reliant Labs

/** The Queues view-models over the fixture world's answers. */

import { describe, expect, it } from "vitest";

import { MlEarlyAccessService, MlPendingReason, type GetMlQueuesResponse } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { answerMl } from "@/fixtures/shop/handlers/ml";
import { attemptId, QUEUES, RUNS } from "@/fixtures/shop/model/ml/world";

import { filterPending, podStartGantt, podStartSegments, queueRows, reasonCounts, reasonKey, selectionOf, summaryNumbers, topStage } from "../queuesModel";

const NOW = Date.UTC(2026, 10, 26, 15, 30);
const method = (name: string) => MlEarlyAccessService.methods.find((m) => m.localName === name)!;
const queues = () => answerMl(method("getMlQueues"), { orgId: "org" }, () => NOW) as unknown as GetMlQueuesResponse;

describe("queuesModel", () => {
  it("queueRows: one row per queue flavor, fill against nominal + borrowing limit", () => {
    const rows = queueRows(queues().queues);
    const research = rows.find((r) => r.key === QUEUES.research.id)!;
    expect(research.fill).toBe(1);
    expect(research.borrowed).toBe(16);
    expect(rows).toHaveLength(4);
  });

  it("reasonCounts and filterPending by the reason's URL key", () => {
    const q = queues();
    const counts = reasonCounts(q.pending);
    expect(counts[0]).toMatchObject({ reason: MlPendingReason.INSUFFICIENT_QUOTA, key: "insufficient-quota", workloads: 2, gpus: 40 });
    expect(counts.reduce((s, c) => s + c.workloads, 0)).toBe(q.pending.length);
    expect(filterPending(q.pending, reasonKey(MlPendingReason.UNTOLERATED_TAINT)).map((p) => p.name)).toEqual(["jupyter-jon-a100-0"]);
    expect(filterPending(q.pending, undefined)).toHaveLength(q.pending.length);
    expect(filterPending(q.pending, "bogus")).toHaveLength(q.pending.length);
  });

  it("selectionOf resolves a queue, or a workload and its queue", () => {
    const q = queues();
    expect(selectionOf(QUEUES.eval.id, q.queues, q.pending).queue?.name).toBe("eval");
    const dpo = selectionOf(attemptId(RUNS.dpo.id, "dpo", 1), q.queues, q.pending);
    expect(dpo.workload?.gpus).toBe(32);
    expect(dpo.queue?.id).toBe(QUEUES.research.id);
    expect(selectionOf("nothing", q.queues, q.pending)).toEqual({});
  });

  it("summaryNumbers: the shortfall of GPUs", () => {
    const k = summaryNumbers(queues().summary);
    expect(k.demanded).toBe(50);
    expect(k.shortfall).toBe(k.demanded - k.free);
    expect(summaryNumbers(undefined).pending).toBe(0);
  });

  it("pod start: queued is the top stage, segments drop empty stages, waterfalls stack in order", () => {
    const q = queues();
    expect(topStage(q.podStart)?.stage).toBe("queued");
    expect(podStartSegments(q.podStart).every((s) => s.value > 0)).toBe(true);
    const { rows, to } = podStartGantt(q.podStartExamples);
    expect(rows).toHaveLength(3);
    for (const r of rows) for (let i = 1; i < r.spans.length; i++) expect(r.spans[i].start).toBeGreaterThanOrEqual(r.spans[i - 1].end!);
    expect(to).toBeGreaterThan(200);
  });
});
