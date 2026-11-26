// Copyright (c) 2026 Reliant Labs

/**
 * The shop pack's Site insights › SEO and Accessibility: the control plane's
 * site audits of shop's storefront (`shop-web`, a hosted static site), as
 * controlplane.v1.SiteAuditService answers them (Forge/Analytics/
 * siteAuditModel.ts reads them).
 *
 * THE STORY (design D7). v1.8.1's audit scored SEO 94 and accessibility 97.
 * v1.8.2 (priya's "show the order's status on the confirmation page", the
 * storefront's one change since) regressed both, as the newest audit shows,
 * re-run two hours ago:
 *
 *   SEO 81            /products/view lost its meta description; the home
 *                     page and /products link to /sale, which answers 404;
 *                     /blog has no og:image (so its link preview has no
 *                     picture)
 *   accessibility 88  two serious contrast failures (the home page's price
 *                     tags, the products grid's sale badge) and one critical
 *                     unnamed button (the cart's quantity stepper)
 *
 * Every environment that serves the storefront answers the same audit, of its
 * own host, with canonicals pointing at prod's. The thumbnails are the pack's
 * own sample art (../assets/shop-web-og.svg), same-origin: an auditor's
 * signed thumbnail URL in a live project, never the page's og:image URL.
 *
 * RunSiteAudit is a write: the demo's read-only transport refuses it before
 * this handler is asked (the Re-run button then says the demo's words), and a
 * writable preview gets a queued manual audit back.
 */

import { create, type JsonObject } from "@bufbuild/protobuf";
import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import {
  GetSiteAuditResponseSchema,
  SiteAuditCategory,
  SiteAuditService,
  SiteAuditStatus,
  SiteAuditTrigger,
  type GetSiteAuditRequest,
  type ListSiteAuditsRequest,
  type RunSiteAuditRequest,
  type SiteAuditFinding,
  type SiteAuditPage,
} from "@/gen/controlplane/services/siteaudit/v1/siteaudit_pb";

import ogThumbnail from "../assets/shop-web-og.svg";
import { DAY, DEMO_FRONTEND, HOUR, MINUTE, demoNow } from "../shopStory";
import { completed } from "./defaults";
import { DEMO_ENVS, DEMO_SCOPE, demoEnvId, type DemoEnvId, type PackScope } from "./scope";
import { ts } from "./wire";

/** The pack's thumbnail of shop-web's og:image: the bundle's own sample art (a same-origin path, or inlined as a data: URL). */
export const SHOP_WEB_OG_THUMBNAIL: string = ogThumbnail;

/** The release the newest audit is of, and the one before it. */
export const AUDITED_RELEASE = "v1.8.2";
export const PREVIOUS_RELEASE = "v1.8.1";
/** How long ago the newest audit finished, and the previous one. */
const FINISHED_AGO = 2 * HOUR;
const PREVIOUS_AGO = 8.5 * DAY;

/** The scores of the two audits (each the mean of its 2xx routes). */
export const AUDIT_SCORES = { current: { seo: 81, a11y: 88 }, previous: { seo: 94, a11y: 97 } } as const;

const HOST: Record<DemoEnvId, string> = { "env-prod": "shop.example.com", "env-staging": "staging.shop.example.com", "env-dev": "dev.shop.example.com" };
const PROD = "https://shop.example.com";

interface RouteStory {
  route: string;
  seo: number;
  a11y: number;
  title: string;
  description: string;
  jsonld: string[];
  /** No og:image tag (the /blog regression). */
  noOgImage?: boolean;
}

