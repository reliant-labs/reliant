// Copyright (c) 2026 Reliant Labs

/**
 * WHAT THE SEO AND ACCESSIBILITY VIEWS SHARE (Site insights R4/R5): one site
 * audit, as control-plane's `controlplane.v1.SiteAuditService.GetSiteAudit`
 * answers it, in the console's terms, and which site and environment a view
 * audits. Pure, so its rules are tested alone.
 *
 * AN AUDIT is the control plane's site auditor crawling one hosted static
 * frontend in one environment after a promotion (or a manual re-run): each
 * route's head facts and scores, and every finding with its evidence and fix,
 * produced by forge's `pkg/siteaudit`. The scoring is Lighthouse-compatible,
 * not Lighthouse itself (design O1), and the views say so.
 *
 *   SiteAudit     the newest finished audit (null before the first one), its
 *                 pages and findings, the audit its deltas are taken against
 *                 (`previous`: the newest finished audit of another release),
 *                 one still running, and the org's manual re-runs left today
 *   AuditTarget   the (environment, frontend) a view reads: a site is never a
 *                 URL (the server takes no URL), so the console names it by
 *                 the frontend's deployment and the environment's id
 *
 * The wire's head facts and evidence are JSON objects (google.protobuf.Struct)
 * in pkg/siteaudit's own spelling (`jsonld_types`, `og_complete`); they are
 * read here, defensively, and nowhere else.
 */

import { timestampMs, type Timestamp } from "@bufbuild/protobuf/wkt";
import type { JsonObject, JsonValue } from "@bufbuild/protobuf";

import {
  SiteAuditCategory,
  SiteAuditStatus,
  SiteAuditTrigger,
  type GetSiteAuditResponse,
  type SiteAudit as WireAudit,
  type SiteAuditFinding as WireFinding,
  type SiteAuditPage as WirePage,
} from "@/gen/controlplane/services/siteaudit/v1/siteaudit_pb";

import { ENV_ALL } from "../Console/envScope";
import { siteServiceNames } from "./speedInsightsModel";

// ── The audit ───────────────────────────────────────────────────────────────

export type AuditCategory = "seo" | "accessibility";
export type AuditRunStatus = "queued" | "running" | "done" | "failed" | "skipped_quota" | "unknown";
export type AuditTrigger = "promotion" | "manual" | "scheduled" | "unknown";

/** One run of the auditor, without its pages. */
export interface AuditRun {
  id: string;
  /** The release served when it was queued; "" when the environment had none. */
  release: string;
  trigger: AuditTrigger;
  status: AuditRunStatus;
  /** 0–100; null until the pass that produces it has run (accessibility needs the render pass). */
  seo: number | null;
  a11y: number | null;
  pagesFetched: number;
  pagesRendered: number;
  /** Why a failed audit failed. */
  error: string;
  queuedAtMs: number | null;
  finishedAtMs: number | null;
}

/** The audit a delta is taken against. */
export interface AuditPrevious {
  release: string;
  seo: number | null;
  a11y: number | null;
  finishedAtMs: number | null;
}

/** One hreflang alternate a page declares. */
export interface AuditAlternate {
  hreflang: string;
  href: string;
}

/** What a page's head says (pkg/siteaudit's Head). */
export interface AuditHead {
  title: string;
  description: string;
  canonical: string;
  lang: string;
  /** The meta robots content. */
  robots: string;
  /** The X-Robots-Tag response header. */
  xRobotsTag: string;
  /** The first value of each og:* and twitter:* tag, keyed by the tag. */
  social: Record<string, string>;
  hreflang: AuditAlternate[];
  jsonldTypes: string[];
  /** Whether nothing keeps the page out of search; null when the audit did not say. */
  indexable: boolean | null;
  /** og:title, og:type, og:image and og:url in the served HTML; null when the audit did not say. */
  ogComplete: boolean | null;
  /** The tags only the rendered DOM has (title, description, canonical). */
  clientRendered: string[];
}

