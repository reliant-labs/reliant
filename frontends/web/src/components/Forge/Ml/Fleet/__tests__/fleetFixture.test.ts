// Copyright (c) 2026 Reliant Labs

/**
 * The fleet fixture (fixtures/shop/model/ml/fleet.ts) agrees with itself
 * and with world.ts: pool counts add up to the fleet's, every device has
 * every field with its DCGM source, the serving cluster's profiling is not
 * collected (never zero), the faulty GPU reads not collected after its Xid,
 * and the series the Overview reads exist.
 */

import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import {
  GetMlFleetResponseSchema,
  MlCollection,
  MlFieldGroup,
  MlHealth,
  MlSubjectKind,
  QueryMlSeriesRequestSchema,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { fleetAnswers, fleetNodes } from "@/fixtures/shop/model/ml/fleet";
import { BAD_GPU_INDEX, BAD_NODE, CLUSTERS, gpuUuid, HOT_NODE, POOLS } from "@/fixtures/shop/model/ml/world";

const NOW = Date.UTC(2026, 10, 26, 15, 0);
const now = () => NOW;

const fleet = () => create(GetMlFleetResponseSchema, fleetAnswers.getMlFleet!({ orgId: "o" } as never, now) as never);

describe("fleet fixture", () => {
  it("pool counts add up to the fleet summary", () => {
    const f = fleet();
    const s = f.summary!;
    expect(s.gpusTotal).toBe(104);
    expect(f.pools.reduce((a, p) => a + p.gpusTotal, 0)).toBe(s.gpusTotal);
    expect(f.pools.reduce((a, p) => a + p.gpusAllocated, 0)).toBe(s.gpusAllocated);
    expect(s.gpusAllocated + s.gpusUnallocated).toBe(s.gpusTotal);
    const byName = Object.fromEntries(f.pools.map((p) => [p.name, p]));
    expect(byName["h100-train"].gpusAllocated).toBe(56);
    expect(byName["h100-spare"].gpusAllocated).toBe(8);
    expect(byName["a100-dev"].gpusAllocated).toBe(12);
    expect(byName["l4-serve"].gpusAllocated).toBe(13);
    expect(s.gpusIdleAllocated).toBe(6);
    expect(s.gpusFaulty).toBe(1);
    expect(s.trainingGoodput).toBeCloseTo(0.83);
    const cat = s.cost24h!.byCategory.reduce((a, c) => a + c.gpuHours, 0);
    expect(cat).toBeCloseTo(s.cost24h!.gpuHours, 3);
    expect(f.topIdle[0].workload?.id).toContain("jupyter-priya-0");
    expect(f.unhealthy[0].subject?.id).toBe(gpuUuid(BAD_NODE, BAD_GPU_INDEX));
    expect(f.unhealthy[0].score).toBe(92);
  });

  it("every node and device belongs to world.ts", () => {
    const names = new Set<string>(Object.values(POOLS).flatMap((p) => p.nodes));
    for (const n of fleetNodes()) {
      expect(names.has(n.name)).toBe(true);
      expect(n.id).toBe(`${n.pool.clusterId}/${n.name}`);
      n.devices.forEach((d, i) => expect(d.uuid).toBe(gpuUuid(n.name, i)));
    }
  });

  it("every device field names its source; serve profiling is not collected", () => {
    for (const n of fleetNodes()) {
      const res = fleetAnswers.getMlNode!({ orgId: "o", nodeId: n.id } as never, now)!;
      const devices = res.node!.devices!;
      expect(devices).toHaveLength(n.devices.length);
      for (const d of devices) {
        expect(d.fields!.length).toBeGreaterThanOrEqual(40);
        for (const f of d.fields!) expect(f.sourceField, f.key).toBeTruthy();
        const prof = d.fields!.filter((f) => f.group === MlFieldGroup.PROFILING);
        if (n.pool.clusterId === CLUSTERS.serve.id) for (const f of prof) expect(f.collection).toBe(MlCollection.NOT_COLLECTED);
      }
    }
  });

  it("the faulty GPU reads not collected after its Xid, never zero", () => {
    const res = fleetAnswers.getMlDevice!({ orgId: "o", uuid: gpuUuid(BAD_NODE, BAD_GPU_INDEX) } as never, now)!;
    expect(res.device!.health).toBe(MlHealth.FAULTY);
    const util = res.device!.fields!.find((f) => f.key === "gpu.util")!;
    expect(util.collection).toBe(MlCollection.NOT_COLLECTED);
    expect(res.placements).toHaveLength(2);
  });

  it("serves the Overview's ORG series and a node's per-device series", () => {
    const req = (subject: object, keys: string[], groupBy?: string) => create(QueryMlSeriesRequestSchema, { orgId: "o", subject, keys, groupBy });
    const org = fleetAnswers.queryMlSeries!(req({ kind: MlSubjectKind.ORG, id: "" }, ["pool.busy", "fleet.allocated", "fleet.busy"], "pool"), now)!;
    const pools = org.series!.filter((s) => s.key === "pool.busy");
    expect(pools.map((s) => s.group).sort()).toEqual(["a100-dev", "h100-spare", "h100-train", "l4-serve"]);
    expect(pools[0].x!.length).toBeGreaterThan(40);
    const alloc = org.series!.find((s) => s.key === "fleet.allocated")!;
    expect(alloc.y![alloc.y!.length - 1]).toBe(89);
    const hot = fleetNodes().find((n) => n.name === HOT_NODE)!;
    const node = fleetAnswers.queryMlSeries!(req({ kind: MlSubjectKind.NODE, id: hot.id }, ["gpu.temp_c"], "device"), now)!;
    expect(node.series).toHaveLength(8);
  });

  it("node events include the device's", () => {
    const n = fleetNodes().find((x) => x.name === BAD_NODE)!;
    const ev = fleetAnswers.listMlEvents!({ orgId: "o", subject: { kind: MlSubjectKind.NODE, id: n.id }, kinds: [] } as never, now)!;
    expect(ev.events!.some((e) => e.id === "ev-xid79-r4vb")).toBe(true);
  });
});
