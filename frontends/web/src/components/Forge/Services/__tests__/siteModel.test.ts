// Copyright (c) 2026 Reliant Labs

/**
 * A static site's model (servicesModel): what GetStatus says beyond the
 * verdict, the environments its page offers and the one it opens on, its
 * Domains rows, and which promotions are its deploys.
 */

import { describe, expect, it } from "vitest";

import type { ForgeDomain } from "@/services/forge/domains";
import type { ForgeHostedWorkload } from "@/services/forge/topology";

import { ENV_ALL } from "../../Console/envScope";
import { defaultSiteEnv, promotionMoves, servicesRows, siteArtifactPrefix, siteDomainRows, siteEnvs, siteStateOf } from "../servicesModel";

const ENVS = [
  { id: "env-prod", name: "prod", release: "v1.8.3" },
  { id: "env-staging", name: "staging", release: "v1.8.3" },
  { id: "env-dev", name: "dev", release: "v1.8.4" },
];

const site = (extra: Partial<ForgeHostedWorkload> = {}): ForgeHostedWorkload => ({ name: "shop-web", tier: "static", verdict: "converged", url: "https://shop-web.reliant.app", ...extra });

function orgDomain(hostname: string, state: ForgeDomain["state"], binding: { environmentId: string; target: string } | null, lastError = ""): ForgeDomain {
  return {
    id: `dom-${hostname}`,
    hostname,
    state,
    origin: "external",
    requiredRecords: [],
    lastError,
    binding: binding ? { id: `bind-${hostname}`, domainId: `dom-${hostname}`, redirectTo: "", ...binding } : null,
  };
}

describe("siteStateOf", () => {
  it("reads a site with no domains, no drift and no suspension as nothing to say", () => {
    expect(siteStateOf(site())).toEqual({ drifted: false, lastError: "", suspendReason: "", domains: [] });
    // An older control plane's "unspecified" is no reason.
    expect(siteStateOf(site({ suspend_reason: "unspecified" })).suspendReason).toBe("");
  });

  it("carries drift, the last error, the suspension and each domain, normalised", () => {
    const state = siteStateOf(
      site({
        drifted: true,
        last_error: "  upload failed ",
        suspend_reason: "billing_lapsed",
        domains: [
          { hostname: "Shop.Example.com", state: "live", serving_path: "edge" },
          { hostname: "sale.shop.example.com", state: "issuing" },
          { hostname: "odd.example.com", state: "teleporting", serving_path: "elsewhere" },
          { hostname: " ", state: "live" },
        ],
      })
    );
    expect(state.drifted).toBe(true);
    expect(state.lastError).toBe("upload failed");
    expect(state.suspendReason).toBe("billing_lapsed");
    expect(state.domains).toEqual([
      { hostname: "shop.example.com", state: "live", servingPath: "edge" },
      { hostname: "sale.shop.example.com", state: "issuing", servingPath: "unknown" },
      { hostname: "odd.example.com", state: "unknown", servingPath: "elsewhere" },
    ]);
  });

  it("is on the Services page's site row too", () => {
    const hosted = new Map([["prod", new Map([["shop-web", site({ drifted: true, domains: [{ hostname: "shop.example.com", state: "live" }] })]])]]);
    const rows = servicesRows({ workloads: [], jobs: [], telemetry: [], hosted, envs: [{ name: "prod", release: "v1.8.3" }], env: ENV_ALL });
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ type: "site", drifted: true, domains: [{ hostname: "shop.example.com", state: "live", servingPath: "unknown" }] });
  });
});