// route, SEO and accessibility scores (their means are AUDIT_SCORES.current), head facts.
const ROUTES: RouteStory[] = [
  { route: "/", seo: 77, a11y: 84, title: "shop — everyday goods, delivered", description: "Everyday goods from independent makers, delivered in two days. Free returns for 30 days.", jsonld: ["Organization", "WebSite"] },
  { route: "/products", seo: 77, a11y: 84, title: "All products · shop", description: "Browse every product in the shop: kitchen, home, garden and gifts, from independent makers.", jsonld: ["BreadcrumbList", "ItemList"] },
  { route: "/products/view", seo: 69, a11y: 92, title: "Stoneware mug, 350 ml · shop", description: "", jsonld: ["BreadcrumbList", "Product"] },
  { route: "/cart", seo: 92, a11y: 79, title: "Your cart · shop", description: "Review the items in your cart, change quantities and go to checkout.", jsonld: [] },
  { route: "/checkout", seo: 92, a11y: 92, title: "Checkout · shop", description: "Pay securely and choose a delivery slot. Orders ship within one working day.", jsonld: [] },
  { route: "/search", seo: 85, a11y: 92, title: "Search · shop", description: "Search shop's products by name, maker or category.", jsonld: ["WebSite"] },
  { route: "/blog", seo: 75, a11y: 93, title: "Notes from the makers · shop", description: "Stories from the people who make what we sell, and how to care for what you buy.", jsonld: ["Blog", "BreadcrumbList"], noOgImage: true },
];

function head(story: RouteStory): JsonObject {
  const url = `${PROD}${story.route}`;
  const social: JsonObject = {
    "og:title": story.title,
    "og:type": story.route === "/products/view" ? "product" : "website",
    "og:url": url,
    "twitter:card": "summary_large_image",
  };
  if (story.description) social["og:description"] = story.description;
  if (!story.noOgImage) social["og:image"] = `${PROD}/og.png`;
  return {
    title: story.title,
    ...(story.description ? { description: story.description } : {}),
    canonical: url,
    lang: "en",
    viewport: "width=device-width, initial-scale=1",
    social,
    jsonld_types: story.jsonld,
    indexable: true,
    og_complete: !story.noOgImage,
  };
}

function pages(): Partial<SiteAuditPage>[] {
  return ROUTES.map((story) => ({
    route: story.route,
    httpStatus: 200,
    rendered: true,
    seoScore: story.seo,
    a11yScore: story.a11y,
    head: head(story),
    ogThumbnailUrl: story.noOgImage ? "" : SHOP_WEB_OG_THUMBNAIL,
  }));
}

type FindingInit = Omit<Partial<SiteAuditFinding>, "$typeName" | "evidence"> & { evidence?: JsonObject };
const seo = (init: FindingInit): FindingInit => ({ category: SiteAuditCategory.SEO, ...init });
const a11y = (init: FindingInit): FindingInit => ({ category: SiteAuditCategory.ACCESSIBILITY, ...init });

const DEQUE = "https://dequeuniversity.com/rules/axe/4.10";