/** One crawled route. */
export interface AuditPage {
  route: string;
  /** The HTTP status; 0 when it got no response. */
  status: number;
  rendered: boolean;
  seo: number | null;
  a11y: number | null;
  head: AuditHead;
  /**
   * The auditor's stored og:image thumbnail, behind a short-lived signed URL
   * (design O11); "" when the route has none. The console never loads the
   * page's own og:image URL.
   */
  thumbnailUrl: string;
}

/** One element an accessibility finding names. */
export interface AuditNode {
  /** Its CSS selector. */
  target: string;
  /** Its outer HTML, truncated by the auditor (300 characters). */
  html: string;
  /** axe's failure summary. */
  summary: string;
}

export type SeoSeverity = "error" | "warn" | "info";

/** One finding. */
export interface AuditFinding {
  category: AuditCategory;
  /** The rule code ("description_missing"; "a11y_violation" for every axe rule). */
  code: string;
  /** For SEO error, warn or info; for accessibility, the engine's (see a11yImpactOf). */
  severity: string;
  /** "" for a site-wide finding (robots.txt, the sitemap). */
  route: string;
  /** What it is about: a tag, a URL, an axe rule id. */
  ref: string;
  /** What was observed, keyed by name; every value as text. */
  evidence: Record<string, string>;
  /** The elements an accessibility finding names. */
  nodes: AuditNode[];
  fix: string;
}

export interface AuditQuota {
  manualPerDay: number;
  manualRemaining: number;
  resetsAtMs: number | null;
  paid: boolean;
}

export interface SiteAudit {
  /** The newest finished audit; null before the site's first. */
  audit: AuditRun | null;
  pages: AuditPage[];
  findings: AuditFinding[];
  previous: AuditPrevious | null;
  /** A queued or running audit beside the finished one. */
  inProgress: AuditRun | null;
  quota: AuditQuota | null;
}

const ms = (t: Timestamp | undefined): number | null => (t ? timestampMs(t) : null);
const score = (v: number | undefined): number | null => (typeof v === "number" && Number.isFinite(v) ? Math.round(v) : null);

const STATUS: Record<SiteAuditStatus, AuditRunStatus> = {
  [SiteAuditStatus.UNSPECIFIED]: "unknown",
  [SiteAuditStatus.QUEUED]: "queued",
  [SiteAuditStatus.RUNNING]: "running",
  [SiteAuditStatus.DONE]: "done",
  [SiteAuditStatus.FAILED]: "failed",
  [SiteAuditStatus.SKIPPED_QUOTA]: "skipped_quota",
};
const TRIGGER: Record<SiteAuditTrigger, AuditTrigger> = {
  [SiteAuditTrigger.UNSPECIFIED]: "unknown",
  [SiteAuditTrigger.PROMOTION]: "promotion",
  [SiteAuditTrigger.MANUAL]: "manual",
  [SiteAuditTrigger.SCHEDULED]: "scheduled",
};

function runOf(a: WireAudit | undefined): AuditRun | null {
  if (!a) return null;
  return {
    id: a.id,
    release: a.release,
    trigger: TRIGGER[a.trigger] ?? "unknown",
    status: STATUS[a.status] ?? "unknown",
    seo: score(a.seoScore),
    a11y: score(a.a11yScore),
    pagesFetched: a.pagesFetched,
    pagesRendered: a.pagesRendered,
    error: a.error,
    queuedAtMs: ms(a.queuedAt),
    finishedAtMs: ms(a.finishedAt),
  };
}

const text = (v: JsonValue | undefined): string => (typeof v === "string" ? v : typeof v === "number" || typeof v === "boolean" ? String(v) : "");
const flag = (v: JsonValue | undefined): boolean | null => (typeof v === "boolean" ? v : null);
const strings = (v: JsonValue | undefined): string[] => (Array.isArray(v) ? v.filter((x): x is string => typeof x === "string") : []);
const object = (v: JsonValue | undefined): JsonObject => (v && typeof v === "object" && !Array.isArray(v) ? (v as JsonObject) : {});

