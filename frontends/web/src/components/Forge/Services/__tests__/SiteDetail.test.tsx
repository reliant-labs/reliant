// Copyright (c) 2026 Reliant Labs

/**
 * A static site's page, one test per section: the header (environment, URL,
 * release, what is wrong), the Site insights card, the deploys (only this
 * site's), the domains, and the hosting sections (traffic, usage and free
 * tier, configuration). The reads are stubbed at their hooks; the models
 * under them are siteModel.test.ts's and staticSiteModel.test.ts's. The
 * hosting answers are the shop pack's (fixtures/shop/handlers/staticSite.ts).
 */

import type React from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import type { ForgeDomain } from "@/services/forge/domains";
import type { ReleasePromotion } from "@/services/forge/releases";
import type { ForgeHostedWorkload } from "@/services/forge/topology";

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

const promotions: { data: ReleasePromotion[] } = { data: [] };
vi.mock("@/hooks/release-queries", () => ({
  useEnvPromotions: (ids: readonly string[]) => new Map(ids.map((id) => [id, { data: promotions.data.filter((p) => p.environmentId === id), isLoading: false, error: null }])),
}));

const orgDomains: { domains: ForgeDomain[] } = { domains: [] };
vi.mock("@/hooks/forge-domain-queries", () => ({
  useForgeDomains: () => ({ data: { availability: "available", domains: orgDomains.domains, detail: "" } }),
}));

const speed = { score: 87 as number | null, rating: "ni" as const, views: 1234, isLoading: false };
const speedCalls: unknown[] = [];
vi.mock("../../Analytics/useSiteSpeedSummary", () => ({
  SITE_SPEED_DEFAULT_RANGE: "7d",
  useSiteInsightsScope: () => null,
  useSiteSpeedSummary: (options: unknown) => {
    speedCalls.push(options);
    return { ...speed, summary: undefined, error: null, query: {} };
  },
}));

vi.mock("@/hooks/observability-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/observability-queries")>()),
  // The sites panel: the storefront reports as "shop-web".
  usePanelQuery: () => ({ data: { columns: ["views"], rows: [{ group: { service: "shop-web" }, values: [1234] }] }, isLoading: false }),
}));

vi.mock("../../Observability/charts/UPlotChart", () => ({ UPlotChart: ({ label }: { label: string }) => <figure data-testid="chart" aria-label={label} /> }));

const { shopStaticSite, shopStaticSiteTraffic } = await import("@/fixtures/shop/handlers/staticSite");
const { trafficView } = await import("../staticSiteModel");
type StaticSiteAnswer = ReturnType<typeof shopStaticSite>;
type TrafficAnswer = ReturnType<typeof shopStaticSiteTraffic>;
const hosting: { site: StaticSiteAnswer | undefined; traffic: TrafficAnswer | undefined; error: unknown; targets: unknown[]; ranges: unknown[] } = { site: undefined, traffic: undefined, error: null, targets: [], ranges: [] };
vi.mock("../useStaticSite", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../useStaticSite")>()),
  useStaticSite: (target: unknown) => {
    hosting.targets.push(target);
    return { data: hosting.site, isLoading: false, error: hosting.error };
  },
  useStaticSiteTraffic: (_target: unknown, range: unknown) => {
    hosting.ranges.push(range);
    return { data: hosting.traffic ? trafficView(hosting.traffic) : undefined, isLoading: false, error: hosting.error };
  },
}));

const { SiteDetail, FIRST_AUDIT_PENDING } = await import("../SiteDetail");
const { StaticSiteHosting, StaticSiteNotFoundMode, StaticSiteTrafficRange } = await import("@/gen/controlplane/services/static_site/v1/static_site_pb");
const { ConnectError, Code } = await import("@connectrpc/connect");
const { resetDemoNowForTests } = await import("@/fixtures/shop/shopStory");
const { ForgeConsoleContext } = await import("@/components/Forge/Console/consoleContextValue");
const { consolePath, withSearch } = await import("@/components/Forge/Console/paths");
type ForgeConsole = NonNullable<React.ContextType<typeof ForgeConsoleContext>>;
type Page = ForgeConsole["page"];

const ENVS = [
  { id: "env-prod", name: "prod", release: "v1.8.3" },
  { id: "env-staging", name: "staging", release: "v1.8.3" },
];

const deployment = (extra: Partial<ForgeHostedWorkload> = {}): ForgeHostedWorkload => ({ name: "shop-web", tier: "static", verdict: "converged", url: "https://shop-web.reliant.app", observed_state: "ready", ...extra });

const PROD = deployment({
  domains: [
    { hostname: "shop.example.com", state: "live", serving_path: "edge" },
    { hostname: "sale.shop.example.com", state: "issuing", serving_path: "edge" },
  ],
});

