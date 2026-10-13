// Copyright (c) 2025 Reliant Labs

/**
 * "Save as code" writes one forge dashboard FILE (forge.dashboards/v1, forge
 * internal/obsdash dashboardFile: the version, then the dashboard). The parity
 * tests feed forge's OWN output through the console's serializers and expect
 * the same bytes back — key order, omitted empty fields, Go's HTML escaping —
 * so a saved file diffs cleanly against what forge generates:
 *
 *   __fixtures__/forge-dashboard-spec.json   forge's golden whole-spec document
 *                                            (internal/obsdash/testdata/dashboard-spec.json)
 *   __fixtures__/forge-dashboard-file.json   forge's marshalDashboardFile output
 *                                            for that spec's releases dashboard
 *
 * Refresh both copies when forge's spec changes.
 */

import { describe, expect, it } from "vitest";

import type { Dashboard, Panel } from "@/services/controlPlane/observability/types";

import forgeFile from "../__fixtures__/forge-dashboard-file.json?raw";
import forgeGolden from "../__fixtures__/forge-dashboard-spec.json?raw";
import { fixtureDashboards } from "../__fixtures__/fixtureApi";
import {
  applyEdits,
  buildDashboardFile,
  containsPattern,
  dashboardFileJson,
  dashboardFileProblems,
  dashboardFileName,
  defaultDashboardName,
  editKey,
  fieldForKey,
  newPanelId,
  panelProblems,
  predicateForFilter,
  SPEC_VERSION,
  specJson,
  specProblems,
  type DashboardFile,
  type ObsdashSpec,
} from "../obsdashSpec";

const overview = (): Dashboard => fixtureDashboards().dashboards[0];

describe("serialization parity with forge", () => {
  it("re-serializes forge's own generated spec byte for byte", () => {
    const parsed = JSON.parse(forgeGolden) as ObsdashSpec;
    expect(specJson(parsed)).toBe(forgeGolden);
  });

  it("forge's generated spec is valid by the console's own rules", () => {
    expect(specProblems(JSON.parse(forgeGolden) as ObsdashSpec)).toEqual([]);
  });

  it("re-serializes forge's own dashboard file byte for byte", () => {
    const parsed = JSON.parse(forgeFile) as DashboardFile;
    expect(dashboardFileJson(parsed)).toBe(forgeFile);
    expect(dashboardFileProblems(parsed)).toEqual([]);
  });

  it("the file is the spec's dashboard with the version in front", () => {
    const spec = JSON.parse(forgeGolden) as ObsdashSpec;
    const file = JSON.parse(forgeFile) as DashboardFile;
    expect(file).toEqual({ version: spec.version, ...spec.dashboards.find((d) => d.id === file.id) });
  });
});

describe("in-place edits", () => {
  it("replaces, hides and appends panels for one dashboard only", () => {
    const base = overview();
    const edited: Panel = { ...base.panels[4], title: "Requests by environment" };
    const added: Panel = { id: "custom-1", title: "Orders", kind: "number", signal: "metrics", series: [{ label: "orders", agg: "sum", field: "orders_created" }] };
    const shown = applyEdits(base, {
      [editKey("overview", base.panels[4].id)]: edited,
      [editKey("overview", "errors")]: null,
      [editKey("overview", "custom-1")]: added,
      [editKey("service-api", "red-by-rpc")]: null,
    });
    expect(shown.panels.find((p) => p.id === base.panels[4].id)?.title).toBe("Requests by environment");
    expect(shown.panels.some((p) => p.id === "errors")).toBe(false);
    expect(shown.panels.at(-1)).toEqual(added);
    expect(shown.panels).toHaveLength(base.panels.length);
    expect(newPanelId(shown)).toBe("custom-2");
  });

  it("an edit cannot move a panel to another id", () => {
    const base = overview();
    const shown = applyEdits(base, { [editKey("overview", "requests")]: { ...base.panels[0], id: "elsewhere" } });
    expect(shown.panels[0].id).toBe("requests");
  });
});