/** A page's head facts, from the wire's JSON object. */
export function headOf(head: JsonObject | undefined): AuditHead {
  const h = head ?? {};
  const social: Record<string, string> = {};
  for (const [key, value] of Object.entries(object(h.social))) social[key] = text(value);
  const hreflang = Array.isArray(h.hreflang)
    ? h.hreflang.map((alt) => ({ hreflang: text(object(alt).hreflang), href: text(object(alt).href) })).filter((alt) => alt.hreflang !== "")
    : [];
  return {
    title: text(h.title),
    description: text(h.description),
    canonical: text(h.canonical),
    lang: text(h.lang),
    robots: text(h.robots),
    xRobotsTag: text(h.x_robots_tag),
    social,
    hreflang,
    jsonldTypes: strings(h.jsonld_types),
    indexable: flag(h.indexable),
    ogComplete: flag(h.og_complete),
    clientRendered: strings(h.client_rendered),
  };
}

function pageOf(p: WirePage): AuditPage {
  return { route: p.route || "/", status: p.httpStatus, rendered: p.rendered, seo: score(p.seoScore), a11y: score(p.a11yScore), head: headOf(p.head), thumbnailUrl: p.ogThumbnailUrl };
}

function findingOf(f: WireFinding): AuditFinding | null {
  const category = f.category === SiteAuditCategory.SEO ? "seo" : f.category === SiteAuditCategory.ACCESSIBILITY ? "accessibility" : null;
  if (!category) return null;
  const evidence: Record<string, string> = {};
  let nodes: AuditNode[] = [];
  for (const [key, value] of Object.entries(f.evidence ?? {})) {
    if (key === "nodes" && Array.isArray(value)) {
      nodes = value.map((n) => ({ target: text(object(n).target), html: text(object(n).html), summary: text(object(n).summary) })).filter((n) => n.target !== "" || n.html !== "");
    } else {
      evidence[key] = text(value);
    }
  }
  // The engine spells a warning "warn"; the proto's comment says "warning". Both read as one.
  const severity = f.severity === "warning" ? "warn" : f.severity;
  return { category, code: f.code, severity, route: f.route, ref: f.ref, evidence, nodes, fix: f.fix };
}

/** GetSiteAudit's answer, in the console's terms. */
export function siteAuditOf(res: GetSiteAuditResponse | undefined): SiteAudit {
  const quota = res?.quota;
  return {
    audit: runOf(res?.audit),
    pages: (res?.pages ?? []).map(pageOf),
    findings: (res?.findings ?? []).map(findingOf).filter((f): f is AuditFinding => f !== null),
    previous: res?.previous ? { release: res.previous.release, seo: score(res.previous.seoScore), a11y: score(res.previous.a11yScore), finishedAtMs: ms(res.previous.finishedAt) } : null,
    inProgress: runOf(res?.inProgress),
    quota: quota ? { manualPerDay: quota.manualPerDay, manualRemaining: quota.manualRemaining, resetsAtMs: ms(quota.resetsAt), paid: quota.paid } : null,
  };
}

/** `current − previous`, or null when either is unknown. */
export function scoreDelta(current: number | null, previous: number | null): number | null {
  return current === null || previous === null ? null : current - previous;
}

/** A delta as words: "+3", "−13", "no change". */
export function formatDelta(delta: number): string {
  if (delta === 0) return "no change";
  return delta > 0 ? `+${delta}` : `−${Math.abs(delta)}`;
}

/** The findings of one category. */
export function findingsOf(audit: SiteAudit, category: AuditCategory): AuditFinding[] {
  return audit.findings.filter((f) => f.category === category);
}

