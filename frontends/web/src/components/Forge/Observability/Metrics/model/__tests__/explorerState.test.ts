// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { MetricCatalogEntry } from "@/services/controlPlane/observability/metricsTypes";
import type { Panel, SeriesIntent } from "@/services/controlPlane/observability/types";

import { fixtureMetricEntry } from "../../../__fixtures__/metricFixtures";
import { panelProblems } from "../../../obsdashSpec";
import { MAX_PANEL_PARAM_CHARS } from "../canonical";
import {
  allowedAggs,
  breakdownPanel,
  canonicalPanel,
  catalogGroup,
  cliCommand,
  defaultSeriesFor,
  emptyState,
  explorerHref,
  flagsProblem,
  linkProblem,
  searchFromState,
  sqlPanelOf,
  stateFromSearch,
  topSeries,
  type ExplorerState,
} from "../explorerState";

const metric = (name: string): MetricCatalogEntry => fixtureMetricEntry(name)!;
const A: SeriesIntent = { label: "A", agg: "quantile", quantile: 0.95, field: "rpc.server.duration", metricType: "histogram" };
const B: SeriesIntent = { label: "B", agg: "rate", field: "rpc.server.calls", metricType: "sum", where: [{ field: "metric.attr.status", op: "eq", value: "error" }] };
const panel = (over: Partial<Panel> = {}): Panel => ({ id: "explore", title: "Explore", kind: "line", signal: "metrics", series: [A], ...over });
const state = (over: Partial<ExplorerState> = {}): ExplorerState => ({ ...emptyState(), panel: panel(), ...over });

describe("allowedAggs", () => {
  it("offers a counter rate and increase, rate first, and no quantile", () => {
    expect(allowedAggs(metric("orders.created"))).toEqual(["rate", "increase", "avg", "min", "max", "last"]);
  });

  it("offers an up-down counter no rate or increase", () => {
    const aggs = allowedAggs(metric("orders.in_flight"));
    expect(aggs[0]).toBe("sum");
    expect(aggs).not.toContain("rate");
    expect(aggs).not.toContain("increase");
    expect(aggs).not.toContain("quantile");
  });

  it("offers a gauge levels only", () => {
    expect(allowedAggs(metric("queue.depth"))).toEqual(["avg", "min", "max", "last", "sum"]);
  });

  it("offers a histogram a quantile first, and rate and count", () => {
    for (const name of ["checkout.latency", "http.client.duration"]) {
      const aggs = allowedAggs(metric(name));
      expect(aggs[0]).toBe("quantile");
      expect(aggs).toEqual(expect.arrayContaining(["rate", "count", "avg", "max"]));
    }
  });

  it("offers a summary avg, count and rate, never a quantile (they cannot be merged across series)", () => {
    expect(allowedAggs(metric("db.pool.wait"))).toEqual(["avg", "count", "rate"]);
  });

  it("offers an unknown kind nothing", () => {
    expect(allowedAggs({ kind: "unknown", monotonic: false })).toEqual([]);
  });

  it("only offers what the spec accepts", () => {
    for (const name of ["orders.created", "orders.in_flight", "queue.depth", "checkout.latency", "http.client.duration", "db.pool.wait"]) {
      const entry = metric(name);
      for (const agg of allowedAggs(entry)) {
        const series: SeriesIntent = { label: "A", agg, field: name, metricType: entry.kind, ...(agg === "quantile" ? { quantile: 0.5 } : {}) };
        expect(panelProblems(panel({ series: [series] })), `${name} ${agg}`).toEqual([]);
      }
    }
  });
});

