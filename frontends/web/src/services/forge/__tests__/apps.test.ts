// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";

import { appLinksOf, buildApps, deployedEnvOf, isDeployed, isLive, primaryLink, type AppEnvStatus } from "../apps";
import type { CloudEnv, CloudEnvStatus } from "../cloudEnvs";
import type { ForgeHostedWorkload } from "../topology";

const env = (id: string, project: string, name: string, kind: CloudEnv["kind"] = "persistent"): CloudEnv => ({ id, project, name, kind });

const workload = (name: string, tier: string, url: string): ForgeHostedWorkload => ({ name, tier, url });

const status = (workloads: ForgeHostedWorkload[], release = "", createdAt?: string): CloudEnvStatus => ({
  verdict: "converged",
  workloads,
  currentPromotion: release
    ? { id: "p", releaseVersion: release, kind: "promote", fromEnvironmentId: "", promotedByUserId: "", promotedByActor: "", note: "", createdAt, artifacts: [] }
    : null,
});

describe("appLinksOf — what a person can open", () => {
  it("lists only sites: an API is not a destination, and what has no URL serves nobody", () => {
    const links = appLinksOf([
      workload("api", "backend", "https://calm-heron.reliantapps.dev"),
      workload("db", "database", ""),
      workload("web", "static", "https://bright-otter.reliantapps.dev/"),
      workload("pending", "static", ""),
      workload("worker", "backend", ""),
    ]);
    expect(links.map((link) => [link.workload, link.host, link.custom])).toEqual([["web", "bright-otter.reliantapps.dev", false]]);
  });

  it("links a site at its custom domain once that is live and healthy", () => {
    const web: ForgeHostedWorkload = {
      ...workload("web", "static", "https://bright-otter.reliantapps.dev"),
      domains: [{ hostname: "shop.example.com", state: "live", serving_path: "edge" }],
    };
    expect(appLinksOf([web])).toEqual([{ workload: "web", url: "https://shop.example.com", host: "shop.example.com", custom: true }]);
  });

  it("keeps the generated address while the custom domain is not ready", () => {
    const web: ForgeHostedWorkload = {
      ...workload("web", "static", "https://bright-otter.reliantapps.dev"),
      domains: [{ hostname: "shop.example.com", state: "issuing", serving_path: "edge" }],
    };
    expect(appLinksOf([web])).toEqual([
      { workload: "web", url: "https://bright-otter.reliantapps.dev", host: "bright-otter.reliantapps.dev", custom: false },
    ]);
  });
});

describe("buildApps — one entry per forge project", () => {
  const envs = [
    env("s-stg", "storefront", "staging"),
    env("s-prod", "storefront", "prod"),
    env("s-pr1", "storefront", "preview-pr-1", "preview"),
    env("s-dev", "storefront", "dev", "local"),
    env("b-prod", "blog", "prod"),
  ];
  const statuses = new Map<string, AppEnvStatus>([
    ["s-prod", status([workload("web", "static", "https://a.example")], "v2", "2026-10-20T10:00:00Z")],
    ["s-stg", undefined],
    ["s-pr1", status([], "v3", "2026-10-21T10:00:00Z")],
    ["b-prod", null],
  ]);

  it("groups hosted environments by project, prod first, previews counted, local left out", () => {
    const [storefront] = buildApps(envs, statuses, []);
    expect(storefront.name).toBe("storefront");
    expect(storefront.envs.map((e) => e.name)).toEqual(["prod", "staging"]);
    expect(storefront.previews).toBe(1);
    // A preview's deploy is still the app's newest deploy.
    expect(storefront.lastDeployedAt).toBe("2026-10-21T10:00:00Z");
  });

  it("tells a status still loading from one that failed", () => {
    const apps = buildApps(envs, statuses, []);
    const storefront = apps.find((a) => a.name === "storefront")!;
    expect(storefront.envs.find((e) => e.name === "staging")!.loading).toBe(true);
    const blog = apps.find((a) => a.name === "blog")!;
    expect(blog.envs[0].loading).toBe(false);
    expect(blog.envs[0].links).toEqual([]);
  });

  it("adds a forge project that was never deployed, and links an app to its Reliant project", () => {
    const apps = buildApps(envs, statuses, [
      { id: "proj-1", is_forge: true, forge_project_name: "storefront" },
      { id: "proj-2", is_forge: true, forge_project_name: "newsletter" },
      { id: "proj-3", is_forge: false, forge_project_name: "not-forge" },
    ]);
    expect(apps.map((a) => a.name)).toEqual(["storefront", "blog", "newsletter"]);
    expect(apps[0].projectId).toBe("proj-1");
    expect(apps[2]).toMatchObject({ name: "newsletter", projectId: "proj-2", envs: [], previews: 0 });
  });

  it("orders by the newest deploy, then the never-deployed by name", () => {
    const apps = buildApps(
      [env("a", "older", "prod"), env("b", "newer", "prod")],
      new Map([
        ["a", status([], "v1", "2026-10-01T00:00:00Z")],
        ["b", status([], "v1", "2026-10-02T00:00:00Z")],
      ]),
      [{ id: "p", is_forge: true, forge_project_name: "aardvark" }]
    );
    expect(apps.map((a) => a.name)).toEqual(["newer", "older", "aardvark"]);
  });

  it("picks prod's frontend as the app's link, and is live only with a URL", () => {
    const [storefront] = buildApps(envs, statuses, []);
    expect(primaryLink(storefront)?.url).toBe("https://a.example");
    expect(isLive(storefront)).toBe(true);
    const blog = buildApps(envs, statuses, []).find((a) => a.name === "blog")!;
    expect(primaryLink(blog)).toBeUndefined();
    expect(isLive(blog)).toBe(false);
  });

  it("does not call an app with only an API live, but does call it deployed", () => {
    const [api] = buildApps(
      [env("x-prod", "api-only", "prod")],
      new Map([["x-prod", status([workload("api", "backend", "https://calm-heron.reliantapps.dev")], "v1", "2026-10-01T00:00:00Z")]]),
      []
    );
    expect(isLive(api)).toBe(false);
    expect(primaryLink(api)).toBeUndefined();
    expect(isDeployed(api)).toBe(true);
    const [never] = buildApps([], new Map(), [{ id: "p", is_forge: true, forge_project_name: "fresh" }]);
    expect(isDeployed(never)).toBe(false);
  });
});

describe("deployedEnvOf — the environment a command deployed", () => {
  it.each([
    ["reliant forge env deploy prod", "prod"],
    ["forge env deploy staging", "staging"],
    ["cd storefront && reliant forge env deploy prod --yes", "prod"],
    ["reliant forge env deploy --target api prod", "prod"],
    ["reliant forge env deploy --frontends-only staging", "staging"],
    ["reliant forge env promote v1.2.0 --to prod", "prod"],
    ["reliant forge env promote --to=staging", "staging"],
    ["reliant forge env deploy dev && reliant forge env deploy prod", "prod"],
  ])("%s → %s", (command, expected) => {
    expect(deployedEnvOf(command)).toBe(expected);
  });

  it.each([
    "reliant forge env status prod",
    "reliant forge env deploy",
    "echo forge env deploy",
    "reliant forge env promote v1.2.0",
    "npm run deploy prod",
  ])("%s → nothing", (command) => {
    // `echo forge env deploy` has no environment after the verb, so nothing is claimed.
    expect(deployedEnvOf(command)).toBeNull();
  });
});
