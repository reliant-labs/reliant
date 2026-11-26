// Copyright (c) 2026 Reliant Labs

/**
 * The Inference fixtures hold together: replica, revision and placement ids
 * are coherent with the world (Fleet's pods and devices), traffic shares sum
 * to 1, the headline numbers come from the curves the charts draw, and every
 * request trace the list names can be opened.
 */

import { toBinary } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { MlEarlyAccessService, MlCollection, MlSubjectKind, type GetMlInferenceServiceResponse, type GetMlTraceResponse, type ListMlTracesResponse, type QueryMlSeriesResponse } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { answerMl } from "@/fixtures/shop/handlers/ml";
import { INFERENCE_REPLICAS, PREV_FP8_REVISION, replicaId } from "@/fixtures/shop/model/ml/inference";
import { CLUSTERS, gpuUuid, POOLS, REVISIONS, SERVICES, TRACES, VERSIONS } from "@/fixtures/shop/model/ml/world";

const NOW = Date.UTC(2026, 10, 26, 15, 30);
const now = () => NOW;
const method = (name: string) => MlEarlyAccessService.methods.find((m) => m.localName === name)!;
const ask = <T>(name: string, req: object) => answerMl(method(name), { orgId: "org", ...req }, now) as unknown as T;
const svc = (id: string) => ask<GetMlInferenceServiceResponse>("getMlInferenceService", { serviceId: id });

