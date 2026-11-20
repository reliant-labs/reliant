// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import type { PanelResult } from "@/services/controlPlane/observability/types";

import {
  chartMetricOf,
  deviceOf,
  percentileOf,
  rateScore,
  realExperienceScore,
  routesByScore,
  speedPanel,
  vitalScore,
  vitalsRows,
  VITALS,
  type Vital,
  type VitalStat,
} from "../speedInsightsModel";

const stat = (good: number, ni: number, poor: number, value: number | null = 1): VitalStat => ({ good, ni, poor, value });
const all = (s: VitalStat) => Object.fromEntries(VITALS.map((v) => [v, s])) as Record<Vital, VitalStat>;

describe("speedInsightsModel", () => {
  it("reads the URL's choices, defaulting anything unknown", () => {
    expect(percentileOf("p95")).toBe("p95");
    expect(percentileOf("p50")).toBe("p75");
    expect(deviceOf("mobile")).toBe("mobile");
    expect(deviceOf("tablet")).toBe("desktop");
    expect(chartMetricOf("inp")).toBe("inp");
    expect(chartMetricOf("nope")).toBe("score");
  });

  it("scores a vital from its split: good in full, needs improvement half, poor not at all", () => {
    expect(vitalScore(stat(80, 10, 10))).toBe(85);
    expect(vitalScore(stat(0, 0, 0))).toBeNull();
  });

  it("weights LCP 30, INP 30, CLS 25, FCP 15 and leaves TTFB out", () => {
    const vitals = { ...all(stat(100, 0, 0)), lcp: stat(0, 0, 100), ttfb: stat(0, 0, 100) };
    expect(realExperienceScore(vitals)).toBe(70);
    // A vital with no sample drops out and the rest are reweighted.
    expect(realExperienceScore({ ...all(stat(100, 0, 0)), inp: stat(0, 0, 0) })).toBe(100);
    expect(realExperienceScore(all(stat(0, 0, 0)))).toBeNull();
    expect([rateScore(95), rateScore(89), rateScore(49)]).toEqual(["good", "ni", "poor"]);
  });

  it("reads a vital with no sample as no value, not 0 ms", () => {
    const columns = [...VITALS.flatMap((v) => [v, `${v}_good`, `${v}_ni`, `${v}_poor`]), "views"];
    const values = columns.map((c) => (c.startsWith("inp") ? 0 : c === "views" ? 10 : 5));
    const [row] = vitalsRows({ columns, rows: [{ group: { page: "/cart" }, values }] } as unknown as PanelResult);
    expect(row.page).toBe("/cart");
    expect(row.vitals.inp.value).toBeNull();
    expect(row.vitals.lcp).toEqual({ value: 5, good: 5, ni: 5, poor: 5 });
  });

  it("lists routes worst score first", () => {
    const row = (page: string, s: VitalStat, views = 1) => ({ page, t: null, views, vitals: all(s) });
    const sorted = routesByScore([row("/a", stat(100, 0, 0)), row("/b", stat(0, 0, 0)), row("/c", stat(0, 0, 10))]);
    expect(sorted.map((r) => r.page)).toEqual(["/c", "/a", "/b"]);
  });

  it("asks for one device at one percentile", () => {
    const sql = speedPanel("routes", "mobile", "p90", "7d").sql ?? "";
    expect(sql).toContain("quantileIf(0.9)");
    expect(sql).toContain("IN ('mobile', 'tablet')");
    expect(sql).toContain("GROUP BY page");
    expect(speedPanel("series", "desktop", "p75", "24h").sql).toContain("INTERVAL 3600 SECOND");
  });
});
