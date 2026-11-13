// Copyright (c) 2026 Reliant Labs

import { describe, expect, it } from "vitest";

import type { DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";

import { displayTitle, HUB_TAB_LIMIT, hubOf, hubsOf, isTabOf, landingOf, withoutSuperseded } from "../dashboardHubs";

const summary = (key: string, over: Partial<DashboardSummary> = {}): DashboardSummary =>
  ({
    ref: { kind: over.source === "builtin" ? "builtin" : over.source === "workspace" ? "workspace" : "release", projectId: "control-plane", key },
    source: "generated",
    title: `control-plane: ${key}`,
    description: "",
    readOnly: true,
    starred: false,
    lastViewedAtMs: null,
    panelCount: 4,
    previewPanels: [],
    release: null,
    workspace: null,
    category: "",
    ...over,
  }) as DashboardSummary;

const builtin = (key: string, title: string, over: Partial<DashboardSummary> = {}) => summary(key, { source: "builtin", title, category: "standard", ...over });

const SERVICES = ["platform-api", "admin-api", "billing-worker", "daemon-gateway", "deploy-worker", "llm-gateway", "metering", "notifier", "obs-query", "operator", "reconciler", "scheduler", "static-origin", "usage-rollup", "workspace-proxy", "auth-bridge"];

/** control-plane's own project as v1.16.0-rc.2 lists it. */
const CONTROL_PLANE: DashboardSummary[] = [
  builtin("service-red", "Service health (RED)", { group: "services" }),
  builtin("usage", "Product usage", { group: "product" }),
  summary("org-llm", { source: "builtin", title: "LLM usage", category: "billing", viewKind: "llm-usage", ref: { kind: "builtin", projectId: "", key: "org-llm" } }),
  summary("overview", { group: "overview" }),
  ...SERVICES.map((name) => summary(`process-${name}`, { group: "services" })),
  summary("acquisition", { group: "product" }),
  summary("product", { group: "product" }),
  summary("users", { group: "product" }),
  summary("workloads", { group: "platform" }),
  summary("releases", { group: "releases" }),
  summary("checkout", { source: "repo", title: "Checkout funnel", group: "product" }),
  summary("payments", { source: "workspace", title: "Payment provider", group: "services" }),
];

describe("the project's dashboard hubs", () => {
  it("drop the project prefix forge puts on a generated title, inside the project", () => {
    expect(displayTitle("control-plane: admin-api", "control-plane")).toBe("admin-api");
    expect(displayTitle("shop: orders service", "shop")).toBe("Orders service");
    expect(displayTitle("shop: users", "shop")).toBe("Users");
    expect(displayTitle("Checkout funnel", "shop")).toBe("Checkout funnel");
    expect(displayTitle("shop: users", undefined)).toBe("shop: users");
  });

  it("make Product analytics of forge's Users, Product and Acquisition, in reading order, and hide the built-in Product usage they supersede", () => {
    const [product] = hubsOf(CONTROL_PLANE, "control-plane");
    expect(product!.id).toBe("product");
    expect(product!.tabs.map((tab) => tab.label)).toEqual(["Users", "Product", "Acquisition"]);
    expect(withoutSuperseded(CONTROL_PLANE).map((d) => d.ref.key)).not.toContain("usage");
  });

  it("keep Product usage as the hub's tab where forge's set is not there", () => {
    const old = CONTROL_PLANE.filter((d) => !["users", "product", "acquisition"].includes(d.ref.key));
    expect(hubsOf(old, "control-plane")[0]!.tabs.map((tab) => tab.ref.key)).toEqual(["usage"]);
  });

  it("resolve the product tabs by id, release or hosted built-in, once each", () => {
    // control-plane #2618 hosts them as built-ins; a release of the same id is the project's own.
    const hosted = [builtin("users", "Users"), builtin("product", "Product"), builtin("acquisition", "Acquisition"), builtin("frontend", "Frontend"), summary("product", { title: "shop: product" })];
    const tabs = hubsOf(hosted, "shop")[0]!.tabs;
    expect(tabs.map((tab) => `${tab.ref.kind}/${tab.ref.key}`)).toEqual(["builtin/users", "release/product", "builtin/acquisition", "builtin/frontend"]);
  });

  it("open an old Product usage link on the Product tab", () => {
    const hub = hubOf(CONTROL_PLANE, { kind: "builtin", key: "usage" }, "control-plane");
    expect(hub?.id).toBe("product");
    expect(hub!.tabs.find((tab) => isTabOf(tab, { kind: "builtin", key: "usage" }))?.label).toBe("Product");
  });

  it("put every service behind one Services hub: all services first, then each workload, then the jobs", () => {
    const services = hubsOf([...CONTROL_PLANE, summary("jobs", { group: "jobs" })], "control-plane").find((h) => h.id === "services")!;
    expect(services.tabs[0]!.label).toBe("All services");
    expect(services.tabs.slice(1, 4).map((tab) => tab.label)).toEqual(["admin-api", "auth-bridge", "billing-worker"]);
    expect(services.tabs.at(-1)!.label).toBe("Jobs");
    // Sixteen services are too many to lay out as tabs.
    expect(services.picker).toBe(true);
    expect(hubOf(CONTROL_PLANE, { kind: "release", key: "process-metering" }, "control-plane")?.id).toBe("services");
    const few = hubsOf(CONTROL_PLANE.filter((d) => !d.ref.key.startsWith("process-") || d.ref.key.endsWith("api")), "control-plane").find((h) => h.id === "services")!;
    expect(few.tabs.length).toBeLessThanOrEqual(HUB_TAB_LIMIT);
    expect(few.picker).toBe(false);
  });

  it("keep control-plane's thirty dashboards to a handful of cards and the team's own rows", () => {
    const { cards, own } = landingOf(CONTROL_PLANE, "control-plane");
    expect(cards.map((card) => (card.kind === "hub" ? card.hub.title : card.title))).toEqual(["Overview", "Product analytics", "Services", "Releases", "Workloads", "LLM usage"]);
    expect(own.map((d) => d.title)).toEqual(["Checkout funnel", "Payment provider"]);
  });

  it("leave an organization view out of the project's hubs, whatever its key", () => {
    const org = (key: string, title: string, over: Partial<DashboardSummary> = {}) =>
      summary(key, { source: "builtin", title, category: "billing", ref: { kind: "builtin", projectId: "", key }, ...over });
    const dashboards = [
      ...CONTROL_PLANE,
      org("product", "Organization product"),
      org("frontend", "Organization frontend"),
      org("usage", "Organization usage", { viewKind: "organization-usage" }),
      org("org-service-red", "Organization service health", { category: "", group: "services" }),
    ];
    const hubs = hubsOf(dashboards, "control-plane");
    const tabs = hubs.flatMap((h) => h.tabs.map((tab) => tab.summary.title));
    expect(tabs).not.toEqual(expect.arrayContaining([expect.stringMatching(/^Organization/)]));
    expect(hubs[0]!.tabs.map((tab) => tab.label)).toEqual(["Users", "Product", "Acquisition"]);
    expect(hubOf(dashboards, { kind: "builtin", key: "frontend" }, "control-plane")).toBeNull();
  });

  it("show organization views as their own cards right after Workloads, before a custom one", () => {
    const dashboards = [
      summary("org-costs", { source: "builtin", title: "Costs", category: "billing", viewKind: "organization-costs", ref: { kind: "builtin", projectId: "", key: "org-costs" } }),
      summary("product", { source: "builtin", title: "Organization product", category: "billing", ref: { kind: "builtin", projectId: "", key: "product" } }),
      summary("notes", { group: "custom", title: "control-plane: notes" }),
      ...CONTROL_PLANE,
    ];
    const { cards } = landingOf(dashboards, "control-plane");
    expect(cards.map((card) => (card.kind === "hub" ? card.hub.title : card.title))).toEqual([
      "Overview",
      "Product analytics",
      "Services",
      "Releases",
      "Workloads",
      "Costs",
      "Organization product",
      "LLM usage",
      "Notes",
    ]);
  });
});
