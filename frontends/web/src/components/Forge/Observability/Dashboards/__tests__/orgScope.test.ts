// Copyright (c) 2025 Reliant Labs
import { describe, expect, it } from "vitest";

import { ENV_ALL } from "../../../Console/envScope";
import { orgPlaceOf } from "../../obsPlace";
import { orgDashboardHrefs, orgExploreHref, orgQueryScope, orgScopeLabel, orgSelection, orgWorkloadHref } from "../orgScope";
import type { DashboardHrefs } from "../DashboardView";
import { workloadPivot } from "../../Metrics/pivots";

const { environments } = orgPlaceOf([
  { id: "e-cp", name: "prod", project: "control-plane", kind: "persistent" },
  { id: "e-web", name: "prod-web", project: "control-plane", kind: "persistent" },
  { id: "e-land", name: "prod", project: "landing", kind: "persistent" },
  { id: "e-land-stg", name: "staging", project: "landing", kind: "persistent" },
] satisfies Parameters<typeof orgPlaceOf>[0]);

describe("an org dashboard's scope", () => {
  it("names environments project/env, so the same env name in two projects is told apart", () => {
    expect(environments.map((e) => e.name)).toEqual(["control-plane/prod", "control-plane/prod-web", "landing/prod", "landing/staging"]);
  });

  it("with nothing picked reads every environment of the org, and names no project", () => {
    const pick = orgSelection(environments, undefined);
    expect(pick).toEqual({ names: [], picked: [], ids: [], env: ENV_ALL });
    const scope = orgQueryScope("org-1", pick);
    expect(scope).toEqual({ orgId: "org-1", envIds: [], filters: [] });
    expect("projectId" in scope).toBe(false);
    expect(orgScopeLabel(pick)).toBe("all projects · all environments");
  });

  it("reads exactly the picked environments, across projects", () => {
    const pick = orgSelection(environments, ["landing/prod", "control-plane/prod-web"]);
    expect(pick.ids).toEqual(["e-web", "e-land"]);
    expect(orgQueryScope("org-1", pick).envIds).toEqual(["e-web", "e-land"]);
    expect(pick.env).toBe(ENV_ALL);
    expect(orgScopeLabel(pick)).toBe("control-plane/prod-web, landing/prod");
  });

  it("one pick is that environment (compare and alerts key on it)", () => {
    const pick = orgSelection(environments, ["landing/staging"]);
    expect(pick).toMatchObject({ ids: ["e-land-stg"], env: "landing/staging" });
  });

  it("drops a pick the org no longer has instead of scoping the page to nothing", () => {
    const pick = orgSelection(environments, ["landing/torn-down-preview"]);
    expect(pick.ids).toEqual([]);
    expect(orgScopeLabel(orgSelection(environments, environments.map((e) => e.name)))).toBe("4 environments");
  });
});

describe("an org dashboard's links", () => {
  const consoleHrefs: DashboardHrefs = {
    logs: () => "/forge/p/control-plane/observability/logs",
    traces: () => "/forge/p/control-plane/observability/traces",
    workload: () => "/forge/p/control-plane/overview",
    list: "/forge/p/control-plane/observability/dashboards",
    release: (v) => `/forge/p/control-plane/releases/${v}`,
    trace: () => "/forge/p/control-plane/observability/traces",
    dashboard: () => "/forge/p/control-plane/dashboards/x",
  };
  const params = (href: string) => new URL(href, "http://x").searchParams;

  it("open the org's explorers on the picked project/env, not the route project's console", () => {
    const hrefs = orgDashboardHrefs(consoleHrefs, orgSelection(environments, ["landing/prod"]));
    const logs = hrefs.logs({ service: "web", range: "1h" });
    expect(logs.startsWith("/forge/observability/logs?")).toBe(true);
    expect(params(logs).get("envs")).toBe(JSON.stringify(["landing/prod"]));
    expect(params(logs).get("service")).toBe("web");
    expect(hrefs.traces({}).startsWith("/forge/observability/traces")).toBe(true);
    expect(hrefs.trace("t1")).toContain("/forge/observability/traces");
    // The dashboard's own pages stay the console's.
    expect(hrefs.list).toBe(consoleHrefs.list);
  });

  it("read every environment when none is picked", () => {
    const logs = orgDashboardHrefs(consoleHrefs, orgSelection(environments, undefined)).logs({});
    expect(logs).toBe("/forge/observability/logs");
  });

  it("open a workload in its own project under the bare environment name, only when one environment is picked", () => {
    expect(orgWorkloadHref(orgSelection(environments, ["landing/staging"]), "web")).toBe("/forge/p/landing/workloads/staging/web");
    expect(orgWorkloadHref(orgSelection(environments, ["landing/prod", "control-plane/prod"]), "web")).toBe("");
    expect(orgWorkloadHref(orgSelection(environments, undefined), "web")).toBe("");
  });

  it("explore in the org's Metrics on the picked environments", () => {
    const explore = orgExploreHref(orgSelection(environments, ["landing/prod"]));
    const href = explore(workloadPivot("web", "cpu"))!;
    expect(href.startsWith("/forge/observability/metrics")).toBe(true);
    expect(params(href).get("envs")).toBe(JSON.stringify(["landing/prod"]));
    expect(params(href).get("env")).toBeNull();
  });
});
