// @vitest-environment node
// Copyright (c) 2025 Reliant Labs

/**
 * The words around a console page (consoleCrumbs.ts): its breadcrumb, the
 * led console's grouped rework of it, and the plain line under its title.
 */

import { describe, expect, it } from "vitest";

import { consoleCrumbs, groupedCrumbs, PAGE_SUBTITLES } from "../consoleCrumbs";
import { OBS_NAV_VIEWS } from "../paths";

const HUB = { open: { kind: "dashboard", dashKind: "release", key: "users" } as const, keys: ["users", "product", "acquisition"] };

describe("consoleCrumbs", () => {
  it("names the page, with its parent as a link", () => {
    expect(consoleCrumbs("shop", { kind: "obs-view", view: "traces" }, "prod")).toEqual([
      { label: "Observability", href: "/forge/p/shop/observability/dashboards?env=prod" },
      { label: "Traces" },
    ]);
    expect(consoleCrumbs("shop", { kind: "environment", env: "dev" }, "prod")).toEqual([
      { label: "Environments", href: "/forge/p/shop/environments?env=prod" },
      { label: "dev" },
    ]);
    expect(consoleCrumbs("shop", { kind: "dashboard", dashKind: "workspace", key: "checkout-latency" }, "prod", "Checkout latency")).toEqual([
      { label: "Observability", href: "/forge/p/shop/observability/dashboards?env=prod" },
      { label: "Dashboards", href: "/forge/p/shop/observability/dashboards?env=prod" },
      { label: "Checkout latency" },
    ]);
  });

  it("crumbs Storage, Data and the old Users page as themselves", () => {
    expect(consoleCrumbs("shop", { kind: "storage" }, "staging")).toEqual([{ label: "Storage" }]);
    expect(consoleCrumbs("shop", { kind: "data" }, "staging")).toEqual([{ label: "Data" }]);
    expect(consoleCrumbs("shop", { kind: "users" }, "staging")).toEqual([{ label: "Users" }]);
  });

  it("files a workload under the Overview scoped to its own environment", () => {
    const page = { kind: "workload" as const, env: "prod", name: "admin-server" };
    expect(consoleCrumbs("shop", page, "e2e")).toEqual([{ label: "Overview", href: "/forge/p/shop?env=prod" }, { label: "admin-server" }]);
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
