// Copyright (c) 2025 Reliant Labs

/**
 * The console's information architecture:
 * the nav lists the product's nouns in the decided order, never the
 * environment roster, and every link carries the scope.
 */

import { describe, expect, it } from "vitest";

import { buildConsoleNav, consoleCrumbs } from "../ConsoleShell";

const ENVS = [
  { name: "prod", preview: false },
  { name: "dev", preview: false, queuedOn: "billing" },
  { name: "pr-1", preview: true },
  { name: "pr-2", preview: true },
];

describe("buildConsoleNav", () => {
  it("is Overview, Releases, Observability ›, then Configure, with Activity pinned last", () => {
    const { items, footer } = buildConsoleNav({ project: "shop", page: { kind: "overview" }, env: "prod", envs: ENVS });
    expect(items.map((item) => [item.label, item.section ?? ""])).toEqual([
      ["Overview", ""],
      ["Releases", ""],
      ["Observability", ""],
      ["Environments", "Configure"],
      ["Secrets", "Configure"],
      ["Domains", "Configure"],
    ]);
    expect(footer.map((item) => item.label)).toEqual(["Activity"]);
  });

  it("opens Observability's first view from its row: it has no home of its own", () => {
    const { items } = buildConsoleNav({ project: "shop", page: { kind: "overview" }, env: "prod", envs: ENVS });
    expect(items.find((item) => item.label === "Observability")?.href).toBe("/forge/p/shop/observability/graph?env=prod");
  });

  it("lists Observability's views as its children", () => {
    const { items } = buildConsoleNav({ project: "shop", page: { kind: "overview" }, env: "prod", envs: ENVS, alertsFiring: 2 });
    const obs = items.find((item) => item.label === "Observability")!;
    expect(obs.children?.map((child) => child.label)).toEqual(["Service graph", "RPC explorer", "Errors", "Sessions", "Logs", "Traces", "Metrics", "Profiles", "Dashboards", "Alerts"]);
    expect(obs.children?.find((child) => child.label === "Alerts")?.badge).toMatchObject({ type: "count", value: 2, tone: "danger" });
  });

  it("hides only known-empty optional views, retaining an active direct link", () => {
    const base = { project: "shop", env: "prod", envs: ENVS, optionalViews: { sessions: false, profiles: false } };
    const labels = (page: Parameters<typeof buildConsoleNav>[0]["page"]) => buildConsoleNav({ ...base, page }).items.find((item) => item.label === "Observability")!.children!.map((child) => child.label);
    expect(labels({ kind: "overview" })).not.toContain("Sessions");
    expect(labels({ kind: "overview" })).not.toContain("Profiles");
    expect(labels({ kind: "obs-view", view: "sessions" })).toContain("Sessions");
    expect(labels({ kind: "obs-view", view: "profiles" })).toContain("Profiles");
  });

  it("files environments under Configure, previews collapsed into one entry", () => {
    const { items } = buildConsoleNav({ project: "shop", page: { kind: "overview" }, env: "prod", envs: ENVS });
    const environments = items.find((item) => item.label === "Environments")!;
    expect(environments.children?.map((child) => child.label)).toEqual(["prod", "dev", "Previews"]);
    expect(environments.children?.[2]?.badge).toMatchObject({ type: "count", value: 2 });
    expect(environments.children?.[1]?.trailing).toBeTruthy();
  });

  it("carries the scope on every link", () => {
    const { items, footer } = buildConsoleNav({ project: "shop", page: { kind: "overview" }, env: "e2e", envs: ENVS });
    const hrefs = [...items, ...footer].flatMap((item) => [item.href, ...(item.children ?? []).map((child) => child.href)]);
    expect(hrefs.length).toBeGreaterThan(15);
    for (const href of hrefs) expect(href).toMatch(/[?&]env=e2e(&|$)/);
    expect(items[0]!.href).toBe("/forge/p/shop?env=e2e");
  });

  it("marks the current page, and the parent group holding it stays open", () => {
    const logs = buildConsoleNav({ project: "shop", page: { kind: "obs-view", view: "logs" }, env: "prod", envs: ENVS });
    const obs = logs.items.find((item) => item.label === "Observability")!;
    expect(obs.active).toBe(false);
    expect(obs.children?.find((child) => child.label === "Logs")?.active).toBe(true);

    const prod = buildConsoleNav({ project: "shop", page: { kind: "environment", env: "prod" }, env: "e2e", envs: ENVS });
    expect(prod.items.find((item) => item.label === "Environments")?.children?.[0]?.active).toBe(true);

    const release = buildConsoleNav({ project: "shop", page: { kind: "release", version: "v1.8.2" }, env: "prod", envs: ENVS });
    expect(release.items.find((item) => item.label === "Releases")?.active).toBe(true);
  });

  it("files a workload under the Overview, not under its environment", () => {
    const page = { kind: "workload" as const, env: "prod", name: "admin-server" };
    const nav = buildConsoleNav({ project: "shop", page, env: "prod", envs: ENVS });
    expect(nav.items.find((item) => item.label === "Overview")?.active).toBe(true);
    expect(nav.items.find((item) => item.label === "Environments")?.children?.some((child) => child.active)).toBe(false);
    expect(consoleCrumbs("shop", page, "e2e")).toEqual([{ label: "Overview", href: "/forge/p/shop?env=prod" }, { label: "admin-server" }]);
  });
});

