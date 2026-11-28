// Copyright (c) 2026 Reliant Labs

/**
 * The static site's hosting over the shop pack's transport
 * (handlers/staticSite.ts): the storefront's usage against the free caps,
 * its configuration and its traffic (about 40k requests a week at a 93% hit
 * ratio, 30 days clamped to the free plan's 7) in every environment that
 * serves it; nothing for another site, environment or org; and the story
 * told for the forge tour's sample with its ids prefixed.
 */

import { createClient, type Transport } from "@connectrpc/connect";
import { afterEach, describe, expect, it } from "vitest";

import { StaticSiteHosting, StaticSiteNotFoundMode, StaticSiteService, StaticSiteTrafficRange } from "@/gen/controlplane/services/static_site/v1/static_site_pb";

import { FREE_CAPS, TRAFFIC_STORY } from "../handlers/staticSite";
import { createShopTransport, SHOP_PACK_SERVICES } from "../index";
import { DEMO_ORG, resetDemoNowForTests } from "../shopStory";

const FIXED_NOW = Date.UTC(2026, 10, 26, 12, 0);
const MIB = 2 ** 20;
const GIB = 2 ** 30;
const client = (transport: Transport = createShopTransport({ now: FIXED_NOW })) => createClient(StaticSiteService, transport);
const site = (environmentId: string, frontend = "shop-web") => ({ environmentId, frontend, orgId: DEMO_ORG });

afterEach(() => resetDemoNowForTests());

describe("the shop pack's static site hosting", () => {
  it("is a service the pack answers", () => {
    expect(SHOP_PACK_SERVICES.map((s) => s.typeName)).toContain(StaticSiteService.typeName);
  });

  it("answers prod's usage against the free caps: 180 MiB of 1 GiB, 2.1 GiB of 10 GiB transfer", async () => {
    const res = await client().getStaticSite(site("env-prod"));
    const usage = res.usage!;
    expect(usage.hosting).toBe(StaticSiteHosting.FREE);
    expect(Number(usage.storageBytes)).toBe(180 * MIB);
    expect(Number(usage.siteStorageLimitBytes)).toBe(FREE_CAPS.siteStorage);
    expect(Number(usage.orgTransferBytes) / GIB).toBeCloseTo(2.1, 5);
    expect(Number(usage.orgTransferLimitBytes)).toBe(10 * GIB);
    expect(Number(usage.siteTransferBytes) / GIB).toBeCloseTo(1.9, 5);
    expect(new Date(Number(usage.periodEnd!.seconds) * 1000).toISOString()).toBe("2026-12-01T00:00:00.000Z");
    expect(res.release).toMatchObject({ version: "v1.8.3" });
    expect(res.hostnames[0]!.hostname).toBe("shop-web.reliant.app");
    expect(res.hostnames.slice(1).map((h) => h.hostname)).toEqual(["sale.shop.example.com", "shop.example.co.uk", "shop.example.com"]);
  });

  it("serves a real 404.html (the SEO story's /sale answers 404) and resolves API_URL to the api", async () => {
    const config = (await client().getStaticSite(site("env-prod"))).configuration!;
    expect(config.notFoundMode).toBe(StaticSiteNotFoundMode.NOT_FOUND_PAGE);
    expect(config.runtimeConfigWorkloadUrls).toEqual({ API_URL: "api" });
    expect(config).toMatchObject({ basePath: "", assetDir: "assets", headersRules: 3, redirectRules: 2 });
  });

  it("answers about 40k requests a week at a 93% hit ratio", async () => {
    const res = await client().getStaticSiteTraffic({ ...site("env-prod"), range: StaticSiteTrafficRange.LAST_7_DAYS });
    const requests = Number(res.totals!.requests);
    expect(requests).toBeGreaterThan(38_000);
    expect(requests).toBeLessThan(42_000);
    expect(res.totals!.cacheHitRatio).toBeCloseTo(TRAFFIC_STORY.hitRatio, 2);
    expect(Number(res.stepSeconds)).toBe(3 * 3600);
    expect(res.historyClamped).toBe(false);
    // Each point's counts add up to the totals.
    expect(res.points.reduce((sum, p) => sum + Number(p.counts!.requests), 0)).toBe(requests);
  });

  it("clamps 30 days to the free plan's 7 days of history", async () => {
    const res = await client().getStaticSiteTraffic({ ...site("env-prod"), range: StaticSiteTrafficRange.LAST_30_DAYS });
    expect(res.historyDays).toBe(7);
    expect(res.historyClamped).toBe(true);
    const spanDays = (Number(res.end!.seconds) - Number(res.start!.seconds)) / 86_400;
    expect(spanDays).toBeCloseTo(7, 1);
    const day = await client().getStaticSiteTraffic({ ...site("env-prod"), range: StaticSiteTrafficRange.LAST_24_HOURS });
    expect(day.historyClamped).toBe(false);
    expect(day.points.length).toBeGreaterThanOrEqual(24);
  });

  it("answers empty for another site, environment or org", async () => {
    expect((await client().getStaticSite(site("env-prod", "shop-admin"))).usage).toBeUndefined();
    expect((await client().getStaticSite(site("env-elsewhere"))).usage).toBeUndefined();
    expect((await client().getStaticSite({ ...site("env-prod"), orgId: "org-someone-else" })).usage).toBeUndefined();
    expect((await client().getStaticSiteTraffic({ ...site("env-prod"), orgId: "org-someone-else", range: StaticSiteTrafficRange.LAST_7_DAYS })).points).toEqual([]);
    expect((await client().getStaticSiteTraffic({ ...site("env-prod", "shop-admin"), range: StaticSiteTrafficRange.LAST_7_DAYS })).points).toEqual([]);
  });

  it("tells the story for the forge tour's sample, its ids prefixed", async () => {
    const sample = client(createShopTransport({ now: FIXED_NOW, app: "sample_shop", org: null, idPrefix: "sample_" }));
    const res = await sample.getStaticSite({ environmentId: "sample_env-prod", frontend: "shop-web", orgId: "org-anyones" });
    expect(Number(res.usage!.storageBytes)).toBe(180 * MIB);
    expect(res.environmentId).toBe("sample_env-prod");
    expect(res.deploymentId.startsWith("sample_")).toBe(true);
    const traffic = await sample.getStaticSiteTraffic({ environmentId: "sample_env-prod", frontend: "shop-web", range: StaticSiteTrafficRange.LAST_7_DAYS });
    expect(Number(traffic.totals!.requests)).toBeGreaterThan(38_000);
  });
});
