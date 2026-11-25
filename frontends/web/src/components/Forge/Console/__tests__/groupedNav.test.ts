// @vitest-environment node
// Copyright (c) 2026 Reliant Labs

/**
 * The led console's grouped nav: the console's same pages and links, in the
 * shell's groups and order, for any project shape (one environment, many,
 * previews, starred dashboards, no Product analytics hub).
 */

import { describe, expect, it } from "vitest";

import type { SidebarNavItem } from "@/components/forge-ui/sidebar_layout";

import { buildConsoleNav } from "../ConsoleShell";
import { buildGroupedConsoleNav, groupedCrumbs, PAGE_SUBTITLES, type GroupedNavInput } from "../groupedNav";
import { OBS_NAV_VIEWS } from "../paths";

const HUB = { open: { kind: "dashboard", dashKind: "release", key: "users" } as const, keys: ["users", "product", "acquisition"] };

const nav = (over: Partial<GroupedNavInput> = {}) => buildGroupedConsoleNav({ project: "shop", page: { kind: "overview" }, env: "prod", envs: [{ name: "prod", preview: false }], ...over });

/** Every row and child, flattened. */
const flat = (items: SidebarNavItem[]): SidebarNavItem[] => items.flatMap((item) => [item, ...flat(item.children ?? [])]);
const lit = (items: SidebarNavItem[]) => flat(items).filter((item) => item.active).map((item) => item.label);
const hrefs = (items: SidebarNavItem[]) => new Set(flat(items).flatMap((item) => (item.href ? [item.href] : [])));

describe("buildGroupedConsoleNav", () => {
  it("draws the shell's groups in the shell's order, Activity last in Configure", () => {
    const { items, footer } = nav({ productAnalytics: HUB });
    expect(items.map((item) => [item.label, (item.children ?? []).map((child) => child.label).join(",")])).toEqual([
      ["Overview", ""],
      ["Dashboards", ""],
      ["Observability", "Overview,Service graph,Metrics,Logs,RPC explorer,Profiles,Errors,Alerts,Traces,Speed insights,Sessions"],
      ["Analytics", "Product overview,Users,Product,Acquisition,Events & metrics"],
      ["Deployments", "Services,Releases,Environments"],
      ["Tests", "Overview,Coverage,Runs,Flaky tests"],
      ["Data", ""],
      ["Storage", ""],
      ["Auth", ""],
      ["Configure", "Secrets,Domains,Usage,Activity"],
    ]);
    expect(footer).toEqual([]);
    // The groups are folding buttons (no page of their own), as in the shell's nav.
    for (const group of items.filter((item) => item.children?.length && item.label !== "Dashboards")) expect(group.href, group.label).toBeUndefined();
  });

  it("keeps the order of the shell's nav (navConfig/hybrid.ts), less the product's own rows", () => {
    const shell = ["Overview", "Dashboards", "Observability", "Analytics", "Deployments", "Tests", "Data", "Storage", "Auth", "Security", "Configure"];
    const labels = nav().items.map((item) => item.label);
    expect(labels).toEqual(shell.filter((label) => labels.includes(label)));
  });

  it("leaves out the pages a host does not serve", () => {
    const labels = flat(nav({ hiddenPages: ["usage", "speed-insights"] }).items).map((item) => item.label);
    expect(labels).not.toContain("Usage");
    expect(labels).not.toContain("Speed insights");
    expect(labels).toContain("Activity");
  });

  it("counts tests needing attention on Tests, and firing alerts on Observability and Alerts", () => {
    const { items } = nav({ navCounts: { tests: 2 }, alertsFiring: 1 });
    expect(items.find((item) => item.label === "Tests")!.badge).toMatchObject({ value: 2 });
    expect(items.find((item) => item.label === "Tests")!.badge).not.toHaveProperty("tone", "danger");
    expect(items.find((item) => item.label === "Observability")!.badge).toMatchObject({ value: 1, tone: "danger" });
    expect(nav().items.find((item) => item.label === "Tests")!.badge).toBeUndefined();
  });

  it("lights the page's row inside its group", () => {
    expect(lit(nav({ page: { kind: "tests-run", run: "r1" } }).items)).toEqual(["Runs"]);
    expect(lit(nav({ page: { kind: "workload", env: "prod", name: "api" } }).items)).toEqual(["Services"]);
    expect(lit(nav({ page: { kind: "obs-overview" } }).items)).toEqual(["Overview"]);
    expect(lit(nav({ page: { kind: "events" } }).items)).toEqual(["Events & metrics"]);
    expect(lit(nav({ page: { kind: "analytics", tab: "users" } }).items)).toEqual(["Users"]);
    expect(lit(nav({ page: { kind: "activity" } }).items)).toEqual(["Activity"]);
  });

  it("reaches every page the console's own nav reaches, with the same scoped links", () => {
    const input = { project: "shop", page: { kind: "overview" } as const, env: "staging", envs: [{ name: "staging", preview: false }] };
    const own = hrefs(buildConsoleNav(input).items);
    const grouped = hrefs(buildGroupedConsoleNav(input).items);
    for (const href of own) expect(grouped).toContain(href);
    for (const view of OBS_NAV_VIEWS) expect([...grouped].some((href) => href.includes(`/observability/${view}`))).toBe(true);
  });

  it("lights the hub's Analytics tab on one of the hub's dashboards", () => {
    expect(lit(nav({ productAnalytics: HUB, page: { kind: "dashboard", dashKind: "release", key: "acquisition" } }).items)).toEqual(["Acquisition"]);
    // Any other dashboard lights Dashboards (top-level, the one place).
    expect(lit(nav({ productAnalytics: HUB, page: { kind: "dashboard", dashKind: "release", key: "api" } }).items)).toEqual(["Dashboards"]);
  });

  it("nests up to three starred dashboards and lights the open one", () => {
    const starred = ["a", "b", "c", "d"].map((key) => ({ title: `Board ${key}`, dashKind: "release" as const, key }));
    const { items } = nav({ starred, page: { kind: "dashboard", dashKind: "release", key: "b" } });
    const dashboards = flat(items).find((item) => item.label === "Dashboards")!;
    expect(dashboards.children?.map((child) => child.label)).toEqual(["Board a", "Board b", "Board c", "All dashboards"]);
    expect(lit(items)).toEqual(["Board b"]);
  });

  it("lists every long-lived environment and the previews as one row", () => {
    const envs = [...Array.from({ length: 12 }, (_, i) => ({ name: `env-${i}-with-a-rather-long-name`, preview: false })), { name: "pr-1", preview: true }, { name: "pr-2", preview: true }];
    const environments = flat(nav({ envs }).items).find((item) => item.label === "Environments")!;
    expect(environments.children).toHaveLength(13);
    expect(environments.children?.at(-1)).toMatchObject({ label: "Previews", badge: { value: 2 } });
  });

  it("leaves Profiles out on a definite empty read, unless it is the open page", () => {
    expect(flat(nav({ optionalViews: { profiles: false } }).items).map((item) => item.label)).not.toContain("Profiles");
    expect(lit(nav({ optionalViews: { profiles: false }, page: { kind: "obs-view", view: "profiles" } }).items)).toEqual(["Profiles"]);
  });

  it("counts alerts firing on Alerts too", () => {
    const alerts = flat(nav({ alertsFiring: 2 }).items).find((item) => item.label === "Alerts")!;
    expect(alerts.badge).toMatchObject({ value: 2, tone: "danger" });
  });

  it("lights no console row while a host's own page is open", () => {
    expect(lit(nav({ hostPageActive: true }).items)).toEqual([]);
    expect(lit(nav({ hostPageActive: true, page: { kind: "environment", env: "prod" } }).items)).toEqual([]);
  });
});