function promotion(id: string, environmentId: string, version: string, extra: Partial<ReleasePromotion> = {}): ReleasePromotion {
  return {
    id,
    environmentId,
    releaseVersion: version,
    fromEnvironmentName: "staging",
    promotedByUserId: "",
    promotedByActor: "jon",
    approvedBy: "",
    note: "",
    createdAt: "2026-11-25T10:00:00Z",
    targets: [],
    gates: [],
    bundleId: "",
    bundleDigest: "",
    resolvedArtifacts: { api: "sha256:a", "sites/shop-web/v1.8.3": "sha256:b" },
    ...extra,
  };
}

const setEnv = vi.fn();

function renderSite(entries: { envName: string; deployment: ForgeHostedWorkload }[], env = "all") {
  const value = {
    forgeProject: "shop",
    env,
    setEnv,
    page: { kind: "service", name: "shop-web" },
    href: (target: Page, extra?: Record<string, unknown>) => withSearch(consolePath("shop", target), { env, ...extra }),
  } as unknown as ForgeConsole;
  return render(
    <ForgeConsoleContext.Provider value={value}>
      <SiteDetail name="shop-web" entries={entries} envs={ENVS} />
    </ForgeConsoleContext.Provider>
  );
}

const GIB = 2n ** 30n;

/** The shop pack's answers for prod's storefront (its traffic over `range`). */
function shopHosting(range = StaticSiteTrafficRange.LAST_7_DAYS) {
  resetDemoNowForTests(Date.UTC(2026, 10, 26, 12, 0));
  hosting.site = shopStaticSite({ environmentId: "env-prod", frontend: "shop-web" } as never);
  hosting.traffic = shopStaticSiteTraffic({ environmentId: "env-prod", frontend: "shop-web", range } as never);
}

beforeEach(() => {
  shopHosting();
  hosting.error = null;
  hosting.targets.length = 0;
  hosting.ranges.length = 0;
  promotions.data = [];
  orgDomains.domains = [];
  speed.score = 87;
  speedCalls.length = 0;
  setEnv.mockReset();
});

