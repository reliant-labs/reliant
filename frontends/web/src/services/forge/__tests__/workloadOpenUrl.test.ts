// Copyright (c) 2025 Reliant Labs

/**
 * Where a deployed workload is opened: its custom domain once that is live and
 * still points at the platform, else its generated *.reliantlabs.dev address.
 * Every apps surface and the console link through workloadOpenUrl, so the rule
 * is pinned here once, for every domain state.
 */

import { describe, expect, it } from "vitest";

import { DeployCustomDomainState, DeployServingPath } from "@/gen/controlplane/controlplane/v1/deploy_pb";

import { toCloudEnvStatus } from "../cloudEnvs";
import { workloadCustomDomain, workloadOpenUrl, type ForgeHostedWorkload, type HostedDomain } from "../topology";

const PLATFORM = "https://bright-otter.sites.reliantlabs.dev";
const site = (domains: HostedDomain[], url = PLATFORM): ForgeHostedWorkload => ({ name: "web", tier: "static", url, domains });

describe("workloadOpenUrl", () => {
  it("opens the generated address when no custom domain is bound", () => {
    expect(workloadOpenUrl(site([]))).toBe(PLATFORM);
    expect(workloadOpenUrl({ name: "web", tier: "static", url: PLATFORM })).toBe(PLATFORM);
  });

  it("opens a live custom domain over HTTPS instead", () => {
    expect(workloadOpenUrl(site([{ hostname: "shop.example.com", state: "live", serving_path: "edge" }]))).toBe("https://shop.example.com");
  });

  it.each(["pending-dns", "verifying", "issuing", "failed", "conflict", "unknown"])(
    "keeps the generated address while the domain is %s",
    (state) => {
      expect(workloadOpenUrl(site([{ hostname: "shop.example.com", state, serving_path: "edge" }]))).toBe(PLATFORM);
    }
  );

  it("does not open a live domain whose DNS now points away from the platform", () => {
    expect(workloadOpenUrl(site([{ hostname: "shop.example.com", state: "live", serving_path: "elsewhere" }]))).toBe(PLATFORM);
  });

  it("trusts LIVE when the serving path could not be resolved", () => {
    expect(workloadOpenUrl(site([{ hostname: "shop.example.com", state: "live", serving_path: "unknown" }]))).toBe("https://shop.example.com");
    expect(workloadOpenUrl(site([{ hostname: "shop.example.com", state: "live" }]))).toBe("https://shop.example.com");
  });

  it("picks the same domain everywhere when several are live: the apex, then by name", () => {
    const domains: HostedDomain[] = [
      { hostname: "www.example.com", state: "live", serving_path: "edge" },
      { hostname: "shop.example.com", state: "live", serving_path: "edge" },
      { hostname: "example.com", state: "live", serving_path: "edge" },
    ];
    expect(workloadCustomDomain(site(domains))).toBe("example.com");
    expect(workloadCustomDomain(site(domains.slice(0, 2)))).toBe("shop.example.com");
  });

  it("opens a live domain even before the platform address is allocated, and nothing when there is neither", () => {
    expect(workloadOpenUrl(site([{ hostname: "shop.example.com", state: "live" }], ""))).toBe("https://shop.example.com");
    expect(workloadOpenUrl(site([], ""))).toBe("");
  });
});

describe("toCloudEnvStatus carries each deployment's custom domains", () => {
  it("maps GetStatus's observed.domains into the workload, state and serving path included", () => {
    const msg = {
      deployments: [
        {
          deployment: {
            id: "d",
            name: "web",
            observed: {
              url: PLATFORM,
              domains: [
                { domain: "shop.example.com", state: DeployCustomDomainState.LIVE, servingPath: DeployServingPath.EDGE },
                { domain: "old.example.com", state: DeployCustomDomainState.LIVE, servingPath: DeployServingPath.ELSEWHERE },
                { domain: "new.example.com", state: DeployCustomDomainState.PENDING_DNS, servingPath: DeployServingPath.UNSPECIFIED },
              ],
            },
          },
        },
      ],
    } as unknown as Parameters<typeof toCloudEnvStatus>[0];
    const [workload] = toCloudEnvStatus(msg).workloads;
    expect(workload.domains).toEqual([
      { hostname: "shop.example.com", state: "live", serving_path: "edge" },
      { hostname: "old.example.com", state: "live", serving_path: "elsewhere" },
      { hostname: "new.example.com", state: "pending-dns", serving_path: "unknown" },
    ]);
    expect(workloadOpenUrl(workload)).toBe("https://shop.example.com");
  });
});