describe("inference fixtures", () => {
  it("lists three services, every one with an rps sparkline and a verdict", () => {
    const { services } = ask<{ services: { id: string; rpsSeries?: { y: number[] }; replicas: number; accelerators: number }[] }>("listMlInferenceServices", {});
    expect(services.map((s) => s.id)).toEqual([SERVICES.prod.id, SERVICES.staging.id, SERVICES.reranker.id]);
    for (const s of services) expect(s.rpsSeries?.y.length).toBeGreaterThan(50);
    expect(services[0]).toMatchObject({ replicas: 4, accelerators: 8 });
  });

  it("places every replica on an l4-serve node, on Fleet's devices", () => {
    expect(INFERENCE_REPLICAS.map(replicaId)).toEqual([
      `${SERVICES.prod.id}/support-assistant-prod-vllm-0`,
      `${SERVICES.prod.id}/support-assistant-prod-vllm-1`,
      `${SERVICES.prod.id}/support-assistant-prod-vllm-2`,
      `${SERVICES.prod.id}/support-assistant-prod-vllm-3`,
      `${SERVICES.staging.id}/support-assistant-staging-vllm-0`,
      `${SERVICES.reranker.id}/reranker-prod-triton-0`,
    ]);
    const used = new Set<string>();
    for (const id of [SERVICES.prod.id, SERVICES.staging.id, SERVICES.reranker.id]) {
      const d = svc(id);
      const revisionIds = new Set(d.revisions.map((r) => r.id));
      for (const r of d.replicas) {
        expect(r.id).toBe(`${id}/${r.pod}`);
        const node = r.nodeId.split("/")[1];
        expect(r.nodeId).toBe(`${CLUSTERS.serve.id}/${node}`);
        expect(POOLS.l4.nodes).toContain(node);
        expect(revisionIds.has(r.revisionId)).toBe(true);
        for (const uuid of r.deviceUuids) {
          expect(used.has(uuid)).toBe(false);
          used.add(uuid);
          const ok = Array.from({ length: POOLS.l4.gpusPerNode }, (_, i) => gpuUuid(node, i)).includes(uuid);
          expect(ok).toBe(true);
        }
      }
    }
    expect(used.size).toBe(11);
    expect(INFERENCE_REPLICAS[0].node).toBe("ip-10-40-1-21");
    expect(INFERENCE_REPLICAS[3].node).toBe("ip-10-40-1-37");
  });

  it("splits prod traffic 90/10 between the control and the fp8 arm", () => {
    const d = svc(SERVICES.prod.id);
    const live = d.revisions.filter((r) => !r.endedAt);
    expect(live.map((r) => r.id).sort()).toEqual([REVISIONS.prodCurrent, REVISIONS.prodFp8].sort());
    expect(live.reduce((s, r) => s + r.trafficShare, 0)).toBeCloseTo(1, 6);
    expect(d.replicas.reduce((s, r) => s + r.rps, 0)).toBeCloseTo(d.service!.rps, 6);
    expect(d.comparison!.arms.reduce((s, a) => s + a.trafficShare, 0)).toBeCloseTo(1, 6);
    expect(d.comparison!.arms.map((a) => a.modelVersionId)).toEqual([VERSIONS.v2.id, VERSIONS.v21fp8.id]);
    expect(d.revisions.map((r) => r.id)).toContain(PREV_FP8_REVISION);
    expect(d.finishReasons.reduce((s, f) => s + f.share, 0)).toBeCloseTo(1, 6);
    expect(d.adapters.reduce((s, f) => s + f.share, 0)).toBeCloseTo(1, 6);
    const cold = d.coldStart!.stages.reduce((s, st) => s + st.share, 0);
    expect(cold).toBeCloseTo(1, 6);
  });

  it("keeps the blended cost per 1M tokens at about $0.41 and 24h cost at 8 L4 × 24h", () => {
    const d = svc(SERVICES.prod.id);
    expect(d.service!.usdPerMillionTokens).toBeGreaterThan(0.36);
    expect(d.service!.usdPerMillionTokens).toBeLessThan(0.46);
    expect(d.service!.cost!.usd).toBeCloseTo(8 * 24 * 0.98, 6);
    expect(d.servingCost!.gpuHours24h).toBe(192);
  });

  it("answers series per replica and marks speculative decoding not applicable on the control", () => {
    const res = ask<QueryMlSeriesResponse>("queryMlSeries", { subject: { kind: MlSubjectKind.INFERENCE_SERVICE, id: SERVICES.prod.id }, keys: ["engine.kv_cache_ratio"], groupBy: "replica" });
    expect(res.series).toHaveLength(4);
    const peak = Math.max(...res.series.flatMap((s) => s.y.filter(Number.isFinite)));
    expect(peak).toBeGreaterThan(0.9);
    const spec = ask<QueryMlSeriesResponse>("queryMlSeries", { subject: { kind: MlSubjectKind.INFERENCE_SERVICE, id: SERVICES.prod.id }, keys: ["engine.spec_decode_acceptance"] });
    expect(spec.series[0].collection).toBe(MlCollection.NOT_APPLICABLE);
    const fp8 = ask<QueryMlSeriesResponse>("queryMlSeries", { subject: { kind: MlSubjectKind.REVISION, id: REVISIONS.prodFp8 }, keys: ["engine.spec_decode_acceptance"] });
    expect(fp8.series[0].collection).toBe(MlCollection.COLLECTED);
  });

  it("opens every request trace it lists, and the slow one has the spec's spans with join keys", () => {
    const list = ask<ListMlTracesResponse>("listMlTraces", { subject: { kind: MlSubjectKind.INFERENCE_SERVICE, id: SERVICES.prod.id } });
    expect(list.traces.length).toBe(20);
    expect(list.traces.map((t) => t.traceId)).toContain(TRACES.slowRequest);
    for (const t of list.traces) {
      const trace = ask<GetMlTraceResponse>("getMlTrace", { traceId: t.traceId });
      expect(trace.spans.length).toBeGreaterThan(5);
      // Encodable on the wire (whole-nanosecond timestamps, int32s).
      expect(() => toBinary(method("getMlTrace").output, trace as never)).not.toThrow();
    }
    for (const id of [SERVICES.prod.id, SERVICES.staging.id, SERVICES.reranker.id]) expect(() => toBinary(method("getMlInferenceService").output, svc(id) as never)).not.toThrow();
    const slow = ask<GetMlTraceResponse>("getMlTrace", { traceId: TRACES.slowRequest });
    const names = slow.spans.map((s) => s.name);
    for (const n of ["POST /chat", "epp.schedule", "llm_request", "queued", "prefill", "decode", "dcgm.sample"]) expect(names).toContain(n);
    const vllm = slow.spans.find((s) => s.name === "llm_request")!;
    expect(vllm.nodeId).toBe(`${CLUSTERS.serve.id}/ip-10-40-1-21`);
    expect(vllm.deviceUuid).toBe(gpuUuid("ip-10-40-1-21", 2));
    expect(vllm.runId).toBe("");
    expect(slow.summary!.durationMs).toBeGreaterThan(4200);
    expect(slow.summary!.durationMs).toBeLessThan(4400);
    expect(ask<GetMlTraceResponse>("getMlTrace", { traceId: TRACES.normalRequest }).spans.length).toBeGreaterThan(5);
    const rr = ask<ListMlTracesResponse>("listMlTraces", { subject: { kind: MlSubjectKind.INFERENCE_SERVICE, id: SERVICES.reranker.id } });
    expect(rr.traces).toHaveLength(0);
    expect(rr.missingFindingId).toBe("f-inference-tracing-off-reranker");
  });
});