function findings(host: string): FindingInit[] {
  const sale = `https://${host}/sale`;
  return [
    // SEO: v1.8.2's three regressions.
    seo({ code: "description_missing", severity: "warn", route: "/products/view", ref: "description", fix: 'Add <meta name="description" content="…"> summarising the page in one or two sentences.' }),
    seo({ code: "og_tag_missing", severity: "warn", route: "/products/view", ref: "og:description", fix: "Add the Open Graph tag to the served HTML so link previews in Slack, X, LinkedIn and Facebook show the page." }),
    seo({ code: "link_broken", severity: "error", route: "/", ref: sale, evidence: { scope: "internal", status: "404" }, fix: "Fix or remove the link: its target answers with an error." }),
    seo({ code: "link_broken", severity: "error", route: "/products", ref: sale, evidence: { scope: "internal", status: "404" }, fix: "Fix or remove the link: its target answers with an error." }),
    seo({ code: "og_tag_missing", severity: "warn", route: "/blog", ref: "og:image", fix: "Add the Open Graph tag to the served HTML so link previews in Slack, X, LinkedIn and Facebook show the page." }),
    // SEO: what was already there in v1.8.1.
    seo({ code: "link_broken", severity: "warn", route: "/blog", ref: "https://makers.example.org/kiln-notes", evidence: { scope: "external", status: "0", error: "timeout after 5s" }, fix: "Fix or remove the link: its target answers with an error." }),
    seo({ code: "description_length", severity: "info", route: "/search", ref: "description", evidence: { description: "Search shop's products by name, maker or category.", length: "49" }, fix: "Keep the description between 50 and 160 characters." }),
    seo({ code: "soft_404", severity: "info", route: "/__forge-audit-404", evidence: { status: "200", mode: "SPA fallback: unknown paths serve index.html with 200" }, fix: "Answer unknown paths with a 404 (ship a 404.html) so search engines do not index error pages." }),
    seo({ code: "robots_no_sitemap", severity: "info", route: "", ref: "", fix: 'Add "Sitemap: https://<host>/sitemap.xml" to robots.txt.' }),
    // Accessibility: v1.8.2's two contrast failures and the unnamed button.
    a11y({
      code: "a11y_violation",
      severity: "serious",
      route: "/",
      ref: "color-contrast",
      evidence: {
        impact: "serious",
        help: "Elements must meet minimum color contrast ratio thresholds",
        help_url: `${DEQUE}/color-contrast`,
        group: "a11y-color-contrast",
        wcag: "wcag2aa,wcag143",
        nodes: [
          { target: ".hero .price-tag", html: '<span class="price-tag">£18</span>', summary: "Fix any of the following: Element has insufficient color contrast of 2.9 (foreground color: #c9a227, background color: #fff8e7, font size: 10.5pt (14px), font weight: normal). Expected contrast ratio of 4.5:1" },
          { target: ".featured li:nth-child(2) .price-tag", html: '<span class="price-tag">£24</span>', summary: "Fix any of the following: Element has insufficient color contrast of 2.9. Expected contrast ratio of 4.5:1" },
        ],
      },
      fix: "Elements must meet minimum color contrast ratio thresholds",
    }),
    a11y({
      code: "a11y_violation",
      severity: "serious",
      route: "/products",
      ref: "color-contrast",
      evidence: {
        impact: "serious",
        help: "Elements must meet minimum color contrast ratio thresholds",
        help_url: `${DEQUE}/color-contrast`,
        group: "a11y-color-contrast",
        wcag: "wcag2aa,wcag143",
        nodes: [{ target: ".product-grid .badge-sale", html: '<span class="badge badge-sale">Sale</span>', summary: "Fix any of the following: Element has insufficient color contrast of 3.1 (foreground color: #ffffff, background color: #f07a5a). Expected contrast ratio of 4.5:1" }],
      },
      fix: "Elements must meet minimum color contrast ratio thresholds",
    }),
    a11y({
      code: "a11y_violation",
      severity: "critical",
      route: "/cart",
      ref: "button-name",
      evidence: {
        impact: "critical",
        help: "Buttons must have discernible text",
        help_url: `${DEQUE}/button-name`,
        group: "a11y-names-labels",
        wcag: "wcag2a,wcag412",
        nodes: [{ target: ".cart-line .qty-stepper > button:nth-child(3)", html: '<button class="qty-increment" type="button"><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M8 3v10M3 8h10"></path></svg></button>', summary: "Fix any of the following: Element does not have inner text that is visible to screen readers. aria-label attribute does not exist or is empty. Element has no title attribute" }],
      },
      fix: "Buttons must have discernible text",
    }),
    // Accessibility: a moderate one v1.8.1 had too.
    a11y({
      code: "a11y_violation",
      severity: "moderate",
      route: "/blog",
      ref: "heading-order",
      evidence: {
        impact: "moderate",
        help: "Heading levels should only increase by one",
        help_url: `${DEQUE}/heading-order`,
        group: "a11y-navigation",
        wcag: "best-practice",
        nodes: [{ target: "article:nth-child(1) > h4", html: "<h4>Firing the spring batch</h4>", summary: "Fix any of the following: Heading order invalid" }],
      },
      fix: "Heading levels should only increase by one",
    }),
  ];
}

const auditId = (env: DemoEnvId, release: string) => `audit-${env.replace("env-", "")}-${release.replace(/\./g, "")}`;

