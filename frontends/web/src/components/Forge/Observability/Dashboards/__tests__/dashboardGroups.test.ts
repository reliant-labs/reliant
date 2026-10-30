// Copyright (c) 2026 Reliant Labs

/**
 * The page groups: the server's `group` wins, an unknown one is Custom, and
 * only a dashboard from an older server (no group) is inferred from its key
 * and source. Search reaches across groups, by tag, workload and group name.
 */

import { describe, expect, it } from "vitest";

import type { DashboardSource, DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";

import { groupCounts, groupIndex, groupOf, inferredGroup, workloadOf } from "../dashboardGroups";
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

  it("infers a group only when the server sent none", () => {
    expect(groupOf(summary("overview"))).toBe("overview");
    expect(groupOf(summary("overview", "generated", { group: "" }))).toBe("overview");
  });
});

describe("inferredGroup (older control planes)", () => {
  it.each([
    ["overview", "generated", "overview"],
    ["process-orders", "generated", "services"],
    ["service-orders", "generated", "services"],
    ["worker-mailer", "generated", "services"],
    ["jobs", "generated", "jobs"],
    ["cron-nightly", "generated", "jobs"],
    ["workloads", "generated", "platform"],
    ["releases", "generated", "releases"],
    ["frontend", "generated", "product"],
    ["frontend-web", "generated", "product"],
    ["custom-metrics", "generated", "custom"],
    ["selfmon-postgres", "repo", "platform"],
    ["product-funnel", "repo", "product"],
    ["selfmon-fleet", "workspace", "platform"],
    ["checkout", "repo", "custom"],
    ["my-board", "workspace", "custom"],
    ["org-costs", "builtin", "platform"],
  ] as const)("%s (%s) is %s", (key, source, group) => {
    expect(inferredGroup(summary(key, source))).toBe(group);
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
      summary("selfmon-db", "repo"),
      summary("workloads"),
      summary("process-b"),
      summary("process-a"),
      summary("org-costs", "builtin"),
    ];
    const grouped = groupIndex(index);
    expect(grouped.map((g) => g.group)).toEqual(["overview", "services", "jobs", "product", "platform", "releases", "custom"]);
    expect(grouped.find((g) => g.group === "services")!.dashboards.map((d) => d.ref.key)).toEqual(["process-b", "process-a"]);
    expect(grouped.find((g) => g.group === "platform")!.dashboards.map((d) => d.ref.key)).toEqual(["org-costs", "workloads", "selfmon-db", "my-board"]);
    expect(groupCounts(index)).toEqual({ overview: 0, services: 2, jobs: 0, product: 0, platform: 4, releases: 0, custom: 0 });
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
