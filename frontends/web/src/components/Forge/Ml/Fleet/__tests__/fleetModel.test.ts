// Copyright (c) 2026 Reliant Labs

import "@/components/Forge/Ml/__tests__/jsdomShims";
import { create } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { MlCollection, MlEventKind, MlEventSchema, MlFieldGroup, MlFieldValueSchema, MlNvlinkPairSchema } from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";

import { eventCountSeries, fmtField, groupFields, groupNotApplicable, groupUncollected, isStale, nvlinkMatrix, parseFleetRoute, plural, STALE_AFTER_MS } from "../fleetModel";

describe("fleetModel", () => {
  it("parses the fleet routes", () => {
    expect(parseFleetRoute([])).toEqual({ page: "overview" });
    expect(parseFleetRoute(["pools", "c", "p"])).toMatchObject({ page: "pool", poolId: "c/p" });
    expect(parseFleetRoute(["c", "n"])).toMatchObject({ page: "node", nodeId: "c/n" });
    expect(parseFleetRoute(["gpus", "GPU-1"])).toEqual({ page: "device", uuid: "GPU-1" });
  });

  it("groups fields in reading order and formats them; not collected is null, never 0", () => {
    const f = (group: MlFieldGroup, key: string, value: number, unit = "", collection = MlCollection.COLLECTED, text = "") => create(MlFieldValueSchema, { group, key, value, unit, collection, text });
    const groups = groupFields([f(MlFieldGroup.THERMAL, "t", 87, "C"), f(MlFieldGroup.UTILIZATION, "u", 0.5, "ratio"), f(MlFieldGroup.PROFILING, "sm", NaN, "ratio", MlCollection.NOT_COLLECTED)]);
    expect(groups.map((g) => g.group)).toEqual([MlFieldGroup.UTILIZATION, MlFieldGroup.PROFILING, MlFieldGroup.THERMAL]);
    expect(groups[1].notCollected).toBe(1);
    expect(fmtField(f(MlFieldGroup.PCIE, "gen", 5))).toBe("5");
    expect(fmtField(f(MlFieldGroup.THERMAL, "t", 87.4, "C"))).toBe("87°C");
    expect(fmtField(f(MlFieldGroup.PROFILING, "sm", NaN, "ratio", MlCollection.NOT_COLLECTED))).toBeNull();
    expect(fmtField(f(MlFieldGroup.CLOCKS, "r", NaN, "", MlCollection.COLLECTED, "HW slowdown"))).toBe("HW slowdown");
  });

  it("builds a symmetric NVLink matrix", () => {
    const m = nvlinkMatrix([create(MlNvlinkPairSchema, { fromIndex: 1, toIndex: 3, state: "degraded", links: 18 })], 4);
    expect(m[1][3].state).toBe("degraded");
    expect(m[3][1].state).toBe("degraded");
    expect(m[2][2].state).toBe("self");
    expect(m[0][1].state).toBe("none");
  });

  it("counts hardware events into buckets", () => {
    const now = Date.UTC(2026, 10, 26, 12);
    const e = (kind: MlEventKind, hoursAgo: number) => create(MlEventSchema, { kind, at: timestampFromMs(now - hoursAgo * 3600_000) });
    const s = eventCountSeries([e(MlEventKind.XID, 1), e(MlEventKind.THROTTLE, 2), e(MlEventKind.THROTTLE, 2.5), e(MlEventKind.XID, 100)], now, 48, 3);
    expect(s.find((x) => x.label === "Xid")!.y.reduce((a, b) => a + b, 0)).toBe(1);
    expect(s.find((x) => x.label === "Throttle")!.y.at(-1)).toBe(2);
  });

  it("reads real-data edge cases: stale clusters, whole groups not collected, plurals", () => {
    const at = Date.UTC(2026, 10, 26, 12);
    expect(isStale({ lastSeenAt: timestampFromMs(at - STALE_AFTER_MS + 1) }, at)).toBe(false);
    expect(isStale({ lastSeenAt: timestampFromMs(at - STALE_AFTER_MS - 1) }, at)).toBe(true);
    expect(isStale({ lastSeenAt: undefined }, at)).toBe(true);
    const nc = (key: string, group: MlFieldGroup) => create(MlFieldValueSchema, { key, group, value: NaN, collection: MlCollection.NOT_COLLECTED });
    const fields = [create(MlFieldValueSchema, { key: "gpu.util", group: MlFieldGroup.UTILIZATION, value: 0.7, collection: MlCollection.COLLECTED }), nc("gpu.temp", MlFieldGroup.THERMAL), nc("gpu.xid.last", MlFieldGroup.XID)];
    expect(groupUncollected(fields, MlFieldGroup.XID)).toBe(true);
    expect(groupUncollected(fields, MlFieldGroup.UTILIZATION)).toBe(false);
    // A group nothing sent says nothing: neither collected nor "not collected".
    expect(groupUncollected(fields, MlFieldGroup.NVLINK)).toBe(false);
    expect(groupNotApplicable(fields, MlFieldGroup.NVLINK)).toBe(false);
    expect(plural(1, "cluster")).toBe("1 cluster");
    expect(plural(0, "pool")).toBe("0 pools");
  });
});
