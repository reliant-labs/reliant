// Copyright (c) 2026 Reliant Labs

/**
 * Site insights' audits over the shop pack's transport (handlers/siteAudit.ts):
 * the storefront's v1.8.1 → v1.8.2 story in every environment that serves
 * it, nothing for another site, environment or org, RunSiteAudit refused by
 * the read-only demo, and the story told for the forge tour's sample with
 * its ids prefixed.
 */

import { createClient, type Transport } from "@connectrpc/connect";
import { afterEach, describe, expect, it } from "vitest";

import { SiteAuditCategory, SiteAuditService } from "@/gen/controlplane/services/siteaudit/v1/siteaudit_pb";

import { AUDIT_SCORES, SHOP_WEB_OG_THUMBNAIL } from "../handlers/siteAudit";
import { createShopTransport, SHOP_PACK_SERVICES, SHOP_READ_ONLY_MESSAGE } from "../index";
import { DEMO_ORG, resetDemoNowForTests } from "../shopStory";

const FIXED_NOW = Date.UTC(2026, 10, 26, 12, 0);
const client = (transport: Transport = createShopTransport({ now: FIXED_NOW })) => createClient(SiteAuditService, transport);
const latest = (environmentId: string, frontend = "shop-web", release?: string) => ({ selector: { case: "latest" as const, value: { environmentId, frontend, release } }, orgId: DEMO_ORG });

afterEach(() => resetDemoNowForTests());

describe("the shop pack's site audits", () => {
  it("is a service the pack answers", () => {
    expect(SHOP_PACK_SERVICES.map((s) => s.typeName)).toContain(SiteAuditService.typeName);
  });

  it("answers prod's newest audit: v1.8.2's regressions against v1.8.1", async () => {
    const res = await client().getSiteAudit(latest("env-prod"));
    expect(res.audit).toMatchObject({ release: "v1.8.2", frontend: "shop-web", project: "shop", seoScore: AUDIT_SCORES.current.seo, a11yScore: AUDIT_SCORES.current.a11y });
    expect(res.previous).toMatchObject({ release: "v1.8.1", seoScore: 94, a11yScore: 97 });
    expect(res.pages.map((p) => p.route)).toEqual(["/", "/products", "/products/view", "/cart", "/checkout", "/search", "/blog"]);
    // Each score is the mean of the routes' (every route answered 200).
    const mean = (pick: (p: (typeof res.pages)[number]) => number | undefined) => Math.round(res.pages.reduce((sum, p) => sum + (pick(p) ?? 0), 0) / res.pages.length);
    expect(mean((p) => p.seoScore)).toBe(AUDIT_SCORES.current.seo);
    expect(mean((p) => p.a11yScore)).toBe(AUDIT_SCORES.current.a11y);
    const seo = res.findings.filter((f) => f.category === SiteAuditCategory.SEO);
    expect(seo.find((f) => f.code === "description_missing")?.route).toBe("/products/view");
    expect(seo.filter((f) => f.code === "link_broken" && f.ref === "https://shop.example.com/sale").map((f) => f.route)).toEqual(["/", "/products"]);
    expect(seo.find((f) => f.code === "og_tag_missing" && f.ref === "og:image")?.route).toBe("/blog");
    const a11y = res.findings.filter((f) => f.category === SiteAuditCategory.ACCESSIBILITY);
    expect(a11y.filter((f) => f.ref === "color-contrast" && f.severity === "serious")).toHaveLength(2);
    expect(a11y.filter((f) => f.ref === "button-name")).toHaveLength(1);
    expect(res.quota).toMatchObject({ manualPerDay: 3, manualRemaining: 2 });
  });

  it("serves thumbnails only from the pack, never a tenant's og:image URL", async () => {
    const res = await client().getSiteAudit(latest("env-prod"));
    const thumbnails = new Set(res.pages.map((p) => p.ogThumbnailUrl).filter(Boolean));
    expect([...thumbnails]).toEqual([SHOP_WEB_OG_THUMBNAIL]);
    expect(SHOP_WEB_OG_THUMBNAIL.startsWith("/") || SHOP_WEB_OG_THUMBNAIL.startsWith("data:")).toBe(true);
    expect(res.pages.find((p) => p.route === "/blog")!.ogThumbnailUrl).toBe("");
  });

  it("audits each environment's own host", async () => {
    const staging = await client().getSiteAudit(latest("env-staging"));
    expect(staging.findings.some((f) => f.ref === "https://staging.shop.example.com/sale")).toBe(true);
    expect(staging.pages[0]!.head!.canonical).toBe("https://shop.example.com/");
  });

  it("answers the previous release by name, as a summary, and no audit for a release it never audited", async () => {
    const previous = await client().getSiteAudit(latest("env-prod", "shop-web", "v1.8.1"));
    expect(previous.audit).toMatchObject({ release: "v1.8.1", seoScore: 94 });
    expect(previous.pages).toEqual([]);
    const never = await client().getSiteAudit(latest("env-prod", "shop-web", "v1.7.0"));
    expect(never.audit).toBeUndefined();
    expect(never.quota).toBeDefined();
  });

  it("has no audit for another site, and answers empty for another environment or org", async () => {
    expect((await client().getSiteAudit(latest("env-prod", "shop-admin"))).audit).toBeUndefined();
    expect((await client().getSiteAudit(latest("env-elsewhere"))).audit).toBeUndefined();
    expect((await client().getSiteAudit({ ...latest("env-prod"), orgId: "org-someone-else" })).audit).toBeUndefined();
    expect((await client().listSiteAudits({ environmentId: "env-prod", frontend: "shop-web" })).audits.map((a) => a.release)).toEqual(["v1.8.2", "v1.8.1"]);
  });

  it("refuses RunSiteAudit: the demo is read-only", async () => {
    await expect(client().runSiteAudit({ environmentId: "env-prod", frontend: "shop-web" })).rejects.toThrow(SHOP_READ_ONLY_MESSAGE);
  });

  it("tells the story for the forge tour's sample, its ids prefixed", async () => {
    const sample = client(createShopTransport({ now: FIXED_NOW, app: "sample_shop", org: null, idPrefix: "sample_" }));
    const res = await sample.getSiteAudit({ selector: { case: "latest", value: { environmentId: "sample_env-prod", frontend: "shop-web" } }, orgId: "org-anyones" });
    expect(res.audit).toMatchObject({ project: "sample_shop", seoScore: AUDIT_SCORES.current.seo });
    expect(res.audit!.id.startsWith("sample_")).toBe(true);
    expect(res.audit!.environmentId).toBe("sample_env-prod");
  });
});