/** The audit's score for a category. */
export function categoryScore(run: AuditRun | AuditPrevious | null, category: AuditCategory): number | null {
  if (!run) return null;
  return category === "seo" ? run.seo : run.a11y;
}

/** A page's score for a category. */
export function pageScore(page: AuditPage, category: AuditCategory): number | null {
  return category === "seo" ? page.seo : page.a11y;
}

/**
 * Pages worst first for a category: the lowest score first, a page with no
 * score (not evaluated) after every scored one, then by route.
 */
export function pagesWorstFirst<T extends AuditPage>(pages: readonly T[], category: AuditCategory): T[] {
  return [...pages].sort((a, b) => {
    const sa = pageScore(a, category);
    const sb = pageScore(b, category);
    if (sa === null && sb !== null) return 1;
    if (sb === null && sa !== null) return -1;
    if (sa !== null && sb !== null && sa !== sb) return sa - sb;
    return a.route.localeCompare(b.route);
  });
}

/** Why an audit is queued or running, in words. */
export function inProgressWords(run: AuditRun): string {
  const why = run.trigger === "manual" ? "A re-run" : run.trigger === "scheduled" ? "The weekly re-audit" : "An audit";
  const of = run.release ? ` of ${run.release}` : "";
  return run.status === "running" ? `${why}${of} is running now.` : `${why}${of} is queued.`;
}

// ── Which site, in which environment ────────────────────────────────────────

/** One environment a static site is deployed in. */
export interface AuditSiteEnv {
  id: string;
  name: string;
  preview: boolean;
}

/** A hosted static frontend and the environments it is deployed in (prod first). */
export interface AuditSite {
  frontend: string;
  envs: AuditSiteEnv[];
}

export type AuditTarget =
  | { kind: "site"; frontend: string; envId: string; envName: string; preview: boolean }
  /** The project deploys no hosted static site: nothing is audited. */
  | { kind: "no-site" }
  /** `?site=` names something that is not a hosted static site (an app frontend). */
  | { kind: "not-static"; site: string }
  /** The site is not deployed in the scoped environment. */
  | { kind: "not-in-env"; frontend: string; env: string };

/**
 * The site and environment an audit view reads. The site is the URL's
 * `?site=` (a telemetry service.name: `<project>-<frontend>` or the bare
 * frontend, siteServiceNames), else the project's first static site. The
 * environment is the console's scope, or with every environment in scope the
 * site's first (prod first, the order `sites` carries).
 */
export function auditTargetOf({ project, site, env, sites }: { project: string; site: string | undefined; env: string; sites: readonly AuditSite[] }): AuditTarget {
  if (sites.length === 0) return { kind: "no-site" };
  const chosen = site ? sites.find((candidate) => siteServiceNames(project, candidate.frontend).includes(site)) : sites[0];
  if (!chosen) return { kind: "not-static", site: site ?? "" };
  const deployed = env === ENV_ALL ? chosen.envs[0] : chosen.envs.find((candidate) => candidate.name === env);
  if (!deployed) return { kind: "not-in-env", frontend: chosen.frontend, env };
  return { kind: "site", frontend: chosen.frontend, envId: deployed.id, envName: deployed.name, preview: deployed.preview };
}

/** The static sites of a project, from each environment's GetStatus workloads, frontends by name. */
export function auditSitesOf(envs: readonly AuditSiteEnv[], workloadsByEnvId: ReadonlyMap<string, readonly { name?: string; tier?: string }[]>): AuditSite[] {
  const byName = new Map<string, AuditSiteEnv[]>();
  for (const env of envs) {
    for (const workload of workloadsByEnvId.get(env.id) ?? []) {
      if (workload.tier !== "static" || !workload.name) continue;
      const list = byName.get(workload.name) ?? [];
      list.push(env);
      byName.set(workload.name, list);
    }
  }
  return [...byName.entries()].map(([frontend, siteEnvs]) => ({ frontend, envs: siteEnvs })).sort((a, b) => a.frontend.localeCompare(b.frontend));
}