describe("groupedCrumbs", () => {
  it("puts a view under its variant group, a hub tab under Analytics, and a dashboard under Dashboards", () => {
    expect(groupedCrumbs({ kind: "obs-view", view: "errors" }, [])).toEqual([{ label: "Performance & issues" }, { label: "Errors" }]);
    expect(groupedCrumbs({ kind: "obs-view", view: "logs" }, [])).toEqual([{ label: "Observability" }, { label: "Logs" }]);
    expect(groupedCrumbs({ kind: "dashboard", dashKind: "release", key: "users" }, [{ label: "Observability" }, { label: "Users" }], HUB)).toEqual([{ label: "Analytics" }, { label: "Users" }]);
    expect(groupedCrumbs({ kind: "dashboard", dashKind: "release", key: "api" }, [{ label: "Observability" }, { label: "Dashboards", href: "/d" }, { label: "api" }])).toEqual([
      { label: "Dashboards", href: "/d" },
      { label: "api" },
    ]);
    expect(groupedCrumbs({ kind: "releases" }, [{ label: "Releases" }])).toEqual([{ label: "Deployments" }, { label: "Releases" }]);
    expect(groupedCrumbs({ kind: "services" }, [{ label: "Services" }])).toEqual([{ label: "Deployments" }, { label: "Services" }]);
    expect(groupedCrumbs({ kind: "events" }, [{ label: "Product analytics" }, { label: "Events & metrics" }])).toEqual([{ label: "Analytics" }, { label: "Events & metrics" }]);
    expect(groupedCrumbs({ kind: "secrets" }, [{ label: "Secrets" }])).toBeNull();
  });
});

describe("PAGE_SUBTITLES", () => {
  it("has a plain line for every Observability view", () => {
    for (const view of OBS_NAV_VIEWS) expect(PAGE_SUBTITLES[view]).toBeTruthy();
  });
});