describe("SiteDetail", () => {
  it("header: opens on the environment with a live custom domain, at that domain, with its release; the picker sets the scope", async () => {
    renderSite([
      { envName: "staging", deployment: deployment() },
      { envName: "prod", deployment: PROD },
    ]);
    expect(screen.getByRole("heading", { name: "shop-web" })).toBeInTheDocument();
    expect(screen.getByTestId("site-url")).toHaveAttribute("href", "https://shop.example.com");
    expect(screen.getByTestId("site-release")).toHaveTextContent("v1.8.3");
    const picker = screen.getByTestId("site-env-picker");
    expect(within(picker).getByRole("button", { name: "prod" })).toHaveAttribute("aria-pressed", "true");
    await userEvent.click(within(picker).getByRole("button", { name: "staging" }));
    expect(setEnv).toHaveBeenCalledWith("staging");
    expect(screen.queryByTestId("site-drifted")).toBeNull();
    expect(screen.queryByTestId("site-suspended")).toBeNull();
    expect(screen.queryByTestId("site-last-error")).toBeNull();
  });

  it("header: says when the site drifted, is suspended, or failed (a degraded one's last error)", () => {
    const { unmount } = renderSite([{ envName: "prod", deployment: deployment({ drifted: true, observed_digest: "sha256:aaaa", desired_digest: "sha256:bbbb" }) }], "prod");
    expect(screen.getByTestId("site-drifted")).toHaveTextContent("serving files other than the ones v1.8.3 asks for");
    expect(screen.getByText("wrong version")).toBeInTheDocument();
    expect(screen.queryByTestId("site-env-picker")).toBeNull();
    unmount();

    const suspended = renderSite([{ envName: "prod", deployment: deployment({ observed_state: "suspended", suspend_reason: "billing_lapsed", declared_run_state: "running" }) }], "prod");
    expect(screen.getByTestId("site-suspended")).toHaveTextContent("Suspended — billing");
    suspended.unmount();

    renderSite([{ envName: "prod", deployment: deployment({ verdict: "degraded", last_error: "upload to the bucket failed" }) }], "prod");
    expect(screen.getByTestId("site-last-error")).toHaveTextContent("upload to the bucket failed");
  });

  it("Site insights: this site's Real Experience Score, the audits' placeholders, and the way in", () => {
    renderSite([{ envName: "prod", deployment: PROD }], "prod");
    expect(speedCalls[0]).toMatchObject({ project: "shop", envId: "env-prod", services: ["shop-shop-web", "shop-web"] });
    const card = screen.getByTestId("site-insights-card");
    expect(within(card).getByTestId("site-insights-speed")).toHaveTextContent("87");
    expect(within(card).getByTestId("site-insights-speed")).toHaveTextContent("Real Experience Score · Needs improvement · 1.2k page loads");
    expect(within(card).getByTestId("site-insights-seo")).toHaveTextContent(FIRST_AUDIT_PENDING);
    expect(within(card).getByTestId("site-insights-accessibility")).toHaveTextContent(FIRST_AUDIT_PENDING);
    // The picker's service.name: the one that sent vitals.
    expect(within(card).getByTestId("site-insights-link")).toHaveAttribute("href", "/forge/p/shop/site-insights?env=prod&site=shop-web");
  });

  it("Site insights: no score before a measured visit", () => {
    speed.score = null;
    renderSite([{ envName: "prod", deployment: PROD }], "prod");
    expect(screen.getByTestId("site-insights-speed")).toHaveTextContent("No measured visit to shop-web in prod yet.");
  });

  it("Recent deploys: the environment's promotions that moved this site's artifact, linked to their release", () => {
    promotions.data = [
      promotion("p3", "env-prod", "v1.8.3"),
      promotion("p2", "env-prod", "v1.8.2", { resolvedArtifacts: { api: "sha256:a" } }),
      promotion("p1", "env-prod", "v1.8.1", { targets: ["api"] }),
      promotion("p0", "env-staging", "v1.8.0"),
    ];
    renderSite([{ envName: "prod", deployment: PROD }], "prod");
    const deploys = screen.getByTestId("site-deploys");
    const rows = within(deploys).getAllByTestId("recent-deploy");
    expect(rows).toHaveLength(1);
    expect(rows[0]).toHaveTextContent("v1.8.3");
    expect(rows[0]).toHaveTextContent("from staging");
    expect(within(rows[0]!).getByRole("link", { name: "v1.8.3" })).toHaveAttribute("href", "/forge/p/shop/releases/v1.8.3?env=prod");
  });

  it("Domains: each hostname with its state and where it is served, and where to add one", () => {
    renderSite([{ envName: "prod", deployment: PROD }], "prod");
    const domains = screen.getByTestId("site-domains");
    const live = within(domains).getByTestId("site-domain-shop.example.com");
    expect(within(live).getByTestId("domain-state-live")).toBeInTheDocument();
    expect(live).toHaveTextContent("Served from the edge");
    expect(within(live).getByRole("link")).toHaveAttribute("href", "https://shop.example.com");
    expect(within(within(domains).getByTestId("site-domain-sale.shop.example.com")).getByTestId("domain-state-issuing")).toBeInTheDocument();
    expect(within(domains).getByTestId("site-add-domain")).toHaveAttribute("href", "/forge/p/shop/domains?env=prod");
  });

  it("Domains: none yet says where visitors reach it", () => {
    renderSite([{ envName: "prod", deployment: deployment() }], "prod");
    expect(screen.getByTestId("site-domains")).toHaveTextContent("No custom domain serves shop-web in prod. Visitors reach it at shop-web.reliant.app.");
  });

  it("hosting: traffic, usage and free tier, then configuration, after the domains, for the site in its environment", () => {
    renderSite([{ envName: "prod", deployment: PROD }], "prod");
    const order = ["site-domains", "site-traffic", "site-usage", "site-configuration"].map((id) => screen.getByTestId(id));
    for (let i = 1; i < order.length; i += 1) expect(order[i - 1]!.compareDocumentPosition(order[i]!) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(hosting.targets[0]).toEqual({ envId: "env-prod", frontend: "shop-web" });
  });

  it("Traffic: requests, the hit ratio (with how it is counted), bandwidth and error rates; the range picker re-reads", async () => {
    renderSite([{ envName: "prod", deployment: PROD }], "prod");
    const traffic = screen.getByTestId("site-traffic");
    expect(within(traffic).getByTestId("site-traffic-requests")).toHaveTextContent(/^Requests(39|4\d)(\.\d)?k/);
    expect(within(traffic).getByTestId("site-traffic-hit-ratio")).toHaveTextContent("93%");
    expect(within(traffic).getByTestId("site-traffic-hit-ratio-note")).toHaveAccessibleName("How the hit ratio is counted");
    expect(within(traffic).getByTestId("site-traffic-errors")).toHaveTextContent("4xx");
    expect(within(traffic).queryByTestId("site-traffic-history")).toBeNull();
    expect(hosting.ranges.at(-1)).toBe("7d");
    await userEvent.click(within(screen.getByTestId("site-traffic-range")).getByRole("button", { name: "30d" }));
    expect(hosting.ranges.at(-1)).toBe("30d");
  });

  it("Traffic: a range the free plan's history clamps says so, with the way to more", () => {
    shopHosting(StaticSiteTrafficRange.LAST_30_DAYS);
    renderSite([{ envName: "prod", deployment: PROD }], "prod");
    const note = screen.getByTestId("site-traffic-history");
    expect(note).toHaveTextContent("Your plan keeps 7 days of traffic history");
    expect(within(note).getByTestId("site-traffic-upgrade")).toHaveAttribute("href", "/settings/billing?tab=hosting");
  });

  it("Traffic: an older control plane says when the metrics arrive", () => {
    hosting.traffic = undefined;
    hosting.error = new ConnectError("unimplemented", Code.Unimplemented);
    renderSite([{ envName: "prod", deployment: PROD }], "prod");
    expect(screen.getByTestId("site-traffic-unavailable")).toHaveTextContent("Traffic metrics arrive with the next control-plane release.");
  });

  it("Usage: under the free caps, no billing link", () => {
    renderSite([{ envName: "prod", deployment: PROD }], "prod");
    const usage = screen.getByTestId("site-usage");
    expect(within(usage).getByTestId("site-usage-site-storage")).toHaveTextContent("180 MiB of 1 GiB");
    expect(within(usage).getByTestId("site-usage-org-transfer")).toHaveTextContent("2.1 GiB of 10 GiB");
    expect(within(usage).getByTestId("site-usage-org-transfer")).toHaveTextContent("This site: 1.9 GiB.");
    expect(within(usage).getByTestId("site-usage-site-storage")).toHaveAttribute("data-state", "under");
    expect(within(usage).queryByTestId("site-usage-cap")).toBeNull();
  });

  it("Usage: near a cap warns; over it links billing", () => {
    hosting.site!.usage!.orgTransferBytes = 9n * GIB;
    const near = renderSite([{ envName: "prod", deployment: PROD }], "prod");
    expect(screen.getByTestId("site-usage-org-transfer")).toHaveAttribute("data-state", "near");
    expect(screen.getByTestId("site-usage-cap")).toHaveTextContent("Close to the free allowance.");
    expect(screen.queryByTestId("site-usage-billing")).toBeNull();
    near.unmount();

    hosting.site!.usage!.orgTransferBytes = 11n * GIB;
    renderSite([{ envName: "prod", deployment: PROD }], "prod");
    expect(screen.getByTestId("site-usage-org-transfer")).toHaveAttribute("data-state", "over");
    expect(screen.getByTestId("site-usage-cap")).toHaveTextContent("Over the free allowance.");
    expect(screen.getByTestId("site-usage-billing")).toHaveAttribute("href", "/settings/billing?tab=hosting");
  });

  it("Usage: paid hosting shows the figures with no free cap", () => {
    hosting.site!.usage = { ...hosting.site!.usage!, hosting: StaticSiteHosting.PAID, siteStorageLimitBytes: 0n, orgStorageLimitBytes: 0n, orgTransferLimitBytes: 0n };
    renderSite([{ envName: "prod", deployment: PROD }], "prod");
    const usage = screen.getByTestId("site-usage");
    expect(usage).toHaveTextContent("no free caps");
    expect(within(usage).queryAllByRole("progressbar")).toHaveLength(0);
    expect(usage).not.toHaveTextContent("of 1 GiB");
  });

  it("Configuration: runtime config (public), base path, asset directory, a real 404.html, and the rule counts", () => {
    renderSite([{ envName: "prod", deployment: PROD }], "prod");
    const config = screen.getByTestId("site-configuration");
    expect(within(config).getByTestId("site-config-runtime")).toHaveTextContent("API_URLURL of api");
    expect(within(config).getByTestId("site-config-runtime")).toHaveTextContent("SITE_NAME= Shop");
    expect(within(config).getByTestId("site-config-runtime")).toHaveTextContent("config.js is served to every visitor");
    expect(within(config).getByTestId("site-config-base-path")).toHaveTextContent("/ (the root)");
    expect(within(config).getByTestId("site-config-asset-dir")).toHaveTextContent("assets");
    expect(within(config).getByTestId("site-config-not-found")).toHaveTextContent("404.html: real 404s");
    expect(within(config).getByTestId("site-config-headers")).toHaveTextContent("3 rules");
    expect(within(config).getByTestId("site-config-redirects")).toHaveTextContent("2 rules");
  });

  it("Configuration: a build without 404.html explains the SPA fallback", () => {
    hosting.site!.configuration!.notFoundMode = StaticSiteNotFoundMode.SPA_FALLBACK;
    hosting.site!.configuration!.headersRules = undefined;
    renderSite([{ envName: "prod", deployment: PROD }], "prod");
    const notFound = screen.getByTestId("site-config-not-found");
    expect(notFound).toHaveTextContent("SPA fallback: unknown paths serve index.html with 200");
    expect(notFound).toHaveTextContent("A missing asset is still a 404.");
    expect(notFound).not.toHaveTextContent("404.html: real 404s");
    expect(screen.getByTestId("site-config-headers")).toHaveTextContent("Not read");
  });
});
