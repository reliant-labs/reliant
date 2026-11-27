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
  siteRows,
  siteServiceNames,
  sitesPanel,
  speedPanel,
  vitalScore,
  vitalsRows,
  VITAL_INFO,
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

  it("scores each vital on its log-normal curve: the good threshold 90, the poor threshold 50", () => {
    for (const v of VITALS) {
      const { good, poor } = VITAL_INFO[v];
      expect(vitalScore(v, stat(1, 0, 0, good))).toBe(90);
      expect(vitalScore(v, stat(1, 0, 0, poor))).toBe(50);
    }
    expect([vitalScore("lcp", stat(1, 0, 0, 2500)), vitalScore("inp", stat(1, 0, 0, 200)), vitalScore("cls", stat(1, 0, 0, 0.1)), vitalScore("fcp", stat(1, 0, 0, 1800))]).toEqual([90, 90, 90, 90]);
    expect([vitalScore("lcp", stat(1, 0, 0, 4000)), vitalScore("inp", stat(1, 0, 0, 500)), vitalScore("cls", stat(1, 0, 0, 0.25)), vitalScore("fcp", stat(1, 0, 0, 3000))]).toEqual([50, 50, 50, 50]);
    // A continuous curve, not a bucket: just under good is a little over 90, and a zero is perfect.
    const under = vitalScore("lcp", stat(1, 0, 0, 2400)) ?? 0;
    expect(under).toBeGreaterThan(90);
    expect(under).toBeLessThan(95);
    expect(vitalScore("cls", stat(1, 0, 0, 0))).toBe(100);
    expect(vitalScore("lcp", stat(1, 0, 0, 60_000))).toBeLessThan(1);
    expect(vitalScore("lcp", stat(0, 0, 0, null))).toBeNull();
  });

  it("scores the selected percentile, so P75 and P95 score differently", () => {
    const at = (k: "good" | "poor") => Object.fromEntries(VITALS.map((v) => [v, stat(100, 0, 0, VITAL_INFO[v][k])])) as Record<Vital, VitalStat>;
    // The same samples: every vital at its good threshold at P75, at its poor one at P95.
    const p75 = realExperienceScore(at("good"));
    const p95 = realExperienceScore(at("poor"));
    expect(p75).toBe(90);
    expect(p95).toBe(50);
    expect(p75).not.toBe(p95);
    expect(speedPanel("summary", "desktop", "p75", "7d").sql).toContain("quantileIf(0.75)");
    expect(speedPanel("summary", "desktop", "p95", "7d").sql).toContain("quantileIf(0.95)");
  });

  it("weights LCP 30, INP 30, CLS 25, FCP 15 and leaves TTFB out", () => {
    const good = (v: Vital) => stat(1, 0, 0, VITAL_INFO[v].good);
    const poor = (v: Vital) => stat(1, 0, 0, VITAL_INFO[v].poor);
    const vitals = { lcp: poor("lcp"), inp: good("inp"), cls: good("cls"), fcp: good("fcp"), ttfb: stat(0, 0, 1, 60_000) };
    // (30 × 50 + 30 × 90 + 25 × 90 + 15 × 90) / 100
    expect(realExperienceScore(vitals)).toBe(78);
    // (30 × 50 + 30 × 90 + 25 × 100 + 15 × 50) / 100 = 74.5
    expect(realExperienceScore({ ...vitals, cls: stat(1, 0, 0, 0), fcp: poor("fcp") })).toBe(75);
    // A vital with no sample drops out and the rest are reweighted: (30 × 50 + 25 × 90 + 15 × 90) / 70.
    expect(realExperienceScore({ ...vitals, inp: stat(0, 0, 0, null) })).toBe(73);
    expect(realExperienceScore(all(stat(0, 0, 0, null)))).toBeNull();
  });

  it("rates the score 90–100 great, 50–89 needs improvement, 0–49 poor", () => {
    expect([rateScore(100), rateScore(90), rateScore(89), rateScore(50), rateScore(49), rateScore(0), rateScore(null)]).toEqual(["good", "good", "ni", "ni", "poor", "poor", null]);
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
    const sorted = routesByScore([row("/a", stat(100, 0, 0, 1)), row("/b", stat(0, 0, 0, null)), row("/c", stat(0, 0, 10, 60_000))]);
    expect(sorted.map((r) => r.page)).toEqual(["/c", "/a", "/b"]);
  });

  it("asks for one device at one percentile", () => {
    const sql = speedPanel("routes", "mobile", "p90", "7d").sql ?? "";
    expect(sql).toContain("quantileIf(0.9)");
    expect(sql).toContain("IN ('mobile', 'tablet')");
    expect(sql).toContain("GROUP BY page");
    expect(speedPanel("series", "desktop", "p75", "24h").sql).toContain("INTERVAL 3600 SECOND");
  });

  it("keeps every panel to the query's sites: service.name in {services}, every site when it names none", () => {
    const filter = "(empty({services:Array(String)}) OR has({services:Array(String)}, ResourceAttributes['service.name']))";
    for (const kind of ["summary", "routes", "series"] as const) expect(speedPanel(kind, "desktop", "p75", "7d").sql).toContain(`AND ${filter}`);
    expect(sitesPanel().sql).toContain(`AND ${filter}`);
    // The panel ids are the fixture's contract, unchanged by the filter.
    expect(speedPanel("summary", "desktop", "p75", "7d").id).toBe("speed-insights-summary");
  });

  it("lists the sites that sent vitals, every device but bots, busiest first", () => {
    const sql = sitesPanel().sql ?? "";
    expect(sitesPanel().id).toBe("speed-insights-sites");
    expect(sql).toContain("ResourceAttributes['service.name'] AS service");
    expect(sql).toContain("!= 'bot'");
    expect(sql).not.toContain("IN ('mobile', 'tablet')");
    expect(sql).toContain("GROUP BY service");
    expect(sql).toContain("ORDER BY views DESC");
    const result = { columns: ["views"], rows: [{ group: { service: "shop-web" }, values: [42] }, { group: { service: "" }, values: [3] }] } as unknown as PanelResult;
    expect(siteRows(result)).toEqual([{ service: "shop-web", views: 42 }]);
  });

  it("names a frontend's telemetry services as forge does, and by its bare name", () => {
    expect(siteServiceNames("shop", "web")).toEqual(["shop-web", "web"]);
    expect(siteServiceNames("", "web")).toEqual(["web"]);
  });
});
