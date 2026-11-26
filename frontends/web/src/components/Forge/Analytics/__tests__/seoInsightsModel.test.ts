// Copyright (c) 2026 Reliant Labs

/**
 * Site insights › SEO as data: one audit off the wire (siteAuditModel.ts),
 * which site and environment a view reads, and the SEO view's checks, routes,
 * broken links and social card (seoInsightsModel.ts). The audits are the
 * shop pack's story (fixtures/shop/handlers/siteAudit.ts) where a real one
 * reads best, and built by hand where a rule needs a case the story lacks.
 */

import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { GetSiteAuditRequestSchema, GetSiteAuditResponseSchema, SiteAuditCategory, SiteAuditStatus } from "@/gen/controlplane/services/siteaudit/v1/siteaudit_pb";
import { AUDIT_SCORES, SHOP_WEB_OG_THUMBNAIL, shopSiteAudit } from "@/fixtures/shop/handlers/siteAudit";
import { resetDemoNowForTests } from "@/fixtures/shop/shopStory";

import { ENV_ALL } from "../../Console/envScope";
import { auditSitesOf, auditTargetOf, formatDelta, headOf, pagesWorstFirst, scoreDelta, siteAuditOf, type AuditPage, type SiteAudit } from "../siteAuditModel";
import { brokenLinks, indexabilityOf, seoCodeLabel, seoRoutes, siteChecks, siteWideFindings, socialCardOf, sortSeoFindings } from "../seoInsightsModel";

const NOW = Date.UTC(2026, 10, 26, 12, 0);
beforeEach(() => resetDemoNowForTests(NOW));
afterEach(() => resetDemoNowForTests());

const latest = (environmentId = "env-prod", frontend = "shop-web", release?: string) =>
  create(GetSiteAuditRequestSchema, { selector: { case: "latest", value: { environmentId, frontend, ...(release ? { release } : {}) } } });
const story = (): SiteAudit => siteAuditOf(shopSiteAudit(latest()));

function audit(init: Parameters<typeof create<typeof GetSiteAuditResponseSchema>>[1]): SiteAudit {
  return siteAuditOf(create(GetSiteAuditResponseSchema, init));
}

const page = (route: string, seo: number | null, extra: Partial<AuditPage> = {}): AuditPage => ({ route, status: 200, rendered: true, seo, a11y: null, head: headOf({}), thumbnailUrl: "", ...extra });