describe("defaultSeriesFor", () => {
  it("defaults a counter to rate", () => {
    expect(defaultSeriesFor(metric("orders.created"), "A")).toEqual({ label: "A", agg: "rate", field: "orders.created", metricType: "sum" });
  });

  it("defaults an up-down counter to sum, not rate", () => {
    expect(defaultSeriesFor(metric("orders.in_flight"), "A")).toMatchObject({ agg: "sum", metricType: "sum" });
  });

  it("defaults a gauge to avg", () => {
    expect(defaultSeriesFor(metric("queue.depth"), "B")).toEqual({ label: "B", agg: "avg", field: "queue.depth", metricType: "gauge" });
  });

  it("defaults every histogram to p95", () => {
    for (const name of ["checkout.latency", "http.client.duration"]) {
      expect(defaultSeriesFor(metric(name), "A")).toMatchObject({ agg: "quantile", quantile: 0.95 });
    }
  });

  it("defaults a summary to avg", () => {
    expect(defaultSeriesFor(metric("db.pool.wait"), "A")).toMatchObject({ agg: "avg", metricType: "summary" });
    expect(defaultSeriesFor(metric("db.pool.wait"), "A").quantile).toBeUndefined();
  });

  it("is always a valid series, and its default is the first allowed agg", () => {
    for (const name of ["orders.created", "orders.in_flight", "queue.depth", "checkout.latency", "http.client.duration", "db.pool.wait"]) {
      const entry = metric(name);
      const series = defaultSeriesFor(entry, "A");
      expect(panelProblems(panel({ series: [series] })), name).toEqual([]);
      expect(allowedAggs(entry)[0] === series.agg || (name === "db.pool.wait" && series.agg === "avg"), name).toBe(true);
    }
  });
});

describe("catalogGroup", () => {
  it("mirrors obsquery's builtin rule: dotted prefixes only", () => {
    expect(catalogGroup("rpc.server.calls", true)).toBe("runtime");
    expect(catalogGroup("go.goroutine.count", false)).toBe("runtime");
    expect(catalogGroup("forge.metrics.rejected_attributes", false)).toBe("runtime");
    expect(catalogGroup("rpcx.thing", false)).toBe("other");
    // IsBuiltinMetric needs a name after the prefix.
    expect(catalogGroup("rpc.", false)).toBe("other");
    expect(catalogGroup("orders.created", true)).toBe("declared");
    expect(catalogGroup("k8s.container.ready", false)).toBe("infra");
    expect(catalogGroup("anything", false)).toBe("other");
  });
});

describe("canonicalPanel", () => {
  it("drops defaults and sorts keys, so equal panels are equal bytes", () => {
    const messy: Panel = {
      unit: "",
      width: 0,
      title: "Explore",
      signal: "metrics",
      id: "explore",
      kind: "line",
      groupBy: [],
      where: [],
      description: "",
      ratio: false,
      formula: "  ",
      limit: 0,
      series: [{ where: [], quantile: undefined, metricType: "histogram", field: "m", agg: "avg", label: "A" }],
    };
    const clean = panel({ series: [{ label: "A", agg: "avg", field: "m", metricType: "histogram" }] });
    expect(JSON.stringify(canonicalPanel(messy))).toBe(JSON.stringify(canonicalPanel(clean)));
    expect(JSON.stringify(canonicalPanel(clean))).toBe('{"id":"explore","kind":"line","series":[{"agg":"avg","field":"m","label":"A","metricType":"histogram"}],"signal":"metrics","title":"Explore"}');
  });

  it("is idempotent", () => {
    const p = panel({ series: [A, B], formula: "A / B", groupBy: ["status"], where: [{ field: "service.name", op: "in", values: ["api", "worker"] }] });
    expect(canonicalPanel(canonicalPanel(p))).toEqual(canonicalPanel(p));
  });

  it("keeps list order and a quantile only on a quantile series", () => {
    const out = canonicalPanel(panel({ series: [B, { ...A, agg: "avg" }], groupBy: ["b", "a"] }));
    expect(out.series.map((s) => s.label)).toEqual(["B", "A"]);
    expect(out.groupBy).toEqual(["b", "a"]);
    expect(out.series[1]!.quantile).toBeUndefined();
  });

  it("is stable whichever order the keys were written in", () => {
    const reordered = JSON.parse('{"title":"Explore","series":[{"label":"A","metricType":"histogram","quantile":0.95,"field":"rpc.server.duration","agg":"quantile"}],"signal":"metrics","kind":"line","id":"explore"}');
    expect(JSON.stringify(canonicalPanel(reordered))).toBe(JSON.stringify(canonicalPanel(panel())));
  });
});

