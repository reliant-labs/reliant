// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { fixtureMetricCatalog } from "../../__fixtures__/metricFixtures";
import {
  attributeField,
  capNote,
  chartUnitOf,
  groupCatalog,
  kindLabel,
  orderedAttributes,
  seriesByValue,
  seriesFigures,
  splitPartial,
  valueUnitOf,
  warningText,
} from "../seriesView";

describe("units", () => {
  it("maps UCUM to the console's formatter", () => {
    expect(valueUnitOf("s")).toBe("s");
    expect(valueUnitOf("ms")).toBe("ms");
    expect(valueUnitOf("By")).toBe("bytes");
    expect(valueUnitOf("{request}/s")).toBe("rate");
    expect(valueUnitOf("{order}")).toBe("count");
    expect(valueUnitOf("1")).toBe("count");
  });

  it("uses the series' common unit, or a count when they disagree", () => {
    const meta = (valueUnit: string) => ({ label: "A", metric: "m", kind: "histogram" as const, agg: "quantile", valueUnit });
    expect(chartUnitOf([meta("s"), meta("s")])).toBe("s");
    expect(chartUnitOf([meta("s"), meta("{call}/s")])).toBe("count");
    expect(chartUnitOf([])).toBe("count");
  });
});

describe("splitPartial", () => {
  it("dashes the open tail from the last complete point", () => {
    expect(splitPartial([1, 2, 3, 4], [false, false, true, true])).toEqual({ complete: [1, 2, null, null], open: [null, 2, 3, 4] });
  });

  it("leaves a series with nothing open alone", () => {
    expect(splitPartial([1, null, 3], undefined)).toEqual({ complete: [1, null, 3], open: [null, null, null] });
  });
});

describe("seriesFigures", () => {
  it("reads the complete buckets only, skipping gaps", () => {
    expect(seriesFigures([2, null, 4, 100], [false, false, false, true])).toEqual({ last: 4, avg: 3, max: 4 });
    expect(seriesFigures([null, null])).toEqual({ last: null, avg: null, max: null });
  });
});

describe("capNote", () => {
  const series = (group: Record<string, string>) => ({ label: "A", group, timestamps: [], values: [], value: null });
  it("says how many of how many when the cap cut the result", () => {
    expect(capNote({ series: [series({ s: "a" }), series({ s: "b" })], rows: [], seriesTotal: 1204, truncated: true })).toEqual({ shown: 2, total: 1204 });
    expect(capNote({ series: [series({ s: "a" })], rows: [], seriesTotal: 1, truncated: false })).toBeNull();
    expect(capNote({ series: [series({ s: "a" })], rows: [], seriesTotal: 0, truncated: true })).toEqual({ shown: 1, total: 0 });
  });

  it("splits a breakdown by value, in the order the result lists them", () => {
    const out = seriesByValue([series({ k: "b" }), series({ k: "a" }), series({ k: "b" })], "k");
    expect(out.map((g) => [g.value, g.series.length])).toEqual([
      ["b", 2],
      ["a", 1],
    ]);
  });
});

describe("attributes", () => {
  it("names an attribute as the dashspec field", () => {
    expect(attributeField({ key: "service.name", resource: true })).toBe("service.name");
    expect(attributeField({ key: "k8s.pod.name", resource: true })).toBe("resource.attr.k8s.pod.name");
    expect(attributeField({ key: "status", resource: false })).toBe("metric.attr.status");
  });

  it("orders the suggested breakdowns first", () => {
    const a = (key: string) => ({ key, resource: false, distinctEstimate: 1, enumerated: [], highCardinality: false });
    expect(orderedAttributes([a("z"), a("b"), a("a")], ["b"]).map((x) => x.key)).toEqual(["b", "a", "z"]);
  });
});

describe("the catalog", () => {
  it("groups in a fixed order and filters by name or description", () => {
    const { metrics } = fixtureMetricCatalog(0);
    expect(groupCatalog(metrics, "").map((g) => g.group)).toEqual(["declared", "runtime", "infra", "other"]);
    expect(groupCatalog(metrics, "goroutine").flatMap((g) => g.metrics.map((m) => m.name))).toEqual(["go.goroutine.count"]);
    expect(groupCatalog(metrics, "Refunds issued").flatMap((g) => g.metrics.map((m) => m.name))).toEqual(["orders.refunds"]);
  });

  it("calls a sum a counter or an up-down counter", () => {
    expect(kindLabel({ kind: "sum", monotonic: true })).toBe("counter");
    expect(kindLabel({ kind: "sum", monotonic: false })).toBe("up-down counter");
    expect(kindLabel({ kind: "histogram", monotonic: false })).toBe("histogram");
  });
});

describe("notes", () => {
  it("words a warning, and passes an unknown one through", () => {
    expect(warningText("step_raised")).toBe("Step raised to the export interval");
    expect(warningText("brand_new_code")).toBe("brand new code");
  });

});