describe("siteAuditOf", () => {
  it("reads the story's audit, its delta against the previous release, and the quota", () => {
    const a = story();
    expect(a.audit).toMatchObject({ release: "v1.8.2", status: "done", trigger: "manual", seo: AUDIT_SCORES.current.seo, a11y: AUDIT_SCORES.current.a11y, pagesFetched: 7 });
    expect(a.audit!.finishedAtMs).toBe(NOW - 2 * 3_600_000);
    expect(a.previous).toMatchObject({ release: "v1.8.1", seo: 94, a11y: 97 });
    expect(scoreDelta(a.audit!.seo, a.previous!.seo)).toBe(-13);
    expect(scoreDelta(a.audit!.a11y, a.previous!.a11y)).toBe(-9);
    expect(a.quota).toMatchObject({ manualPerDay: 3, manualRemaining: 2, paid: false });
    expect(a.inProgress).toBeNull();
  });

  it("reads a page's head facts in pkg/siteaudit's own spelling", () => {
    const view = story().pages.find((p) => p.route === "/products/view")!;
    expect(view.head).toMatchObject({ title: "Stoneware mug, 350 ml · shop", description: "", canonical: "https://shop.example.com/products/view", jsonldTypes: ["BreadcrumbList", "Product"], indexable: true, ogComplete: true });
    expect(view.head.social["og:image"]).toBe("https://shop.example.com/og.png");
    expect(view.thumbnailUrl).toBe(SHOP_WEB_OG_THUMBNAIL);
  });

  it("reads an accessibility finding's nodes out of its evidence, and the rest as text", () => {
    const button = story().findings.find((f) => f.ref === "button-name")!;
    expect(button.category).toBe("accessibility");
    expect(button.nodes).toHaveLength(1);
    expect(button.nodes[0]!.target).toBe(".cart-line .qty-stepper > button:nth-child(3)");
    expect(button.evidence).toMatchObject({ impact: "critical", group: "a11y-names-labels", help_url: "https://dequeuniversity.com/rules/axe/4.10/button-name" });
    expect(button.evidence).not.toHaveProperty("nodes");
  });

  it("reads a site with no finished audit as no audit, and drops a finding of no category", () => {
    const empty = audit({ quota: { manualPerDay: 3, manualRemaining: 3 }, inProgress: { id: "a1", status: SiteAuditStatus.RUNNING, release: "v2" } });
    expect(empty.audit).toBeNull();
    expect(empty.inProgress).toMatchObject({ id: "a1", status: "running", release: "v2" });
    const odd = audit({ findings: [{ category: SiteAuditCategory.UNSPECIFIED, code: "x" }, { category: SiteAuditCategory.SEO, code: "title_missing", severity: "warning" }] });
    expect(odd.findings.map((f) => [f.code, f.severity])).toEqual([["title_missing", "warn"]]);
  });

  it("says a delta in words", () => {
    expect(formatDelta(-13)).toBe("−13");
    expect(formatDelta(4)).toBe("+4");
    expect(formatDelta(0)).toBe("no change");
    expect(scoreDelta(null, 90)).toBeNull();
    expect(scoreDelta(90, null)).toBeNull();
  });
});

describe("pagesWorstFirst", () => {
  it("puts the lowest score first and an unscored page last, ties by route", () => {
    const sorted = pagesWorstFirst([page("/b", 90), page("/none", null), page("/a", 90), page("/low", 40)], "seo");
    expect(sorted.map((p) => p.route)).toEqual(["/low", "/a", "/b", "/none"]);
  });
});

describe("auditTargetOf", () => {
  const sites = auditSitesOf(
    [
      { id: "env-prod", name: "prod", preview: false },
      { id: "env-pr-7", name: "pr-7", preview: true },
    ],
    new Map([
      ["env-prod", [{ name: "api", tier: "backend" }, { name: "shop-web", tier: "static" }, { name: "docs", tier: "static" }]],
      ["env-pr-7", [{ name: "shop-web", tier: "static" }]],
    ])
  );

  it("lists the hosted static sites, each with the environments it is in", () => {
    expect(sites).toEqual([
      { frontend: "docs", envs: [{ id: "env-prod", name: "prod", preview: false }] },
      { frontend: "shop-web", envs: [{ id: "env-prod", name: "prod", preview: false }, { id: "env-pr-7", name: "pr-7", preview: true }] },
    ]);
  });

  it("reads ?site= as either spelling of the site's service name, else the first site", () => {
    expect(auditTargetOf({ project: "shop", site: "shop-shop-web", env: ENV_ALL, sites })).toMatchObject({ kind: "site", frontend: "shop-web", envId: "env-prod" });
    expect(auditTargetOf({ project: "shop", site: "shop-web", env: "pr-7", sites })).toEqual({ kind: "site", frontend: "shop-web", envId: "env-pr-7", envName: "pr-7", preview: true });
    expect(auditTargetOf({ project: "shop", site: undefined, env: ENV_ALL, sites })).toMatchObject({ kind: "site", frontend: "docs" });
  });

  it("says why there is nothing to read", () => {
    expect(auditTargetOf({ project: "shop", site: undefined, env: ENV_ALL, sites: [] })).toEqual({ kind: "no-site" });
    expect(auditTargetOf({ project: "shop", site: "shop-admin", env: ENV_ALL, sites })).toEqual({ kind: "not-static", site: "shop-admin" });
    expect(auditTargetOf({ project: "shop", site: "docs", env: "pr-7", sites })).toEqual({ kind: "not-in-env", frontend: "docs", env: "pr-7" });
  });
});