function auditRow(env: DemoEnvId, release: string, app: string) {
  const now = demoNow();
  const current = release === AUDITED_RELEASE;
  const finished = now - (current ? FINISHED_AGO : PREVIOUS_AGO);
  const scores = current ? AUDIT_SCORES.current : AUDIT_SCORES.previous;
  return {
    id: auditId(env, release),
    environmentId: env,
    project: app,
    frontend: DEMO_FRONTEND,
    release,
    trigger: current ? SiteAuditTrigger.MANUAL : SiteAuditTrigger.PROMOTION,
    status: SiteAuditStatus.DONE,
    rulesVersion: "1",
    seoScore: scores.seo,
    a11yScore: scores.a11y,
    pagesFetched: ROUTES.length,
    pagesRendered: ROUTES.length,
    queuedAt: ts(finished - 3 * MINUTE),
    startedAt: ts(finished - 2 * MINUTE),
    finishedAt: ts(finished),
  };
}

/** The org's allowance: one of three re-runs spent today (the re-run two hours ago). */
function quota() {
  const now = demoNow();
  const midnight = new Date(now);
  midnight.setUTCHours(24, 0, 0, 0);
  return { manualPerDay: 3, manualRemaining: 2, auditsPerDay: 10, auditsRemaining: 10, resetsAt: ts(midnight.getTime()), paid: false };
}

/** The audit GetSiteAudit names: by id, or a site's newest (of a release). */
function selected(req: GetSiteAuditRequest): { env: DemoEnvId; release: string } | null {
  if (req.selector.case === "auditId") {
    for (const env of DEMO_ENVS) for (const release of [AUDITED_RELEASE, PREVIOUS_RELEASE]) if (auditId(env.id, release) === req.selector.value) return { env: env.id, release };
    return null;
  }
  if (req.selector.case !== "latest") return null;
  const env = demoEnvId(req.selector.value.environmentId);
  if (!env || req.selector.value.frontend !== DEMO_FRONTEND) return null;
  const release = req.selector.value.release ?? AUDITED_RELEASE;
  return release === AUDITED_RELEASE || release === PREVIOUS_RELEASE ? { env, release } : null;
}

/** The shop pack's GetSiteAudit answer for `req` (exported for the views' tests). */
export function shopSiteAudit(req: GetSiteAuditRequest, { app }: PackScope = DEMO_SCOPE) {
  const pick = selected(req);
  if (!pick) {
    // An unknown id would be NotFound on the server; a site with no finished audit (of that release) is an unset audit.
    return create(GetSiteAuditResponseSchema, { quota: quota() });
  }
  const current = pick.release === AUDITED_RELEASE;
  return create(GetSiteAuditResponseSchema, {
    audit: auditRow(pick.env, pick.release, app),
    // The previous audit's pages and findings are not part of the story: it is a summary.
    pages: current ? pages() : [],
    findings: current ? findings(HOST[pick.env]) : [],
    previous: current ? { id: auditId(pick.env, PREVIOUS_RELEASE), release: PREVIOUS_RELEASE, seoScore: AUDIT_SCORES.previous.seo, a11yScore: AUDIT_SCORES.previous.a11y, finishedAt: ts(demoNow() - PREVIOUS_AGO) } : undefined,
    quota: quota(),
  } as never);
}

export function siteAuditScenarioHandlers(scope: PackScope = DEMO_SCOPE): MockHandlers {
  return completed(SiteAuditService, {
    [`${SiteAuditService.typeName}/GetSiteAudit`]: (req: GetSiteAuditRequest) => shopSiteAudit(req, scope),
    [`${SiteAuditService.typeName}/ListSiteAudits`]: (req: ListSiteAuditsRequest) => {
      const env = demoEnvId(req.environmentId);
      if (!env || req.frontend !== DEMO_FRONTEND) return { audits: [] };
      return { audits: [auditRow(env, AUDITED_RELEASE, scope.app), auditRow(env, PREVIOUS_RELEASE, scope.app)] };
    },
    [`${SiteAuditService.typeName}/RunSiteAudit`]: (req: RunSiteAuditRequest) => {
      const env = demoEnvId(req.environmentId);
      const now = demoNow();
      const audit = { id: `audit-${(env ?? "env").replace("env-", "")}-manual`, environmentId: req.environmentId, project: scope.app, frontend: req.frontend, release: "v1.8.3", trigger: SiteAuditTrigger.MANUAL, status: SiteAuditStatus.QUEUED, queuedAt: ts(now) };
      return { auditId: audit.id, created: true, audit, quota: { ...quota(), manualRemaining: 1 } };
    },
  });
}
