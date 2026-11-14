// @vitest-environment node
// Copyright (c) 2026 Reliant Labs

/**
 * The led console's grouped nav: the console's same pages and links,
 * regrouped, for any project shape (one environment, many, previews, starred
 * dashboards, no Product analytics hub).
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
  it("groups the console's pages, Configure last and Activity in the footer", () => {
    const { items, footer } = nav({ productAnalytics: HUB });
    expect(items.map((item) => [item.label, (item.children ?? []).map((child) => child.label).join(",")])).toEqual([
      ["Overview", ""],
      ["Issues", "Errors,Alerts"],
      ["Performance", "Traces,RPC explorer"],
      ["Sessions", ""],
      ["Product analytics", ""],
      ["Releases", ""],
      ["Data", ""],
      ["Users", ""],
      ["More tools", "Dashboards,Logs,Metrics,Profiles,Service graph,Storage"],
      ["Environments", "prod"],
      ["Secrets", ""],
      ["Domains", ""],
    ]);
    expect(footer.map((item) => item.label)).toEqual(["Activity"]);
  });

  it("reaches every page the console's own nav reaches, with the same scoped links", () => {
    const input = { project: "shop", page: { kind: "overview" } as const, env: "staging", envs: [{ name: "staging", preview: false }] };
    const own = hrefs(buildConsoleNav(input).items);
    const grouped = hrefs(buildGroupedConsoleNav(input).items);
    for (const href of own) expect(grouped).toContain(href);
    for (const view of OBS_NAV_VIEWS) expect([...grouped].some((href) => href.includes(`/observability/${view}`))).toBe(true);
  });

  it("has no Product analytics row without a hub, and lights it on any of the hub's tabs", () => {
    expect(nav().items.map((item) => item.label)).not.toContain("Product analytics");
    expect(lit(nav({ productAnalytics: HUB, page: { kind: "dashboard", dashKind: "release", key: "acquisition" } }).items)).toEqual(["Product analytics"]);
    // Any other dashboard lights Dashboards, under More tools.
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
    const environments = nav({ envs }).items.find((item) => item.label === "Environments")!;
    expect(environments.children).toHaveLength(13);
    expect(environments.children?.at(-1)).toMatchObject({ label: "Previews", badge: { value: 2 } });
  });

  it("leaves Profiles out on a definite empty read, unless it is the open page", () => {
    expect(flat(nav({ optionalViews: { profiles: false } }).items).map((item) => item.label)).not.toContain("Profiles");
    expect(lit(nav({ optionalViews: { profiles: false }, page: { kind: "obs-view", view: "profiles" } }).items)).toEqual(["Profiles"]);
  });

  it("counts alerts firing on Alerts", () => {
    const alerts = flat(nav({ alertsFiring: 2 }).items).find((item) => item.label === "Alerts")!;
    expect(alerts.badge).toMatchObject({ value: 2, tone: "danger" });
  });

  it("lights no console row while a host's own page is open", () => {
    expect(lit(nav({ hostPageActive: true }).items)).toEqual([]);
    expect(lit(nav({ hostPageActive: true, page: { kind: "environment", env: "prod" } }).items)).toEqual([]);
  });
});

describe("groupedCrumbs", () => {
  it("puts a view under its group, a hub tab under Product analytics, and a dashboard under More tools", () => {
    expect(groupedCrumbs({ kind: "obs-view", view: "errors" }, [])).toEqual([{ label: "Issues" }, { label: "Errors" }]);
    expect(groupedCrumbs({ kind: "obs-view", view: "sessions" }, [])).toEqual([{ label: "Sessions" }]);
    expect(groupedCrumbs({ kind: "dashboard", dashKind: "release", key: "users" }, [{ label: "Observability" }, { label: "Users" }], HUB)).toEqual([{ label: "Product analytics" }, { label: "Users" }]);
    expect(groupedCrumbs({ kind: "dashboard", dashKind: "release", key: "api" }, [{ label: "Observability" }, { label: "Dashboards", href: "/d" }, { label: "api" }])).toEqual([
      { label: "More tools" },
      { label: "Dashboards", href: "/d" },
      { label: "api" },
    ]);
    expect(groupedCrumbs({ kind: "releases" }, [{ label: "Releases" }])).toBeNull();
  });
});

describe("PAGE_SUBTITLES", () => {
  it("has a plain line for every Observability view", () => {
    for (const view of OBS_NAV_VIEWS) expect(PAGE_SUBTITLES[view]).toBeTruthy();
  });
});
