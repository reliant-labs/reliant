// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY: the forge shell — the same route tree (buildForgeShellTree),
 * layouts and pages the forge host mounts — against canned control-plane
 * answers, signed in as nobody (the guard is a pass-through here).
 *
 *   /forge-shell-preview?path=/apps
 *   /forge-shell-preview?path=/sites | /usage | /settings/tokens | /claim
 *   /forge-shell-preview?path=/forge/p/storefront/environments/prod
 *
 * Open it with `?shell=forge` once (remembered) so the page is in the forge
 * shell. The fake sits at fetch, as in ForgePanePreview, so every hook,
 * transport and classifier runs as in the product; console reads that are not
 * the shell's own fall through to ForgePanePreview's fixtures.
 */

import { useEffect, useMemo, useState, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { createMemoryHistory, createRouter, RouterProvider } from "@tanstack/react-router";

import { buildForgeShellTree } from "@/forgeShellRoutes";
import { ObservabilityApiProvider } from "@/services/controlPlane/observability/ObservabilityApiContext";
import { createFixtureApi } from "@/components/Forge/Observability/__fixtures__/fixtureApi";
import { forgePaneAnswer } from "@/components/Forge/__preview__/ForgePanePreview";
import { useActiveOrgStore } from "@/store/activeOrgStore";

const NOW = Date.now();
const iso = (offsetMs: number) => new Date(NOW + offsetMs).toISOString();
const DAY = 86_400_000;

const ORG = { id: "org_demo", name: "Acme Labs", slug: "acme", role: "owner", isDefault: true, isOwner: true };

/** Two apps: storefront (prod + staging, one static site each) and an API-only app. */
const ENVS = [
  { id: "env-sf-prod", name: "prod", project: "storefront", kind: "DEPLOY_ENVIRONMENT_KIND_PERSISTENT", createdAt: iso(-20 * DAY) },
  { id: "env-sf-stg", name: "staging", project: "storefront", kind: "DEPLOY_ENVIRONMENT_KIND_PERSISTENT", createdAt: iso(-20 * DAY) },
  { id: "env-blog-prod", name: "prod", project: "blog", kind: "DEPLOY_ENVIRONMENT_KIND_PERSISTENT", createdAt: iso(-9 * DAY) },
  { id: "env-api-prod", name: "prod", project: "billing-api", kind: "DEPLOY_ENVIRONMENT_KIND_PERSISTENT", createdAt: iso(-4 * DAY) },
];

function status(envId: string) {
  const site = (name: string, url: string) => ({
    deployment: { id: `dep-${envId}-${name}`, name, tier: "DEPLOY_TIER_STATIC", observed: { url, state: "DEPLOY_OBSERVED_STATE_READY" } },
    verdict: "DEPLOY_VERDICT_CONVERGED",
    observedAt: iso(-3600_000),
  });
  const api = (name: string) => ({
    deployment: { id: `dep-${envId}-${name}`, name, tier: "DEPLOY_TIER_BACKEND", observed: { url: `https://${name}.svc.test`, state: "DEPLOY_OBSERVED_STATE_READY" } },
    verdict: "DEPLOY_VERDICT_CONVERGED",
    observedAt: iso(-3600_000),
  });
  const promotion = (version: string, ago: number) => ({ id: `promo-${envId}`, releaseVersion: version, promotedByActor: "ci", createdAt: iso(-ago) });
  switch (envId) {
    case "env-sf-prod":
      return { environmentVerdict: "DEPLOY_VERDICT_CONVERGED", deployments: [site("web", "https://shop.acme.dev"), api("api")], currentPromotion: promotion("v1.4.2", 2 * 3600_000) };
    case "env-sf-stg":
      return { environmentVerdict: "DEPLOY_VERDICT_CONVERGED", deployments: [site("web", "https://web-storefront-staging.sites.reliantlabs.dev")], currentPromotion: promotion("v1.5.0-rc.1", 40 * 60_000) };
    case "env-blog-prod":
      return { environmentVerdict: "DEPLOY_VERDICT_CONVERGED", deployments: [site("site", "https://site-blog.sites.reliantlabs.dev")], currentPromotion: promotion("v0.3.0", 3 * DAY) };
    default:
      return { environmentVerdict: "DEPLOY_VERDICT_CONVERGED", deployments: [api("billing-api")], currentPromotion: promotion("v0.9.1", 5 * DAY) };
  }
}

const DOMAINS = [
  { id: "d1", hostname: "shop.acme.dev", state: "DEPLOY_CUSTOM_DOMAIN_STATE_LIVE", binding: { id: "b1", domainId: "d1", environmentId: "env-sf-prod", target: "web" } },
  { id: "d2", hostname: "www.shop.acme.dev", state: "DEPLOY_CUSTOM_DOMAIN_STATE_VERIFYING", binding: { id: "b2", domainId: "d2", environmentId: "env-sf-prod", target: "web" } },
  { id: "d3", hostname: "blog.acme.dev", state: "DEPLOY_CUSTOM_DOMAIN_STATE_PENDING_DNS", binding: { id: "b3", domainId: "d3", environmentId: "env-blog-prod", target: "site" } },
];

const CATEGORY_COST: Record<string, number> = {
  managed_llm: 42,
  machines: 18.4,
  hosted_apps: 11.2,
  databases: 6.5,
  static_hosting: 0.8,
  hosted_other: 1.9,
  observability: 3.3,
};

function usage(body: Record<string, unknown>) {
  const days = 14;
  const points = [];
  for (let d = days; d > 0; d--) {
    const bucketStart = new Date(Date.UTC(new Date(NOW).getUTCFullYear(), new Date(NOW).getUTCMonth(), new Date(NOW).getUTCDate()) - d * DAY).toISOString();
    for (const [category, monthly] of Object.entries(CATEGORY_COST)) {
      const nanos = String(Math.round(((monthly / 30) * (0.7 + ((d * 7 + category.length) % 6) / 10)) * 1e9));
      points.push({
        bucketStart,
        category,
        groupKey: body.groupBy === 2 || body.groupBy === "ORGANIZATION_USAGE_GROUP_BY_ENV" ? "env-sf-prod" : "",
        groupLabel: body.groupBy === 2 || body.groupBy === "ORGANIZATION_USAGE_GROUP_BY_ENV" ? "storefront/prod" : category,
        detail: category === "machines" ? "large" : "",
        ...(category === "managed_llm" ? { actualChargesUsdNanos: nanos } : { listEquivalentUsdNanos: nanos }),
        quantities: category === "machines" ? [{ id: "running_seconds", unit: "s", value: 20 * 3600 }] : [],
        unpricedDimensions: [],
      });
    }
  }
  return {
    orgId: ORG.id,
    currency: "USD",
    points,
    coverage: Object.keys(CATEGORY_COST).map((category) => ({ category, completeness: "complete", limitations: [] })),
    spendRates: Object.entries(CATEGORY_COST).map(([category, monthly]) => ({
      category,
      basis: category === "managed_llm" ? "actual" : "list_equivalent",
      last24hUsdNanos: String(Math.round((monthly / 30) * 1e9)),
      averageDaily7dUsdNanos: String(Math.round((monthly / 30) * 0.95 * 1e9)),
      measuredHours: 168,
      unpricedDimensions: [],
      lowerBound: false,
    })),
    inventory: [
      { category: "machines", size: "large", state: "running", count: "2", storageGib: "200" },
      { category: "machines", size: "small", state: "stopped", count: "1", storageGib: "50" },
      { category: "databases", size: "starter", state: "running", environmentId: "env-sf-prod", environmentName: "storefront/prod", count: "1", storageGib: "10" },
      { category: "managed_llm", size: "", state: "", count: "1", storageGib: "0" },
    ],
    completeness: "complete",
  };
}

function costSummary() {
  const start = new Date(Date.UTC(new Date(NOW).getUTCFullYear(), new Date(NOW).getUTCMonth(), 1));
  const end = new Date(Date.UTC(start.getUTCFullYear(), start.getUTCMonth() + 1, 1));
  return {
    orgId: ORG.id,
    currency: "USD",
    periods: [{ id: "month", start: start.toISOString(), end: end.toISOString() }],
    sources: [
      { category: "managed_llm", periodId: "month", actualChargesUsdNanos: "42000000000", allowances: [], completeness: "complete" },
      { category: "machines", periodId: "month", listEquivalentUsdNanos: "18400000000", allowances: [{ id: "machine_hours", unit: "hours", included: 100, consumed: 64 }], completeness: "complete" },
      {
        category: "hosted_infrastructure",
        periodId: "month",
        listEquivalentUsdNanos: "20400000000",
        creditsUsdNanos: "5000000000",
        allowances: [{ id: "static_egress", unit: "GiB", included: 10, consumed: 3.2 }],
        completeness: "complete",
      },
      { category: "observability", periodId: "month", listEquivalentUsdNanos: "3300000000", allowances: [{ id: "ingest", unit: "GiB", included: 5, consumed: 1.1 }], completeness: "complete" },
    ],
  };
}

const TOKENS = [
  { id: "tok_1", name: "github-actions", displayPrefix: "rlt_4f9a", scopes: ["deploy:read", "deploy:write"], createdAt: iso(-12 * DAY), lastUsedAt: iso(-2 * 3600_000), expiresAt: iso(78 * DAY), orgId: ORG.id },
  { id: "tok_2", name: "laptop", displayPrefix: "rlt_88c1", scopes: ["deploy:read", "secret:read"], createdAt: iso(-30 * DAY), orgId: ORG.id },
];

const CLAIM = {
  state: "CLAIMABLE_STATE_LIVE",
  claimable: true,
  project: "storefront",
  env: "prod",
  hostnames: ["amber-quokka.reliantpreview.dev"],
  expiresAt: iso(70 * 3600_000),
  purgeAfter: iso((70 + 168) * 3600_000),
};

function shellAnswer(path: string, body: Record<string, unknown>): unknown | undefined {
  const method = path.split("/").pop() ?? "";
  if (path.includes("OrganizationService/ListMyOrganizations")) return { organizations: [ORG], defaultOrgId: ORG.id };
  if (path.includes("DeployService/ListEnvironments")) return { environments: ENVS };
  if (path.includes("DeployService/GetStatus")) return status(String(body.environmentId ?? ""));
  if (path.includes("DomainService/ListDomains")) return { domains: DOMAINS };
  if (path.includes("BillingService/ListOrganizationUsage")) return usage(body);
  if (path.includes("BillingService/GetOrganizationCostSummary")) return costSummary();
  if (path.includes("AccessTokenService/ListMyTokens")) return { tokens: TOKENS };
  if (path.includes("AccessTokenService/ListOrgMemberGrants")) {
    return {
      members: [
        { userId: "u1", email: "ada@acme.dev", role: "owner", scopes: ["deploy:read", "deploy:write", "secret:read", "secret:write", "domain:read", "domain:write", "token:read", "token:write"] },
        { userId: "u2", email: "lin@acme.dev", role: "member", scopes: ["deploy:read", "deploy:write"] },
      ],
      grantable: ["deploy:read", "deploy:write", "secret:read", "secret:write", "domain:read", "domain:write", "token:read", "token:write"],
    };
  }
  if (path.includes("ClaimableService/GetClaimFreeSite")) return { choiceRequired: false };
  if (path.includes("ClaimableService/GetClaim")) return CLAIM;
  if (method === "GetLiveView" && body.project === "storefront") {
    return {
      environments: ENVS.filter((e) => e.project === "storefront").map((environment) => ({
        environment: { ...environment, currentPromotionId: `promo-${environment.id}` },
        currentPromotion: status(environment.id).currentPromotion,
        phase: "DEPLOY_ROLLOUT_PHASE_SUCCEEDED",
        drift: { state: "in_sync", observedAt: iso(-3600_000) },
      })),
    };
  }
  return undefined;
}

function installFakeNetwork(): () => void {
  const realFetch = window.fetch.bind(window);
  window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === "string" ? input : input instanceof URL ? input.href : input.url;
    const path = new URL(url, window.location.origin).pathname;
    let body: Record<string, unknown> = {};
    try {
      const raw = init?.body;
      const text = typeof raw === "string" ? raw : raw instanceof Uint8Array ? new TextDecoder().decode(raw) : "";
      body = text ? (JSON.parse(text) as Record<string, unknown>) : {};
    } catch {
      body = {};
    }
    const mine = shellAnswer(path, body);
    const reply = mine !== undefined ? { status: 200, json: mine } : forgePaneAnswer("registered", path, body);
    if (!reply) {
      // Anything else the preview does not model answers empty rather than
      // reaching a backend that is not there.
      if (path.includes(".v1.")) return new Response("{}", { status: 200, headers: { "content-type": "application/json" } });
      return realFetch(input, init);
    }
    await new Promise((resolve) => setTimeout(resolve, 120));
    return new Response(JSON.stringify(reply.json), { status: reply.status, headers: { "content-type": "application/json" } });
  };
  return () => {
    window.fetch = realFetch;
  };
}