describe("URL state", () => {
  const full = state({
    panel: panel({ series: [A, B], formula: "B / A", groupBy: ["status", "rpc.method"], limit: 10, kind: "table", where: [{ field: "service.name", op: "eq", value: "api" }] }),
    detailMetric: "rpc.server.duration",
    breakdownKey: "status",
    hidden: ["B", "a,b", "ü"],
    chart: "table",
    compare: "period",
  });

  it("round-trips a full state, unicode and commas in labels included", () => {
    const search = searchFromState(full);
    expect(Object.keys(search).filter((k) => search[k] !== undefined).sort()).toEqual(["bd", "compare", "hide", "m", "q"]);
    const back = stateFromSearch(search);
    expect(back).toEqual({ ...full, panel: canonicalPanel(full.panel) });
  });

  it("round-trips through a real query string", () => {
    const href = explorerHref("/forge/p/x/observability/metrics", full.panel, { ...full });
    const params = Object.fromEntries(new URL(href, "https://x.test").searchParams) as Record<string, string>;
    expect(stateFromSearch(params).panel).toEqual(canonicalPanel(full.panel));
  });

  it("round-trips an area chart, which shares the line kind", () => {
    const area = state({ chart: "area" });
    const search = searchFromState(area);
    expect(search.chart).toBe("area");
    expect(stateFromSearch(search).chart).toBe("area");
  });

  it("drops every default, so the empty state is a bare URL", () => {
    const search = searchFromState(emptyState());
    expect(Object.keys(search).filter((k) => search[k] !== undefined)).toEqual(["q"]);
    expect(explorerHref("/m", panel(), {})).toMatch(/^\/m\?q=[A-Za-z0-9_-]+$/);
  });

  it("derives the chart from the panel's kind when the URL has none", () => {
    const search = searchFromState(state({ panel: panel({ kind: "number" }), chart: "number" }));
    expect(stateFromSearch({ q: search.q }).chart).toBe("number");
    expect(stateFromSearch({ q: search.q }).panel.kind).toBe("number");
  });

  it("encodes the same state to the same link every time", () => {
    const reordered = { ...full, panel: JSON.parse(JSON.stringify(full.panel)) as Panel };
    expect(searchFromState(reordered)).toEqual(searchFromState(full));
  });

  it("opens an empty explorer on a damaged or missing q, never an error", () => {
    for (const q of [undefined, "", "not base64 !!", "e30", btoa("[]"), btoa("null"), "bm90IGpzb24"]) {
      const s = stateFromSearch({ q });
      expect(s.panel.signal).toBe("metrics");
      expect(s.hidden).toEqual([]);
    }
  });

  it("ignores an unknown compare mode and a malformed hide list", () => {
    const s = stateFromSearch({ compare: "yesterday", hide: "%E0%A4%A,ok" });
    expect(s.compare).toBe("off");
    expect(s.hidden).toEqual(["ok"]);
  });

  it("refuses a link over 8 KB with a message, and omits q rather than truncating", () => {
    const huge = state({ panel: panel({ formula: "A", series: Array.from({ length: 10 }, (_, i) => ({ label: String.fromCharCode(65 + i), agg: "avg", field: `metric.${"x".repeat(300)}.${i}`, metricType: "gauge", where: Array.from({ length: 10 }, (_, j) => ({ field: `metric.attr.k${j}`, op: "eq", value: "v".repeat(60) })) })) }) });
    expect(linkProblem(huge)).toMatch(/too large for a link \(\d+\.\d KB of 8 KB\)/);
    expect(searchFromState(huge).q).toBeUndefined();
    expect(linkProblem(full)).toBeNull();
    expect(searchFromState(full).q!.length).toBeLessThanOrEqual(MAX_PANEL_PARAM_CHARS);
  });

  it("decodes only a q within the cap", () => {
    expect(stateFromSearch({ q: "e".repeat(MAX_PANEL_PARAM_CHARS + 1) }).panel.series).toEqual([]);
  });

  it("round-trips the SQL tab and its panel, which implies the tab", () => {
    const sql = "SELECT 1 AS value FROM otel_metrics_sum WHERE TimeUnix >= {from:DateTime64(9)}";
    const withSql = state({ sqlPanel: sqlPanelOf(sql), tab: "sql" });
    const search = searchFromState(withSql);
    expect(search.tab).toBeUndefined();
    const back = stateFromSearch(search);
    expect(back.tab).toBe("sql");
    expect(back.sqlPanel).toEqual(canonicalPanel(sqlPanelOf(sql)));
    expect(stateFromSearch(searchFromState(state({ tab: "sql" }))).tab).toBe("sql");
    expect(stateFromSearch(searchFromState(state())).tab).toBeUndefined();
  });

  it("ignores a sql key that is not a SQL panel", () => {
    const notSql = searchFromState(state()).q;
    expect(stateFromSearch({ sql: notSql }).sqlPanel).toBeUndefined();
    expect(stateFromSearch({ sql: "garbage" }).tab).toBeUndefined();
  });
});

