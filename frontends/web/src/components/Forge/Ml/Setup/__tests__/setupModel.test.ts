// Copyright (c) 2026 Reliant Labs

import "@/components/Forge/Ml/__tests__/jsdomShims";
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { GetMlStackResponseSchema, MlComponentState } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { setupAnswers } from "@/fixtures/shop/model/ml/setup";
import { CLUSTERS, FINDINGS } from "@/fixtures/shop/model/ml/world";

import { orderCoverage, setupKpis } from "../setupModel";

const stack = () => create(GetMlStackResponseSchema, setupAnswers.getMlStack!({ orgId: "o" } as never, () => Date.UTC(2026, 10, 26, 15)) as never);

describe("setup", () => {
  it("covers both clusters; every finding a row names exists", () => {
    const s = stack();
    expect(s.clusters.map((c) => c.cluster?.id)).toEqual([CLUSTERS.train.id, CLUSTERS.serve.id]);
    for (const c of s.clusters) {
      expect(c.discoverCommand).toBe(`reliant forge ml discover --cluster ${c.cluster?.id}`);
      for (const i of c.coverage) if (i.findingId) expect(Object.values(FINDINGS), i.key).toContain(i.findingId);
    }
    const serve = s.clusters[1];
    expect(serve.coverage.find((i) => i.key === "profiling-counters")).toMatchObject({ state: MlComponentState.MISSING, findingId: FINDINGS.dcgmProfilingOff });
    expect(serve.coverage.find((i) => i.key === "node-health")?.findingId).toBe(FINDINGS.npdGpuRulesMissing);
    expect(s.clusters[0].coverageRatio).toBeGreaterThan(serve.coverageRatio);
  });

  it("puts gaps first and counts them", () => {
    const s = stack();
    const ordered = orderCoverage(s.clusters[1].coverage);
    expect(ordered[0].state).toBe(MlComponentState.MISSING);
    expect(ordered.at(-1)?.state).toBe(MlComponentState.NOT_APPLICABLE);
    const k = setupKpis(s.clusters);
    expect(k.clusters).toBe(2);
    expect(k.missing).toBeGreaterThanOrEqual(3);
    expect(k.lastDiscoveryMs).toBeGreaterThan(k.oldestDiscoveryMs!);
  });
});