function PassThrough({ children }: { children: ReactNode }) {
  return <>{children}</>;
}

export function ForgeShellPreview() {
  return <ForgeShellPreviewCase key={window.location.search} />;
}

function ForgeShellPreviewCase() {
  const path = new URLSearchParams(window.location.search).get("path") || "/apps";
  const [ready, setReady] = useState(false);

  useEffect(() => {
    const restore = installFakeNetwork();
    // No signed-in user here, so ActiveOrgBinding never settles an org: pin
    // the fixture org the way it would.
    useActiveOrgStore.setState({ effectiveId: ORG.id });
    if (path.startsWith("/claim")) {
      window.localStorage.setItem("reliant:claim", JSON.stringify({ token: "rcl_9f3KQeAbCdEfGhIjKlMnOpQrStUvWxYz0123456789", savedAt: Date.now() }));
    }
    setReady(true);
    return restore;
  }, [path]);

  const queryClient = useMemo(() => new QueryClient({ defaultOptions: { queries: { retry: false } } }), []);
  const router = useMemo(
    () => createRouter({ routeTree: buildForgeShellTree({ Guard: PassThrough }), history: createMemoryHistory({ initialEntries: [path] }) }),
    [path],
  );
  const obsApi = useMemo(() => createFixtureApi(), []);

  if (!ready) return null;
  return (
    <QueryClientProvider client={queryClient}>
      <ObservabilityApiProvider api={obsApi}>
        <RouterProvider router={router} />
      </ObservabilityApiProvider>
    </QueryClientProvider>
  );
}