describe("breakdownPanel", () => {
  it("groups by the one key, replacing any other group-by, and does not mutate", () => {
    const p = panel({ groupBy: ["a", "b"] });
    const b = breakdownPanel(p, "rpc.method", 8);
    expect(b.groupBy).toEqual(["rpc.method"]);
    expect(b.id).toBe("explore-by-rpc.method");
    expect(b.title).toBe("Explore by rpc.method");
    expect(p.groupBy).toEqual(["a", "b"]);
  });

  it("gives an id the spec accepts, whatever the key", () => {
    const b = breakdownPanel(panel(), "metric.attr.http route/x y", 8);
    expect(panelProblems(b).filter((m) => m.includes("panel id"))).toEqual([]);
  });

  it("caps a table or bar at top rows (1 to 200), and a line has no limit", () => {
    expect(breakdownPanel(panel({ kind: "table" }), "k", 8).limit).toBe(8);
    expect(breakdownPanel(panel({ kind: "bar", limit: 3 }), "k", 500).limit).toBe(200);
    expect(breakdownPanel(panel({ kind: "bar" }), "k", 0).limit).toBe(1);
    expect(breakdownPanel(panel({ kind: "line", limit: 5 }), "k", 8).limit).toBeUndefined();
  });

  it("is a valid panel", () => {
    expect(panelProblems(breakdownPanel(panel(), "status", 8))).toEqual([]);
    expect(panelProblems(breakdownPanel(panel({ kind: "bar" }), "status", 8))).toEqual([]);
  });

  it("topSeries keeps the first n", () => {
    expect(topSeries([1, 2, 3, 4], 2)).toEqual([1, 2]);
    expect(topSeries([1], 8)).toEqual([1]);
    expect(topSeries([1, 2], -1)).toEqual([]);
  });
});

describe("explorerHref", () => {
  it("opens on the panel with its view state", () => {
    const href = explorerHref("/forge/p/x/observability/metrics", panel(), { detailMetric: "rpc.server.duration", compare: "period" });
    const url = new URL(href, "https://x.test");
    expect(url.pathname).toBe("/forge/p/x/observability/metrics");
    expect(url.searchParams.get("m")).toBe("rpc.server.duration");
    expect(url.searchParams.get("compare")).toBe("period");
    expect(stateFromSearch(Object.fromEntries(url.searchParams)).panel).toEqual(canonicalPanel(panel()));
  });

  it("decodes back to the pivoted panel (what R3's pivots assert)", () => {
    const pivot = panel({ series: [A, { ...B, agg: "rate" }], where: [{ field: "service.name", op: "eq", value: "api" }] });
    const params = Object.fromEntries(new URL(explorerHref("/m", pivot), "https://x.test").searchParams) as Record<string, string>;
    expect(stateFromSearch(params).panel).toEqual(canonicalPanel(pivot));
  });
});

