// Copyright (c) 2026 Reliant Labs

/**
 * SITE INSIGHTS › SEO, as data (SeoInsightsView.tsx draws it): the site
 * checks, the routes table, one route's findings and social card, and the
 * broken links, read from one site audit (siteAuditModel.ts). Pure.
 *
 * The findings are forge `pkg/siteaudit`'s, by rule code; the words for each
 * code are here (SEO_CODE_LABELS), the remedy is the engine's own `fix`.
 *
 * INDEXABILITY is judged against what the host is MEANT to be (design D5): a
 * preview host is meant to stay out of search, so its noindex reads "Not
 * indexed (preview)" and costs nothing. The target supplies the preview
 * expectation; engine findings also establish it: `indexing_mismatch` means the
 * host was meant to be indexed and is not (an error); `preview_indexable`
 * means it was meant to be noindex and is indexable (a platform defect the
 * score ignores); a noindex home page with neither is noindex by design.
 */

import type { AuditFinding, AuditPage, SeoSeverity, SiteAudit } from "./siteAuditModel";
import { findingsOf, pagesWorstFirst } from "./siteAuditModel";

/** Most serious first. */
export const SEO_SEVERITY_ORDER: Record<SeoSeverity, number> = { error: 0, warn: 1, info: 2 };
export const SEO_SEVERITY_LABEL: Record<SeoSeverity, string> = { error: "Error", warn: "Warning", info: "Advice" };

/** A finding's severity as one of the engine's three; anything else is advice. */
export function seoSeverityOf(finding: AuditFinding): SeoSeverity {
  return finding.severity === "error" || finding.severity === "warn" ? finding.severity : "info";
}

/** What each SEO rule code says, as a heading. */
export const SEO_CODE_LABELS: Record<string, string> = {
  title_missing: "Title missing",
  title_multiple: "More than one title",
  title_length: "Title length",
  title_duplicate: "Title shared with another page",
  title_placeholder: "Starter title",
  description_missing: "Meta description missing",
  description_length: "Description length",
  description_duplicate: "Description shared with another page",
  description_placeholder: "Starter description",
  canonical_missing: "Canonical link missing",
  canonical_conflicting: "Conflicting canonical links",
  canonical_relative: "Relative canonical",
  canonical_invalid: "Invalid canonical",
  canonical_foreign_host: "Canonical points at another host",
  canonical_to_root: "Canonical points at the home page",
  head_client_rendered: "Tag set by JavaScript only",
  og_tag_missing: "Open Graph tag missing",
  twitter_card_missing: "Twitter card missing",
  og_image_relative: "Relative og:image",
  og_image_unreachable: "og:image unreachable",
  og_image_placeholder: "Starter og:image",
  indexing_mismatch: "Kept out of search",
  preview_indexable: "Preview host is indexable",
  duplicate_without_canonical: "Indexable copy without a canonical",
  blocked_by_robots: "Blocked by robots.txt",
  robots_unreachable: "robots.txt unreachable",
  robots_missing: "robots.txt missing",
  robots_invalid: "robots.txt line not understood",
  robots_no_sitemap: "robots.txt names no sitemap",
  sitemap_missing: "Sitemap missing",
  sitemap_unreachable: "Sitemap unreachable",
  sitemap_invalid: "Sitemap invalid",
  sitemap_too_large: "Sitemap too large",
  sitemap_url_invalid: "Sitemap URL invalid",
  sitemap_host_mismatch: "Sitemap lists another host",
  sitemap_url_broken: "Sitemap URL broken",
  sitemap_url_noindex: "Sitemap lists a noindex page",
  sitemap_url_blocked: "Sitemap lists a blocked page",
  hreflang_invalid: "Invalid hreflang",
  hreflang_not_reciprocal: "hreflang not reciprocal",
  hreflang_no_x_default: "No x-default hreflang",
  hreflang_missing_self: "hreflang omits the page itself",
  jsonld_invalid: "Invalid JSON-LD",
  jsonld_no_context: "JSON-LD without @context",
  jsonld_missing_property: "JSON-LD property missing",
  link_broken: "Broken link",
  link_not_crawlable: "Link crawlers cannot follow",
  link_text_generic: "Generic link text",
  link_text_empty: "Link without text",
  image_alt_missing: "Image without alt text",
  lang_missing: "No lang attribute",
  viewport_missing: "No viewport meta tag",
  status_error: "Error status",
  fetch_failed: "Could not be fetched",
  soft_404: "Unknown paths answer 200",
};

