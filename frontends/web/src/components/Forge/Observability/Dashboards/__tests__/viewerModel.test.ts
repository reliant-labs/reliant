// Copyright (c) 2025 Reliant Labs
import { describe, expect, it } from "vitest";
import type { Dashboard, Panel } from "@/services/controlPlane/observability/types";
import { breakdownFields, visiblePanel, viewerQuery } from "../viewerModel";

const panel = (id: string, signal = "traces"): Panel => ({ id, title: id, kind: "line", signal, series: [{ label: "calls", agg: "count" }], groupBy: ["service.name"] });
const trace = { ...panel("trace-rpc"), section: "activity" };
const metric = panel("metric-rpc", "metrics");
const runtime = panel("runtime", "metrics");
const money = { ...panel("money", ""), kind: "sql", section: "financial" };
const dashboard: Dashboard = {
  id: "process", title: "Process", sections: [{ id: "activity", title: "Activity" }, { id: "financial", title: "Financial" }],
  panels: [trace, metric, runtime, money],
  filters: [
    { id: "process", label: "Process", field: "service.name", panelIds: [trace.id, metric.id, runtime.id] },
    { id: "rpc-service", label: "RPC service", field: "span.attr.rpc.service", panelIds: [trace.id] },
    { id: "rpc-method", label: "RPC method", field: "span.attr.rpc.method", panelIds: [trace.id] },
    { id: "procedure", label: "RPC procedure", field: "metric.attr.procedure", signal: "metrics", panelIds: [metric.id] },
  ],
};

describe("dashboard viewer projection", () => {
  it("targets process/RPC/procedure independently and leaves financial facts untouched", () => {
    const search = { service: "api", rpcService: "pkg.Service", rpcMethod: "Get", rpcProcedure: "/pkg.Service/Get" };
    const result = viewerQuery(dashboard, trace, search);
    expect(result.services).toEqual(["api"]);
    expect(result.panel.where).toEqual([{ field: "span.attr.rpc.service", op: "eq", value: "pkg.Service" }, { field: "span.attr.rpc.method", op: "eq", value: "Get" }]);
    expect(viewerQuery(dashboard, metric, search).panel.where).toEqual([{ field: "metric.attr.procedure", op: "eq", value: "/pkg.Service/Get" }]);
    expect(viewerQuery(dashboard, runtime, search).panel).toBe(runtime);
    expect(viewerQuery(dashboard, money, search)).toEqual({ panel: money, filters: [], services: [] });
    expect(trace.where).toBeUndefined();
  });

  it("uses declared breakdown fields, explicit none, and saved grouping without mutating the spec", () => {
    expect(breakdownFields(dashboard)).toContain("span.attr.rpc.method");
    expect(viewerQuery(dashboard, trace, { breakdown: "span.attr.rpc.method" }).panel.groupBy).toEqual(["span.attr.rpc.method"]);
    expect(viewerQuery(dashboard, runtime, { breakdown: "span.attr.rpc.method" }).panel).toBe(runtime);
    expect(viewerQuery(dashboard, trace, { breakdown: "none" }).panel.groupBy).toEqual([]);
    expect(viewerQuery(dashboard, trace, {}).panel).toBe(trace);
    expect(viewerQuery(dashboard, trace, { breakdown: "invented.client" }).panel).toBe(trace);
    expect(trace.groupBy).toEqual(["service.name"]);
  });

  it("preserves independent section/metric choices when a section is re-enabled", () => {
    expect(visiblePanel(trace, { hiddenSections: ["activity"] })).toBe(false);
    expect(visiblePanel(trace, { hiddenSections: [], hiddenPanels: [trace.id] })).toBe(false);
    expect(visiblePanel(money, { hiddenSections: ["activity"], hiddenPanels: [trace.id] })).toBe(true);
  });

  it("empty panelIds applies to all; undeclared URL filters do nothing", () => {
    expect(viewerQuery({ ...dashboard, filters: [{ id: "process", label: "Process", field: "service.name", panelIds: [] }] }, runtime, { service: "worker" }).services).toEqual(["worker"]);
    expect(viewerQuery({ ...dashboard, filters: [] }, trace, { service: "worker", rpcMethod: "Get" }).panel).toBe(trace);
  });
});
