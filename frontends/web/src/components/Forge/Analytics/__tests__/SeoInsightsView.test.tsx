// Copyright (c) 2026 Reliant Labs

/**
 * Site insights › SEO, drawn: over the shop pack's audit (the v1.8.2
 * regressions), the header's score and delta, the site checks, the routes
 * worst first, a route's drawer with its findings and link preview, the
 * broken links; and the states before an audit. The data rules are
 * seoInsightsModel.test.ts's.
 */

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { fireEvent, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { GetSiteAuditRequestSchema, GetSiteAuditResponseSchema, SiteAuditSchema, SiteAuditStatus, SiteAuditTrigger } from "@/gen/controlplane/services/siteaudit/v1/siteaudit_pb";
import { SHOP_WEB_OG_THUMBNAIL, shopSiteAudit } from "@/fixtures/shop/handlers/siteAudit";
import { resetDemoNowForTests } from "@/fixtures/shop/shopStory";
import { useRouteOrgScope } from "@/lib/routeOrg";
import { renderWithQuery } from "@/test/renderWithQuery";

import { SeoInsightsBody } from "../SeoInsightsView";
import { ObsReadOnlyProvider } from "../../Observability/obsReadOnly";
import { AuditEmpty, AuditTargetNotice, RERUN_READ_ONLY_WORDS, rerunErrorWords } from "../siteAuditParts";
import { siteAuditOf } from "../siteAuditModel";

const NOW = Date.UTC(2026, 10, 26, 12, 0);
// Re-run sends the org it runs in (RerunButton), so the live console is on an org page.
const routeOrg = useRouteOrgScope.getState();
beforeEach(() => {
  resetDemoNowForTests(NOW);
  useRouteOrgScope.setState({ scope: { kind: "member", org: { id: "org-shop", slug: "shop" } } });
});
afterEach(() => {
  resetDemoNowForTests();
  useRouteOrgScope.setState(routeOrg);
});

const TARGET = { kind: "site" as const, frontend: "shop-web", envId: "env-prod", envName: "prod", preview: false };
const storyAudit = () => siteAuditOf(shopSiteAudit(create(GetSiteAuditRequestSchema, { selector: { case: "latest", value: { environmentId: "env-prod", frontend: "shop-web" } } })));

describe("SeoInsightsBody", () => {
  it("heads the view with the score, its drop since v1.8.1, the release and the re-runs left", () => {
    renderWithQuery(<SeoInsightsBody audit={storyAudit()} target={TARGET} />);
    expect(screen.getByTestId("site-audit-seo-score")).toHaveTextContent("81");
    expect(screen.getByTestId("site-audit-seo-delta")).toHaveTextContent("−13 since v1.8.1 (94)");
    expect(screen.getByTestId("site-audit-when")).toHaveTextContent(/^Release v1\.8\.2 · shop-web in prod · audited \d+[smhd] ago · 7 pages read$/);
    expect(screen.getByText(/Lighthouse-compatible scoring/)).toBeInTheDocument();
    const rerun = screen.getByTestId("site-audit-rerun");
    expect(within(rerun).getByRole("button", { name: /Re-run/ })).toBeEnabled();
    expect(rerun).toHaveTextContent("2 of 3 re-runs left today");
    expect(screen.getByTestId("site-audit-seo-counts")).toHaveTextContent("Errors2Warnings4Broken links3");
  });

  it("shows the site checks, the routes worst first and the broken links", () => {
    renderWithQuery(<SeoInsightsBody audit={storyAudit()} target={TARGET} />);
    expect(screen.getByTestId("site-audit-check-indexing")).toHaveTextContent("Indexed");
    expect(screen.getByTestId("site-audit-check-canonical")).toHaveTextContent("shop.example.com");
    const routes = within(screen.getByTestId("site-insights-seo-routes")).getAllByTestId("site-audit-seo-route");
    expect(routes.map((row) => row.querySelector("button")!.textContent)).toEqual(["/products/view", "/blog", "/", "/products", "/search", "/cart", "/checkout"]);
    expect(routes[0]).toHaveTextContent("missing");
    const links = screen.getByTestId("site-audit-broken-links");
    expect(within(links).getAllByText("https://shop.example.com/sale")).toHaveLength(2);
    expect(links).toHaveTextContent("timeout after 5s");
    expect(screen.getByTestId("site-audit-site-wide")).toHaveTextContent("Unknown paths answer 200");
  });

  it("opens a route's drawer: its findings with the fix, and its link preview from the thumbnail", () => {
    renderWithQuery(<SeoInsightsBody audit={storyAudit()} target={TARGET} />);
    fireEvent.click(screen.getByRole("button", { name: "/products/view" }));
    const drawer = screen.getByTestId("site-audit-route-drawer");
    expect(within(drawer).getAllByTestId("site-audit-finding").map((f) => f.textContent)).toEqual([
      expect.stringContaining("Meta description missing"),
      expect.stringContaining("Open Graph tag missing"),
    ]);
    expect(drawer).toHaveTextContent('Add <meta name="description"');
    expect(within(drawer).getByTestId("social-card-slack")).toHaveTextContent("Stoneware mug, 350 ml · shop");
    expect(within(drawer).getByTestId("social-card-image")).toHaveAttribute("src", SHOP_WEB_OG_THUMBNAIL);
    expect(within(drawer).getByTestId("social-card-missing")).toHaveTextContent("og:description");
    fireEvent.click(within(drawer).getByRole("button", { name: "LinkedIn" }));
    expect(within(drawer).getByTestId("social-card-linkedin")).toHaveTextContent("shop.example.com");
    fireEvent.click(within(drawer).getByRole("button", { name: "X" }));
    expect(within(drawer).getByTestId("social-card-x")).toHaveTextContent("From shop.example.com");
  });

  it("draws /blog's link preview without a picture: it declares no og:image", () => {
    renderWithQuery(<SeoInsightsBody audit={storyAudit()} target={TARGET} />);
    fireEvent.click(screen.getByRole("button", { name: "/blog" }));
    const drawer = screen.getByTestId("site-audit-route-drawer");
    expect(within(drawer).getByTestId("social-card-no-image")).toHaveTextContent("No og:image");
    expect(within(drawer).queryByRole("img")).toBeNull();
  });

  it("says when another audit is running, and holds Re-run until it ends", () => {
    const res = shopSiteAudit(create(GetSiteAuditRequestSchema, { selector: { case: "latest", value: { environmentId: "env-prod", frontend: "shop-web" } } }));
    res.inProgress = create(SiteAuditSchema, { id: "a2", status: SiteAuditStatus.QUEUED, trigger: SiteAuditTrigger.PROMOTION, release: "v1.8.3" });
    const audit = siteAuditOf(res);
    renderWithQuery(<SeoInsightsBody audit={audit} target={TARGET} />);
    expect(screen.getByTestId("site-audit-in-progress")).toHaveTextContent("An audit of v1.8.3 is queued.");
    expect(within(screen.getByTestId("site-audit-rerun")).getByRole("button", { name: /Re-run/ })).toBeDisabled();
  });
});

describe("before an audit", () => {
  it("says the first audit runs after the next release", () => {
    const empty = siteAuditOf(create(GetSiteAuditResponseSchema, { quota: { manualPerDay: 3, manualRemaining: 3 } }));
    renderWithQuery(<AuditEmpty audit={empty} envId="env-prod" frontend="shop-web" />);
    expect(screen.getByTestId("site-audit-empty")).toHaveTextContent("Your first audit runs after the next release");
    expect(screen.getByTestId("site-audit-rerun")).toHaveTextContent("3 of 3 re-runs left today");
  });

  it("names why there is nothing to audit", () => {
    const { rerender } = renderWithQuery(<AuditTargetNotice target={{ kind: "no-site" }} loading={false} />);
    expect(screen.getByTestId("site-audit-no-target")).toHaveTextContent("No hosted static site to audit");
    rerender(<AuditTargetNotice target={{ kind: "not-in-env", frontend: "shop-web", env: "dev" }} loading={false} />);
    expect(screen.getByTestId("site-audit-no-target")).toHaveTextContent("shop-web is not deployed in dev");
  });

  it("never sends a re-run from a read-only console (the demo): it says when audits run instead", () => {
    renderWithQuery(
      <ObsReadOnlyProvider value={{ readOnly: true, reason: "Read-only demo" }}>
        <SeoInsightsBody audit={storyAudit()} target={TARGET} />
      </ObsReadOnlyProvider>
    );
    const rerun = screen.getByTestId("site-audit-rerun");
    expect(within(rerun).getByRole("button", { name: /Re-run/ })).toBeDisabled();
    expect(within(rerun).getByRole("button", { name: /Re-run/ })).toHaveAttribute("title", "Read-only demo");
    expect(rerun).toHaveTextContent(RERUN_READ_ONLY_WORDS);
  });

  it("says a refused re-run in words", () => {
    expect(rerunErrorWords(new ConnectError("quota", Code.ResourceExhausted))).toBe("No re-runs are left today. An audit still runs after every release.");
    expect(rerunErrorWords(new ConnectError("This is a demo. Changes are turned off.", Code.PermissionDenied))).toBe("This is a demo. Changes are turned off.");
  });
});