/** A rule code's heading; an unknown code as its words ("some_rule" → "Some rule"). */
export function seoCodeLabel(code: string): string {
  const known = SEO_CODE_LABELS[code];
  if (known) return known;
  const words = code.replace(/_/g, " ").trim();
  return words ? words[0]!.toUpperCase() + words.slice(1) : code;
}

/** Most serious first, then by code and ref, so a list reads the same every time. */
export function sortSeoFindings(findings: readonly AuditFinding[]): AuditFinding[] {
  return [...findings].sort(
    (a, b) => SEO_SEVERITY_ORDER[seoSeverityOf(a)] - SEO_SEVERITY_ORDER[seoSeverityOf(b)] || a.code.localeCompare(b.code) || a.route.localeCompare(b.route) || a.ref.localeCompare(b.ref)
  );
}

// ── Indexability ────────────────────────────────────────────────────────────

export type Indexing = "index" | "noindex";

export interface Indexability {
  /** What the host is meant to be. */
  expected: Indexing;
  /** What it is: blocked means robots.txt disallows the site's root. */
  actual: Indexing | "blocked" | "unknown";
  /** The words the site check shows. */
  label: string;
  /** Whether it costs the site anything (expected and actual disagree in the harmful direction). */
  problem: boolean;
  /** A platform defect, not the site's: a preview host that is indexable. */
  platform: boolean;
}

const NOINDEX = /(^|[\s,])(noindex|none)($|[\s,])/i;
const isNoindex = (page: AuditPage) => NOINDEX.test(page.head.robots) || NOINDEX.test(page.head.xRobotsTag);

/** The host's expected and actual indexing (design D5). */
export function indexabilityOf(audit: SiteAudit, preview?: boolean): Indexability {
  const seo = findingsOf(audit, "seo");
  const mismatch = seo.filter((f) => f.code === "indexing_mismatch");
  if (mismatch.length > 0) {
    const blocked = mismatch.some((f) => f.ref === "robots_txt");
    return { expected: "index", actual: blocked ? "blocked" : "noindex", label: blocked ? "Blocked by robots.txt, meant to be indexed" : "Not indexed, meant to be", problem: true, platform: false };
  }
  if (seo.some((f) => f.code === "preview_indexable")) {
    return { expected: "noindex", actual: "index", label: "Indexable (preview, meant to be noindex)", problem: false, platform: true };
  }
  const home = audit.pages.find((page) => page.route === "/") ?? audit.pages[0];
  if (!home) return { expected: preview ? "noindex" : "index", actual: "unknown", label: "Not judged", problem: false, platform: false };
  const noindex = isNoindex(home) || home.head.indexable === false;
  const expectedNoindex = preview ?? noindex;
  if (noindex) return { expected: expectedNoindex ? "noindex" : "index", actual: "noindex", label: expectedNoindex ? "Not indexed (preview)" : "Not indexed, meant to be", problem: !expectedNoindex, platform: false };
  if (preview) return { expected: "noindex", actual: "index", label: "Indexable (preview, meant to be noindex)", problem: false, platform: true };
  return { expected: "index", actual: "index", label: "Indexed", problem: false, platform: false };
}

// ── Site checks ─────────────────────────────────────────────────────────────

export type CheckId = "robots" | "sitemap" | "indexing" | "canonical" | "hreflang" | "structured-data";
/** pass: nothing to fix; warn / fail: findings at that level; info: advice only; none: not used or not judged. */
export type CheckState = "pass" | "info" | "warn" | "fail" | "none";

export interface SiteCheck {
  id: CheckId;
  label: string;
  state: CheckState;
  /** The one line under the label. */
  value: string;
  /** The findings behind it, most serious first. */
  findings: AuditFinding[];
}

