// Copyright (c) 2025 Reliant Labs

/**
 * Which workloads are frontends, what address each is live at, and which
 * custom domains serve it. The default platform address counts as live; a
 * custom domain is never required.
 */

import { describe, expect, it } from "vitest";

import type { ForgeDomain } from "../domains";
import { frontendsInScope, frontendsOf, type FrontendEnv, type ScopeEnv } from "../frontends";
import type { ForgeHostedWorkload } from "../topology";

const PROD: FrontendEnv = { id: "env-prod", name: "prod", release: "v1.7.15" };

function workload(over: Partial<ForgeHostedWorkload>): ForgeHostedWorkload {
  return { name: "web", tier: "static", verdict: "converged", observed_state: "ready", declared_run_state: "running", ...over };
}

function domain(over: Partial<ForgeDomain> & { hostname: string }): ForgeDomain {
  return {
    id: over.hostname,
    state: "live",
    origin: "external",
    requiredRecords: [],
    lastError: "",
    binding: { id: "b", domainId: over.hostname, environmentId: "env-prod", target: "web", redirectTo: "" },
    ...over,
  };
}

describe("frontendsOf", () => {
  it("lists only static-tier workloads, by name", () => {
    const out = frontendsOf(
      PROD,
      [
        workload({ name: "docs", url: "https://b.sites.example.dev" }),
        workload({ name: "api", tier: "backend", url: "https://api.example.dev" }),
        workload({ name: "db", tier: "database" }),
        workload({ name: "web", url: "https://a.sites.example.dev" }),
      ],
      []
    );
    expect(out.map((f) => f.name)).toEqual(["docs", "web"]);
  });

  it("is live at the platform address with no custom domain at all", () => {
    const [site] = frontendsOf(PROD, [workload({ url: "https://valiant-caribou.sites.reliantlabs.dev/" })], []);
    expect(site).toMatchObject({
      url: "https://valiant-caribou.sites.reliantlabs.dev/",
      host: "valiant-caribou.sites.reliantlabs.dev",
      release: "v1.7.15",
      domains: [],
    });
  });

  it("keeps the environment's own scheme and port — a dev site is plain http", () => {
    const [site] = frontendsOf(PROD, [workload({ url: "http://wild-mongoose.apps.localhost:38080" })], null);
    expect(site!.url).toBe("http://wild-mongoose.apps.localhost:38080");
    expect(site!.host).toBe("wild-mongoose.apps.localhost:38080");
  });

  it("reports no address yet as an empty url, not an invented one", () => {
    const [site] = frontendsOf(PROD, [workload({ url: "" })], null);
    expect(site!.url).toBe("");
    expect(site!.host).toBe("");
  });

  it("attaches the domains bound to THIS site in THIS environment, openable only once live", () => {
    const [site] = frontendsOf(
      PROD,
      [workload({ url: "https://a.sites.example.dev" })],
      [
        domain({ hostname: "b.example.com", state: "pending-dns" }),
        domain({ hostname: "a.example.com" }),
        domain({ hostname: "other-target.example.com", binding: { id: "x", domainId: "x", environmentId: "env-prod", target: "docs", redirectTo: "" } }),
        domain({ hostname: "other-env.example.com", binding: { id: "y", domainId: "y", environmentId: "env-staging", target: "web", redirectTo: "" } }),
        domain({ hostname: "www.example.com", binding: { id: "z", domainId: "z", environmentId: "env-prod", target: "", redirectTo: "a.example.com" } }),
        domain({ hostname: "unbound.example.com", binding: null }),
      ]
    );
    expect(site!.domains).toEqual([
      { hostname: "a.example.com", state: "live", url: "https://a.example.com" },
      { hostname: "b.example.com", state: "pending-dns", url: null },
    ]);
  });

  it("says domains are unknown (null), not none, when the org's domains could not be read", () => {
    const [site] = frontendsOf(PROD, [workload({})], null);
    expect(site!.domains).toBeNull();
  });

  it("drops a deleted deployment and marks a stopped one", () => {
    const out = frontendsOf(
      PROD,
      [
        workload({ name: "gone", observed_state: "deleted" }),
        workload({ name: "off", observed_state: "suspended", declared_run_state: "suspended" }),
        workload({ name: "on" }),
      ],
      null
    );
    expect(out.map((f) => [f.name, f.stopped])).toEqual([
      ["off", true],
      ["on", false],
    ]);
  });
});

describe("frontendsInScope", () => {
  const envs: ScopeEnv[] = [
    { id: "env-staging", name: "staging", release: "v1.7.14", preview: false, placed: true },
    { id: "env-prod", name: "prod", release: "v1.7.15", preview: false, placed: true },
    { id: "env-pr-1", name: "pr-1", release: "v1.7.16", preview: true, placed: true },
    { id: "env-self", name: "self", release: "v1", preview: false, placed: false },
  ];
  const site = (url: string) => new Map([["web", workload({ url })]]);
  const hosted = new Map([
    ["staging", site("https://s.sites.example.dev")],
    ["prod", site("https://p.sites.example.dev")],
    ["pr-1", site("https://pr.sites.example.dev")],
    ["self", site("https://self.example.dev")],
  ]);

  it("covers every long-lived environment for 'all', prod first, never previews or self-managed ones", () => {
    expect(frontendsInScope("all", envs, hosted, null).map((f) => f.env)).toEqual(["prod", "staging"]);
  });

  it("covers just the named environment, a preview included", () => {
    expect(frontendsInScope("pr-1", envs, hosted, null).map((f) => f.url)).toEqual(["https://pr.sites.example.dev"]);
    expect(frontendsInScope("self", envs, hosted, null)).toEqual([]);
  });
});
