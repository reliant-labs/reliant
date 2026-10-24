// Copyright (c) 2025 Reliant Labs

/**
 * The console's copy of forge's panel rules (obsdashSpec.ts): what the panel
 * builder says before anything is saved. forge's own generated spec must pass
 * them (it is the reference), and each rule — the browser kinds' (C7)
 * included — is pinned by the sentence it shows.
 */

import { describe, expect, it } from "vitest";

import type { Dashboard, Panel } from "@/services/controlPlane/observability/types";

import forgeGolden from "../__fixtures__/forge-dashboard-spec.json?raw";
import { fixtureReleaseDashboards } from "../__fixtures__/fixtureApi";
import { containsPattern, dashboardProblems, fieldForKey, panelProblems, predicateForFilter } from "../obsdashSpec";

describe("forge's own dashboards", () => {
  it("forge's generated spec is valid by the console's rules", () => {
    const spec = JSON.parse(forgeGolden) as { dashboards: Dashboard[] };
    const problems = spec.dashboards.flatMap((d) => dashboardProblems({ ...d, filters: d.filters ?? [], panels: d.panels.map((p) => ({ ...p, series: p.series ?? [] })) }));
    expect(problems).toEqual([]);
  });

  it("the fixture's repo dashboards — the browser kinds included — are valid", () => {
    expect(fixtureReleaseDashboards().repo.flatMap(dashboardProblems)).toEqual([]);
  });
});

describe("validation", () => {
  const good: Panel = { id: "p", title: "Latency", kind: "line", signal: "traces", series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }] };

  it("accepts a well-formed panel", () => {
    expect(panelProblems(good)).toEqual([]);
  });

  it.each<[string, Partial<Panel>, string]>([
    ["unknown kind", { kind: "heatmap" }, "not a panel kind"],
    ["unknown signal", { signal: "profiles" }, "not a signal"],
    ["no title", { title: " " }, "title"],
    ["no series", { series: [] }, "at least one series"],
    ["quantile out of range", { series: [{ label: "q", agg: "quantile", quantile: 95, field: "span.duration" }] }, "fraction"],
    ["agg without a field", { series: [{ label: "s", agg: "sum" }] }, "needs a field"],
    ["unknown agg", { series: [{ label: "s", agg: "median", field: "x" }] }, "not an aggregation"],
    ["ratio of one series", { ratio: true }, "exactly two"],
    ["four group-bys", { groupBy: ["a", "b", "c", "d"] }, "at most three"],
    ["bad operator", { where: [{ field: "span.name", op: "like", value: "x" }] }, "not an operator"],
    ["eq without a value", { where: [{ field: "span.name", op: "eq" }] }, "give a value"],
    ["in without values", { where: [{ field: "span.name", op: "in", values: [] }] }, "at least one value"],
    ["width off the grid", { width: 30 }, "1 to 24"],
    ["funnel steps on a line", { funnel: { steps: ["a", "b"] } }, "Only a funnel panel"],
    ["has-error on a line", { hasError: true }, "Only a sessions panel"],
  ])("refuses %s", (_name, patch, message) => {
    expect(panelProblems({ ...good, ...patch }).join(" ")).toContain(message);
  });

  it("refuses two panels with one id", () => {
    expect(dashboardProblems({ id: "d", title: "D", filters: [], panels: [good, good] })).toEqual(['Two panels share the id "p".']);
  });
});

