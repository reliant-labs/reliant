// Copyright (c) 2026 Reliant Labs

/**
 * Site insights › Accessibility as data (accessibilityInsightsModel.ts): axe
 * violations folded into one issue per rule, in Lighthouse's groups, most
 * serious first; each route's score and counts; the help link kept to
 * deque's; WCAG tags in words. Over the shop pack's audit story where it
 * reads best, by hand where a rule needs a case the story lacks.
 */

import { create } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { GetSiteAuditRequestSchema, GetSiteAuditResponseSchema, SiteAuditCategory } from "@/gen/controlplane/services/siteaudit/v1/siteaudit_pb";
import { shopSiteAudit } from "@/fixtures/shop/handlers/siteAudit";
import { resetDemoNowForTests } from "@/fixtures/shop/shopStory";

import { siteAuditOf, type AuditFinding } from "../siteAuditModel";
import { a11yIssues, a11yRoutes, groupOf, helpUrlOf, impactCounts, impactOf, issuesByGroup, wcagLabels } from "../accessibilityInsightsModel";

beforeEach(() => resetDemoNowForTests(Date.UTC(2026, 10, 26, 12, 0)));
afterEach(() => resetDemoNowForTests());

const story = () => siteAuditOf(shopSiteAudit(create(GetSiteAuditRequestSchema, { selector: { case: "latest", value: { environmentId: "env-prod", frontend: "shop-web" } } })));
const finding = (init: Partial<AuditFinding>): AuditFinding => ({ category: "accessibility", code: "a11y_violation", severity: "", route: "/", ref: "", evidence: {}, nodes: [], fix: "", ...init });

describe("a11yIssues", () => {
  it("folds the story's violations into one issue per rule, most serious first", () => {
    const issues = a11yIssues(story());
    expect(issues.map((i) => [i.rule, i.impact, i.routes])).toEqual([
      ["button-name", "critical", ["/cart"]],
      ["color-contrast", "serious", ["/", "/products"]],
      ["heading-order", "moderate", ["/blog"]],
    ]);
    const contrast = issues[1]!;
    expect(contrast.nodes.map((n) => [n.route, n.target])).toEqual([
      ["/", ".hero .price-tag"],
      ["/", ".featured li:nth-child(2) .price-tag"],
      ["/products", ".product-grid .badge-sale"],
    ]);
    expect(contrast).toMatchObject({ help: "Elements must meet minimum color contrast ratio thresholds", group: "a11y-color-contrast", wcag: ["WCAG 2 AA", "1.4.3"], helpUrl: "https://dequeuniversity.com/rules/axe/4.10/color-contrast" });
    expect(impactCounts(issues)).toEqual({ critical: 1, serious: 1, moderate: 1, minor: 0 });
  });

  it("narrows to one route", () => {
    expect(a11yIssues(story(), "/products").map((i) => [i.rule, i.nodes.length])).toEqual([["color-contrast", 1]]);
    expect(a11yIssues(story(), "/checkout")).toEqual([]);
  });

  it("uses the rule id when accessibility help and fix are empty", () => {
    const audit = siteAuditOf(
      create(GetSiteAuditResponseSchema, {
        findings: [{ category: SiteAuditCategory.ACCESSIBILITY, code: "a11y_violation", route: "/", ref: "image-alt", evidence: { help: "" }, fix: "" }],
      })
    );
    expect(a11yIssues(audit)).toEqual([expect.objectContaining({ rule: "image-alt", help: "image-alt" })]);
  });

  it("takes a rule's worst impact across routes, and counts the elements the audit did not keep", () => {
    const audit = siteAuditOf(
      create(GetSiteAuditResponseSchema, {
        findings: [
          { category: SiteAuditCategory.ACCESSIBILITY, code: "a11y_violation", route: "/a", ref: "label", severity: "moderate", evidence: { impact: "moderate", nodes_omitted: "4" } },
          { category: SiteAuditCategory.ACCESSIBILITY, code: "a11y_violation", route: "/b", ref: "label", severity: "critical", evidence: { impact: "critical" } },
          { category: SiteAuditCategory.SEO, code: "title_missing", route: "/a", severity: "error" },
        ],
      })
    );
    expect(a11yIssues(audit)).toEqual([expect.objectContaining({ rule: "label", impact: "critical", routes: ["/a", "/b"], omitted: 4, group: "a11y-names-labels" })]);
  });
});

describe("issuesByGroup", () => {
  it("lists Lighthouse's groups in its order, leaving out empty ones", () => {
    expect(issuesByGroup(a11yIssues(story())).map((s) => s.group)).toEqual(["a11y-names-labels", "a11y-color-contrast", "a11y-navigation"]);
  });
});

describe("a11yRoutes", () => {
  it("lists the routes worst accessibility score first, each with its issues by impact", () => {
    const rows = a11yRoutes(story());
    expect(rows.map((r) => [r.route, r.score])).toEqual([
      ["/cart", 79],
      ["/", 84],
      ["/products", 84],
      ["/checkout", 92],
      ["/products/view", 92],
      ["/search", 92],
      ["/blog", 93],
    ]);
    expect(rows[0]!.counts).toEqual({ critical: 1, serious: 0, moderate: 0, minor: 0 });
    expect(rows.find((r) => r.route === "/")!.counts.serious).toBe(1);
  });
});

describe("the words and links", () => {
  it("reads an impact from the evidence, the severity, or the engine's severity", () => {
    expect(impactOf(finding({ evidence: { impact: "minor" }, severity: "info" }))).toBe("minor");
    expect(impactOf(finding({ severity: "serious" }))).toBe("serious");
    expect(impactOf(finding({ severity: "error" }))).toBe("critical");
    expect(impactOf(finding({ severity: "warn" }))).toBe("serious");
    expect(impactOf(finding({ severity: "info" }))).toBe("moderate");
  });

  it("finds a rule's group: the stamped one, the table's, ARIA for aria-*, else Other", () => {
    expect(groupOf(finding({ ref: "color-contrast", evidence: { group: "a11y-aria" } }))).toBe("a11y-aria");
    expect(groupOf(finding({ ref: "html-has-lang" }))).toBe("a11y-language");
    expect(groupOf(finding({ ref: "aria-roles" }))).toBe("a11y-aria");
    expect(groupOf(finding({ ref: "region" }))).toBe("other");
  });

  it("links only to deque: the finding's own link when it is deque's, else deque's page for the rule", () => {
    expect(helpUrlOf(finding({ ref: "label", evidence: { help_url: "https://dequeuniversity.com/rules/axe/4.9/label?application=axeAPI" } }))).toBe("https://dequeuniversity.com/rules/axe/4.9/label?application=axeAPI");
    expect(helpUrlOf(finding({ ref: "label", evidence: { help_url: "https://evil.example/phish" } }))).toBe("https://dequeuniversity.com/rules/axe/4.10/label");
    expect(helpUrlOf(finding({ ref: "label", evidence: { help_url: "javascript:alert(1)" } }))).toBe("https://dequeuniversity.com/rules/axe/4.10/label");
    expect(helpUrlOf(finding({ ref: "../../x", evidence: {} }))).toBeNull();
  });

  it("says WCAG tags in words", () => {
    expect(wcagLabels("wcag2a,wcag412")).toEqual(["WCAG 2 A", "4.1.2"]);
    expect(wcagLabels("wcag21aa, wcag1410")).toEqual(["WCAG 2.1 AA", "1.4.10"]);
    expect(wcagLabels("best-practice")).toEqual(["Best practice"]);
    expect(wcagLabels("")).toEqual([]);
  });
});