const CHECK_CODES: Record<Exclude<CheckId, "indexing">, (code: string) => boolean> = {
  robots: (code) => code.startsWith("robots_") || code === "blocked_by_robots",
  sitemap: (code) => code.startsWith("sitemap_"),
  canonical: (code) => code.startsWith("canonical_") || code === "duplicate_without_canonical",
  hreflang: (code) => code.startsWith("hreflang_"),
  "structured-data": (code) => code.startsWith("jsonld_"),
};

function stateOf(findings: readonly AuditFinding[]): CheckState {
  if (findings.some((f) => seoSeverityOf(f) === "error")) return "fail";
  if (findings.some((f) => seoSeverityOf(f) === "warn")) return "warn";
  if (findings.length > 0) return "info";
  return "pass";
}

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;
const hostOf = (url: string): string => {
  try {
    return new URL(url).host;
  } catch {
    return "";
  }
};

/** robots.txt, the sitemap, indexability, the canonical host, hreflang and structured data, in that order. */
export function siteChecks(audit: SiteAudit, preview?: boolean): SiteCheck[] {
  const seo = sortSeoFindings(findingsOf(audit, "seo"));
  const of = (id: Exclude<CheckId, "indexing">) => seo.filter((f) => CHECK_CODES[id](f.code));

  const robots = of("robots");
  const robotsValue = robots.some((f) => f.code === "robots_missing")
    ? "Missing"
    : robots.some((f) => f.code === "robots_unreachable")
      ? "Unreachable"
      : robots.length > 0
        ? plural(robots.length, "finding")
        : "Allows crawling";

  const sitemap = of("sitemap");
  const broken = sitemap.filter((f) => f.code === "sitemap_url_broken").length;
  const sitemapValue = sitemap.some((f) => f.code === "sitemap_missing")
    ? "Missing"
    : sitemap.some((f) => f.code === "sitemap_unreachable" || f.code === "sitemap_invalid")
      ? "Unreadable"
      : broken > 0
        ? `${plural(broken, "listed URL")} broken`
        : sitemap.length > 0
          ? plural(sitemap.length, "finding")
          : "Valid";

  const indexing = indexabilityOf(audit, preview);
  const indexingFindings = seo.filter((f) => f.code === "indexing_mismatch" || f.code === "preview_indexable");

  const canonical = of("canonical");
  const canonicalHosts = new Set(audit.pages.map((page) => hostOf(page.head.canonical)).filter(Boolean));
  const canonicalValue = canonical.some((f) => f.code === "canonical_foreign_host")
    ? "Points at another host"
    : canonicalHosts.size === 1
      ? [...canonicalHosts][0]!
      : canonicalHosts.size > 1
        ? `${canonicalHosts.size} hosts`
        : "None declared";

  const hreflang = of("hreflang");
  const usesHreflang = audit.pages.some((page) => page.head.hreflang.length > 0);

  const jsonld = of("structured-data");
  const types = [...new Set(audit.pages.flatMap((page) => page.head.jsonldTypes))].sort();

  return [
    { id: "robots", label: "robots.txt", state: stateOf(robots), value: robotsValue, findings: robots },
    { id: "sitemap", label: "sitemap.xml", state: stateOf(sitemap), value: sitemapValue, findings: sitemap },
    {
      id: "indexing",
      label: "Indexing",
      state: indexing.problem ? "fail" : indexing.platform ? "info" : indexing.actual === "unknown" ? "none" : "pass",
      value: indexing.label,
      findings: indexingFindings,
    },
    { id: "canonical", label: "Canonical host", state: canonicalHosts.size === 0 && canonical.length === 0 ? "none" : stateOf(canonical), value: canonicalValue, findings: canonical },
    { id: "hreflang", label: "hreflang", state: usesHreflang || hreflang.length > 0 ? stateOf(hreflang) : "none", value: usesHreflang ? (hreflang.length > 0 ? plural(hreflang.length, "finding") : "Valid") : "Not used", findings: hreflang },
    {
      id: "structured-data",
      label: "Structured data",
      state: types.length > 0 || jsonld.length > 0 ? stateOf(jsonld) : "none",
      value: types.length > 0 ? types.join(", ") : jsonld.length > 0 ? plural(jsonld.length, "finding") : "None",
      findings: jsonld,
    },
  ];
}

// ── Routes ──────────────────────────────────────────────────────────────────

