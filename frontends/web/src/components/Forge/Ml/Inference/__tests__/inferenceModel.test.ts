// Copyright (c) 2026 Reliant Labs

/** The Inference view-models over the fixture world's answers. */

import { describe, expect, it } from "vitest";

import { MlEarlyAccessService, MlReleaseVerdict, MlSubjectKind, type GetMlInferenceServiceResponse, type ListMlInferenceServicesResponse } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { answerMl } from "@/fixtures/shop/handlers/ml";
import { mlObs } from "@/fixtures/shop/model/ml/__tests__/mlObs";
import { REVISIONS, SERVICES } from "@/fixtures/shop/model/ml/world";

import { armDeltaLabel, armMetricRows, armVerdict, byKey, deltaTone, fmtDelta, heatmapRows, notApplicable, orderRevisions, relDelta, servicesKpis, sloTone } from "../inferenceModel";

const NOW = Date.UTC(2026, 10, 26, 15, 30);
const method = (name: string) => MlEarlyAccessService.methods.find((m) => m.localName === name)!;
const ask = <T>(name: string, req: object) => answerMl(method(name), { orgId: "org", ...req }, () => NOW) as unknown as T;
const prod = () => ask<GetMlInferenceServiceResponse>("getMlInferenceService", { serviceId: SERVICES.prod.id });

describe("inferenceModel", () => {
  it("servicesKpis: total rps, the worst SLO and TTFT, blended $/1M and GPU-weighted busy", () => {
    const { services } = ask<ListMlInferenceServicesResponse>("listMlInferenceServices", {});
    const k = servicesKpis(services);
    expect(k.services).toBe(3);
    expect(k.gpus).toBe(11);
    expect(k.rps).toBeCloseTo(services.reduce((s, x) => s + x.rps, 0), 6);
    expect(k.worstTtft?.id).toBe(SERVICES.prod.id);
    expect(k.worstSlo?.id).toBe(SERVICES.prod.id);
    // Blended over prod and staging: staging's cost per token is far higher, so the blend is above prod's.
    expect(k.blendedUsdPerMillion).toBeGreaterThan(services[0].usdPerMillionTokens);
    expect(k.busy).toBeGreaterThan(0);
    expect(k.busy).toBeLessThan(1);
  });

  it("sloTone and deltas", () => {
    expect(sloTone(0.991, 0.99)).toBe("success");
    expect(sloTone(0.988, 0.99)).toBe("warning");
    expect(sloTone(0.98, 0.99)).toBe("danger");
    expect(fmtDelta(relDelta({ before: 395, after: 411 }))).toBe("+4.1%");
    expect(fmtDelta(-0.23)).toBe("−23%");
    expect(fmtDelta(0)).toBe("±0%");
    expect(deltaTone({ significant: true, regression: true })).toBe("warning");
    expect(deltaTone({ significant: false, regression: true })).toBe("neutral");
  });

  it("orders live revisions by traffic, then ended ones, and finds the TTFT regression", () => {
    const revs = orderRevisions(prod().revisions);
    expect(revs.map((r) => r.id).slice(0, 2)).toEqual([REVISIONS.prodCurrent, REVISIONS.prodFp8]);
    expect(revs.slice(2).every((r) => r.endedAt)).toBe(true);
    expect(revs[0].verdict).toBe(MlReleaseVerdict.PASS);
    const ttft = revs[0].deltas.find((d) => d.label === "TTFT p95")!;
    expect(relDelta(ttft)).toBeCloseTo(0.0405, 3);
  });

  it("puts the Changes arms side by side on one scale with verdicts", () => {
    const rows = armMetricRows(prod().comparison);
    expect(rows.map((r) => r.label)).toContain("Guardrail trigger rate");
    for (const r of rows) {
      expect(r.lo).toBeLessThan(Math.min(r.control.ciLow, ...r.arms.map((a) => a.metric.ciLow)));
      expect(r.hi).toBeGreaterThan(Math.max(r.control.ciHigh, ...r.arms.map((a) => a.metric.ciHigh)));
    }
    const verdict = (label: string) => armVerdict(rows.find((r) => r.label === label)!.arms[0].metric);
    expect(verdict("TTFT p95")).toEqual({ label: "better", tone: "success" });
    expect(verdict("Guardrail trigger rate")).toEqual({ label: "worse", tone: "warning" });
    expect(verdict("Judge score (1–5)").label).toBe("no significant difference");
    const guard = rows.find((r) => r.label === "Guardrail trigger rate")!;
    expect(armDeltaLabel(guard, guard.arms[0].metric)).toBe("+0.4pt");
    const ttft = rows.find((r) => r.label === "TTFT p95")!;
    expect(armDeltaLabel(ttft, ttft.arms[0].metric)).toBe("−14%");
    expect(armMetricRows(undefined)).toEqual([]);
  });

  it("heatmapRows averages per-replica KV into quarter-hours and flags the hot replica", async () => {
    const res = { series: await mlObs(NOW).series({ kind: MlSubjectKind.INFERENCE_SERVICE, id: SERVICES.prod.id }, ["engine.kv_cache_ratio"], { groupBy: "replica" }) };
    const { rows, columns } = heatmapRows(res.series, 3, undefined, (s) => Math.max(...s.y.filter(Number.isFinite)) >= 0.9);
    expect(rows).toHaveLength(4);
    expect(columns.length).toBe(Math.ceil(res.series[0].x.length / 3));
    expect(rows.find((r) => r.label === "vllm-1")?.tone).toBe("warning");
    // vllm-2 was scaled down for part of the day: those cells are "no sample".
    expect(rows.find((r) => r.label === "vllm-2")!.cells.some((c) => c === null)).toBe(true);
  });

  it("notApplicable names why the reranker has no TTFT", async () => {
    const series = await mlObs(NOW).series({ kind: MlSubjectKind.INFERENCE_SERVICE, id: SERVICES.reranker.id }, ["serve.ttft", "serve.e2e"], { stats: ["p50", "p95", "p99"] });
    expect(notApplicable(byKey(series, "serve.ttft"))).toMatch(/no token generation/);
    expect(notApplicable(byKey(series, "serve.e2e"))).toBeNull();
  });

});
