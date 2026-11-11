// @vitest-environment node
import { describe, expect, it } from "vitest";

import type { AppSummary } from "@/services/forge/apps";
import type { ForgeDomain } from "@/services/forge/domains";

import { forgeEntryProps } from "../forgeEntry";
import { sitesOf } from "../sites";

const app = (name: string, envs: AppSummary["envs"]): AppSummary => ({ name, envs, previews: 0 });
const env = (id: string, name: string, links: AppSummary["envs"][number]["links"]) => ({
  id,
  name,
  preview: false,
  verdict: "healthy" as never,
  release: "v1",
  links,
  loading: false,
  unread: false,
});
const domain = (hostname: string, environmentId: string, target: string, state: ForgeDomain["state"] = "live"): ForgeDomain => ({
  id: hostname,
  hostname,
  state,
  origin: "external",
  requiredRecords: [],
  lastError: "",
  binding: { id: `b-${hostname}`, domainId: hostname, environmentId, target, redirectTo: "" },
});

describe("sitesOf", () => {
  it("lists every static site per environment with the domains bound to exactly it", () => {
    const apps = [
      app("storefront", [
        env("env_prod", "prod", [{ workload: "web", url: "https://shop.example.com", host: "shop.example.com", custom: true }]),
        env("env_stg", "staging", [{ workload: "web", url: "https://web-stg.sites.test", host: "web-stg.sites.test", custom: false }]),
      ]),
      app("api-only", [env("env_api", "prod", [])]),
    ];
    const domains = [
      domain("shop.example.com", "env_prod", "web"),
      domain("www.shop.example.com", "env_prod", "web", "pending-dns"),
      domain("other.example.com", "env_prod", "admin"),
    ];
    const rows = sitesOf(apps, domains);
    expect(rows.map((r) => `${r.app}/${r.env}/${r.site}`)).toEqual(["storefront/prod/web", "storefront/staging/web"]);
    expect(rows[0].domains).toEqual([
      { hostname: "shop.example.com", state: "live" },
      { hostname: "www.shop.example.com", state: "pending-dns" },
    ]);
    expect(rows[1].domains).toEqual([]);
  });
});

describe("forgeEntryProps", () => {
  it("carries rid, utm_* and the journey from the landing link", () => {
    expect(forgeEntryProps("?rid=visitor_12345678&utm_source=forge_landing&utm_campaign=forge_standalone&journey=forge&x=1", undefined)).toEqual({
      rid: "visitor_12345678",
      utm_source: "forge_landing",
      utm_campaign: "forge_standalone",
      journey: "forge",
    });
  });

  it("falls back to the handoff held at boot when the URL carries none", () => {
    expect(forgeEntryProps("", "?rid=visitor_12345678")).toEqual({ rid: "visitor_12345678" });
  });

  it("ignores an unknown journey and a malformed rid", () => {
    expect(forgeEntryProps("?journey=nonsense&rid=x", undefined)).toEqual({});
  });
});
