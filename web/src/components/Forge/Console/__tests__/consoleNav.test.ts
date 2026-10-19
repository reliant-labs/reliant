// Copyright (c) 2025 Reliant Labs

/**
 * The console's information architecture (research/console-v3/DECISIONS.md):
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
      ["Monitoring settings", "Configure"],
      ["Project settings", "Configure"],
    ]);
    expect(footer.map((item) => item.label)).toEqual(["Activity"]);
  });

  it("lists Observability's views as its children (D9)", () => {
    const { items } = buildConsoleNav({ project: "shop", page: { kind: "overview" }, env: "prod", envs: ENVS, alertsFiring: 2 });
    const obs = items.find((item) => item.label === "Observability")!;
    expect(obs.children?.map((child) => child.label)).toEqual(["Logs", "Traces", "Metrics", "Profiles", "Dashboards", "Alerts"]);
    expect(obs.children?.find((child) => child.label === "Alerts")?.badge).toMatchObject({ type: "count", value: 2, tone: "danger" });
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
});

describe("consoleCrumbs", () => {
  it("names the page, with its parent as a link", () => {
    expect(consoleCrumbs("shop", { kind: "obs-view", view: "traces" }, "prod")).toEqual([
      { label: "Observability", href: "/forge/p/shop/observability?env=prod" },
      { label: "Traces" },
    ]);
    expect(consoleCrumbs("shop", { kind: "environment", env: "dev" }, "prod")).toEqual([
      { label: "Environments", href: "/forge/p/shop/environments?env=prod" },
      { label: "dev" },
    ]);
  });
});