describe("cliCommand", () => {
  it("emits a one-series quantile as flags (golden)", () => {
    expect(cliCommand("prod", state(), "1h")).toEqual({
      kind: "flags",
      text: "forge obs metrics query prod rpc.server.duration --agg quantile --quantile 0.95 --since 1h",
    });
  });

  it("emits a counter, group-by, filter and range", () => {
    const s = state({ panel: panel({ series: [{ label: "A", agg: "rate", field: "rpc.server.calls", metricType: "sum" }], groupBy: ["service.name"], where: [{ field: "service.name", op: "neq", value: "web" }] }) });
    expect(cliCommand("dev", s, "6h").text).toBe("forge obs metrics query dev rpc.server.calls --agg rate --filter 'service.name!=web' --group-by service.name --since 6h");
  });

  it("emits two queries, a per-series where, a formula, a top and a compare (golden)", () => {
    const s = state({
      panel: panel({ series: [A, B], formula: "B / A * 100", groupBy: ["rpc.method"], limit: 10, kind: "table" }),
      chart: "table",
      compare: "period",
    });
    expect(cliCommand("prod", s, "24h")).toEqual({
      kind: "flags",
      text: "forge obs metrics query prod rpc.server.duration --agg quantile --quantile 0.95 --query B=rpc.server.calls:rate --where B:metric.attr.status=error --group-by rpc.method --formula 'B / A * 100' --kind table --top 10 --compare 24h --since 24h",
    });
  });

  it("names a quantile in a --query spec, and a release compare", () => {
    const s = state({ panel: panel({ series: [{ ...B, where: [] }, { label: "P", agg: "quantile", quantile: 0.99, field: "rpc.server.duration", metricType: "histogram" }].map((x, i) => ({ ...x, label: i === 0 ? "A" : "P" })) }), compare: "release" });
    const text = cliCommand("prod", s, "1h").text;
    expect(text).toContain("--query P=rpc.server.duration:quantile:0.99");
    expect(text).toContain("--compare prev-release");
  });

  it("spells seven days in hours, because a duration has no day unit", () => {
    expect(cliCommand("prod", state(), "7d").text).toContain("--since 168h");
    expect(cliCommand("prod", state({ compare: "period" }), "7d").text).toContain("--compare 168h");
  });

  it("uses the chart's kind: number is --kind number, line and area are the default", () => {
    expect(cliCommand("p", state({ chart: "number", panel: panel({ kind: "number" }) }), "1h").text).toContain("--kind number");
    expect(cliCommand("p", state({ chart: "area" }), "1h").text).not.toContain("--kind");
  });

  it("falls back to --panel - with the panel JSON when a series is beyond the flags", () => {
    const beyond = state({ panel: panel({ series: [{ ...A, thresholds: { good: 1, poor: 2 } }] }) });
    const out = cliCommand("prod", beyond, "1h");
    expect(out.kind).toBe("panel");
    expect(out.text.startsWith("forge obs query prod --panel - --since 1h <<'EOF'\n")).toBe(true);
    expect(out.text.endsWith("\nEOF")).toBe(true);
    expect(JSON.parse(out.panelJson!)).toEqual(canonicalPanel(beyond.panel));
    expect(out.text).toContain(out.panelJson!);
  });

  it.each<[string, Partial<Panel>]>([
    ["a regex filter", { where: [{ field: "service.name", op: "match", value: "a.*" }] }],
    ["an in filter", { where: [{ field: "service.name", op: "in", values: ["a", "b"] }] }],
    ["a ratio", { ratio: true, series: [A, B] }],
    ["a bar chart's limit on a line", { limit: 5 }],
    ["a series not labelled A first", { series: [{ ...A, label: "X" }] }],
    ["a series with no metric type", { series: [{ label: "A", agg: "avg", field: "m" }] }],
    ["a quantile with no level", { series: [{ ...A, quantile: undefined }] }],
    ["a per-series regex", { series: [{ ...A, where: [{ field: "k", op: "prefix", value: "x" }] }] }],
    ["a sql panel", { sql: "SELECT 1" }],
    ["four group-bys", { groupBy: ["a", "b", "c", "d"] }],
  ])("falls back to --panel - for %s", (_name, patch) => {
    const s = state({ panel: panel(patch) });
    expect(flagsProblem(canonicalPanel(s.panel))).not.toBeNull();
    expect(cliCommand("prod", s, "1h").kind).toBe("panel");
  });

  it("falls back for a bar, which the flags cannot draw", () => {
    expect(cliCommand("prod", state({ chart: "bar", panel: panel({ groupBy: ["status"] }) }), "1h").kind).toBe("panel");
  });

  it("quotes an env or metric a shell would split", () => {
    expect(cliCommand("my env", state({ panel: panel({ series: [{ ...A, field: "a;b" }] }) }), "1h").text).toContain("forge obs metrics query 'my env' 'a;b'");
    expect(cliCommand("it's", state(), "1h").text).toContain("'it'\\''s'");
  });
});