describe("starred dashboards in the nav", () => {
  const starred = [
    { title: "Checkout latency", dashKind: "workspace" as const, key: "checkout-latency" },
    { title: "Service · api", dashKind: "release" as const, key: "service-api" },
    { title: "Workloads", dashKind: "release" as const, key: "workloads" },
    { title: "Daemon fleet", dashKind: "workspace" as const, key: "daemon-fleet" },
  ];
  const dashboardsOf = (nav: ReturnType<typeof buildConsoleNav>) =>
    nav.items.find((item) => item.label === "Observability")!.children!.find((child) => child.label === "Dashboards")!;

  it("nests the first three under Dashboards, then All dashboards", () => {
    const dashboards = dashboardsOf(buildConsoleNav({ project: "shop", page: { kind: "overview" }, env: "prod", envs: ENVS, starred }));
    expect(dashboards.children?.map((child) => child.label)).toEqual(["Checkout latency", "Service · api", "Workloads", "All dashboards"]);
    expect(dashboards.children?.[1]?.href).toBe("/forge/p/shop/observability/dashboards/release/service-api?env=prod");
  });

  it("lights the open starred dashboard, or Dashboards itself for any other", () => {
    const onStar = dashboardsOf(buildConsoleNav({ project: "shop", page: { kind: "dashboard", dashKind: "release", key: "workloads" }, env: "prod", envs: ENVS, starred }));
    expect(onStar.active).toBe(false);
    expect(onStar.children?.find((child) => child.label === "Workloads")?.active).toBe(true);
    const other = dashboardsOf(buildConsoleNav({ project: "shop", page: { kind: "dashboard", dashKind: "workspace", key: "daemon-fleet" }, env: "prod", envs: ENVS, starred }));
    expect(other.active).toBe(true);
  });

  it("nests nothing without stars", () => {
    expect(dashboardsOf(buildConsoleNav({ project: "shop", page: { kind: "overview" }, env: "prod", envs: ENVS })).children).toBeUndefined();
  });
});

describe("consoleCrumbs", () => {
  it("names the page, with its parent as a link", () => {
    expect(consoleCrumbs("shop", { kind: "obs-view", view: "traces" }, "prod")).toEqual([
      { label: "Observability", href: "/forge/p/shop/observability/graph?env=prod" },
      { label: "Traces" },
    ]);
    expect(consoleCrumbs("shop", { kind: "environment", env: "dev" }, "prod")).toEqual([
      { label: "Environments", href: "/forge/p/shop/environments?env=prod" },
      { label: "dev" },
    ]);
    expect(consoleCrumbs("shop", { kind: "dashboard", dashKind: "workspace", key: "checkout-latency" }, "prod", "Checkout latency")).toEqual([
      { label: "Observability", href: "/forge/p/shop/observability/graph?env=prod" },
      { label: "Dashboards", href: "/forge/p/shop/observability/dashboards?env=prod" },
      { label: "Checkout latency" },
    ]);
  });
});
