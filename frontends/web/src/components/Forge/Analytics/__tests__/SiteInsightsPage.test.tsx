// Copyright (c) 2026 Reliant Labs

/**
 * Site insights' frame: the view its path names, the tabs (which keep the
 * site), the SEO (R4) and Accessibility (R5) views now that their flags are
 * set, and the "coming soon" an audit view says before. The views' own data
 * is their models' and the shop fixture's tests'.
 */

import type React from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

const search: { site?: string } = {};
vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useSearch: () => search,
  useNavigate: () => vi.fn(),
}));
vi.mock("../SpeedInsightsPage", () => ({ SpeedInsightsView: () => <div data-testid="speed-view" /> }));
vi.mock("../SeoInsightsView", () => ({ SeoInsightsView: () => <div data-testid="seo-view" /> }));
vi.mock("../AccessibilityInsightsView", () => ({ AccessibilityInsightsView: () => <div data-testid="a11y-view" /> }));

const { SiteAuditComingSoon, SiteInsightsPage, SITE_AUDIT_VIEWS_READY, siteInsightsViewOf } = await import("../SiteInsightsPage");
const { ForgeConsoleContext } = await import("@/components/Forge/Console/consoleContextValue");
const { consolePath, withSearch } = await import("@/components/Forge/Console/paths");
type ForgeConsole = NonNullable<React.ContextType<typeof ForgeConsoleContext>>;
type Page = ForgeConsole["page"];

function renderWithQueryless(ui: React.ReactElement, site?: string, page: Page = { kind: "site-insights" }) {
  search.site = site;
  const value = { forgeProject: "shop", page, href: (target: Page, extra?: Record<string, unknown>) => withSearch(consolePath("shop", target), { env: "prod", ...extra }) } as unknown as ForgeConsole;
  return render(<ForgeConsoleContext.Provider value={value}>{ui}</ForgeConsoleContext.Provider>);
}

function renderAt(page: Page, site?: string) {
  return renderWithQueryless(<SiteInsightsPage />, site, page);
}

describe("SiteInsightsPage", () => {
  it("shows Speed at the bare path, and the old page kind is the same page", () => {
    expect(siteInsightsViewOf({ kind: "site-insights" })).toBe("speed");
    expect(siteInsightsViewOf({ kind: "site-insights", view: "accessibility" })).toBe("accessibility");
    renderAt({ kind: "site-insights" });
    expect(screen.getByTestId("console-site-insights")).toBeInTheDocument();
    expect(screen.getByTestId("speed-view")).toBeInTheDocument();
  });

  it("shows the SEO and Accessibility views now that their flags are set, under tabs that keep the site", () => {
    expect(SITE_AUDIT_VIEWS_READY).toEqual({ seo: true, accessibility: true });
    renderAt({ kind: "site-insights", view: "seo" }, "shop-web");
    expect(screen.getByTestId("seo-view")).toBeInTheDocument();
    expect(screen.queryByTestId("site-insights-seo-coming-soon")).toBeNull();
    renderAt({ kind: "site-insights", view: "accessibility" }, "shop-web");
    expect(screen.getByTestId("a11y-view")).toBeInTheDocument();
    expect(screen.queryByTestId("speed-view")).toBeNull();
  });

  it("says what an audit view will show before its page ships", () => {
    renderWithQueryless(<SiteAuditComingSoon view="accessibility" />, "shop-web");
    expect(screen.getByTestId("site-insights-accessibility-coming-soon")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "Site insights" })).toBeInTheDocument();
    expect(screen.getByTestId("site-insights-tab-accessibility")).toHaveAttribute("aria-current", "page");
    expect(screen.getByTestId("site-insights-tab-speed")).not.toHaveAttribute("aria-current");
    expect(screen.getByTestId("site-insights-tab-speed").getAttribute("href")).toBe("/forge/p/shop/site-insights?env=prod&site=shop-web");
    expect(screen.getByTestId("site-insights-tab-seo").getAttribute("href")).toBe("/forge/p/shop/site-insights/seo?env=prod&site=shop-web");
  });
});
