// Copyright (c) 2026 Reliant Labs

/**
 * A static site's page, one test per section: the header (environment, URL,
 * release, what is wrong), the Site insights card, the deploys (only this
 * site's) and the domains. The reads are stubbed at their hooks; the model
 * under them is siteModel.test.ts's.
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

// The audits' read (GetSiteAudit), answered by the shop pack's story (v1.8.2 audited: SEO 81, accessibility 88,
// down from 94 and 97 on v1.8.1), by a site with no finished audit yet, or still loading.
const audit = { mode: "story" as "story" | "none" | "loading" };
const auditReads: unknown[] = [];
vi.mock("@/hooks/controlplane/site-audit-service-hooks_gen", async (importOriginal) => {
  const { create } = await import("@bufbuild/protobuf");
  const { GetSiteAuditRequestSchema, GetSiteAuditResponseSchema } = await import("@/gen/controlplane/services/siteaudit/v1/siteaudit_pb");
  const { shopSiteAudit } = await import("@/fixtures/shop/handlers/siteAudit");
  type Req = Parameters<typeof shopSiteAudit>[0];
  type Res = ReturnType<typeof shopSiteAudit>;
  return {
    ...(await importOriginal<typeof import("@/hooks/controlplane/site-audit-service-hooks_gen")>()),
    useGetSiteAudit: (req: unknown, options: { enabled?: boolean; select: (res: Res) => unknown }) => {
      if (!options.enabled) return { data: undefined, isLoading: false, error: null };
      auditReads.push(req);
      if (audit.mode === "loading") return { data: undefined, isLoading: true, error: null };
      const res = audit.mode === "story" ? shopSiteAudit(create(GetSiteAuditRequestSchema, req as Req)) : create(GetSiteAuditResponseSchema, {});
      return { data: options.select(res), isLoading: false, error: null };
    },
  };
});

vi.mock("@/hooks/observability-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/observability-queries")>()),
  useObservabilityOrg: () => ({ orgId: "org-shop", isLoading: false, error: null, refetch: () => undefined }),
  // The sites panel: the storefront reports as "shop-web".
  usePanelQuery: () => ({ data: { columns: ["views"], rows: [{ group: { service: "shop-web" }, values: [1234] }] }, isLoading: false }),
}));

const { SiteDetail } = await import("../SiteDetail");
const { FIRST_AUDIT_PENDING } = await import("../../Analytics/siteAuditParts");
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

beforeEach(() => {
  promotions.data = [];
  orgDomains.domains = [];
  speed.score = 87;
  speedCalls.length = 0;
  audit.mode = "story";
  auditReads.length = 0;
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

  it("Site insights: this site's Real Experience Score, its audit scores, and the way in", () => {
    renderSite([{ envName: "prod", deployment: PROD }], "prod");
    expect(speedCalls[0]).toMatchObject({ project: "shop", envId: "env-prod", services: ["shop-shop-web", "shop-web"] });
    const card = screen.getByTestId("site-insights-card");
    expect(within(card).getByTestId("site-insights-speed")).toHaveTextContent("87");
    expect(within(card).getByTestId("site-insights-speed")).toHaveTextContent("Real Experience Score · Needs improvement · 1.2k page loads");
    expect(within(card).getByTestId("site-insights-seo")).toHaveTextContent("SEO81−13 since v1.8.1");
    expect(within(card).getByTestId("site-insights-accessibility")).toHaveTextContent("Accessibility88−9 since v1.8.1");
    // The picker's service.name: the one that sent vitals.
    expect(within(card).getByTestId("site-insights-link")).toHaveAttribute("href", "/forge/p/shop/site-insights?env=prod&site=shop-web");
  });

  it("Site insights: the audit read names this site in the shown environment, and a score below 90 is rated", () => {
    renderSite([{ envName: "prod", deployment: PROD }], "prod");
    expect(auditReads).toContainEqual({ selector: { case: "latest", value: { environmentId: "env-prod", frontend: "shop-web" } }, orgId: "org-shop" });
    expect(within(screen.getByTestId("site-audit-summary-seo")).getByText("81")).toHaveClass("text-warning-ink");
    expect(within(screen.getByTestId("site-audit-summary-accessibility")).getByText("88")).toHaveClass("text-warning-ink");
  });

  it("Site insights: before the first audit, SEO and accessibility say when it runs", () => {
    audit.mode = "none";
    renderSite([{ envName: "prod", deployment: PROD }], "prod");
    expect(screen.getByTestId("site-insights-seo")).toHaveTextContent(`SEO—${FIRST_AUDIT_PENDING}`);
    expect(screen.getByTestId("site-insights-accessibility")).toHaveTextContent(`Accessibility—${FIRST_AUDIT_PENDING}`);
  });

  it("Site insights: a site in an environment the console does not list reads no audit", () => {
    renderSite([{ envName: "preview", deployment: PROD }], "preview");
    expect(auditReads).toEqual([]);
    expect(screen.getByTestId("site-insights-seo")).toHaveTextContent(FIRST_AUDIT_PENDING);
    expect(screen.getByTestId("site-insights-accessibility")).toHaveTextContent(FIRST_AUDIT_PENDING);
  });

  it("Site insights: the audit scores load in place", () => {
    audit.mode = "loading";
    renderSite([{ envName: "prod", deployment: PROD }], "prod");
    expect(within(screen.getByTestId("site-insights-seo")).getByLabelText("Loading the audit score")).toBeInTheDocument();
    expect(within(screen.getByTestId("site-insights-accessibility")).getByLabelText("Loading the audit score")).toBeInTheDocument();
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
});
