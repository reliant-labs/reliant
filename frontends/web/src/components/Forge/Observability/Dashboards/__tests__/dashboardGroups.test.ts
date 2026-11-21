// @vitest-environment node
// Copyright (c) 2026 Reliant Labs

/**
 * The page groups: the server's `group`, and an unknown or absent one is
 * Custom. Search reaches across groups, by tag, workload and group name.
 */

import { describe, expect, it } from "vitest";

import type { DashboardSource, DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";

import { groupCounts, groupIndex, groupOf, listSections, workloadOf } from "../dashboardGroups";
import { filterIndex, starredOf } from "../dashboardModel";

function summary(key: string, source: DashboardSource = "generated", extra: Partial<DashboardSummary> = {}): DashboardSummary {
  const kind = source === "generated" || source === "repo" ? "release" : source === "builtin" ? "builtin" : "workspace";
  return {
    ref: { kind, projectId: "shop", key },
    source,
    title: key,
    description: "",
    readOnly: kind !== "workspace",
    starred: false,
    lastViewedAtMs: null,
    panelCount: 1,
    previewPanels: [],
    release: null,
    workspace: null,
    category: "",
    ...extra,
  };
}

describe("groupOf", () => {
  it("takes the server's group", () => {
    expect(groupOf(summary("anything", "repo", { group: "jobs" }))).toBe("jobs");
    expect(groupOf(summary("selfmon-db", "repo", { group: "custom" }))).toBe("custom");
  });

  it("reads an unknown group as custom, never as a guess", () => {
    expect(groupOf(summary("overview", "generated", { group: "dashboards-v2" }))).toBe("custom");
  });

  it("reads an absent group as custom, never as a guess from the key", () => {
    expect(groupOf(summary("overview"))).toBe("custom");
    expect(groupOf(summary("overview", "generated", { group: "" }))).toBe("custom");
  });
});

describe("workloadOf", () => {
  it("names the Forge workload of a generated per-workload dashboard", () => {
    expect(workloadOf(summary("process-orders"))).toBe("orders");
    expect(workloadOf(summary("service-billing-api"))).toBe("billing-api");
  });

  it("never guesses one for anything else", () => {
    expect(workloadOf(summary("overview"))).toBeNull();
    expect(workloadOf(summary("process-orders", "repo"))).toBeNull();
    expect(workloadOf(summary("service-x", "workspace"))).toBeNull();
  });
});

describe("groupIndex", () => {
  it("returns every group in page order, built-in and generated first inside one, else the server's order", () => {
    const index = [
      summary("my-board", "workspace", { group: "platform" }),
      summary("selfmon-db", "repo", { group: "platform" }),
      summary("workloads", "generated", { group: "platform" }),
      summary("process-b", "generated", { group: "services" }),
      summary("process-a", "generated", { group: "services" }),
      summary("org-costs", "builtin", { group: "platform" }),
    ];
    const grouped = groupIndex(index);
    expect(grouped.map((g) => g.group)).toEqual(["overview", "services", "jobs", "product", "platform", "releases", "custom"]);
    expect(grouped.find((g) => g.group === "services")!.dashboards.map((d) => d.ref.key)).toEqual(["process-b", "process-a"]);
    expect(grouped.find((g) => g.group === "platform")!.dashboards.map((d) => d.ref.key)).toEqual(["org-costs", "workloads", "selfmon-db", "my-board"]);
    expect(groupCounts(index)).toEqual({ overview: 0, services: 2, jobs: 0, product: 0, platform: 4, releases: 0, custom: 0 });
  });

  it("lists Built-in first, then only the groups that hold something", () => {
    const services = { group: "services" };
    const index = [summary("process-a", "generated", services), summary("org-costs", "builtin", { group: "product" }), summary("my-board", "workspace")];
    expect(listSections(index).map((s) => [s.id, s.dashboards.map((d) => d.ref.key)])).toEqual([
      ["builtin", ["org-costs"]],
      ["services", ["process-a"]],
      ["custom", ["my-board"]],
    ]);
    expect(listSections([summary("process-a", "generated", services)]).map((s) => s.id)).toEqual(["services"]);
    expect(listSections([])).toEqual([]);
  });
});

describe("search across groups", () => {
  const index = [
    summary("process-orders", "generated", { title: "shop: orders service", group: "services" }),
    summary("jobs", "generated", { title: "shop: jobs", group: "jobs" }),
    summary("fleet", "workspace", { title: "Daemon fleet", group: "platform", tags: ["daemons"], starred: true }),
    summary("checkout", "repo", { title: "Checkout funnel", group: "product", tags: ["checkout"], starred: true }),
  ];

  it("matches a workload name, a tag and a group's name", () => {
    expect(filterIndex(index, { q: "orders" }).map((d) => d.ref.key)).toEqual(["process-orders"]);
    expect(filterIndex(index, { q: "daemons" }).map((d) => d.ref.key)).toEqual(["fleet"]);
    expect(filterIndex(index, { q: "product" }).map((d) => d.ref.key)).toEqual(["checkout"]);
  });

  it("narrows to one group, and keeps the other filters", () => {
    expect(filterIndex(index, { group: "jobs" }).map((d) => d.ref.key)).toEqual(["jobs"]);
    expect(filterIndex(index, { group: "platform", source: "release" })).toEqual([]);
  });

  it("keeps starred dashboards pinned whatever group they are in", () => {
    expect(starredOf(filterIndex(index, {})).map((d) => d.ref.key)).toEqual(["fleet", "checkout"]);
    expect(starredOf(filterIndex(index, { q: "funnel" })).map((d) => d.ref.key)).toEqual(["checkout"]);
  });
});
