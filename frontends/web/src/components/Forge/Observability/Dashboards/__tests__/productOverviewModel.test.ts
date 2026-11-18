// Copyright (c) 2026 Reliant Labs

/**
 * The model behind the Product analytics Overview: "Last 30 days" in every
 * picker and what a view kept for less says, a sql panel's subtitle, the
 * hub's first tab, and the spec rules the console checks before a save.
 */

import { describe, expect, it } from "vitest";

import { panelRead } from "@/hooks/observability-queries";
import type { DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";
import type { Dashboard, Panel } from "@/services/controlPlane/observability/types";
import { dashboardDefaultRange, RANGE_LABELS, RANGE_PRESETS, rangeCapNote, readRange } from "@/services/controlPlane/observability/window";
import { dashboardProblems, panelProblems } from "../../obsdashSpec";
import { drillTarget, hubsOf } from "../dashboardHubs";
import { sqlSubtitlePanel, subtitleOf } from "../viewerModel";

const sql = (id: string, extra: Partial<Panel> = {}): Panel => ({ id, title: id, kind: "sql", signal: "", series: [], sql: "SELECT 1", ...extra });

describe("Last 30 days", () => {
  it("is in every picker, and a 30-day dashboard opens on it", () => {
    expect(RANGE_PRESETS).toContain("30d");
    expect(RANGE_LABELS["30d"]).toBe("Last 30 days");
    expect(dashboardDefaultRange("30d")).toBe("30d");
  });

  it("reads what a view keeps, and says so", () => {
    expect(readRange("30d", "logs")).toBe("7d");
    expect(readRange("30d", "metrics")).toBe("30d");
    expect(readRange("24h", "traces")).toBe("24h");
    expect(rangeCapNote("30d", "logs", "Logs are")).toBe("Logs are kept for 7 days, so this shows the last 7 days.");
    expect(rangeCapNote("7d", "logs", "Logs are")).toBeNull();
    expect(rangeCapNote("30d", "sql", "Events are")).toBeNull();
  });

  it("caps a dashboard panel by what it reads", () => {
    expect(panelRead({ kind: "sql", signal: "" })).toBe("sql");
    expect(panelRead({ kind: "line", signal: "logs" })).toBe("logs");
    expect(panelRead({ kind: "number", signal: "metrics" })).toBe("metrics");
    expect(panelRead({ kind: "table", signal: "traces" })).toBe("traces");
    expect(panelRead({ kind: "sessions", signal: "traces" })).toBe("sessions");
  });
});

describe("a sql panel's subtitle (subtitleSql)", () => {
  it("is read as a sql panel of its own", () => {
    const p = sql("new", { subtitleSql: "SELECT 'since 14 Nov'", trendSql: "SELECT 2" });
    expect(sqlSubtitlePanel(p)).toMatchObject({ id: "new.subtitle", sql: "SELECT 'since 14 Nov'", trendSql: undefined, subtitleSql: undefined });
    expect(sqlSubtitlePanel(sql("plain"))).toBeUndefined();
    expect(sqlSubtitlePanel({ ...sql("x"), kind: "line", subtitleSql: "SELECT 1" })).toBeUndefined();
  });

  it("is the first cell, and nothing for no row, an empty string or a NULL", () => {
    expect(subtitleOf({ rows: [{ group: { Subtitle: "since 14 Nov" }, values: [] }], series: [] })).toBe("since 14 Nov");
    expect(subtitleOf({ rows: [{ group: { Subtitle: "" }, values: [] }], series: [] })).toBeNull();
    expect(subtitleOf({ rows: [{ group: { Subtitle: "\\N" }, values: [] }], series: [] })).toBeNull();
    expect(subtitleOf({ rows: [], series: [] })).toBeNull();
    expect(subtitleOf({ rows: [{ group: {}, values: [12] }], series: [] })).toBe("12");
    expect(subtitleOf(undefined)).toBeNull();
  });
});

describe("the Product analytics hub", () => {
  const summary = (key: string, title: string) =>
    ({ ref: { kind: "release", projectId: "shop", key }, source: "generated", title, group: "product" }) as DashboardSummary;

  it("leads with the Overview, called Overview", () => {
    const hubs = hubsOf([summary("users", "shop: users"), summary("acquisition", "shop: acquisition"), summary("product-overview", "shop: product overview"), summary("product", "shop: product")], "shop");
    const product = hubs.find((h) => h.id === "product")!;
    expect(product.tabs.map((t) => [t.ref.key, t.label])).toEqual([
      ["product-overview", "Overview"],
      ["users", "Users"],
      ["product", "Product"],
      ["acquisition", "Acquisition"],
    ]);
  });

  it("drills to where the target dashboard lives, whatever this one is", () => {
    const hub = hubsOf([summary("product-overview", "shop: product overview"), summary("users", "shop: users")], "shop").find((h) => h.id === "product")!;
    // A workspace copy of the Overview drills to the project's own Users, not to a copy.
    expect(drillTarget({ kind: "workspace", projectId: "shop", key: "my-overview" }, "users", hub)).toEqual({ kind: "release", projectId: "shop", key: "users" });
    expect(drillTarget({ kind: "workspace", projectId: "shop", key: "my-overview" }, "acquisition", null)).toEqual({ kind: "release", projectId: "shop", key: "acquisition" });
    // A built-in drills to the built-in standing in for the target, and a hub tab's ref wins.
    expect(drillTarget({ kind: "builtin", projectId: "shop", key: "product-overview" }, "users", null)).toEqual({ kind: "builtin", projectId: "shop", key: "users" });
    const builtinHub = { tabs: [{ ref: { kind: "builtin" as const, projectId: "shop", key: "users" }, label: "Users", summary: summary("users", "Users") }] };
    expect(drillTarget({ kind: "release", projectId: "shop", key: "product-overview" }, "users", builtinHub)).toEqual({ kind: "builtin", projectId: "shop", key: "users" });
  });

  it("keeps an older project's tabs when it has no Overview", () => {
    const hubs = hubsOf([summary("users", "shop: users"), summary("product", "shop: product")], "shop");
    expect(hubs.find((h) => h.id === "product")!.tabs.map((t) => t.label)).toEqual(["Users", "Product"]);
  });
});

describe("the spec rules the console checks (forge dashspec)", () => {
  it("accepts the Overview's kinds and display fields", () => {
    expect(panelProblems(sql("visitors", { trendSql: "SELECT 1", display: { style: "kpi", good: "up", units: { DAU: "count" } }, drill: { dashboard: "acquisition", panel: "visitors" } }))).toEqual([]);
    expect(panelProblems(sql("channels", { trendSql: "SELECT 1", display: { style: "barlist", bar: "Visitors", share: true } }))).toEqual([]);
    expect(panelProblems(sql("funnel", { display: { style: "funnel", layout: "steps" } }))).toEqual([]);
    expect(panelProblems(sql("new", { subtitleSql: "SELECT 'since 14 Nov'" }))).toEqual([]);
    expect(panelProblems({ id: "f", title: "F", kind: "funnel", signal: "traces", series: [], funnel: { steps: ["a", "b"] }, display: { layout: "steps" } })).toEqual([]);
  });

  it("refuses an option a renderer would ignore", () => {
    expect(panelProblems(sql("x", { display: { good: "sideways" as "up" } }))).toEqual([expect.stringContaining("not a direction")]);
    expect(panelProblems(sql("x", { display: { style: "barlist", good: "up" } }))).toEqual([expect.stringContaining("Only a figure")]);
    expect(panelProblems(sql("x", { display: { bar: "n" } }))).toEqual([expect.stringContaining("Only a bar list")]);
    expect(panelProblems(sql("x", { display: { units: { n: "count" } } }))).toEqual([expect.stringContaining("own units")]);
    expect(panelProblems(sql("x", { display: { style: "bars", layout: "steps" } }))).toEqual([expect.stringContaining("Only a funnel")]);
    expect(panelProblems(sql("x", { display: { style: "barlist", topN: 3 } }))).toEqual([expect.stringContaining("no series to fold")]);
    expect(panelProblems({ ...sql("x"), kind: "line", signal: "traces", series: [{ label: "n", agg: "count" }], subtitleSql: "SELECT 1", sql: undefined })).toEqual([
      expect.stringContaining("Only a sql panel takes a subtitle statement"),
    ]);
  });

  it("checks what a Needs-attention panel watches", () => {
    const attention = (a: Panel["attention"]): Panel => ({ id: "w", title: "Needs attention", kind: "attention", signal: "", series: [], attention: a });
    const board = (...panels: Panel[]): Dashboard => ({ id: "d", title: "D", filters: [], annotations: [], panels });
    const good = sql("visitors", { display: { good: "up" } });
    expect(dashboardProblems(board(good, attention({ watch: ["visitors"], threshold: 0.05 })))).toEqual([]);
    expect(dashboardProblems(board(good, attention({ watch: ["gone"] })))).toEqual([expect.stringContaining('no panel "gone"')]);
    expect(dashboardProblems(board(sql("neutral"), attention({ watch: ["neutral"] })))).toEqual([expect.stringContaining("does not say which way is good")]);
    expect(dashboardProblems(board(sql("neutral"), attention(undefined)))).toEqual([expect.stringContaining("watches nothing")]);
    expect(dashboardProblems(board(good, attention({ figures: [sql("bounce")] })))).toEqual([expect.stringContaining("say which way is good")]);
    expect(dashboardProblems(board(good, attention({ threshold: 5 })))).toEqual([expect.stringContaining("threshold")]);
  });
});
