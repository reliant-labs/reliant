// Copyright (c) 2026 Reliant Labs

/**
 * Site insights' frame: the view its path names, the tabs (which keep the
 * site), and the audit views' "coming soon" until their flag flips. The Speed
 * view's data is speedInsightsModel's and the shop fixture's tests'.
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

const { SiteInsightsPage, SITE_AUDIT_VIEWS_READY, siteInsightsViewOf } = await import("../SiteInsightsPage");
const { ForgeConsoleContext } = await import("@/components/Forge/Console/consoleContextValue");
const { consolePath, withSearch } = await import("@/components/Forge/Console/paths");
type ForgeConsole = NonNullable<React.ContextType<typeof ForgeConsoleContext>>;
type Page = ForgeConsole["page"];

function renderAt(page: Page, site?: string) {
  search.site = site;
  const value = { forgeProject: "shop", page, href: (target: Page, extra?: Record<string, unknown>) => withSearch(consolePath("shop", target), { env: "prod", ...extra }) } as unknown as ForgeConsole;
  return render(
    <ForgeConsoleContext.Provider value={value}>
      <SiteInsightsPage />
    </ForgeConsoleContext.Provider>,
  );
}

describe("SiteInsightsPage", () => {
  it("shows Speed at the bare path, and the old page kind is the same page", () => {
    expect(siteInsightsViewOf({ kind: "site-insights" })).toBe("speed");
    expect(siteInsightsViewOf({ kind: "site-insights", view: "accessibility" })).toBe("accessibility");
    renderAt({ kind: "site-insights" });
    expect(screen.getByTestId("console-site-insights")).toBeInTheDocument();
    expect(screen.getByTestId("speed-view")).toBeInTheDocument();
  });

  it("says what SEO and Accessibility will show until their flag flips, under tabs that keep the site", () => {
    expect(SITE_AUDIT_VIEWS_READY).toEqual({ seo: false, accessibility: false });
    renderAt({ kind: "site-insights", view: "seo" }, "shop-web");
    expect(screen.getByTestId("site-insights-seo-coming-soon")).toBeInTheDocument();
    expect(screen.queryByTestId("speed-view")).toBeNull();
    expect(screen.getByRole("heading", { name: "Site insights" })).toBeInTheDocument();
    expect(screen.getByTestId("site-insights-tab-seo")).toHaveAttribute("aria-current", "page");
    expect(screen.getByTestId("site-insights-tab-speed")).not.toHaveAttribute("aria-current");
    expect(screen.getByTestId("site-insights-tab-speed").getAttribute("href")).toBe("/forge/p/shop/site-insights?env=prod&site=shop-web");
    expect(screen.getByTestId("site-insights-tab-accessibility").getAttribute("href")).toBe("/forge/p/shop/site-insights/accessibility?env=prod&site=shop-web");
  });
});
