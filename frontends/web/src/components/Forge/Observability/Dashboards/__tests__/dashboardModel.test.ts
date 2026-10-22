// Copyright (c) 2025 Reliant Labs

/**
 * The rules the dashboards pages follow, without rendering: the index's
 * filters and shelves, what a comparison reads, the alert bands, the layout
 * grid, the drill-down links, and the spec's JSON round trip.
 */

import { describe, expect, it } from "vitest";

import type { AlertEvent } from "@/services/controlPlane/observability/alertingTypes";
import { dashboardFromJson, dashboardToJson, panelFromJson } from "@/services/controlPlane/observability/dashboardsApi";
import type { DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";
import type { Promotion } from "@/services/controlPlane/observability/deliveryTypes";
import type { Dashboard, Panel } from "@/services/controlPlane/observability/types";

import {
  alertBands,
  compareTarget,
  filterIndex,
  movePanel,
  newPanelId,
  ownerOf,
  ownersOf,
  recentOf,
  resizePanel,
  slugify,
  spanOf,
  starredOf,
  widthOfSpan,
} from "../dashboardModel";
import { countsErrors, drillSearches, scopeKeyOf } from "../drill";

const NOW = Date.UTC(2026, 9, 18, 16, 0);

function summary(over: Partial<DashboardSummary> & Pick<DashboardSummary, "title" | "source">): DashboardSummary {
  const kind = over.source === "workspace" ? "workspace" : "release";
  return {
    ref: { kind, projectId: "shop", key: over.title.toLowerCase().replace(/\W+/g, "-") },
    description: "",
    readOnly: kind !== "workspace",
    starred: false,
    lastViewedAtMs: null,
    panelCount: 3,
    previewPanels: [],
    release: null,
    workspace: null,
    category: "",
    ...over,
  };
}

const INDEX: DashboardSummary[] = [
  summary({ title: "Service api", source: "generated", starred: true, lastViewedAtMs: NOW - 3_600_000 }),
  summary({ title: "Deploy tree", source: "repo", description: "Flux trees" }),
  summary({
    title: "Checkout latency",
    source: "workspace",
    starred: true,
    lastViewedAtMs: NOW - 60_000,
    workspace: { version: 4, createdBy: { id: "u1", name: "sean", email: "" }, updatedBy: null, createdAtMs: null, updatedAtMs: null, forkedFrom: "", envIds: [] },
  }),
  summary({
    title: "Billing funnel",
    source: "workspace",
    workspace: { version: 3, createdBy: { id: "u2", name: "", email: "ada@x.dev" }, updatedBy: null, createdAtMs: null, updatedAtMs: null, forkedFrom: "", envIds: [] },
  }),
];

describe("the index", () => {
  it("names owners: forge, the repo, or the author (their email when unnamed)", () => {
    expect(INDEX.map(ownerOf)).toEqual(["forge", "repo", "sean", "ada@x.dev"]);
    expect(ownersOf(INDEX)).toEqual(["ada@x.dev", "sean", "forge", "repo"]);
  });

  it("filters by text (title, description, key), source and owner, together", () => {
    expect(filterIndex(INDEX, { q: "flux" }).map((d) => d.title)).toEqual(["Deploy tree"]);
    expect(filterIndex(INDEX, { source: "release" }).map((d) => d.title)).toEqual(["Service api", "Deploy tree"]);
    expect(filterIndex(INDEX, { source: "workspace", owner: "sean" }).map((d) => d.title)).toEqual(["Checkout latency"]);
    expect(filterIndex(INDEX, { source: "release", owner: "sean" })).toEqual([]);
  });

  it("shelves the starred in index order, and the recently viewed newest first", () => {
    expect(starredOf(INDEX).map((d) => d.title)).toEqual(["Service api", "Checkout latency"]);
    expect(recentOf(INDEX).map((d) => d.title)).toEqual(["Checkout latency", "Service api"]);
  });

  it("makes a slug the server accepts from any title", () => {
    expect(slugify("Checkout latency (p95)")).toBe("checkout-latency-p95");
    expect(slugify("  ")).toBe("dashboard");
    expect(slugify("Ünïcodé  stuff")).toBe("unicode-stuff");
    expect(slugify("x".repeat(80))).toHaveLength(63);
  });
});

describe("compare", () => {
  const promotion = (envId: string, atMs: number, version: string): Promotion => ({
    promotionId: `p-${version}`,
    envId,
    envName: envId.replace("env-", ""),
    releaseId: version,
    version,
    gitCommit: "",
    gitTag: "",
    promotedAtMs: atMs,
    promotedBy: "sean",
    promotedByUserId: "",
    kind: "promote",
  });

  it("previous period shifts by the window's own length", () => {
    expect(compareTarget("period", "6h", [], null, NOW)).toMatchObject({ mode: "period", offsetMs: 6 * 3_600_000 });
  });

  it("previous release reads the window that ended when the scoped environment's newest release was promoted", () => {
    const ledger = [promotion("env-prod", NOW - 5 * 3_600_000, "v1.8.2"), promotion("env-prod", NOW - 2 * 3_600_000, "v1.8.3"), promotion("env-staging", NOW - 60_000, "v1.8.4")];
    expect(compareTarget("release", "1h", ledger, "env-prod", NOW)).toMatchObject({ mode: "release", offsetMs: 2 * 3_600_000, label: "before v1.8.3" });
  });

  it("says why previous release cannot be compared: every environment, or nothing promoted", () => {
    expect(compareTarget("release", "1h", [], null, NOW)).toMatchObject({ mode: "unavailable", reason: expect.stringContaining("one environment") });
    expect(compareTarget("release", "1h", [], "env-prod", NOW)).toMatchObject({ mode: "unavailable", reason: expect.stringContaining("No release") });
  });
});

describe("alert bands", () => {
  const event = (transition: AlertEvent["transition"], minutesAgo: number, over: Partial<AlertEvent> = {}): AlertEvent => ({
    envId: "env-prod",
    rule: "CheckoutLatencyHigh",
    fingerprint: "f1",
    labels: {},
    transition,
    value: 1,
    atMs: NOW - minutesAgo * 60_000,
    ...over,
  });

  it("is one band per firing stretch, closed by a resolve or open until now", () => {
    const bands = alertBands([event("pending", 40), event("firing", 38), event("resolved", 24), event("firing", 10)], ["env-prod"], NOW);
    expect(bands).toHaveLength(2);
    expect(bands[0]).toMatchObject({ startSec: (NOW - 38 * 60_000) / 1000, endSec: (NOW - 24 * 60_000) / 1000, label: "CheckoutLatencyHigh firing · 14m" });
    expect(bands[1]).toMatchObject({ endSec: NOW / 1000, label: "CheckoutLatencyHigh firing · 10m" });
  });

  it("leaves out other environments, and alerts about another service when one is picked", () => {
    const events = [
      event("firing", 30, { envId: "env-staging" }),
      event("firing", 20, { fingerprint: "f2", labels: { service: "worker" } }),
      event("firing", 15, { fingerprint: "f3", labels: { service: "api" } }),
      event("firing", 5, { fingerprint: "f4" }),
    ];
    expect(alertBands(events, ["env-prod"], NOW).length).toBe(3);
    expect(alertBands(events, ["env-prod"], NOW, "api").map((b) => b.startSec)).toEqual([(NOW - 15 * 60_000) / 1000, (NOW - 5 * 60_000) / 1000]);
  });
});

describe("layout", () => {
  const panel = (id: string, over: Partial<Panel> = {}): Panel => ({ id, title: id, kind: "line", signal: "traces", series: [], ...over });
  const dashboard: Dashboard = { id: "d", title: "D", filters: [], panels: [panel("a", { width: 6 }), panel("b", { width: 18 }), panel("c"), panel("n", { kind: "number" })] };

  it("draws forge's 24 columns as four spans, the kind's default when unset", () => {
    expect(dashboard.panels.map(spanOf)).toEqual([1, 3, 4, 1]);
    expect(spanOf(panel("x", { width: 9 }))).toBe(2);
    expect([1, 2, 3, 4].map(widthOfSpan)).toEqual([6, 12, 18, 24]);
  });

  it("moves and resizes one panel, and finds an unused id", () => {
    expect(movePanel(dashboard, "c", 0).panels.map((p) => p.id)).toEqual(["c", "a", "b", "n"]);
    expect(movePanel(dashboard, "a", 99).panels.map((p) => p.id)).toEqual(["b", "c", "n", "a"]);
    expect(resizePanel(dashboard, "a", 2).panels[0].width).toBe(12);
    expect(newPanelId({ ...dashboard, panels: [panel("panel-1")] })).toBe("panel-2");
  });
});

describe("drill-down", () => {
  const panel: Panel = {
    id: "p",
    title: "Failures",
    kind: "line",
    signal: "traces",
    where: [{ field: "span.status", op: "eq", value: "error" }],
    series: [{ label: "errors", agg: "count" }],
    groupBy: ["span.name", "resource.attr.service.version"],
  };

  it("turns a series' group into explorer filters: operation, attributes, service, errors", () => {
    const { logs, traces, service } = drillSearches(panel, { "span.name": "CreateOrder", "resource.attr.service.version": "v1.8.3", "service.name": "api" }, { range: "6h" });
    expect(service).toBe("api");
    expect(traces).toEqual({ range: "6h", service: "api", f: ["service.version:v1.8.3"], q: "CreateOrder", errors: true });
    expect(logs).toEqual({ range: "6h", service: "api", f: ["service.version:v1.8.3"], severity: "error,fatal" });
  });

  it("falls back to the panel's own service, then the variable", () => {
    expect(drillSearches({ ...panel, where: [{ field: "service.name", op: "eq", value: "worker" }] }, {}, { range: "1h", service: "api" }).service).toBe("worker");
    expect(drillSearches({ ...panel, where: [] }, {}, { range: "1h", service: "api" }).service).toBe("api");
  });

  it("knows which fields the scope can filter on", () => {
    expect(scopeKeyOf("span.attr.http.route")).toBe("http.route");
    expect(scopeKeyOf("deployment.environment.name")).toBeNull();
    expect(scopeKeyOf("span.duration")).toBeNull();
    expect(countsErrors({ ...panel, where: [] })).toBe(false);
  });
});

describe("the spec as JSON", () => {
  it("fills the arrays a forge file omits, and keeps keys it does not know", () => {
    const doc = dashboardFromJson({ id: "d", title: "D", panels: [{ id: "i", title: "Issues", kind: "issues", signal: "traces", future: { x: 1 } }] });
    expect(doc.filters).toEqual([]);
    expect(doc.annotations).toEqual([]);
    expect(doc.panels[0].series).toEqual([]);
    expect((doc.panels[0] as unknown as { future: unknown }).future).toEqual({ x: 1 });
    expect(panelFromJson(null)).toMatchObject({ id: "", kind: "", series: [] });
  });

  it("writes plain JSON with undefined keys dropped", () => {
    const json = dashboardToJson({ id: "d", title: "D", description: undefined, filters: [], panels: [{ id: "p", title: "P", kind: "line", signal: "traces", series: [], unit: undefined }] });
    expect(json).toEqual({ id: "d", title: "D", filters: [], panels: [{ id: "p", title: "P", kind: "line", signal: "traces", series: [] }] });
  });
});
