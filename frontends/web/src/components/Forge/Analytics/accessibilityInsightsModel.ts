// Copyright (c) 2026 Reliant Labs

/**
 * SITE INSIGHTS › ACCESSIBILITY, as data (AccessibilityInsightsView.tsx
 * draws it): each route's score, and the audit's axe violations as ISSUES,
 * one per axe rule across the site, grouped the way Lighthouse's
 * accessibility category groups them. Pure.
 *
 * The audit engine (forge pkg/siteaudit) reports one finding per (route, axe
 * rule): code `a11y_violation`, the rule id as its ref, the impact, the
 * Lighthouse group, the WCAG tags and the deque help link in its evidence,
 * and up to 20 elements (a selector, a snippet truncated to 300 characters,
 * axe's failure summary). An issue folds a rule's findings together: its
 * worst impact, the routes it is on, every element with its route.
 *
 * The help link and the snippets come out of a browser that ran the
 * customer's page, so they are untrusted: a snippet is only ever text, and
 * a help link is shown only when it is deque's (helpUrlOf).
 */

import type { AuditFinding, AuditNode, SiteAudit } from "./siteAuditModel";
import { findingsOf, pagesWorstFirst } from "./siteAuditModel";

// ── Impact ──────────────────────────────────────────────────────────────────

/** axe's impact, most serious first. */
export const IMPACTS = ["critical", "serious", "moderate", "minor"] as const;
export type Impact = (typeof IMPACTS)[number];
export const IMPACT_LABEL: Record<Impact, string> = { critical: "Critical", serious: "Serious", moderate: "Moderate", minor: "Minor" };

const isImpact = (s: string): s is Impact => (IMPACTS as readonly string[]).includes(s);

/**
 * A finding's axe impact: the evidence's, else its severity when that is an
 * impact word, else the engine's severity mapped back (error is what it calls
 * critical, warn serious, info moderate or minor).
 */
export function impactOf(finding: AuditFinding): Impact {
  const evidence = finding.evidence.impact ?? "";
  if (isImpact(evidence)) return evidence;
  if (isImpact(finding.severity)) return finding.severity;
  return finding.severity === "error" ? "critical" : finding.severity === "warn" ? "serious" : "moderate";
}

// ── Lighthouse's groups ─────────────────────────────────────────────────────

/** Lighthouse's accessibility groups, in its report's order. */
export const A11Y_GROUPS = [
  "a11y-names-labels",
  "a11y-color-contrast",
  "a11y-aria",
  "a11y-navigation",
  "a11y-tables-lists",
  "a11y-best-practices",
  "a11y-audio-video",
  "a11y-language",
  "other",
] as const;
export type A11yGroup = (typeof A11Y_GROUPS)[number];

export const A11Y_GROUP_LABEL: Record<A11yGroup, { title: string; about: string }> = {
  "a11y-names-labels": { title: "Names and labels", about: "Controls, images and frames a screen reader can name." },
  "a11y-color-contrast": { title: "Contrast", about: "Text and links people with low vision can read." },
  "a11y-aria": { title: "ARIA", about: "ARIA roles and attributes used the way assistive technology expects." },
  "a11y-navigation": { title: "Navigation", about: "Headings, focus order and ways to skip repeated content." },
  "a11y-tables-lists": { title: "Tables and lists", about: "Lists and tables whose structure assistive technology can read." },
  "a11y-best-practices": { title: "Best practices", about: "Viewport zoom, landmarks, touch targets and more." },
  "a11y-audio-video": { title: "Audio and video", about: "Captions for media." },
  "a11y-language": { title: "Internationalization", about: "The page's language, so it is read in the right voice." },
  other: { title: "Other", about: "Rules outside Lighthouse's accessibility category." },
};

/**
 * Each axe rule Lighthouse scores, by group: forge pkg/siteaudit's
 * A11yAudits (RulesVersion 1). The engine already stamps a finding's group;
 * this is the fallback for one it did not.
 */
const RULE_GROUP: Record<string, A11yGroup> = {
  accesskeys: "a11y-navigation",
  bypass: "a11y-navigation",
  "heading-order": "a11y-navigation",
  tabindex: "a11y-navigation",
  "button-name": "a11y-names-labels",
  "document-title": "a11y-names-labels",
  "form-field-multiple-labels": "a11y-names-labels",
  "frame-title": "a11y-names-labels",
  "image-alt": "a11y-names-labels",
  "input-button-name": "a11y-names-labels",
  "input-image-alt": "a11y-names-labels",
  label: "a11y-names-labels",
  "link-name": "a11y-names-labels",
  "object-alt": "a11y-names-labels",
  "select-name": "a11y-names-labels",
  "skip-link": "a11y-names-labels",
  "color-contrast": "a11y-color-contrast",
  "link-in-text-block": "a11y-color-contrast",
  "definition-list": "a11y-tables-lists",
  dlitem: "a11y-tables-lists",
  list: "a11y-tables-lists",
  listitem: "a11y-tables-lists",
  "td-headers-attr": "a11y-tables-lists",
  "th-has-data-cells": "a11y-tables-lists",
  "html-has-lang": "a11y-language",
  "html-lang-valid": "a11y-language",
  "html-xml-lang-mismatch": "a11y-language",
  "valid-lang": "a11y-language",
  "video-caption": "a11y-audio-video",
  "meta-refresh": "a11y-best-practices",
  "meta-viewport": "a11y-best-practices",
  "target-size": "a11y-best-practices",
  "landmark-one-main": "a11y-best-practices",
  "autocomplete-valid": "a11y-best-practices",
  "presentation-role-conflict": "a11y-best-practices",
  "svg-img-alt": "a11y-best-practices",
};

