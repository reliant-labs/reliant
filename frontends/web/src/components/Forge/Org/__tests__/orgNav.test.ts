// Copyright (c) 2025 Reliant Labs

/**
 * The org console's information architecture: Apps (the /apps list, with the
 * org's apps nested) and Observability › its views, every org link carrying
 * the org filters.
 */

import { describe, expect, it } from "vitest";

import { buildOrgNav, orgCrumbs } from "../OrgShell";

const APPS = ["api", "shop"];
const OVERVIEW = { kind: "observability" } as const;

describe("buildOrgNav", () => {
  it("is Apps, then Observability ›", () => {
    const { items } = buildOrgNav({ page: OVERVIEW, apps: APPS });
    expect(items.map((item) => item.label)).toEqual(["Apps", "Observability"]);
    expect(items[0]!.href).toBeUndefined();
    expect(items[1]!.href).toBe("/forge/observability");
  });

  it("nests each app under Apps, linking its console", () => {
    const apps = buildOrgNav({ page: OVERVIEW, apps: APPS }).items[0]!;
    expect(apps.children?.map((child) => [child.label, child.href])).toEqual([
      ["All apps", "/apps"],
      ["api", "/forge/p/api"],
      ["shop", "/forge/p/shop"],
    ]);
  });

  it("nests no apps when the org has none", () => {
    expect(buildOrgNav({ page: OVERVIEW, apps: [] }).items[0]!.children?.map((child) => child.href)).toEqual(["/apps"]);
  });

  it("encodes an app name in its path", () => {
    const apps = buildOrgNav({ page: OVERVIEW, apps: ["my app"] }).items[0]!;
    expect(apps.children?.[1]?.href).toBe("/forge/p/my%20app");
  });

  it("lists Observability's views as its children, in the console's order", () => {
    const obs = buildOrgNav({ page: OVERVIEW, apps: APPS }).items[1]!;
    expect(obs.children?.map((child) => child.label)).toEqual([
      "Service graph",
      "RPC explorer",
      "Errors",
      "Sessions",
      "Logs",
      "Traces",
      "Metrics",
      "Profiles",
      "Dashboards",
      "Alerts",
    ]);
    expect(obs.children?.map((child) => child.href)).toEqual([
      "/forge/observability/graph",
      "/forge/observability/rpc",
      "/forge/observability/errors",
      "/forge/observability/sessions",
      "/forge/observability/logs",
      "/forge/observability/traces",
      "/forge/observability/metrics",
      "/forge/observability/profiles",
      "/forge/observability/dashboards",
      "/forge/observability/alerts",
    ]);
  });

  it("badges Alerts with the firing count, and only when something is firing", () => {
    const alerts = (firing?: number) =>
      buildOrgNav({ page: OVERVIEW, apps: APPS, alertsFiring: firing }).items[1]!.children!.find((child) => child.label === "Alerts")!;
    expect(alerts(3).badge).toMatchObject({ type: "count", value: 3, tone: "danger" });
    expect(alerts(0).badge).toBeUndefined();
    expect(alerts().badge).toBeUndefined();
  });

  it("marks the current page, and only it", () => {
    const active = (page: Parameters<typeof buildOrgNav>[0]["page"]) => {
      const { items } = buildOrgNav({ page, apps: APPS });
      return items
        .flatMap((item) => [item, ...(item.children ?? [])])
        .filter((item) => item.active)
        .map((item) => item.label);
    };
    expect(active({ kind: "observability" })).toEqual(["Observability"]);
    expect(active({ kind: "obs-view", view: "logs" })).toEqual(["Logs"]);
  });

  it("carries the org filters on every org link, but not into an app's console", () => {
    const { items } = buildOrgNav({
      page: { kind: "obs-view", view: "logs" },
      apps: APPS,
      search: { apps: ["shop"], envs: ["shop/prod"], range: "7d", q: "timeout", view: "traces", project: "x", unrelated: 1 },
    });
    const orgLinks = [items[1]!, ...items[1]!.children!];
    for (const link of orgLinks) {
      const [, query = ""] = link.href!.split("?");
      const params = new URLSearchParams(query);
      expect(JSON.parse(params.get("apps")!)).toEqual(["shop"]);
      expect(JSON.parse(params.get("envs")!)).toEqual(["shop/prod"]);
      expect(params.get("range")).toBe("7d");
      expect(params.get("q")).toBe("timeout");
      // `view` is the path now; `project` would redirect the org page into one app.
      expect(params.has("view")).toBe(false);
      expect(params.has("project")).toBe(false);
      expect(params.has("unrelated")).toBe(false);
    }
    // /apps and each app's console are outside the org console: they take none of them.
    expect(items[0]!.children?.map((child) => child.href)).toEqual(["/apps", "/forge/p/api", "/forge/p/shop"]);
  });
});

describe("orgCrumbs", () => {
  it("is Observability, or Observability › the view", () => {
    expect(orgCrumbs({ kind: "observability" })).toEqual([{ label: "Observability" }]);
    expect(orgCrumbs({ kind: "obs-view", view: "alerts" })).toEqual([
      { label: "Observability", href: "/forge/observability" },
      { label: "Alerts" },
    ]);
  });

  it("keeps the filters on the way back up", () => {
    const [root] = orgCrumbs({ kind: "obs-view", view: "logs" }, { apps: ["shop"] });
    expect(root!.href).toContain("/forge/observability?apps=");
    expect(JSON.parse(new URLSearchParams(root!.href!.split("?")[1]).get("apps")!)).toEqual(["shop"]);
  });
});
