// Copyright (c) 2026 Reliant Labs

/**
 * A static site's audit findings in release health (Site insights R6):
 * seo_regressed and a11y_regressed are labelled, carry the policy the server
 * gave them, and link to their site's SEO or Accessibility view; a code this
 * console does not know still renders as before ("Finding" and the message).
 */

import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";

import type { ReleaseFinding, ReleaseHealth } from "@/services/controlPlane/observability/deliveryTypes";

import { createFixtureApi } from "../__fixtures__/fixtureApi";
import { findingFigure, ReleaseComparison, siteAuditFindingHref, siteAuditFindingKind } from "../ReleaseComparison";
import { renderWithApi } from "./obsTestUtils";

vi.mock("uplot", async () => (await import("./obsMocks")).obsMocks.uplot());
vi.mock("@tanstack/react-virtual", async () => (await import("./obsMocks")).obsMocks.virtual());
vi.mock("@/components/forge-ui/link", () => ({
  default: ({ href, children, ...rest }: { href: string; children: ReactNode }) => (
    <a href={href} {...rest}>
      {children}
    </a>
  ),
}));

const T = Date.UTC(2026, 10, 20, 12);

const SEO_SCORE: ReleaseFinding = {
  code: "seo_regressed",
  service: "shop-web",
  value: 27,
  threshold: 10,
  policy: "warn",
  message: "shop-web: SEO score dropped since v1.8.1: site 94 -> 81 (-13), /products/view 96 -> 69 (-27) (limit 10 points)",
};
const A11Y_ERRORS: ReleaseFinding = {
  code: "a11y_regressed",
  service: "shop-web",
  value: 2,
  threshold: 1,
  policy: "fail",
  message: "shop-web: 2 new accessibility critical violations since v1.8.1: button-name on /cart; color-contrast on /",
};
const UNKNOWN: ReleaseFinding = { code: null, service: "shop-web", value: 3, threshold: 1, policy: "warn", message: "A newer server's finding." };

function health(findings: ReleaseFinding[], verdict: ReleaseHealth["verdict"]): ReleaseHealth {
  return {
    promotion: { promotionId: "promo-182", version: "v1.8.2", gitCommit: "abc1234", promotedAtMs: T, previousVersion: "v1.8.1" },
    before: { startMs: T - 15 * 60_000, endMs: T },
    after: { startMs: T, endMs: T + 15 * 60_000 },
    afterComplete: true,
    services: [],
    newIssues: [],
    regressedIssues: [],
    firingAlerts: [],
    workloadFindings: [],
    verdict,
    findings,
    thresholds: [],
    notEvaluated: [],
  };
}

function renderHealth(h: ReleaseHealth, { inApp = true }: { inApp?: boolean } = {}) {
  const api = { ...createFixtureApi(), getReleaseHealth: async () => h };
  renderWithApi(
    api,
    <ReleaseComparison
      scope={{ orgId: "org-fixture", projectId: inApp ? "shop" : undefined, envIds: ["env-prod"], filters: [] }}
      subject="prod"
      target={{ envId: "env-prod", promotedAtMs: T, promotionId: "promo-182" }}
      envName="prod"
      width="15m"
      navigate={() => {}}
    />
  );
}

const findingRow = (code: string) => screen.getAllByTestId("obs-compare-finding").find((li) => li.getAttribute("data-code") === code)!;

describe("site-audit findings in release health", () => {
  it("labels seo_regressed as a warning and links to the site's SEO view", async () => {
    renderHealth(health([SEO_SCORE], "pass"));
    await waitFor(() => expect(screen.getAllByTestId("obs-compare-finding")).toHaveLength(1));
    const row = findingRow("seo_regressed");
    expect(row).toHaveAttribute("data-policy", "warn");
    expect(row).toHaveTextContent("Warn");
    expect(row).toHaveTextContent("SEO regressed");
    expect(row).toHaveTextContent("−27 points vs 10");
    expect(within(row).getByTestId("obs-compare-finding-site-insights")).toHaveAttribute("href", "/forge/p/shop/site-insights/seo?site=shop-web&env=prod");
  });

  it("labels a11y_regressed, fails it when the server's policy is fail, and links to Accessibility", async () => {
    renderHealth(health([A11Y_ERRORS], "fail"));
    await waitFor(() => expect(screen.getAllByTestId("obs-compare-finding")).toHaveLength(1));
    const row = findingRow("a11y_regressed");
    expect(row).toHaveAttribute("data-policy", "fail");
    expect(row).toHaveTextContent("Fail");
    expect(row).toHaveTextContent("Accessibility regressed");
    expect(row).toHaveTextContent("2 new");
    expect(within(row).getByTestId("obs-compare-finding-site-insights")).toHaveAttribute(
      "href",
      "/forge/p/shop/site-insights/accessibility?site=shop-web&env=prod"
    );
  });

  it("renders an unknown code as before: \"Finding\", its message, and no link", async () => {
    renderHealth(health([UNKNOWN], "pass"));
    await waitFor(() => expect(screen.getAllByTestId("obs-compare-finding")).toHaveLength(1));
    const row = findingRow("");
    expect(row).toHaveTextContent("Finding");
    expect(row).toHaveTextContent("3 vs 1");
    expect(row).toHaveTextContent("A newer server's finding.");
    expect(within(row).queryByTestId("obs-compare-finding-site-insights")).not.toBeInTheDocument();
  });

  it("links nothing without the app's name", async () => {
    renderHealth(health([SEO_SCORE], "pass"), { inApp: false });
    await waitFor(() => expect(screen.getAllByTestId("obs-compare-finding")).toHaveLength(1));
    expect(screen.queryByTestId("obs-compare-finding-site-insights")).not.toBeInTheDocument();
  });
});

describe("site-audit finding helpers", () => {
  it("tells a score drop from new errors by the message", () => {
    expect(siteAuditFindingKind(SEO_SCORE)).toBe("score");
    expect(siteAuditFindingKind(A11Y_ERRORS)).toBe("new_errors");
    expect(siteAuditFindingKind(UNKNOWN)).toBeNull();
    expect(siteAuditFindingKind({ ...UNKNOWN, code: "latency" })).toBeNull();
  });

  it("figures each form in its own unit", () => {
    expect(findingFigure(SEO_SCORE)).toBe("−27 points vs 10");
    expect(findingFigure({ ...SEO_SCORE, message: "shop-web: 4 new SEO errors since v1.8.1: link_broken /sale on /", value: 4, threshold: 1 })).toBe("4 new");
  });

  it("links only site-audit codes, and leaves out an empty site", () => {
    expect(siteAuditFindingHref({ ...SEO_SCORE, service: "" }, "shop")).toBe("/forge/p/shop/site-insights/seo");
    expect(siteAuditFindingHref({ ...UNKNOWN, code: "error_rate" }, "shop")).toBeNull();
  });
});