describe("indexabilityOf (expected against actual, design D5)", () => {
  it("is indexed, as prod is meant to be, in the story", () => {
    expect(indexabilityOf(story())).toMatchObject({ expected: "index", actual: "index", label: "Indexed", problem: false });
  });

  it("reads a noindex host with no mismatch as noindex by design: Not indexed (preview), no problem", () => {
    const preview = audit({ pages: [{ route: "/", httpStatus: 200, head: { robots: "noindex", indexable: false } }] });
    expect(indexabilityOf(preview)).toEqual({ expected: "noindex", actual: "noindex", label: "Not indexed (preview)", problem: false, platform: false });
    const header = audit({ pages: [{ route: "/", httpStatus: 200, head: { x_robots_tag: "noindex, nofollow" } }] });
    expect(indexabilityOf(header).label).toBe("Not indexed (preview)");
    expect(siteChecks(preview).find((c) => c.id === "indexing")!.state).toBe("pass");
  });

  it("is an error when a host meant to be indexed is not, or robots.txt blocks it", () => {
    const meta = audit({ pages: [{ route: "/", head: { robots: "noindex" } }], findings: [{ category: SiteAuditCategory.SEO, code: "indexing_mismatch", severity: "error", route: "/", ref: "meta_robots" }] });
    expect(indexabilityOf(meta)).toMatchObject({ expected: "index", actual: "noindex", problem: true });
    expect(siteChecks(meta).find((c) => c.id === "indexing")!.state).toBe("fail");
    const blocked = audit({ findings: [{ category: SiteAuditCategory.SEO, code: "indexing_mismatch", severity: "error", ref: "robots_txt" }] });
    expect(indexabilityOf(blocked)).toMatchObject({ actual: "blocked", problem: true });
  });

  it("reads an indexable preview host as the platform's defect, not the site's", () => {
    const leak = audit({ pages: [{ route: "/", head: { indexable: true } }], findings: [{ category: SiteAuditCategory.SEO, code: "preview_indexable", severity: "info", ref: "host" }] });
    expect(indexabilityOf(leak)).toMatchObject({ expected: "noindex", actual: "index", problem: false, platform: true });
    expect(siteChecks(leak).find((c) => c.id === "indexing")!.state).toBe("info");
  });
});

describe("siteChecks", () => {
  it("judges the story: robots.txt advice, a valid sitemap, one canonical host, no hreflang, its JSON-LD types", () => {
    const checks = Object.fromEntries(siteChecks(story()).map((c) => [c.id, c]));
    expect(Object.keys(checks)).toEqual(["robots", "sitemap", "indexing", "canonical", "hreflang", "structured-data"]);
    expect(checks.robots).toMatchObject({ state: "info", value: "1 finding" });
    expect(checks.sitemap).toMatchObject({ state: "pass", value: "Valid" });
    expect(checks.canonical).toMatchObject({ state: "pass", value: "shop.example.com" });
    expect(checks.hreflang).toMatchObject({ state: "none", value: "Not used" });
    expect(checks["structured-data"]!.value).toBe("Blog, BreadcrumbList, ItemList, Organization, Product, WebSite");
  });

  it("names the worst: a missing robots.txt, broken sitemap URLs, a foreign canonical", () => {
    const bad = audit({
      pages: [{ route: "/", head: { canonical: "https://elsewhere.example/" } }],
      findings: [
        { category: SiteAuditCategory.SEO, code: "robots_missing", severity: "info" },
        { category: SiteAuditCategory.SEO, code: "sitemap_url_broken", severity: "warn", ref: "https://a/x" },
        { category: SiteAuditCategory.SEO, code: "sitemap_url_broken", severity: "warn", ref: "https://a/y" },
        { category: SiteAuditCategory.SEO, code: "canonical_foreign_host", severity: "error", route: "/" },
      ],
    });
    const checks = Object.fromEntries(siteChecks(bad).map((c) => [c.id, c]));
    expect(checks.robots).toMatchObject({ value: "Missing", state: "info" });
    expect(checks.sitemap).toMatchObject({ value: "2 listed URLs broken", state: "warn" });
    expect(checks.canonical).toMatchObject({ value: "Points at another host", state: "fail" });
  });
});