/** An axe rule's Lighthouse group: the finding's own, else the table's (every aria-* rule is ARIA). */
export function groupOf(finding: AuditFinding): A11yGroup {
  const stamped = finding.evidence.group ?? "";
  if ((A11Y_GROUPS as readonly string[]).includes(stamped)) return stamped as A11yGroup;
  const rule = finding.ref;
  if (RULE_GROUP[rule]) return RULE_GROUP[rule];
  if (rule.startsWith("aria-") || rule === "duplicate-id-aria") return "a11y-aria";
  return "other";
}

// ── Links and tags ──────────────────────────────────────────────────────────

const RULE_ID = /^[a-z0-9-]+$/;

/**
 * The rule's help page: the finding's own link when it is deque's (https,
 * dequeuniversity.com), else deque's page for the rule id; null for neither.
 * Never another host: the link was produced inside the customer's page.
 */
export function helpUrlOf(finding: AuditFinding): string | null {
  const raw = finding.evidence.help_url ?? "";
  try {
    const url = new URL(raw);
    if (url.protocol === "https:" && (url.hostname === "dequeuniversity.com" || url.hostname.endsWith(".dequeuniversity.com"))) return url.toString();
  } catch {
    // not a URL: fall back to the rule id
  }
  return RULE_ID.test(finding.ref) ? `https://dequeuniversity.com/rules/axe/4.10/${finding.ref}` : null;
}

/** "wcag2aa,wcag143" → ["WCAG 2 AA", "1.4.3"]: the conformance level, then the success criteria. */
export function wcagLabels(tags: string): string[] {
  const out: string[] = [];
  for (const tag of tags.split(",").map((t) => t.trim()).filter(Boolean)) {
    const level = /^wcag(2|21|22)(a{1,3})$/.exec(tag);
    if (level) {
      out.push(`WCAG ${level[1]!.split("").join(".")} ${level[2]!.toUpperCase()}`);
      continue;
    }
    const criterion = /^wcag(\d)(\d)(\d+)$/.exec(tag);
    if (criterion) {
      out.push(`${criterion[1]}.${criterion[2]}.${criterion[3]}`);
      continue;
    }
    if (tag === "best-practice") out.push("Best practice");
    else out.push(tag);
  }
  return out;
}

// ── Issues ──────────────────────────────────────────────────────────────────

/** One element of an issue, on one route. */
export interface IssueNode extends AuditNode {
  route: string;
}

/** One axe rule's violations across the site. */
export interface A11yIssue {
  /** The axe rule id ("color-contrast"). */
  rule: string;
  /** What the rule asks, in axe's words. */
  help: string;
  impact: Impact;
  group: A11yGroup;
  wcag: string[];
  helpUrl: string | null;
  /** The routes it is on, in route order. */
  routes: string[];
  nodes: IssueNode[];
  /** Elements axe found that the audit did not keep (over 20 on a route). */
  omitted: number;
}

const IMPACT_RANK: Record<Impact, number> = { critical: 0, serious: 1, moderate: 2, minor: 3 };

/** Every accessibility issue of the audit (of one route when `route` is set), most serious first. */
export function a11yIssues(audit: SiteAudit, route?: string): A11yIssue[] {
  const byRule = new Map<string, A11yIssue>();
  for (const f of findingsOf(audit, "accessibility")) {
    if (route !== undefined && f.route !== route) continue;
    const rule = f.ref || f.code;
    const impact = impactOf(f);
    let issue = byRule.get(rule);
    if (!issue) {
      issue = { rule, help: f.evidence.help || rule, impact, group: groupOf(f), wcag: wcagLabels(f.evidence.wcag ?? ""), helpUrl: helpUrlOf(f), routes: [], nodes: [], omitted: 0 };
      byRule.set(rule, issue);
    }
    if (IMPACT_RANK[impact] < IMPACT_RANK[issue.impact]) issue.impact = impact;
    if (!issue.routes.includes(f.route)) issue.routes.push(f.route);
    issue.nodes.push(...f.nodes.map((n) => ({ ...n, route: f.route })));
    issue.omitted += Number(f.evidence.nodes_omitted) || 0;
  }
  const issues = [...byRule.values()];
  for (const issue of issues) issue.routes.sort();
  return issues.sort((a, b) => IMPACT_RANK[a.impact] - IMPACT_RANK[b.impact] || b.nodes.length - a.nodes.length || a.rule.localeCompare(b.rule));
}

export interface A11yGroupSection {
  group: A11yGroup;
  issues: A11yIssue[];
}

/** Issues in Lighthouse's groups, in its order; a group with no issue is left out. */
export function issuesByGroup(issues: readonly A11yIssue[]): A11yGroupSection[] {
  return A11Y_GROUPS.map((group) => ({ group, issues: issues.filter((issue) => issue.group === group) })).filter((section) => section.issues.length > 0);
}

/** How many issues there are of each impact. */
export function impactCounts(issues: readonly A11yIssue[]): Record<Impact, number> {
  const out: Record<Impact, number> = { critical: 0, serious: 0, moderate: 0, minor: 0 };
  for (const issue of issues) out[issue.impact] += 1;
  return out;
}

// ── Routes ──────────────────────────────────────────────────────────────────

export interface A11yRouteRow {
  route: string;
  status: number;
  /** null when the render pass did not run on it (axe needs it). */
  score: number | null;
  rendered: boolean;
  counts: Record<Impact, number>;
}

/** Every crawled route, worst accessibility score first; routes axe did not run on last. */
export function a11yRoutes(audit: SiteAudit): A11yRouteRow[] {
  return pagesWorstFirst(audit.pages, "accessibility").map((page) => ({
    route: page.route,
    status: page.status,
    score: page.a11y,
    rendered: page.rendered,
    counts: impactCounts(a11yIssues(audit, page.route)),
  }));
}
