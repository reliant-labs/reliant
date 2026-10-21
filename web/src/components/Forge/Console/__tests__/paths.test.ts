// Copyright (c) 2025 Reliant Labs

/**
 * The console's URL scheme and the redirects from every retired URL. The
 * redirect table is the contract notification deep links depend on: a
 * control plane that wrote `/forge/env/prod?forgeProject=x` last week must
 * still land on prod.
 */

import { describe, expect, it } from "vitest";
import { defaultParseSearch } from "@tanstack/react-router";

import {
  consolePageOf,
  consolePath,
  envScopeOfEnvs,
  legacyEnvTarget,
  legacyObservabilityTarget,
  obsViewOfPage,
  pageOfObsView,
  pinnedEnvOf,
  withSearch,
  type ConsolePage,
} from "../paths";

const PAGES: ConsolePage[] = [
  { kind: "overview" },
  { kind: "workload", env: "prod", name: "admin-server" },
  { kind: "workload", env: "pr 12/x", name: "web" },
  { kind: "releases" },
  { kind: "release", version: "20261018.155157-8aecd00c380b" },
  { kind: "release", version: "v1.8.0+build/7" },
  { kind: "observability" },
  { kind: "obs-view", view: "logs" },
  { kind: "obs-view", view: "metrics" },
  { kind: "obs-view", view: "alerts" },
  { kind: "obs-view", view: "dashboards" },
  { kind: "dashboard", dashKind: "release", key: "service-api" },
  { kind: "dashboard", dashKind: "workspace", key: "checkout latency/2" },
  { kind: "environments" },
  { kind: "environment", env: "prod" },
  { kind: "environment", env: "pr 12/x" },
  { kind: "secrets" },
  { kind: "domains" },
  { kind: "monitoring" },
  { kind: "settings" },
  { kind: "activity" },
];

describe("console paths", () => {
  it("are the PROPOSAL §3.2 scheme", () => {
    expect(consolePath("control-plane", { kind: "overview" })).toBe("/forge/p/control-plane");
    expect(consolePath("control-plane", { kind: "obs-view", view: "traces" })).toBe("/forge/p/control-plane/observability/traces");
    expect(consolePath("control-plane", { kind: "environment", env: "prod" })).toBe("/forge/p/control-plane/environments/prod");
    expect(consolePath("control-plane", { kind: "release", version: "v1.8.2" })).toBe("/forge/p/control-plane/releases/v1.8.2");
    expect(consolePath("control-plane", { kind: "workload", env: "prod", name: "admin-server" })).toBe("/forge/p/control-plane/workloads/prod/admin-server");
  });

  it("pins a workload's page to its environment, as an environment's page is", () => {
    expect(pinnedEnvOf({ kind: "workload", env: "e2e", name: "api" })).toBe("e2e");
    expect(pinnedEnvOf({ kind: "overview" })).toBeNull();
    // A workload is one per environment: a path without both names no page.
    expect(consolePageOf("/forge/p/x/workloads/prod")).toBeNull();
    expect(consolePageOf("/forge/p/x/workloads/prod/api/extra")).toBeNull();
  });

  it.each(PAGES)("round-trips %o through its path", (page) => {
    expect(consolePageOf(consolePath("my shop", page))).toEqual({ project: "my shop", page });
  });

  it("knows a path outside the console, or a page that does not exist, is not one", () => {
    expect(consolePageOf("/forge")).toBeNull();
    expect(consolePageOf("/forge/env/prod")).toBeNull();
    expect(consolePageOf("/forge/p/x/observability/workloads")).toBeNull();
    expect(consolePageOf("/forge/p/x/nope")).toBeNull();
    // A dashboard is addressed by its kind: a template is no page, and nothing nests below a dashboard.
    expect(consolePageOf("/forge/p/x/observability/dashboards/template/service-red")).toBeNull();
    expect(consolePageOf("/forge/p/x/observability/dashboards/workspace")).toBeNull();
    expect(consolePageOf("/forge/p/x/observability/dashboards/workspace/a/b")).toBeNull();
    expect(consolePageOf("/forge/p/x/observability/logs/workspace/a")).toBeNull();
  });

  it("puts a dashboard under Observability › Dashboards, by kind and key", () => {
    expect(consolePath("shop", { kind: "dashboard", dashKind: "workspace", key: "checkout-latency" })).toBe("/forge/p/shop/observability/dashboards/workspace/checkout-latency");
  });

  it("builds a query string the router parses back to the same search", () => {
    const search = { env: "prod", f: ["service.version:v1"], live: true, service: "123", q: undefined };
    const href = withSearch("/x", search);
    expect(defaultParseSearch(href.slice(href.indexOf("?")))).toEqual({ env: "prod", f: ["service.version:v1"], live: true, service: "123" });
    expect(withSearch("/x", {})).toBe("/x");
  });
});

