// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import type { Panel } from "@/services/controlPlane/observability/types";

import { kclText } from "../../alertsAsCode";
import { alertExprProblem, bindPanelSql, chString, ENV_ID_PARAM, intervalOf, MAX_EXPR_BYTES } from "../alertExpr";
import { buildAlertSnippet, defaultChoices, durationSeconds, labelName, ruleNameFor, unsupportedReason, type AlertChoices, type AlertScope } from "../alertSnippet";
import { formulaSql, parseFormula } from "../formula";

const scope: AlertScope = { envName: "prod", services: [], dashboardTitle: "Checkout latency" };

const p95: Panel = {
  id: "p95",
  title: "p95 checkout",
  kind: "number",
  signal: "traces",
  where: [
    { field: "span.kind", op: "eq", value: "server" },
    { field: "span.name", op: "eq", value: "orders.v1.OrdersService/CreateOrder" },
  ],
  series: [{ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" }],
  unit: "ms",
};

const errorRatio: Panel = {
  id: "err",
  title: "Error rate",
  kind: "line",
  signal: "traces",
  ratio: true,
  unit: "percent",
  groupBy: ["service.name"],
  series: [
    { label: "errors", agg: "count", where: [{ field: "span.status", op: "eq", value: "error" }] },
    { label: "requests", agg: "count" },
  ],
};

const sqlPanel: Panel = {
  id: "q",
  title: "Slow queries",
  kind: "sql",
  signal: "",
  series: [],
  sql: "SELECT ServiceName AS service, quantile(0.95)(Duration) / 1e6 AS value\nFROM otel_traces\nWHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}\n  AND (empty({services:Array(String)}) OR has({services:Array(String)}, ServiceName))\nGROUP BY service",
};

const choices = (panel: Panel, patch: Partial<AlertChoices> = {}): AlertChoices => ({ ...defaultChoices(panel), ...patch });
const ok = (panel: Panel, patch: Partial<AlertChoices> = {}, s: AlertScope = scope) => {
  const result = buildAlertSnippet(panel, choices(panel, patch), s);
  if (result.status !== "ok") throw new Error(`unsupported: ${result.reason}`);
  return result;
};

describe("alertable panels", () => {
  it("builds a rule from a builder panel: its filters, aggregate and threshold, scoped to the environment", () => {
    const r = ok(p95, { threshold: "800", comparator: "above", window: "10m", hold: "5m", severity: "critical", name: "CheckoutSlow" });
    expect(r.ready).toBe(true);
    expect(r.expr).toContain("FROM otel_traces");
    expect(r.expr).toContain("TraceState");
    expect(r.expr).toContain("quantileExactWeightedIf(0.95)((Duration / 1000000), toUInt64(round(w)), 1)");
    expect(r.expr).toContain("Timestamp >= now() - INTERVAL 10 MINUTE");
    expect(r.expr).toContain(`ResourceAttributes['reliant.env.id'] = ${ENV_ID_PARAM}`);
    expect(r.expr).toContain("lower(replaceOne(SpanKind, 'SPAN_KIND_', '')) = 'server'");
    expect(r.expr).toContain("SpanName = 'orders.v1.OrdersService/CreateOrder'");
    expect(r.expr).toContain("HAVING value > 800");
    expect(r.rule).toContain('alert = "CheckoutSlow"');
    expect(r.rule).not.toContain('"for"');
    expect(ok(p95, { hold: "10m" }).rule).toContain('"for" = "10m"');
    expect(r.rule).toContain('labels = {severity = "critical"}');
    expect(r.path).toBe("deploy/kcl/prod/main.k");
    expect(r.block).toMatch(/^observability = forge\.Observability \{\n {4}alerts = forge\.Alerts \{\n {8}rules = \[/);
  });

  it("compares in the direction chosen, and keeps the worst offenders when more instances fire than are kept", () => {
    expect(ok(p95, { comparator: "below" }).expr).toContain("HAVING value < ");
    const grouped = ok(errorRatio, { comparator: "below_or_equal", threshold: "1" });
    expect(grouped.expr).toContain("HAVING value <= 0.01");
    expect(grouped.expr).toContain("ORDER BY value ASC\nLIMIT 200");
    expect(ok(errorRatio).expr).toContain("ORDER BY value DESC\nLIMIT 200");
  });

  it("turns a ratio into a guarded division, a percent threshold into a fraction, and a group-by into labels", () => {
    const r = ok(errorRatio, { threshold: "5" });
    expect(r.expr).toContain("ifNotFinite(");
    expect(r.expr).toMatch(/ifNotFinite\(ifNull\(toFloat64\(sumIf\(w, .*\)\), 0\) \/ ifNull\(toFloat64\(sumIf\(w, 1\)\), 0\), 0\)/);
    expect(r.expr).toContain("toString(ServiceName) AS service");
    expect(r.expr).toContain("GROUP BY service");
    expect(r.expr).toContain("HAVING value > 0.05");
    expect(r.rule).toContain("{{ $labels.service }}: Error rate is {{ $value }}");
  });

  it("evaluates a formula the way the panel does, over the series it names", () => {
    const formula: Panel = { ...errorRatio, ratio: undefined, formula: "A / B * 100", unit: undefined };
    const r = ok(formula, { threshold: "5" });
    expect(r.expr).toMatch(/ifNotFinite\(ifNotFinite\(ifNull\(.*\), 0\) \* 100, 0\) AS value/s);
    expect(r.notes[0]).toContain("A / B * 100");
    const broken = buildAlertSnippet({ ...formula, formula: "A / Z" }, choices(formula), scope);
    expect(broken).toMatchObject({ status: "unsupported", reason: expect.stringContaining('"Z" names a series the panel does not have') });
  });

  it("watches the chosen series when a panel plots several, and a logs panel counts unweighted", () => {
    const logs: Panel = {
      id: "l",
      title: "Error logs",
      kind: "line",
      signal: "logs",
      series: [
        { label: "errors", agg: "count", where: [{ field: "log.severity", op: "gte", value: "error" }] },
        { label: "all", agg: "count" },
      ],
    };
    const first = ok(logs);
    expect(first.expr).toContain("FROM otel_logs");
    expect(first.expr).toContain("countIf(upper(SeverityText) IN ('ERROR', 'ERR', 'FATAL', 'CRITICAL', 'PANIC'))");
    expect(first.expr).not.toContain("TraceState");
    expect(ok(logs, { series: 1 }).expr).toContain("countIf(1)");
  });

  it("scopes to the dashboard's services", () => {
    const r = ok(p95, {}, { ...scope, services: ["api", "worker"] });
    expect(r.expr).toContain("ServiceName IN ('api', 'worker')");
    expect(r.notes.join(" ")).toContain("api, worker");
  });

  it("converts a gauge metric, reading the latest value of each series", () => {
    const gauge: Panel = {
      id: "g",
      title: "Goroutines",
      kind: "line",
      signal: "metrics",
      series: [{ label: "goroutines", agg: "avg", field: "go.goroutine.count", metricType: "gauge" }],
      groupBy: ["service.name"],
    };
    const r = ok(gauge, { threshold: "5000" });
    expect(r.expr).toContain("MetricName = 'go.goroutine.count'");
    expect(r.expr).toContain("argMax(Value, TimeUnix) AS v");
    expect(r.expr).toContain("FROM otel_metrics_gauge");
    expect(r.expr).toContain("avg(v) AS value");
    expect(r.expr).toContain("HAVING value > 5000");
  });

  it("refuses counters and histograms, which need the differences between exports, and says what to copy", () => {
    const counter: Panel = { id: "c", title: "Requests", kind: "line", signal: "metrics", series: [{ label: "r", agg: "sum", field: "http.requests", metricType: "sum" }] };
    expect(buildAlertSnippet(counter, choices(counter), scope)).toMatchObject({ status: "unsupported", reason: expect.stringContaining("counter"), instead: expect.stringContaining("alert-defaults_gen.json") });
    const hist: Panel = { ...counter, series: [{ label: "l", agg: "quantile", quantile: 0.95, field: "rpc.server.duration", metricType: "histogram" }] };
    expect(buildAlertSnippet(hist, choices(hist), scope)).toMatchObject({ status: "unsupported", reason: expect.stringContaining("histogram") });
  });
});

describe("a SQL panel", () => {
  it("wraps the panel's query, binds its scope parameters to the alert's window and environment, and filters on value", () => {
    const r = ok(sqlPanel, { threshold: "500", window: "15m" });
    expect(r.expr).toMatch(/^SELECT \*\nFROM \(\n {2}SELECT ServiceName AS service/);
    expect(r.expr).toContain("Timestamp >= (now64(9) - INTERVAL 15 MINUTE) AND Timestamp < now64(9)");
    expect(r.expr).toContain("empty(CAST([], 'Array(String)'))");
    expect(r.expr).toContain("\n)\nWHERE value > 500\nORDER BY value DESC\nLIMIT 200");
    expect(r.expr).not.toMatch(/\{(from|to|services)/);
    expect(r.ready).toBe(true);
  });

  it("binds the service filter and the environment name, and leaves literals alone", () => {
    const bound = bindPanelSql("SELECT {envs:Array(String)} AS e, '{from:DateTime64(9)}' AS lit, {services:Array(String)} AS s -- {to:DateTime64(9)}", {
      windowSeconds: 300,
      envName: "prod",
      services: ["api"],
    });
    expect(bound.sql).toBe("SELECT ['prod'] AS e, '{from:DateTime64(9)}' AS lit, ['api'] AS s -- {to:DateTime64(9)}");
    expect(bound.unbound).toEqual([]);
  });

  it("explains a parameter an alert is not given instead of writing a rule that cannot run", () => {
    const custom = { ...sqlPanel, sql: "SELECT {threshold:Float64} AS value" };
    expect(buildAlertSnippet(custom, choices(custom), scope)).toMatchObject({ status: "unsupported", reason: expect.stringContaining("{threshold:Float64}") });
  });

  it("warns when there is no value column, and drops a trailing semicolon", () => {
    const noValue = { ...sqlPanel, sql: "SELECT count() AS n FROM otel_logs;" };
    const r = ok(noValue);
    expect(r.warnings[0]).toContain("no column named value");
    expect(r.expr).not.toContain(";");
  });

  it("refuses SQL the control plane would refuse, with the reason", () => {
    const bad = { ...sqlPanel, sql: "SELECT * FROM system.users" };
    const r = ok(bad);
    expect(r.ready).toBe(false);
    expect(r.exprProblem).toContain('"system"');
  });
});

describe("panels that cannot become an alert", () => {
  const cases: [string, Panel, RegExp, RegExp][] = [
    ["a heatmap", { id: "h", title: "Latency", kind: "heatmap", signal: "traces", series: [{ label: "latency", agg: "count", field: "span.duration" }] }, /distributed over time/, /p95 \(or max\) series over span\.duration/],
    ["a funnel", { id: "f", title: "Checkout", kind: "funnel", signal: "traces", series: [], funnel: { steps: ["a", "b"] } }, /conversion across ordered steps/, /one step's event/],
    ["an issues list", { id: "i", title: "Issues", kind: "issues", signal: "traces", series: [] }, /grouped errors/, /how many there are/],
    ["a sessions list", { id: "s", title: "Sessions", kind: "sessions", signal: "traces", series: [] }, /browser sessions/, /sessions with an error/],
    ["a rows panel", { id: "r", title: "Recent", kind: "search", signal: "logs", series: [], columns: ["log.body"] }, /individual log lines/, /how many rows match/],
  ];
  for (const [name, panel, reason, instead] of cases) {
    it(`refuses ${name} and says what to build instead`, () => {
      const r = buildAlertSnippet(panel, defaultChoices(panel), scope);
      expect(r).toMatchObject({ status: "unsupported" });
      if (r.status === "unsupported") {
        expect(r.reason).toMatch(reason);
        expect(r.instead).toMatch(instead);
      }
      expect(unsupportedReason(panel)).not.toBeNull();
    });
  }

  it("refuses an unknown kind and an unset signal", () => {
    expect(unsupportedReason({ id: "x", title: "x", kind: "gauge", signal: "traces", series: [] })).toMatchObject({ reason: expect.stringContaining('"gauge"') });
    expect(unsupportedReason({ id: "x", title: "x", kind: "line", signal: "", series: [] })).toMatchObject({ reason: expect.stringContaining("choose traces, logs or metrics") });
  });
});

describe("the form's choices", () => {
  it("names each problem on its field and writes no rule until they are fixed", () => {
    const r = ok(p95, { name: "9bad name", threshold: "fast", window: "30s", hold: "five" });
    expect(r.fieldProblems).toMatchObject({ name: expect.stringContaining("Letters"), threshold: "A number, like 0.5.", window: expect.stringContaining("At least 1m"), hold: expect.stringContaining("Like") });
    expect(r.expr).toBeNull();
    expect(r.rule).toBeNull();
    expect(r.ready).toBe(false);
    expect(ok(p95, { window: "169h" }).fieldProblems.window).toContain("At most 7 days");
    expect(ok(p95, { threshold: "-3" }).fieldProblems.threshold).toContain("never negative");
  });

  it("refuses a name the environment already declares", () => {
    const r = buildAlertSnippet(p95, choices(p95, { name: "CheckoutSlow" }), scope, { recorded: true, own: ["CheckoutSlow"], all: ["CheckoutSlow", "ErrorRate"] });
    expect(r).toMatchObject({ fieldProblems: { name: expect.stringContaining("already declares a rule called CheckoutSlow") }, ready: false });
  });

  it("starts from the panel: a name from its title, a threshold from its unit, the web-vitals 'poor' bound when it has one", () => {
    expect(defaultChoices(p95)).toMatchObject({ name: "P95CheckoutHigh", comparator: "above", threshold: "500", window: "5m", hold: "5m", severity: "warning" });
    expect(defaultChoices(errorRatio).threshold).toBe("5");
    const vital = { ...p95, series: [{ label: "lcp", agg: "quantile", quantile: 0.75, field: "span.duration", thresholds: { good: 2500, poor: 4000 } }] };
    expect(defaultChoices(vital).threshold).toBe("4000");
    expect(ruleNameFor("99% error-rate!", "below")).toBe("99ErrorRateLow".replace(/^/, "Panel"));
    expect(ruleNameFor("", "above")).toBe("PanelHigh");
  });

  it("reads durations the way forge's KCL does", () => {
    expect(durationSeconds("90s")).toBe(90);
    expect(durationSeconds("1h30m")).toBe(5400);
    expect(durationSeconds("5")).toBeNull();
    expect(durationSeconds("1d")).toBeNull();
    expect(intervalOf(7200)).toBe("2 HOUR");
    expect(intervalOf(90)).toBe("90 SECOND");
  });

  it("makes group-by labels that cannot collide with the value column or the platform's", () => {
    expect(labelName("service.name", new Set())).toBe("service");
    expect(labelName("span.attr.http.route", new Set())).toBe("http_route");
    expect(labelName("span.name", new Set())).toBe("name");
    expect(labelName("span.attr.value", new Set())).toBe("value_");
    expect(labelName("resource.attr.reliant.env", new Set())).toBe("reliant_env_");
    expect(labelName("span.attr.a.b", new Set(["a_b"]))).toBe("a_b_2");
  });
});

describe("the alert-rule SQL guard", () => {
  it("accepts one read-only SELECT and refuses what forge's guard refuses", () => {
    for (const sql of ["SELECT 1 AS value", "with x as (select 1) select * from x", "SELECT 'drop table; x' AS value".replace(";", "\\x3B"), "SELECT `update` AS value"]) {
      expect(alertExprProblem(sql), sql).toBeNull();
    }
    const refused: [string, RegExp][] = [
      ["", /empty/],
      ["INSERT INTO t VALUES (1)", /must start with SELECT/],
      ["SELECT 1 AS value; DROP TABLE t", /more than one statement/],
      ["SELECT * FROM system.users", /system/],
      ["SELECT 1 SETTINGS readonly = 0", /settings/i],
      ["SELECT 1 FORMAT Native", /FORMAT/],
      ["SELECT * FROM url('http://x', 'CSV')", /url\(\)/],
      ["SELECT * FROM URL ('http://x', 'CSV')", /url\(\)/],
      ["SELECT 'oops", /unterminated/],
      ["SELECT 1 /* oops", /unterminated/],
      ["SELECT 1\0", /NUL/],
      ["/* hi */ DROP TABLE t", /must start with SELECT/],
    ];
    for (const [sql, why] of refused) expect(alertExprProblem(sql), sql).toMatch(why);
    expect(alertExprProblem("SELECT '" + "a".repeat(MAX_EXPR_BYTES) + "'")).toContain("longer than");
  });

  it("also refuses what only forge.AlertRule's schema refuses: a ';' inside a string, and a leading comment", () => {
    expect(alertExprProblem("SELECT 'a;b' AS value")).toContain("';'");
    expect(alertExprProblem("-- why\nSELECT 1 AS value")).toContain("leading comment");
  });

  it("holds the sampling-weight expression to its own rule: no semicolon anywhere", () => {
    expect(ok(p95).expr).not.toContain(";");
    expect(ok(p95).exprProblem).toBeNull();
  });

  it("quotes ClickHouse strings the way the server does", () => {
    expect(chString("it's a \\ path\n")).toBe("'it\\'s a \\\\ path\\n'");
  });
});

describe("formulas", () => {
  it("parses forge's grammar, and refuses what forge refuses", () => {
    expect(formulaSql(parseFormula("A / B * 100", 2), (i) => `v${i}`)).toBe("ifNotFinite(ifNotFinite(v0 / v1, 0) * 100, 0)");
    expect(formulaSql(parseFormula("-(A + 2.5e1) / (B - 1)", 2), (i) => `v${i}`)).toBe("ifNotFinite(ifNotFinite(-(v0 + 2.5e1) / (v1 - 1), 0), 0)");
    expect(() => parseFormula("A +", 2)).toThrow(/ends too early/);
    expect(() => parseFormula("A / C", 2)).toThrow(/"C" names a series/);
    expect(() => parseFormula("1 + 2", 2)).toThrow(/uses no series/);
    expect(() => parseFormula("A $ B", 2)).toThrow(/unexpected "\$"/);
    expect(() => parseFormula("(".repeat(40) + "A" + ")".repeat(40), 1)).toThrow(/nested too deeply/);
    expect(() => parseFormula("A".repeat(300), 1)).toThrow(/longer than 256/);
  });
});

describe("multi-line SQL in KCL", () => {
  it("writes a triple-quoted block that KCL reads back as the same text", () => {
    const sql = "SELECT a,\n  b\nFROM t\nWHERE x = '\\\\.' AND y = ${z}";
    const kcl = kclText(sql);
    expect(kcl.startsWith('"""\\\n')).toBe(true);
    expect(kcl.endsWith('\n"""')).toBe(true);
    expect(kcl).toContain("\\${z}");
    expect(kcl).toContain("'\\\\\\\\.'");
    expect(kclText("SELECT 1")).toBe('"SELECT 1"');
  });
});