describe("metric aggregations (MX-F1's vocabulary)", () => {
  const metricPanel = (series: Panel["series"], over: Partial<Panel> = {}): Panel => ({ id: "m", title: "Metric", kind: "line", signal: "metrics", series, ...over });
  const s = (agg: string, metricType: string, extra: Record<string, unknown> = {}) => ({ label: "A", agg, field: "some.metric", metricType, ...extra });

  it.each([
    ["rate", "sum"],
    ["rate", "histogram"],
    ["rate", "exp_histogram"],
    ["rate", "summary"],
    ["increase", "sum"],
    ["increase", "histogram"],
    ["min", "gauge"],
    ["min", "sum"],
    ["count", "histogram"],
    ["count", "exp_histogram"],
    ["count", "summary"],
    ["avg", "summary"],
    ["last", "gauge"],
    ["sum", "gauge"],
    ["max", "histogram"],
  ])("accepts %s over a %s", (agg, type) => {
    expect(panelProblems(metricPanel([s(agg, type)]))).toEqual([]);
  });

  it.each([0.5, 0.9, 0.95, 0.99, 0.999, 1])("accepts any quantile level: %s", (q) => {
    expect(panelProblems(metricPanel([s("quantile", "histogram", { quantile: q })]))).toEqual([]);
    expect(panelProblems(metricPanel([s("quantile", "exp_histogram", { quantile: q })]))).toEqual([]);
  });

  it.each<[string, string, string, string]>([
    ["rate", "gauge", "", "does not grow"],
    ["increase", "gauge", "", "does not grow"],
    ["quantile", "sum", "", "needs a histogram"],
    ["quantile", "gauge", "", "needs a histogram"],
    ["quantile", "summary", "", "needs a histogram"],
    ["count", "gauge", "", "counts a histogram's observations"],
    ["count", "sum", "", "counts a histogram's observations"],
    ["count_distinct", "histogram", "", "not defined over a metric"],
    ["median", "sum", "", "not an aggregation"],
  ])("refuses %s over a %s", (agg, type, _x, message) => {
    expect(panelProblems(metricPanel([s(agg, type, agg === "quantile" ? { quantile: 0.9 } : {})])).join(" ")).toContain(message);
  });

  it("refuses an unknown metric type, naming the five kinds", () => {
    expect(panelProblems(metricPanel([s("avg", "counter")])).join(" ")).toContain("metric's type");
  });

  it("refuses rate, increase, min and last over traces and logs, which have no growth or level", () => {
    for (const agg of ["rate", "increase", "min", "last"]) {
      const problems = panelProblems({ id: "t", title: "T", kind: "line", signal: "traces", series: [{ label: "A", agg, field: "span.duration" }] }).join(" ");
      expect(problems, agg).toContain("defined for metrics");
    }
  });

  it("still accepts min and count over traces' own aggregations that existed before", () => {
    expect(panelProblems({ id: "t", title: "T", kind: "line", signal: "traces", series: [{ label: "A", agg: "count" }] })).toEqual([]);
    expect(panelProblems({ id: "t", title: "T", kind: "line", signal: "traces", series: [{ label: "A", agg: "max", field: "span.duration" }] })).toEqual([]);
  });

  it("validates a formula with forge's parser: a reference past the series is refused", () => {
    expect(panelProblems(metricPanel([s("rate", "sum"), { ...s("rate", "sum"), label: "B" }], { formula: "A / B * 100" }))).toEqual([]);
    expect(panelProblems(metricPanel([s("rate", "sum")], { formula: "A / C" })).join(" ")).toContain("Formula:");
    expect(panelProblems(metricPanel([s("rate", "sum")], { formula: "A +" })).join(" ")).toContain("Formula:");
  });

  it("refuses a formula on a table, and with a ratio", () => {
    expect(panelProblems(metricPanel([s("rate", "sum")], { kind: "table", formula: "A" })).join(" ")).toContain("line or number");
    expect(panelProblems(metricPanel([s("rate", "sum"), { ...s("rate", "sum"), label: "B" }], { ratio: true, formula: "A / B" })).join(" ")).toContain("formula or a ratio");
  });

  it("accepts a table and a bar over metrics, with a limit; a bar needs a group-by", () => {
    expect(panelProblems(metricPanel([s("rate", "sum")], { kind: "table", limit: 20 }))).toEqual([]);
    expect(panelProblems(metricPanel([s("rate", "sum")], { kind: "bar", groupBy: ["status"], limit: 10 }))).toEqual([]);
    expect(panelProblems(metricPanel([s("rate", "sum")], { kind: "bar" })).join(" ")).toContain("needs a group-by");
  });

  it.each<[Partial<Panel>, string]>([
    [{ kind: "line", limit: 5 }, "Only a table or bar panel takes a limit"],
    [{ kind: "table", limit: 201 }, "from 1 to 200"],
    [{ kind: "table", limit: 2.5 }, "from 1 to 200"],
    [{ kind: "table", limit: -1 }, "from 1 to 200"],
  ])("refuses limit %j", (patch, message) => {
    expect(panelProblems(metricPanel([s("rate", "sum")], patch)).join(" ")).toContain(message);
  });

  it("treats a zero limit as unset", () => {
    expect(panelProblems(metricPanel([s("rate", "sum")], { limit: 0 }))).toEqual([]);
  });

  it("keeps the old rule: a count over a metric is not defined for a counter", () => {
    expect(panelProblems(metricPanel([s("count", "sum")])).join(" ")).toContain("histogram");
  });
});

describe("the browser kinds (C7)", () => {
  const funnel: Panel = { id: "f", title: "Checkout", kind: "funnel", signal: "traces", series: [], funnel: { steps: ["view", "buy"], window: "1h", by: "session" } };

  it("accepts a funnel, an issues panel and a sessions panel, with an empty signal too", () => {
    expect(panelProblems(funnel)).toEqual([]);
    expect(panelProblems({ id: "i", title: "Issues", kind: "issues", signal: "", series: [] })).toEqual([]);
    expect(panelProblems({ id: "s", title: "Sessions", kind: "sessions", signal: "traces", series: [], hasError: true })).toEqual([]);
  });

  it.each<[string, Partial<Panel>, string]>([
    ["one step", { funnel: { steps: ["view"] } }, "2 to 10 steps"],
    ["eleven steps", { funnel: { steps: Array.from({ length: 11 }, (_, i) => `e${i}`) } }, "2 to 10 steps"],
    ["a repeated step", { funnel: { steps: ["view", "view"] } }, "different event"],
    ["a blank step", { funnel: { steps: ["view", " "] } }, "Name every funnel step"],
    ["a window in weeks", { funnel: { steps: ["a", "b"], window: "2w" } }, "s, m, h or d"],
    ["counted per device", { funnel: { steps: ["a", "b"], by: "device" } }, '"session" or per "user"'],
    ["series", { series: [{ label: "x", agg: "count" }] }, "takes no series"],
    ["a group-by", { groupBy: ["service.name"] }, "no ratio, group-by or columns"],
    ["logs", { signal: "logs" }, "its signal is traces"],
  ])("refuses a funnel with %s", (_name, patch, message) => {
    expect(panelProblems({ ...funnel, ...patch }).join(" ")).toContain(message);
  });
});

describe("filters and group-by in obsdash's vocabulary", () => {
  it("names resource keys as resource attributes and the signal's own keys as its attributes", () => {
    expect(fieldForKey("service.name", "traces")).toBe("service.name");
    expect(fieldForKey("service.version", "traces")).toBe("resource.attr.service.version");
    expect(fieldForKey("http.route", "traces")).toBe("span.attr.http.route");
    expect(fieldForKey("status", "traces")).toBe("span.status");
  });

  it("spells contains as a case-insensitive RE2 match, escaping the value", () => {
    expect(containsPattern("a.b")).toBe("(?i)a\\.b");
    expect(predicateForFilter({ key: "http.route", op: "contains", value: "orders" }, "traces")).toEqual({ field: "span.attr.http.route", op: "match", value: "(?i)orders" });
  });
});
