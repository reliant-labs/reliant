// @vitest-environment node
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
  carriedObsSearch,
  carriedOrgSearch,
  consolePageOf,
  consolePath,
  envScopeOfEnvs,
  legacyEnvTarget,
  legacyObservabilityTarget,
  obsViewOfPage,
  orgPageOf,
  orgPath,
  pageOfObsView,
  pageOfTarget,
  pinnedEnvOf,
  withSearch,
  type ConsolePage,
  type LegacyEnvSearch,
  type OrgPage,
} from "../paths";

it("round-trips the deprecated Speed insights target before the route redirects it", () => {
  const path = "/forge/p/shop/speed-insights";
  const parsed = consolePageOf(path)!;
  expect(parsed).toEqual({ project: "shop", page: { kind: "speed-insights" } });
  expect(pageOfTarget(parsed.page)).toEqual({ kind: "speed-insights" });
  expect(consolePath(parsed.project, pageOfTarget(parsed.page))).toBe(path);
});

const PAGES: ConsolePage[] = [
  { kind: "overview" },
  { kind: "workload", env: "prod", name: "admin-server" },
  { kind: "workload", env: "pr 12/x", name: "web" },
  { kind: "releases" },
  { kind: "release", version: "20261018.155157-8aecd00c380b" },
  { kind: "release", version: "v1.8.0+build/7" },
  { kind: "obs-view", view: "logs" },
  { kind: "obs-view", view: "metrics" },
  { kind: "obs-view", view: "alerts" },
  { kind: "obs-view", view: "dashboards" },
  { kind: "dashboard", dashKind: "release", key: "service-api" },
  { kind: "dashboard", dashKind: "workspace", key: "checkout latency/2" },
  { kind: "storage" },
  { kind: "data" },
  { kind: "auth" },
  { kind: "auth", tab: "sign-in" },
  { kind: "environments" },
  { kind: "environment", env: "prod" },
  { kind: "environment", env: "pr 12/x" },
  { kind: "secrets" },
  { kind: "domains" },
  { kind: "activity" },
  { kind: "vulnerabilities" },
  { kind: "vulnerability", advisory: "CVE-2026-4101" },
  { kind: "vulnerability", advisory: "GHSA-7f3x-q2mv-9hc4" },
  { kind: "sboms" },
];