describe("siteEnvs and defaultSiteEnv", () => {
  it("orders the site's environments as the console does, with each one's id, release, verdict and URL", () => {
    const sites = siteEnvs(
      [
        { envName: "dev", deployment: site({ verdict: "converging" }) },
        { envName: "preview-42", deployment: site() },
        { envName: "prod", deployment: site({ domains: [{ hostname: "shop.example.com", state: "live", serving_path: "edge" }] }) },
      ],
      ENVS
    );
    expect(sites.map((s) => [s.envName, s.envId, s.release, s.status])).toEqual([
      ["prod", "env-prod", "v1.8.3", "Live"],
      ["dev", "env-dev", "v1.8.4", "Deploying"],
      ["preview-42", "", "", "Live"],
    ]);
    // A live custom domain is where it is opened.
    expect(sites[0]).toMatchObject({ url: "https://shop.example.com", customDomain: "shop.example.com" });
    expect(sites[1]).toMatchObject({ url: "https://shop-web.reliant.app", customDomain: "" });
  });

  it("opens on the scope's environment, else the one with a live custom domain, else the first", () => {
    const withDomain = siteEnvs(
      [
        { envName: "staging", deployment: site({ domains: [{ hostname: "staging.shop.example.com", state: "verifying" }] }) },
        { envName: "dev", deployment: site({ domains: [{ hostname: "dev.shop.example.com", state: "live" }] }) },
      ],
      ENVS
    );
    expect(defaultSiteEnv(withDomain, "staging")?.envName).toBe("staging");
    expect(defaultSiteEnv(withDomain, ENV_ALL)?.envName).toBe("dev");
    // A live domain whose DNS points elsewhere does not serve the site.
    const elsewhere = siteEnvs(
      [
        { envName: "staging", deployment: site() },
        { envName: "dev", deployment: site({ domains: [{ hostname: "dev.shop.example.com", state: "live", serving_path: "elsewhere" }] }) },
      ],
      ENVS
    );
    expect(defaultSiteEnv(elsewhere, ENV_ALL)?.envName).toBe("staging");
    expect(defaultSiteEnv([], ENV_ALL)).toBeNull();
  });
});

describe("siteDomainRows", () => {
  const [prod] = siteEnvs(
    [
      {
        envName: "prod",
        deployment: site({
          domains: [
            { hostname: "sale.shop.example.com", state: "pending-dns", serving_path: "unknown" },
            { hostname: "shop.example.com", state: "live", serving_path: "edge" },
          ],
        }),
      },
    ],
    ENVS
  );

  it("has no row for a site with no domain", () => {
    const [bare] = siteEnvs([{ envName: "prod", deployment: site() }], ENVS);
    expect(siteDomainRows(bare!, "shop-web", [orgDomain("api.shop.example.com", "live", { environmentId: "env-prod", target: "api" })])).toEqual([]);
  });

  it("joins GetStatus's domains with the org's bound to this site here; the org's state wins; live first", () => {
    const rows = siteDomainRows(prod!, "shop-web", [
      orgDomain("sale.shop.example.com", "issuing", { environmentId: "env-prod", target: "shop-web" }, "acme order stalled"),
      orgDomain("shop.example.co.uk", "live", { environmentId: "env-prod", target: "shop-web" }),
      orgDomain("staging.shop.example.com", "verifying", { environmentId: "env-staging", target: "shop-web" }),
      orgDomain("api.shop.example.com", "live", { environmentId: "env-prod", target: "api" }),
      orgDomain("parked.example.com", "pending-dns", null),
    ]);
    expect(rows.map((r) => [r.hostname, r.state, r.servingPath])).toEqual([
      ["shop.example.com", "live", "edge"],
      ["shop.example.co.uk", "live", "unknown"],
      ["sale.shop.example.com", "issuing", "unknown"],
    ]);
    expect(rows[2]!.statusLine).toEqual({ tone: "danger", text: "Stuck: acme order stalled" });
    expect(rows[0]!.statusLine).toBeNull();
  });
});

describe("promotionMoves", () => {
  const p = (targets: string[], artifacts: string[]) => ({ targets, resolvedArtifacts: Object.fromEntries(artifacts.map((a) => [a, "sha256:0"])) });

  it("is the workload page's rule without an artifact prefix: a scoped deploy must name it", () => {
    expect(promotionMoves(p([], ["api"]), "api")).toBe(true);
    expect(promotionMoves(p(["api"], []), "api")).toBe(true);
    expect(promotionMoves(p(["worker"], ["api"]), "api")).toBe(false);
  });

  it("with the site's artifact prefix, also needs one of its artifacts when the promotion froze any", () => {
    const prefix = siteArtifactPrefix("shop-web");
    expect(prefix).toBe("sites/shop-web/");
    expect(promotionMoves(p([], ["api", "sites/shop-web/v1.8.3"]), "shop-web", prefix)).toBe(true);
    expect(promotionMoves(p([], ["api", "shop-web"]), "shop-web", prefix)).toBe(true);
    expect(promotionMoves(p([], ["api", "worker"]), "shop-web", prefix)).toBe(false);
    expect(promotionMoves(p([], []), "shop-web", prefix)).toBe(true);
    expect(promotionMoves(p(["api"], ["sites/shop-web/v1.8.3"]), "shop-web", prefix)).toBe(false);
  });
});