describe("Observability views as pages (D9)", () => {
  it("files each view under its new home", () => {
    expect(pageOfObsView(undefined)).toEqual({ kind: "observability" });
    expect(pageOfObsView("logs")).toEqual({ kind: "obs-view", view: "logs" });
    expect(pageOfObsView("usage")).toEqual({ kind: "settings" });
  });

  it("knows which view each page hosts, and that Metrics and Dashboards host none", () => {
    expect(obsViewOfPage({ kind: "overview" })).toBeNull();
    expect(obsViewOfPage({ kind: "obs-view", view: "metrics" })).toBeNull();
    expect(obsViewOfPage({ kind: "obs-view", view: "dashboards" })).toBeNull();
    expect(obsViewOfPage({ kind: "dashboard", dashKind: "release", key: "x" })).toBeNull();
    expect(obsViewOfPage({ kind: "secrets" })).toBeNull();
  });

  it("turns the old env filter into the scope: one env, else all", () => {
    expect(envScopeOfEnvs(["e2e"])).toBe("e2e");
    expect(envScopeOfEnvs(undefined)).toBe("all");
    expect(envScopeOfEnvs([])).toBe("all");
    expect(envScopeOfEnvs(["prod", "e2e"])).toBe("all");
  });
});

describe("redirects from the retired URLs", () => {
  it.each([
    [{}, "/forge/p/shop/environments/prod?env=prod"],
    [{ tab: "overview" }, "/forge/p/shop/environments/prod?env=prod"],
    [{ tab: "changes" }, "/forge/p/shop/environments/prod?env=prod"],
    [{ tab: "running" }, "/forge/p/shop/environments/prod?env=prod&tab=running"],
    [{ tab: "domains" }, "/forge/p/shop/environments/prod?env=prod&tab=domains"],
    [{ tab: "identity" }, "/forge/p/shop/environments/prod?env=prod&tab=identity"],
    [{ tab: "releases" }, "/forge/p/shop/releases?env=prod"],
    [{ tab: "activity" }, "/forge/p/shop/activity?env=prod"],
    [{ tab: "secrets", secret: "DATABASE_URL" }, "/forge/p/shop/secrets?env=prod&secret=DATABASE_URL"],
    [{ tab: "observability" }, "/forge/p/shop/observability?env=prod"],
  ])("/forge/env/prod %o → %s", (search, target) => {
    expect(legacyEnvTarget("shop", "prod", search)).toBe(target);
  });

  it("keeps an Observability tab's view and filters, scoped to the env", () => {
    const href = legacyEnvTarget("shop", "prod", { tab: "observability", view: "traces", trace: "abc", range: "6h" });
    expect(href.startsWith("/forge/p/shop/observability/traces?")).toBe(true);
    expect(defaultParseSearch(href.slice(href.indexOf("?")))).toEqual({ env: "prod", trace: "abc", range: "6h" });
  });

  it("sends /forge/observability to the page hosting its view, with envs as the scope", () => {
    expect(legacyObservabilityTarget("shop", {})).toBe("/forge/p/shop/observability?env=all");
    expect(legacyObservabilityTarget("shop", { view: "logs", envs: ["e2e"], q: "timeout" })).toBe(
      "/forge/p/shop/observability/logs?env=e2e&q=timeout"
    );
    expect(legacyObservabilityTarget("shop", { view: "usage" })).toBe("/forge/p/shop/settings?env=all");
  });

  it("sends the retired Workloads view to the Overview, or to the workload's page when it names one environment", () => {
    expect(legacyObservabilityTarget("shop", { view: "workloads", envs: ["prod"] })).toBe("/forge/p/shop?env=prod");
    expect(legacyObservabilityTarget("shop", { view: "workloads", envs: ["prod"], workload: "api" })).toBe("/forge/p/shop/workloads/prod/api?env=prod");
    // Every environment: the Overview, filtered to the workload, lets the reader pick one.
    expect(legacyObservabilityTarget("shop", { view: "workloads", workload: "api" })).toBe("/forge/p/shop?env=all&q=api");
    expect(legacyEnvTarget("shop", "e2e", { tab: "observability", view: "workloads", workload: "api" })).toBe("/forge/p/shop/workloads/e2e/api?env=e2e");
  });
});
