// Copyright (c) 2025 Reliant Labs

/**
 * The panel builder's output against forge's own validator.
 *
 * Every panel the builder makes, of every kind and display style, is written
 * to `__fixtures__/editor-panels.json` exactly as the console sends it
 * (dashboardToJson), beside panels the builder's checks refuse. This test
 * holds the fixture to the builder: it fails when the builder's output moves
 * and the fixture did not. Go's internal/dashspeccontract holds the fixture to
 * forge: forge's ParseDashboard (the pin control-plane validates every save
 * with) must accept every valid panel and refuse every invalid one. Together:
 * what the builder saves, forge accepts.
 *
 * After a deliberate change to the builder:
 *
 *   UPDATE_EDITOR_PANELS=1 npx vitest run src/components/Forge/Observability/Dashboards/__tests__/editorPanelsContract.test.ts
 */

import { readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { describe, expect, it } from "vitest";

import { canonicalJson } from "@/services/controlPlane/observability/canonicalJson";
import { dashboardToJson } from "@/services/controlPlane/observability/dashboardsApi";
import type { Dashboard, Panel } from "@/services/controlPlane/observability/types";

import { panelProblems } from "../../obsdashSpec";
import { DISPLAY_STYLES, starterPanel, VISUALIZATIONS, withDisplay, withKind, withSignal, type Visualization } from "../panelBuilder";

const FIXTURE = join(__dirname, "..", "__fixtures__", "editor-panels.json");

const shell: Dashboard = { id: "editor-panels", title: "Every panel the builder makes", filters: [], panels: [] };

/** A builder panel of `kind`, as the picker makes it from the starter panel, under its own id. */
function made(kind: Visualization, id: string = kind, finish: (panel: Panel) => Panel = (panel) => panel): Panel {
  return finish({ ...withKind(starterPanel(shell), kind), id, title: `${kind} ${id}` });
}

/** What the author must still give a kind the picker cannot complete on its own. */
function completed(panel: Panel): Panel {
  switch (panel.kind) {
    case "funnel":
      return { ...panel, funnel: { ...panel.funnel!, steps: ["signup_started", "signup_completed"] } };
    case "postgres":
      return { ...panel, postgres: { ...panel.postgres!, database: "app" } };
    default:
      return panel;
  }
}

function validPanels(): Panel[] {
  const panels = VISUALIZATIONS.map((v) => made(v.kind, v.kind, completed));
  // Every display style of every kind that takes one, with the options that style uses.
  for (const [kind, styles] of Object.entries(DISPLAY_STYLES)) {
    for (const style of styles ?? []) {
      let panel = made(kind as Visualization, `${kind}-${style}`, completed);
      if (style === "pie" || style === "donut") panel = { ...panel, series: panel.series.slice(0, 1) };
      panel = withDisplay(panel, { style });
      if (style === "area" || style === "bars" || (kind === "bar" && style === "bar")) panel = withDisplay(panel, { stack: true });
      if (kind === "bar" && style === "bar") panel = withDisplay(panel, { horizontal: true });
      if (style === "gauge") panel = withDisplay(panel, { min: 0, max: 500 });
      if ((kind === "line" || (kind === "sql" && style !== "funnel") || style === "pie" || style === "donut")) panel = withDisplay(panel, { topN: 8 });
      panels.push(panel);
    }
  }
  panels.push(
    made("number", "number-gauge-percent", (p) => withDisplay({ ...p, unit: "percent" }, { style: "gauge" })),
    made("bar", "bar-limited", (p) => ({ ...p, limit: 5 })),
    made("table", "table-limited", (p) => ({ ...p, limit: 20 })),
    made("heatmap", "heatmap-logs", (p) => ({ ...withSignal(p, "logs"), series: [{ label: "events", agg: "count", field: "log.attr.duration_ms" }], unit: "ms" })),
    made("line", "line-logs", (p) => withSignal(p, "logs")),
    made("line", "line-metrics", (p) => ({ ...withSignal(p, "metrics"), series: [{ label: "memory", agg: "avg", field: "go.memory.used", metricType: "gauge" }], unit: "bytes" })),
    made("sql", "sql-min-step", (p) => ({ ...p, minStep: "1d", sql: "SELECT toDate(Timestamp) AS day, count() AS events FROM product_events WHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)} GROUP BY day ORDER BY day" })),
    made("postgres", "postgres-line", (p) => ({
      ...p,
      unit: "count",
      postgres: { database: "app", draw: "line", time: "t", values: ["signups"], group: ["plan"], sql: "SELECT date_trunc('hour', created_at) AS t, plan, count(*) AS signups FROM users WHERE created_at >= $1 AND created_at < $2 GROUP BY 1, 2 ORDER BY 1" },
    })),
    made("postgres", "postgres-bar", (p) => ({ ...p, postgres: { database: "app", draw: "bar", sql: "SELECT state, count(*) AS orders FROM orders GROUP BY state" } })),
    made("text", "text-table", (p) => ({ ...p, markdown: "| service | owner |\n| --- | --- |\n| api | payments |" }))
  );
  return panels;
}

const base = (kind: string, extra: Partial<Panel>): Panel => ({ id: `bad-${kind}`, title: `bad ${kind}`, kind, signal: "traces", series: [{ label: "requests", agg: "count" }], ...extra });

/** Panels the builder's checks refuse: forge must refuse each too, or the builder would block a save forge takes. */
const INVALID: { why: string; panel: Panel }[] = [
  { why: "a text panel takes no series", panel: { id: "text-series", title: "t", kind: "text", signal: "", markdown: "hi", series: [{ label: "a", agg: "count" }] } },
  { why: "a text panel takes no signal", panel: { id: "text-signal", title: "t", kind: "text", signal: "traces", markdown: "hi", series: [] } },
  { why: "a text panel needs Markdown", panel: { id: "text-empty", title: "t", kind: "text", signal: "", markdown: "   ", series: [] } },
  { why: "a sql panel takes no filters", panel: { id: "sql-where", title: "s", kind: "sql", signal: "", sql: "SELECT 1", series: [], where: [{ field: "service.name", op: "eq", value: "api" }] } },
  { why: "a sql panel needs a statement", panel: { id: "sql-empty", title: "s", kind: "sql", signal: "", series: [] } },
  { why: "a sql panel's minStep is a span", panel: { id: "sql-step", title: "s", kind: "sql", signal: "", sql: "SELECT 1", minStep: "daily", series: [] } },
  { why: "a postgres line needs a time column", panel: { id: "pg-line", title: "p", kind: "postgres", signal: "", series: [], postgres: { database: "app", draw: "line", sql: "SELECT 1" } } },
  { why: "a postgres database is a lowercase name", panel: { id: "pg-db", title: "p", kind: "postgres", signal: "", series: [], postgres: { database: "App DB", sql: "SELECT 1" } } },
  { why: "a postgres panel takes no signal", panel: { id: "pg-signal", title: "p", kind: "postgres", signal: "traces", series: [], postgres: { database: "app", sql: "SELECT 1" } } },
  { why: "a postgres number shows one value", panel: { id: "pg-number", title: "p", kind: "postgres", signal: "", series: [], postgres: { database: "app", draw: "number", values: ["a", "b"], sql: "SELECT 1 AS a, 2 AS b" } } },
  { why: "a pie takes exactly one series", panel: base("bar", { id: "pie-two", groupBy: ["service.name"], display: { style: "pie" }, series: [{ label: "a", agg: "count" }, { label: "b", agg: "count" }] }) },
  { why: "a gauge needs a max", panel: base("number", { id: "gauge-max", display: { style: "gauge" } }) },
  { why: "stacked lines are refused", panel: base("line", { id: "line-stack", display: { stack: true } }) },
  { why: "topN does not apply to a bar panel's bars", panel: base("bar", { id: "bar-top-n", groupBy: ["service.name"], display: { topN: 5 } }) },
  { why: "horizontal applies to bars only", panel: base("bar", { id: "pie-horizontal", groupBy: ["service.name"], display: { style: "pie", horizontal: true } }) },
  { why: "a table is drawn one way", panel: base("table", { id: "table-display", display: { style: "line" } }) },
  { why: "a style the kind does not take", panel: base("line", { id: "line-pie", display: { style: "pie" } }) },
  { why: "a bar panel needs a group-by", panel: base("bar", { id: "bar-group" }) },
  { why: "a heatmap takes no group-by", panel: base("heatmap", { id: "heatmap-group", groupBy: ["service.name"], series: [{ label: "e", agg: "count", field: "span.duration" }] }) },
  { why: "a heatmap counts", panel: base("heatmap", { id: "heatmap-agg", series: [{ label: "e", agg: "avg", field: "span.duration" }] }) },
  { why: "a heatmap buckets a numeric field", panel: base("heatmap", { id: "heatmap-field", series: [{ label: "e", agg: "count", field: "span.name" }] }) },
  { why: "a heatmap is not over metrics", panel: base("heatmap", { id: "heatmap-metrics", signal: "metrics", series: [{ label: "e", agg: "count", field: "m", metricType: "histogram" }] }) },
  { why: "a filter names a field of another signal", panel: base("line", { id: "logs-span-kind", signal: "logs", where: [{ field: "span.kind", op: "eq", value: "server" }] }) },
  { why: "a group-by names a field of another signal", panel: base("table", { id: "metrics-span-name", signal: "metrics", groupBy: ["span.name"], series: [{ label: "m", agg: "avg", field: "go.memory.used", metricType: "gauge" }] }) },
  { why: "only a text panel takes Markdown", panel: base("line", { id: "line-markdown", markdown: "hi" }) },
  { why: "only a sql panel takes a statement", panel: base("line", { id: "line-sql", sql: "SELECT 1" }) },
];

const VALID = validPanels();

function fixture() {
  return {
    about: "Written by editorPanelsContract.test.ts from the panel builder; read by internal/dashspeccontract against forge's ParseDashboard. Do not edit by hand.",
    valid: dashboardToJson({ ...shell, panels: VALID }),
    invalid: INVALID.map(({ why, panel }) => ({ why, panel: canonicalJson(panel) })),
  };
}

describe("the builder's panels and forge's validator", () => {
  it("makes every kind and every display style complete by the builder's own checks", () => {
    const kinds = new Set(VALID.map((p) => p.kind));
    expect([...kinds].sort()).toEqual(VISUALIZATIONS.map((v) => v.kind).sort());
    for (const panel of VALID) expect({ id: panel.id, problems: panelProblems(panel) }).toEqual({ id: panel.id, problems: [] });
  });

  it("refuses every invalid panel, as forge does", () => {
    for (const { why, panel } of INVALID) expect({ why, refused: panelProblems(panel).length > 0 }).toEqual({ why, refused: true });
  });

  it("sends nothing a kind does not take, even empty", () => {
    const text = (fixture().valid.panels as Record<string, unknown>[]).find((p) => p.id === "text")!;
    expect(Object.keys(text).sort()).toEqual(["height", "id", "kind", "markdown", "title", "width"]);
  });

  it("matches the fixture forge's validator reads", () => {
    const want = `${JSON.stringify(fixture(), null, 2)}\n`;
    if (process.env.UPDATE_EDITOR_PANELS) writeFileSync(FIXTURE, want);
    expect(readFileSync(FIXTURE, "utf8"), "the builder's output moved: regenerate the fixture (see this file's header) and run internal/dashspeccontract").toBe(want);
  });
});