export interface SeoRouteRow {
  page: AuditPage;
  route: string;
  status: number;
  score: number | null;
  title: string;
  titleLength: number;
  description: string;
  descriptionLength: number;
  canonical: string;
  ogComplete: boolean | null;
  indexable: boolean | null;
  jsonldTypes: string[];
  /** This route's SEO findings, most serious first. */
  findings: AuditFinding[];
  errors: number;
  warnings: number;
}

const length = (s: string) => [...s].length;

/** Every crawled route, worst SEO score first. */
export function seoRoutes(audit: SiteAudit): SeoRouteRow[] {
  const seo = findingsOf(audit, "seo");
  return pagesWorstFirst(audit.pages, "seo").map((page) => {
    const findings = sortSeoFindings(seo.filter((f) => f.route === page.route));
    return {
      page,
      route: page.route,
      status: page.status,
      score: page.seo,
      title: page.head.title,
      titleLength: length(page.head.title),
      description: page.head.description,
      descriptionLength: length(page.head.description),
      canonical: page.head.canonical,
      ogComplete: page.head.ogComplete,
      indexable: page.head.indexable,
      jsonldTypes: page.head.jsonldTypes,
      findings,
      errors: findings.filter((f) => seoSeverityOf(f) === "error").length,
      warnings: findings.filter((f) => seoSeverityOf(f) === "warn").length,
    };
  });
}

/**
 * The SEO findings no crawled route owns, most serious first: site-wide ones
 * (route "": robots.txt, the sitemap) and those on a path that is not a page
 * of the audit (the soft-404 probe of a made-up path).
 */
export function siteWideFindings(audit: SiteAudit): AuditFinding[] {
  const routes = new Set(audit.pages.map((page) => page.route));
  return sortSeoFindings(findingsOf(audit, "seo").filter((f) => f.route === "" || !routes.has(f.route)));
}

// ── Broken links ────────────────────────────────────────────────────────────

export interface BrokenLink {
  /** The route the link is on. */
  source: string;
  /** The URL it points at. */
  target: string;
  /** The HTTP status; 0 when the check got none. */
  status: number;
  /** Why the check failed, when it did. */
  error: string;
  internal: boolean;
}

/** Every broken link, internal first (they are the site's to fix), then by source and target. */
export function brokenLinks(audit: SiteAudit): BrokenLink[] {
  return findingsOf(audit, "seo")
    .filter((f) => f.code === "link_broken")
    .map((f) => ({ source: f.route, target: f.ref, status: Number(f.evidence.status) || 0, error: f.evidence.error ?? "", internal: f.evidence.scope !== "external" }))
    .sort((a, b) => Number(b.internal) - Number(a.internal) || a.source.localeCompare(b.source) || a.target.localeCompare(b.target));
}

// ── Social card ─────────────────────────────────────────────────────────────

export interface SocialCard {
  title: string;
  description: string;
  /** The domain a card names: og:url's, else the canonical's. */
  domain: string;
  /** The auditor's signed thumbnail; "" when there is none to show. */
  imageUrl: string;
  /** Whether the page declares an og:image at all (it may be unreachable, so no thumbnail). */
  declaresImage: boolean;
  /** X's layout: twitter:card, "summary" when unset. */
  twitterCard: string;
  /** The Open Graph tags a scraper wants and the page lacks. */
  missing: string[];
}

const OG_WANTED = ["og:title", "og:description", "og:image", "og:url"] as const;

/**
 * How a link to the page previews in Slack, X and LinkedIn: what their
 * scrapers read from the served HTML (Open Graph first, the title and
 * description as the fallback they use), with the image from the auditor's
 * stored thumbnail only (design O11).
 */
export function socialCardOf(page: AuditPage): SocialCard {
  const social = page.head.social;
  const domain = hostOf(social["og:url"] ?? "") || hostOf(page.head.canonical);
  return {
    title: social["og:title"] || social["twitter:title"] || page.head.title,
    description: social["og:description"] || social["twitter:description"] || page.head.description,
    domain,
    imageUrl: page.thumbnailUrl,
    declaresImage: !!(social["og:image"] || social["twitter:image"]),
    twitterCard: social["twitter:card"] || "summary",
    missing: OG_WANTED.filter((tag) => !social[tag]),
  };
}