describe("filters and group-by become panel predicates", () => {
  it.each([
    ["service.name", "traces", "service.name"],
    ["deployment.environment.name", "metrics", "deployment.environment.name"],
    ["service.version", "logs", "resource.attr.service.version"],
    ["status", "traces", "span.status"],
    ["status", "logs", "log.attr.status"],
    ["http.route", "traces", "span.attr.http.route"],
    ["k8s.event.reason", "logs", "log.attr.k8s.event.reason"],
    ["k8s.container.last_terminated_reason", "metrics", "metric.attr.k8s.container.last_terminated_reason"],
    ["orders.status", "metrics", "metric.attr.orders.status"],
    ["span.attr.lcp", "traces", "span.attr.lcp"],
  ])("%s on a %s panel is %s", (key, signal, field) => {
    expect(fieldForKey(key, signal)).toBe(field);
  });

  it("spells each scope operator in obsdash's vocabulary", () => {
    expect(predicateForFilter({ key: "service.version", op: "in", values: ["v1", "v2"] }, "traces")).toEqual({ field: "resource.attr.service.version", op: "in", values: ["v1", "v2"] });
    expect(predicateForFilter({ key: "k8s.pod.name", op: "exists" }, "logs")).toEqual({ field: "resource.attr.k8s.pod.name", op: "exists" });
    expect(predicateForFilter({ key: "status", op: "neq", value: "error" }, "traces")).toEqual({ field: "span.status", op: "neq", value: "error" });
    // contains is case-insensitive and literal: RE2 metacharacters are escaped.
    expect(predicateForFilter({ key: "http.route", op: "contains", value: "/orders/{id}" }, "traces")).toEqual({
      field: "span.attr.http.route",
      op: "match",
      value: "(?i)/orders/\\{id\\}",
    });
    expect(containsPattern("a.b*c")).toBe("(?i)a\\.b\\*c");
  });

  it("bakes the filters into every panel and the group-by into the aggregating ones", () => {
    const base = overview();
    const file = buildDashboardFile({
      dashboard: base,
      name: "shop-overview",
      title: "Shop overview (by environment)",
      filters: [{ key: "service.version", op: "eq", value: "v1.8.3" }],
      groupBy: ["deployment.environment.name", "status"],
    });
    expect(file).toMatchObject({ version: SPEC_VERSION, id: "shop-overview", title: "Shop overview (by environment)", filters: base.filters });
    const rate = file.panels.find((p) => p.id === "request-rate")!;
    expect(rate.where?.at(-1)).toEqual({ field: "resource.attr.service.version", op: "eq", value: "v1.8.3" });
    expect(rate.where).toHaveLength((base.panels.find((p) => p.id === "request-rate")!.where ?? []).length + 1);
    expect(rate.groupBy).toEqual(["deployment.environment.name", "span.status"]);
    // A metrics panel reads its own attribute map, so its status is metric.attr.status.
    expect(file.panels.find((p) => p.id === "go-goroutines")!.groupBy).toEqual(["deployment.environment.name", "metric.attr.status"]);
    // A search panel lists rows: filtered, never grouped.
    const search = file.panels.find((p) => p.kind === "search")!;
    expect(search.groupBy).toBeUndefined();
    expect(search.where?.at(-1)?.field).toBe("resource.attr.service.version");
    expect(dashboardFileProblems(file)).toEqual([]);
  });

  it("keeps each panel's own group-by when no group-by was chosen", () => {
    const base = overview();
    const file = buildDashboardFile({ dashboard: base, name: "x", title: "x", filters: [], groupBy: [] });
    expect(file.panels.find((p) => p.id === "request-rate")!.groupBy).toEqual(["service.name"]);
  });

  it("keeps the release markers and drops who served it", () => {
    const base: Dashboard = { ...overview(), source: "gen", annotations: [{ source: "releases", label: "Deploys" }] };
    const file = buildDashboardFile({ dashboard: base, name: "x", title: "x", filters: [], groupBy: [] });
    expect(file.annotations).toEqual([{ source: "releases", label: "Deploys" }]);
    expect(file).not.toHaveProperty("source");
    expect(JSON.parse(dashboardFileJson(file))).not.toHaveProperty("source");
  });

  it("writes the file the way forge does", () => {
    const base = overview();
    const json = dashboardFileJson(buildDashboardFile({ dashboard: base, name: "shop-overview", title: "t", filters: [], groupBy: [] }));
    expect(json.endsWith("}\n")).toBe(true);
    const doc = JSON.parse(json);
    expect(Object.keys(doc)).toEqual(["version", "id", "title", "description", "filters", "panels"]);
    // omitempty: an unset unit, ratio or width is absent, not "" / false / 0.
    const sessions = doc.panels.find((p: { id: string }) => p.id === "error-logs");
    expect(sessions).not.toHaveProperty("ratio");
    expect(sessions).not.toHaveProperty("unit");
    expect(sessions).not.toHaveProperty("groupBy");
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
  ])("refuses %s", (_name, patch, message) => {
    expect(panelProblems({ ...good, ...patch }).join(" ")).toContain(message);
  });

  it("refuses two panels with one id, and a wrong version", () => {
    const spec: ObsdashSpec = { version: "forge.dashboards/v0", project: "p", dashboards: [{ id: "d", title: "D", filters: [], panels: [good, good] }], savedSearches: [] };
    expect(specProblems(spec)).toEqual(["The version must be forge.dashboards/v1.", 'Two panels share the id "p".']);
  });
});

describe("file names", () => {
  it("defaults to the generated dashboard's name, which makes the saved copy win", () => {
    expect(defaultDashboardName("shop", "service-orders")).toBe("shop-service-orders");
  });

  it("makes any name a safe file name and never a generated one", () => {
    expect(dashboardFileName("My Dashboard!")).toBe("my-dashboard");
    expect(dashboardFileName("shop-overview_gen")).toBe("shop-overview");
    expect(dashboardFileName("../../etc/passwd")).toBe("etc-passwd");
    expect(dashboardFileName("")).toBe("dashboard");
  });
});
