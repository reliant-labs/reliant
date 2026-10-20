// Copyright (c) 2025 Reliant Labs

/**
 * The panel builder's moves, each a function from panel to panel: every one
 * leaves a panel forge accepts (obsdashSpec's rules), so the builder can
 * never produce a spec the server would refuse for leftovers.
 */

import { describe, expect, it } from "vitest";

import type { Dashboard, Panel } from "@/services/controlPlane/observability/types";

import { panelProblems } from "../../obsdashSpec";
import {
  addSeries,
  duplicatePanel,
  followUnit,
  parsePanelJson,
  presetOf,
  removeSeries,
  starterPanel,
  suggestedUnit,
  upsertPanel,
  withErrorsOnly,
  withKind,
  withPreset,
  withRatio,
  withSignal,
} from "../panelBuilder";

const dashboard: Dashboard = { id: "d", title: "D", filters: [], panels: [{ id: "panel-1", title: "Requests", kind: "line", signal: "traces", series: [{ label: "requests", agg: "count" }] }] };
const starter = starterPanel(dashboard);

describe("the builder's moves", () => {
  it("starts from a valid panel with an unused id", () => {
    expect(starter.id).toBe("panel-2");
    expect(panelProblems(starter)).toEqual([]);
  });

  it("sets aggregation presets, with the field each needs, and reads them back", () => {
    const p95 = withPreset(starter.series[0], "p95", "traces");
    expect(p95).toEqual({ label: "p95", agg: "quantile", quantile: 0.95, field: "span.duration" });
    expect(presetOf(p95)).toBe("p95");
    const count = withPreset(p95, "count", "traces");
    expect(count).toMatchObject({ label: "requests", agg: "count" });
    expect(count.quantile).toBeUndefined();
    // A label the author wrote survives a preset change.
    expect(withPreset({ ...p95, label: "checkout" }, "p99", "traces").label).toBe("checkout");
  });

  it("narrows a series to errors in the signal's own field, and back", () => {
    const traces = withErrorsOnly(starter.series[0], true, "traces");
    expect(traces.where).toEqual([{ field: "span.status", op: "eq", value: "error" }]);
    expect(withErrorsOnly(traces, false, "traces").where).toBeUndefined();
    expect(withErrorsOnly(starter.series[0], true, "logs").where).toEqual([{ field: "log.severity", op: "eq", value: "error" }]);
  });

  it("makes a ratio an error rate in percent: failed over all", () => {
    const ratio = withRatio(starter, true);
    expect(ratio).toMatchObject({ ratio: true, unit: "percent", kind: "line" });
    expect(ratio.series.map((s) => [s.label, s.where?.length ?? 0])).toEqual([
      ["errors", 1],
      ["requests", 0],
    ]);
    expect(panelProblems(ratio)).toEqual([]);
    expect(removeSeries(ratio, 1).ratio).toBeUndefined();
  });

  it.each(["line", "number", "table", "funnel", "issues", "sessions", "search"] as const)("switches to %s leaving nothing that kind refuses", (kind) => {
    const panel = withKind({ ...withRatio(starter, true), groupBy: ["span.name"] }, kind);
    const problems = panelProblems(kind === "funnel" ? { ...panel, funnel: { steps: ["view", "buy"] } } : panel);
    expect(problems).toEqual([]);
  });

  it("clears what belonged to the old signal", () => {
    const metrics = withSignal({ ...starter, series: [withErrorsOnly(starter.series[0], true, "traces")] }, "metrics");
    expect(metrics.series[0]).toMatchObject({ agg: "avg", field: "", metricType: "gauge", where: [] });
    expect(withSignal(metrics, "logs").series[0].metricType).toBeUndefined();
  });

  it("follows the unit to what is measured, unless the author picked one", () => {
    const p95: Panel = { ...starter, series: [withPreset(starter.series[0], "p95", "traces")] };
    expect(suggestedUnit(p95)).toBe("ms");
    expect(followUnit(starter, p95).unit).toBe("ms");
    expect(followUnit({ ...starter, unit: "bytes" }, { ...p95, unit: "bytes" }).unit).toBe("bytes");
  });

  it("names new series a, b, c", () => {
    expect(addSeries(addSeries(starter)).series.map((s) => s.label)).toEqual(["requests", "a", "b"]);
  });
});

describe("panels in a dashboard", () => {
  it("duplicates after the original, and upserts by id", () => {
    const twice = duplicatePanel(dashboard, "panel-1");
    expect(twice.panels.map((p) => [p.id, p.title])).toEqual([
      ["panel-1", "Requests"],
      ["panel-2", "Requests (copy)"],
    ]);
    expect(upsertPanel(twice, { ...twice.panels[0], title: "Renamed" }).panels[0].title).toBe("Renamed");
    expect(upsertPanel(twice, starterPanel(twice)).panels).toHaveLength(3);
  });

  it("parses a panel typed as JSON, or says why not", () => {
    expect(parsePanelJson('{"id":"x","kind":"issues","title":"I","signal":"traces"}')).toMatchObject({ panel: { id: "x", series: [] } });
    expect(parsePanelJson("{")).toMatchObject({ problem: expect.stringContaining("Not JSON") });
    expect(parsePanelJson("[]")).toEqual({ problem: "A panel is one JSON object." });
    expect(parsePanelJson('{"title":"x"}')).toEqual({ problem: 'A panel needs an "id" and a "kind".' });
  });
});
