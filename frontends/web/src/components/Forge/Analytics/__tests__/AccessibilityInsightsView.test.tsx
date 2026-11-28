// Copyright (c) 2026 Reliant Labs

/**
 * Site insights › Accessibility, drawn: over the shop pack's audit (v1.8.2's
 * two contrast failures and the unnamed button), the header's score, delta
 * and issues by impact; the routes worst first, filtering the issues; the
 * issues in Lighthouse's groups, each opening to its elements. The data
 * rules are accessibilityInsightsModel.test.ts's.
 */

import { create } from "@bufbuild/protobuf";
import { fireEvent, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { GetSiteAuditRequestSchema } from "@/gen/controlplane/services/siteaudit/v1/siteaudit_pb";
import { shopSiteAudit } from "@/fixtures/shop/handlers/siteAudit";
import { resetDemoNowForTests } from "@/fixtures/shop/shopStory";
import { renderWithQuery } from "@/test/renderWithQuery";

import { AccessibilityInsightsBody } from "../AccessibilityInsightsView";
import { siteAuditOf } from "../siteAuditModel";

beforeEach(() => resetDemoNowForTests(Date.UTC(2026, 10, 26, 12, 0)));
afterEach(() => resetDemoNowForTests());

const TARGET = { kind: "site" as const, frontend: "shop-web", envId: "env-prod", envName: "prod", preview: false };
const storyResponse = () => shopSiteAudit(create(GetSiteAuditRequestSchema, { selector: { case: "latest", value: { environmentId: "env-prod", frontend: "shop-web" } } }));

describe("AccessibilityInsightsBody", () => {
  it("heads the view with the score, its drop since v1.8.1 and the issues by impact", () => {
    renderWithQuery(<AccessibilityInsightsBody audit={siteAuditOf(storyResponse())} target={TARGET} />);
    expect(screen.getByTestId("site-audit-accessibility-score")).toHaveTextContent("88");
    expect(screen.getByTestId("site-audit-accessibility-delta")).toHaveTextContent("−9 since v1.8.1 (97)");
    expect(screen.getByTestId("site-audit-a11y-counts")).toHaveTextContent("Critical1Serious1Moderate1Minor0");
    expect(screen.getByText(/Lighthouse-compatible scoring/)).toHaveTextContent("accessibility category");
  });

  it("lists the routes worst first, and the issues in Lighthouse's groups, most serious first", () => {
    renderWithQuery(<AccessibilityInsightsBody audit={siteAuditOf(storyResponse())} target={TARGET} />);
    const routes = within(screen.getByTestId("site-insights-a11y-routes")).getAllByTestId("site-audit-a11y-route");
    expect(routes.map((r) => r.querySelector("button")!.textContent)).toEqual(["/cart", "/", "/products", "/checkout", "/products/view", "/search", "/blog"]);
    expect(within(screen.getByTestId("site-audit-a11y-group-a11y-names-labels")).getByText("Buttons must have discernible text")).toBeInTheDocument();
    const contrast = within(screen.getByTestId("site-audit-a11y-group-a11y-color-contrast")).getByTestId("site-audit-a11y-issue");
    expect(contrast).toHaveTextContent("Serious");
    expect(contrast).toHaveTextContent("WCAG 2 AA · 1.4.3");
    expect(contrast).toHaveTextContent("3 elements on 2 routes");
    expect(screen.getByTestId("site-audit-a11y-group-a11y-navigation")).toHaveTextContent("Heading levels should only increase by one");
  });

  it("opens an issue: each element's selector, snippet, what fails, its route, and deque's help", () => {
    renderWithQuery(<AccessibilityInsightsBody audit={siteAuditOf(storyResponse())} target={TARGET} />);
    const issue = within(screen.getByTestId("site-audit-a11y-group-a11y-names-labels")).getByTestId("site-audit-a11y-issue");
    fireEvent.click(within(issue).getByRole("button", { expanded: false }));
    const detail = within(issue).getByTestId("site-audit-a11y-issue-detail");
    const node = within(detail).getByTestId("site-audit-a11y-node");
    expect(node).toHaveTextContent(".cart-line .qty-stepper > button:nth-child(3)");
    expect(node).toHaveTextContent('<button class="qty-increment" type="button">');
    expect(node).toHaveTextContent("on /cart");
    expect(node).toHaveTextContent("Element does not have inner text");
    expect(within(detail).getByTestId("site-audit-a11y-help")).toHaveAttribute("href", "https://dequeuniversity.com/rules/axe/4.10/button-name");
    // A snippet is text, never markup.
    expect(node.querySelector("button.qty-increment")).toBeNull();
  });

  it("filters the issues to a route picked in the table, and clears it", () => {
    renderWithQuery(<AccessibilityInsightsBody audit={siteAuditOf(storyResponse())} target={TARGET} />);
    fireEvent.click(screen.getByRole("button", { name: "/products" }));
    expect(screen.getAllByTestId("site-audit-a11y-issue")).toHaveLength(1);
    expect(screen.getByTestId("site-audit-a11y-issue")).toHaveTextContent("1 element on /products");
    fireEvent.click(screen.getByRole("button", { name: "/checkout" }));
    expect(screen.getByTestId("site-audit-a11y-no-issues")).toHaveTextContent("no accessibility issue on this route");
    fireEvent.click(screen.getByTestId("site-audit-a11y-route-filter"));
    expect(screen.getAllByTestId("site-audit-a11y-issue")).toHaveLength(3);
  });

  it("says accessibility was not checked when the audit rendered no page", () => {
    const res = storyResponse();
    res.audit!.pagesRendered = 0;
    renderWithQuery(<AccessibilityInsightsBody audit={siteAuditOf(res)} target={TARGET} />);
    expect(screen.getByTestId("site-audit-a11y-not-rendered")).toHaveTextContent("accessibility was not checked");
    expect(screen.queryByTestId("site-insights-a11y-routes")).toBeNull();
  });
});