describe("console paths", () => {
  it("are the console's URL scheme", () => {
    expect(consolePath("control-plane", { kind: "overview" })).toBe("/forge/p/control-plane");
    expect(consolePath("control-plane", { kind: "obs-view", view: "traces" })).toBe("/forge/p/control-plane/observability/traces");
    expect(consolePath("control-plane", { kind: "environment", env: "prod" })).toBe("/forge/p/control-plane/environments/prod");
    expect(consolePath("control-plane", { kind: "release", version: "v1.8.2" })).toBe("/forge/p/control-plane/releases/v1.8.2");
    expect(consolePath("control-plane", { kind: "workload", env: "prod", name: "admin-server" })).toBe("/forge/p/control-plane/workloads/prod/admin-server");
    expect(consolePath("control-plane", { kind: "storage" })).toBe("/forge/p/control-plane/storage");
    expect(consolePath("control-plane", { kind: "data" })).toBe("/forge/p/control-plane/data");
    // The old Users page is Auth's first tab (U6d): its path is Auth's.
    expect(consolePath("control-plane", { kind: "users" })).toBe("/forge/p/control-plane/auth");
    expect(consolePath("control-plane", { kind: "auth" })).toBe("/forge/p/control-plane/auth");
    expect(consolePath("control-plane", { kind: "auth", tab: "sign-in" })).toBe("/forge/p/control-plane/auth/sign-in");
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

  it("reads /observability as Observability's own Overview (U6e), and builds it back", () => {
    expect(consolePageOf("/forge/p/x/observability")).toEqual({ project: "x", page: { kind: "obs-overview" } });
    expect(consolePath("x", { kind: "obs-overview" })).toBe("/forge/p/x/observability");
  });

  it("knows a path outside the console, or a page that does not exist, is not one", () => {
    expect(consolePageOf("/forge")).toBeNull();
    expect(consolePageOf("/forge/env/prod")).toBeNull();
    expect(consolePageOf("/forge/p/x/observability/workloads")).toBeNull();
    expect(consolePageOf("/forge/p/x/nope")).toBeNull();
    // The retired pages are redirects, not pages.
    expect(consolePageOf("/forge/p/x/monitoring")).toBeNull();
    expect(consolePageOf("/forge/p/x/settings")).toBeNull();
    // A dashboard is addressed by its kind: a template is no page, and nothing nests below a dashboard.
    expect(consolePageOf("/forge/p/x/observability/dashboards/template/service-red")).toBeNull();
    expect(consolePageOf("/forge/p/x/observability/dashboards/workspace")).toBeNull();
    expect(consolePageOf("/forge/p/x/observability/dashboards/workspace/a/b")).toBeNull();
    expect(consolePageOf("/forge/p/x/observability/logs/workspace/a")).toBeNull();
  });

  it("puts Vulnerabilities and SBOMs under Security", () => {
    expect(consolePath("shop", { kind: "vulnerabilities" })).toBe("/forge/p/shop/security/vulnerabilities");
    expect(consolePath("shop", { kind: "vulnerability", advisory: "CVE-2026-4101" })).toBe("/forge/p/shop/security/vulnerabilities/CVE-2026-4101");
    expect(consolePath("shop", { kind: "sboms" })).toBe("/forge/p/shop/security/sboms");
    // The section itself is a redirect, and nothing nests below an advisory or the SBOMs.
    expect(consolePageOf("/forge/p/shop/security")).toBeNull();
    expect(consolePageOf("/forge/p/shop/security/vulnerabilities/CVE-1/x")).toBeNull();
    expect(consolePageOf("/forge/p/shop/security/sboms/x")).toBeNull();
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

  it("answers a repeated search from memory, and never confuses a value with its type or another order", () => {
    const search = { env: "prod", f: ["a"], page: 2 };
    const first = withSearch("/x", search);
    expect(withSearch("/x", { ...search })).toBe(first);
    // A number and its digits as a string encode differently ("2" is quoted).
    expect(withSearch("/x", { env: "prod", f: ["a"], page: "2" })).not.toBe(first);
    expect(withSearch("/x", { f: ["a"], env: "prod", page: 2 })).toBe("/x?f=%5B%22a%22%5D&env=prod&page=2");
    expect(withSearch("/y", search)).toBe(first.replace("/x", "/y"));
    expect(withSearch("/x", { ...search, f: ["b"] })).not.toBe(first);
  });
});

describe("Observability views as pages", () => {
  it("files each view under its new home", () => {
    // The panel's own overview is the console's Overview: one project home.
    expect(pageOfObsView(undefined)).toEqual({ kind: "overview" });
    expect(pageOfObsView("overview")).toEqual({ kind: "overview" });
    expect(pageOfObsView("logs")).toEqual({ kind: "obs-view", view: "logs" });
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
  it.each<[LegacyEnvSearch, string]>([
    [{}, "/forge/p/shop/environments/prod?env=prod"],
    [{ tab: "overview" }, "/forge/p/shop/environments/prod?env=prod"],
    [{ tab: "changes" }, "/forge/p/shop/environments/prod?env=prod"],
    [{ tab: "running" }, "/forge/p/shop/environments/prod?env=prod&tab=running"],
    [{ tab: "domains" }, "/forge/p/shop/domains?env=prod"],
    [{ tab: "identity" }, "/forge/p/shop/environments/prod?env=prod&tab=identity"],
    [{ tab: "releases" }, "/forge/p/shop/releases?env=prod"],
    [{ tab: "activity" }, "/forge/p/shop/activity?env=prod"],
    [{ tab: "secrets", secret: "DATABASE_URL" }, "/forge/p/shop/secrets?env=prod&secret=DATABASE_URL"],
    [{ tab: "observability" }, "/forge/p/shop?env=prod"],
    [{ tab: "observability", view: "usage" }, "/settings/billing"],
    // The control plane's queued-deploy link names its org: the environment
    // page asks before switching to it, so the redirect must not drop it.
    [{ org: "org-b" }, "/forge/p/shop/environments/prod?env=prod&org=org-b"],
    [{ tab: "identity", org: "org-b" }, "/forge/p/shop/environments/prod?env=prod&tab=identity&org=org-b"],
  ])("/forge/env/prod %o → %s", (search, target) => {
    expect(legacyEnvTarget("shop", "prod", search)).toBe(target);
  });

  it("keeps an Observability tab's view and filters, scoped to the env", () => {
    const href = legacyEnvTarget("shop", "prod", { tab: "observability", view: "traces", trace: "abc", range: "6h" });
    expect(href.startsWith("/forge/p/shop/observability/traces?")).toBe(true);
    expect(defaultParseSearch(href.slice(href.indexOf("?")))).toEqual({ env: "prod", trace: "abc", range: "6h" });
  });

  it("sends /forge/observability to the page hosting its view, with envs as the scope", () => {
    expect(legacyObservabilityTarget("shop", {})).toBe("/forge/p/shop?env=all");
    expect(legacyObservabilityTarget("shop", { view: "logs", envs: ["e2e"], q: "timeout" })).toBe(
      "/forge/p/shop/observability/logs?env=e2e&q=timeout"
    );
    // The organization's usage is the app's Settings › Billing.
    expect(legacyObservabilityTarget("shop", { view: "usage" })).toBe("/settings/billing");
  });

  it("sends the retired Workloads view to the Overview, or to the workload's page when it names one environment", () => {
    expect(legacyObservabilityTarget("shop", { view: "workloads", envs: ["prod"] })).toBe("/forge/p/shop?env=prod");
    expect(legacyObservabilityTarget("shop", { view: "workloads", envs: ["prod"], workload: "api" })).toBe("/forge/p/shop/workloads/prod/api?env=prod");
    // Every environment: the Overview, filtered to the workload, lets the reader pick one.
    expect(legacyObservabilityTarget("shop", { view: "workloads", workload: "api" })).toBe("/forge/p/shop?env=all&q=api");
    expect(legacyEnvTarget("shop", "e2e", { tab: "observability", view: "workloads", workload: "api" })).toBe("/forge/p/shop/workloads/e2e/api?env=e2e");
  });
});

const ORG_PAGES: OrgPage[] = [
  { kind: "observability" },
  { kind: "obs-view", view: "errors" },
  { kind: "obs-view", view: "dashboards" },
  { kind: "obs-view", view: "alerts" },
];

describe("org paths", () => {
  it("are /forge/observability and /forge/observability/<view>", () => {
    expect(orgPath({ kind: "observability" })).toBe("/forge/observability");
    expect(orgPath({ kind: "obs-view", view: "logs" })).toBe("/forge/observability/logs");
  });

  it.each(ORG_PAGES)("round-trip %o through orgPageOf", (page) => {
    expect(orgPageOf(orgPath(page))).toEqual(page);
  });

  it("tolerate a trailing slash", () => {
    expect(orgPageOf("/forge/observability/")).toEqual({ kind: "observability" });
  });

  it("are null outside the org pages", () => {
    expect(orgPageOf("/")).toBeNull();
    // The Apps list is /apps, not an org console page; bare /forge redirects there.
    expect(orgPageOf("/forge")).toBeNull();
    expect(orgPageOf("/forge/observability/heatmaps")).toBeNull();
    expect(orgPageOf("/forge/observability/logs/extra")).toBeNull();
    expect(orgPageOf("/forge/p/shop")).toBeNull();
    expect(orgPageOf("/forge/p/shop/observability")).toBeNull();
    expect(orgPageOf("/forge/domains")).toBeNull();
    expect(orgPageOf("/forge/env/prod")).toBeNull();
  });

  it("leave the project console's own parsing alone", () => {
    expect(consolePageOf("/forge")).toBeNull();
    expect(consolePageOf("/forge/observability")).toBeNull();
  });
});

describe("carried search", () => {
  const search = { range: "6h", q: "timeout", envs: ["shop/prod"], apps: ["shop"], view: "logs", project: "p1", unrelated: 1 };

  it("an org page carries the Observability keys plus envs and apps (the view is the path)", () => {
    expect(carriedOrgSearch(search)).toEqual({ range: "6h", q: "timeout", envs: ["shop/prod"], apps: ["shop"] });
  });

  it("an app's console takes neither the org's envs nor its apps", () => {
    expect(carriedObsSearch(search)).toEqual({ range: "6h", q: "timeout" });
  });
});
