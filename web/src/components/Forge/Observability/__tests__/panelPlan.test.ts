// Copyright (c) 2025 Reliant Labs

/** How forge's dashboard panels map onto the RPCs control-plane offers. */

import { describe, expect, it } from "vitest";

import { fixtureDashboards } from "../__fixtures__/fixtureApi";
import { planPanel } from "../panelPlan";

const overview = fixtureDashboards().dashboards[0];
const service = fixtureDashboards().dashboards[1];
const panel = (dashboard: typeof overview, id: string) => dashboard.panels.find((p) => p.id === id)!;

describe("planPanel", () => {
  it("answers service-level RED from the service overview", () => {
    expect(planPanel(panel(overview, "requests"))).toMatchObject({
      kind: "overview",
      shape: "number",
      measures: [{ measure: "requests" }],
    });
    // An errors-scoped number still only counts errors — still an overview answer.
    expect(planPanel(panel(overview, "errors"))).toMatchObject({ kind: "overview", measures: [{ measure: "errors" }] });
    expect(planPanel(panel(overview, "error-rate"))).toMatchObject({
      kind: "overview",
      shape: "line",
      byService: true,
      measures: [{ measure: "errorRate" }],
    });
    expect(planPanel(panel(overview, "red-by-service"))).toMatchObject({ kind: "overview", shape: "table", byService: true });
  });

  it("applies the dashboard's service filter as the scope's services", () => {
    expect(planPanel(panel(overview, "requests"), "api")).toMatchObject({ kind: "overview", services: ["api"] });
  });

  it("answers an rpc's panels from ListTraces, drawing a latency line as the window's numbers", () => {
    const plan = planPanel(panel(service, "rpc-CreateOrder-latency"));
    expect(plan).toMatchObject({
      kind: "red",
      scope: { rootSpanName: "orders.v1.OrdersService/CreateOrder", errorsOnly: false },
      measures: [{ measure: "p50" }, { measure: "p95" }, { measure: "p99" }],
    });
    expect(plan.kind === "red" && plan.note).toMatch(/per-rpc trends/);
    expect(planPanel(panel(service, "rpc-CreateOrder-rate"))).toMatchObject({ kind: "red", measures: [{ measure: "rate" }] });
  });

  it("groups the per-rpc table from a trace sample, scoped by the proto service prefix", () => {
    expect(planPanel(panel(service, "red-by-rpc"))).toMatchObject({
      kind: "rpcTable",
      scope: { rootSpanName: "orders.v1.OrdersService/" },
      measures: [{ measure: "requests" }, { measure: "errors" }, { measure: "p95" }],
    });
  });

  it("maps a metrics panel to QueryMetric one series at a time", () => {
    expect(planPanel(panel(overview, "go-goroutines"), "worker")).toEqual({
      kind: "metric",
      shape: "line",
      queries: [
        {
          label: "goroutines",
          metricName: "go.goroutine.count",
          metricType: "sum",
          aggregation: "sum",
          groupBy: [],
          filters: [],
          services: ["worker"],
        },
      ],
    });
  });

  it("turns a search panel into a way into Logs, carrying its scope", () => {
    expect(planPanel(panel(overview, "error-logs"), "api")).toEqual({ kind: "link", to: "logs", service: "api", errorsOnly: true });
  });

  it("says so, rather than guessing, when no RPC computes a panel", () => {
    expect(
      planPanel({
        id: "sessions",
        title: "Sessions",
        kind: "number",
        signal: "traces",
        series: [{ label: "sessions", agg: "count_distinct", field: "resource.attr.rum.sessionId" }],
      })
    ).toMatchObject({ kind: "none" });
    expect(
      planPanel({
        id: "top-db",
        title: "DB by operation",
        kind: "table",
        signal: "traces",
        groupBy: ["span.attr.db.operation"],
        series: [{ label: "calls", agg: "count" }],
      })
    ).toMatchObject({ kind: "none", reason: expect.stringContaining("span.attr.db.operation") });
  });
});