describe("seoRoutes", () => {
  it("lists the story's routes worst first, with their head facts and findings", () => {
    const rows = seoRoutes(story());
    expect(rows.map((r) => [r.route, r.score])).toEqual([
      ["/products/view", 69],
      ["/blog", 75],
      ["/", 77],
      ["/products", 77],
      ["/search", 85],
      ["/cart", 92],
      ["/checkout", 92],
    ]);
    const view = rows[0]!;
    expect(view.description).toBe("");
    expect(view.findings.map((f) => f.code)).toEqual(["description_missing", "og_tag_missing"]);
    expect(rows.find((r) => r.route === "/")!).toMatchObject({ errors: 1, warnings: 0, titleLength: 32 });
    expect(rows.find((r) => r.route === "/blog")!.ogComplete).toBe(false);
  });

  it("keeps the findings no route owns (site-wide, and the soft-404 probe) for the site", () => {
    expect(siteWideFindings(story()).map((f) => f.code)).toEqual(["robots_no_sitemap", "soft_404"]);
  });
});

describe("brokenLinks", () => {
  it("lists internal links first: /sale from the home page and /products, then the external one", () => {
    expect(brokenLinks(story())).toEqual([
      { source: "/", target: "https://shop.example.com/sale", status: 404, error: "", internal: true },
      { source: "/products", target: "https://shop.example.com/sale", status: 404, error: "", internal: true },
      { source: "/blog", target: "https://makers.example.org/kiln-notes", status: 0, error: "timeout after 5s", internal: false },
    ]);
  });
});

describe("socialCardOf", () => {
  it("previews from Open Graph and the auditor's thumbnail, never the og:image URL", () => {
    const home = story().pages.find((p) => p.route === "/")!;
    expect(socialCardOf(home)).toEqual({
      title: "shop — everyday goods, delivered",
      description: "Everyday goods from independent makers, delivered in two days. Free returns for 30 days.",
      domain: "shop.example.com",
      imageUrl: SHOP_WEB_OG_THUMBNAIL,
      declaresImage: true,
      twitterCard: "summary_large_image",
      missing: [],
    });
  });

  it("names what a page lacks: /blog has no og:image, so no picture", () => {
    const blog = socialCardOf(story().pages.find((p) => p.route === "/blog")!);
    expect(blog).toMatchObject({ imageUrl: "", declaresImage: false, missing: ["og:image"] });
  });

  it("falls back to the title and description, as scrapers do", () => {
    const bare = socialCardOf(page("/x", 50, { head: headOf({ title: "T", description: "D", canonical: "https://h.example/x" }) }));
    expect(bare).toMatchObject({ title: "T", description: "D", domain: "h.example", twitterCard: "summary", missing: ["og:title", "og:description", "og:image", "og:url"] });
  });
});

describe("the SEO words", () => {
  it("labels known codes and spells an unknown one", () => {
    expect(seoCodeLabel("description_missing")).toBe("Meta description missing");
    expect(seoCodeLabel("brand_new_rule")).toBe("Brand new rule");
  });

  it("sorts findings most serious first", () => {
    const sorted = sortSeoFindings(story().findings.filter((f) => f.category === "seo"));
    expect(sorted[0]!.severity).toBe("error");
    expect(sorted.at(-1)!.severity).toBe("info");
  });
});
