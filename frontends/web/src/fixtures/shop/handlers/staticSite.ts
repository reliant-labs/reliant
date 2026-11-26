// Copyright (c) 2026 Reliant Labs

/**
 * The shop pack's static site hosting: the storefront (`shop-web`, a hosted
 * static site in every environment) as controlplane.v1.StaticSiteService
 * answers it — the static site page's Traffic, Usage and free tier, and
 * Configuration sections (Forge/Services/SiteHosting.tsx read them).
 *
 * THE NUMBERS (design D7). The org holds the free static hosting allowance:
 *
 *   prod      about 40k requests a week at a 93% cache hit ratio; 180 MiB
 *             stored (the live release and the retained ones) of 1 GiB
 *   the org   2.1 GiB of 10 GiB transfer this month (prod's 1.9 GiB, the
 *             rest staging's and dev's), 236 MiB of 10 GiB stored
 *
 * Traffic keeps the free plan's 7 days of history, so a 30-day read is
 * clamped (history_clamped) and the page shows the upgrade note. The build
 * ships a 404.html (the SEO story's /sale answers a real 404), declares three
 * _headers rules and two _redirects, and resolves API_URL to the api's URL.
 */

import { create } from "@bufbuild/protobuf";
import type { MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { DeployCustomDomainState } from "@/gen/controlplane/controlplane/v1/deploy_pb";
import {
  GetStaticSiteResponseSchema,
  GetStaticSiteTrafficResponseSchema,
  StaticSiteHosting,
  StaticSiteHostnameSource,
  StaticSiteNotFoundMode,
  StaticSiteService,
  StaticSiteTrafficRange,
  type GetStaticSiteRequest,
  type GetStaticSiteTrafficRequest,
} from "@/gen/controlplane/services/static_site/v1/static_site_pb";

import { DAY, DEMO_FRONTEND, HOUR, MINUTE, RUNNING, demoNow } from "../shopStory";
import { artifactDigest, DEMO_DOMAINS } from "./console";
import { completed } from "./defaults";
import { DEMO_SCOPE, demoEnvId, type DemoEnvId, type PackScope } from "./scope";
import { ts } from "./wire";

const MIB = 2 ** 20;
const GIB = 2 ** 30;

/** The free allowance's caps (control-plane internal/freehosting). */
export const FREE_CAPS = { siteStorage: GIB, orgStorage: 10 * GIB, orgTransfer: 10 * GIB } as const;

/** Each environment's share of the storefront's traffic and storage. */
const ENV_STORY: Record<DemoEnvId, { weeklyRequests: number; storage: number; transferThisMonth: number }> = {
  "env-prod": { weeklyRequests: 40_000, storage: 180 * MIB, transferThisMonth: 1.9 * GIB },
  "env-staging": { weeklyRequests: 1_400, storage: 38 * MIB, transferThisMonth: 0.15 * GIB },
  "env-dev": { weeklyRequests: 420, storage: 18 * MIB, transferThisMonth: 0.05 * GIB },
};

/** The org's totals: every environment's storefront, the org's one static site. */
export const ORG_USAGE = {
  storage: Object.values(ENV_STORY).reduce((sum, e) => sum + e.storage, 0),
  transfer: Object.values(ENV_STORY).reduce((sum, e) => sum + e.transferThisMonth, 0),
};

/** The cache hit ratio the story's traffic holds to, and its error rates. */
export const TRAFFIC_STORY = { hitRatio: 0.93, rate4xx: 0.011, rate5xx: 0.002 } as const;

/** Bytes per response: an edge hit (logged with headers), an origin answer (body only). */
const HIT_BYTES = 14_200;
const MISS_BYTES = 9_600;

const HOSTS: Record<DemoEnvId, string> = { "env-prod": "shop-web.reliant.app", "env-staging": "shop-web-staging.reliant.app", "env-dev": "shop-web-dev.reliant.app" };

/** The next UTC month's start: when the transfer period resets. */
function periodEnd(now: number): number {
  const d = new Date(now);
  return Date.UTC(d.getUTCFullYear(), d.getUTCMonth() + 1, 1);
}

/** The shop pack's GetStaticSite answer (exported for the page's tests). */
export function shopStaticSite(req: GetStaticSiteRequest) {
  const env = demoEnvId(req.environmentId);
  if (!env || req.frontend !== DEMO_FRONTEND) return create(GetStaticSiteResponseSchema);
  const story = ENV_STORY[env];
  const running = RUNNING[env];
  const now = demoNow();
  const digest = artifactDigest(DEMO_FRONTEND, running.version);
  const custom = DEMO_DOMAINS.filter((d) => d.env === env && d.target === DEMO_FRONTEND)
    .map((d) => ({ hostname: d.hostname, source: StaticSiteHostnameSource.CUSTOM, domainState: d.state as DeployCustomDomainState }))
    .sort((a, b) => (a.hostname < b.hostname ? -1 : 1));
  return create(GetStaticSiteResponseSchema, {
    environmentId: req.environmentId,
    frontend: DEMO_FRONTEND,
    deploymentId: `dep-${env.replace("env-", "")}-${DEMO_FRONTEND}`,
    hostnames: [{ hostname: HOSTS[env], source: StaticSiteHostnameSource.PLATFORM }, ...custom],
    release: { version: running.version, digest, servedDigest: digest, promotedAt: ts(now - running.age) },
    usage: {
      hosting: StaticSiteHosting.FREE,
      storageBytes: BigInt(story.storage),
      siteStorageLimitBytes: BigInt(FREE_CAPS.siteStorage),
      orgStorageBytes: BigInt(ORG_USAGE.storage),
      orgStorageLimitBytes: BigInt(FREE_CAPS.orgStorage),
      orgStorageMeasured: true,
      orgTransferBytes: BigInt(Math.round(ORG_USAGE.transfer)),
      orgTransferLimitBytes: BigInt(FREE_CAPS.orgTransfer),
      periodEnd: ts(periodEnd(now)),
      siteTransferBytes: BigInt(Math.round(story.transferThisMonth)),
    },
    configuration: {
      runtimeConfig: { SITE_NAME: "Shop", ANALYTICS_SAMPLE_RATE: "0.25", CHECKOUT_ENABLED: "true" },
      runtimeConfigWorkloadUrls: { API_URL: "api" },
      basePath: "",
      assetDir: "assets",
      notFoundMode: StaticSiteNotFoundMode.NOT_FOUND_PAGE,
      headersRules: 3,
      redirectRules: 2,
    },
  });
}

/** How far each range reads, and its step (control-plane statictraffic.StepFor of the span asked for). */
const RANGE: Record<Exclude<StaticSiteTrafficRange, StaticSiteTrafficRange.UNSPECIFIED>, { span: number; step: number }> = {
  [StaticSiteTrafficRange.LAST_24_HOURS]: { span: DAY, step: HOUR },
  [StaticSiteTrafficRange.LAST_7_DAYS]: { span: 7 * DAY, step: 3 * HOUR },
  [StaticSiteTrafficRange.LAST_30_DAYS]: { span: 30 * DAY, step: 6 * HOUR },
};

/** The free plan's traffic history. */
const HISTORY_DAYS = 7;

/** Requests per hour at `t`: busiest in the European afternoon, quietest before dawn. */
function hourlyRate(weekly: number, t: number): number {
  const hour = new Date(t).getUTCHours() + new Date(t).getUTCMinutes() / 60;
  return (weekly / (7 * 24)) * (1 + 0.55 * Math.sin(((hour - 9) / 24) * 2 * Math.PI));
}

function counts(requests: number, wobble: number) {
  const hitRatio = Math.min(0.99, TRAFFIC_STORY.hitRatio + 0.015 * wobble);
  const cacheHits = Math.round(requests * hitRatio);
  const cacheMisses = requests - cacheHits;
  const requests4xx = Math.round(requests * TRAFFIC_STORY.rate4xx);
  const requests5xx = Math.round(requests * TRAFFIC_STORY.rate5xx);
  const hitBytes = cacheHits * HIT_BYTES;
  const missBytes = cacheMisses * MISS_BYTES;
  const of = (n: number) => (requests > 0 ? n / requests : undefined);
  return {
    requests: BigInt(requests),
    cacheHits: BigInt(cacheHits),
    cacheMisses: BigInt(cacheMisses),
    cacheHitRatio: of(cacheHits),
    bytes: BigInt(hitBytes + missBytes),
    hitBytes: BigInt(hitBytes),
    missBytes: BigInt(missBytes),
    requests4xx: BigInt(requests4xx),
    requests5xx: BigInt(requests5xx),
    rate4xx: of(requests4xx),
    rate5xx: of(requests5xx),
  };
}

/** The shop pack's GetStaticSiteTraffic answer: a series of `range`, clamped to the free plan's history. */
export function shopStaticSiteTraffic(req: GetStaticSiteTrafficRequest) {
  const env = demoEnvId(req.environmentId);
  if (!env || req.frontend !== DEMO_FRONTEND) return create(GetStaticSiteTrafficResponseSchema, { environmentId: req.environmentId, frontend: req.frontend, historyDays: HISTORY_DAYS });
  const { span, step } = RANGE[req.range === StaticSiteTrafficRange.UNSPECIFIED ? StaticSiteTrafficRange.LAST_24_HOURS : req.range];
  const end = Math.floor(demoNow() / MINUTE) * MINUTE + MINUTE;
  const asked = end - span;
  const start = Math.max(asked, end - HISTORY_DAYS * DAY);
  const weekly = ENV_STORY[env].weeklyRequests;
  const points = [];
  let requests = 0;
  let hits = 0;
  let r4xx = 0;
  let r5xx = 0;
  let hitBytes = 0;
  let missBytes = 0;
  for (let t = Math.floor(start / step) * step, i = 0; t < end; t += step, i += 1) {
    const n = Math.round(hourlyRate(weekly, t + step / 2) * (step / HOUR));
    const c = counts(n, Math.sin(i * 0.35));
    points.push({ start: ts(t), counts: c });
    requests += n;
    hits += Number(c.cacheHits);
    r4xx += Number(c.requests4xx);
    r5xx += Number(c.requests5xx);
    hitBytes += Number(c.hitBytes);
    missBytes += Number(c.missBytes);
  }
  const of = (n: number) => (requests > 0 ? n / requests : undefined);
  return create(GetStaticSiteTrafficResponseSchema, {
    environmentId: req.environmentId,
    frontend: DEMO_FRONTEND,
    start: ts(start),
    end: ts(end),
    stepSeconds: BigInt(step / 1000),
    points,
    totals: {
      requests: BigInt(requests),
      cacheHits: BigInt(hits),
      cacheMisses: BigInt(requests - hits),
      cacheHitRatio: of(hits),
      bytes: BigInt(hitBytes + missBytes),
      hitBytes: BigInt(hitBytes),
      missBytes: BigInt(missBytes),
      requests4xx: BigInt(r4xx),
      requests5xx: BigInt(r5xx),
      rate4xx: of(r4xx),
      rate5xx: of(r5xx),
    },
    historyDays: HISTORY_DAYS,
    historyClamped: start > asked,
  });
}

// The scope argument keeps the pack's handler signature: the answers carry no project, and an
// id prefix or a foreign org is handled outside (handlers/scope.ts, handlers/idPrefix.ts).
export function staticSiteScenarioHandlers(_scope: PackScope = DEMO_SCOPE): MockHandlers {
  return completed(StaticSiteService, {
    [`${StaticSiteService.typeName}/GetStaticSite`]: (req: GetStaticSiteRequest) => shopStaticSite(req),
    [`${StaticSiteService.typeName}/GetStaticSiteTraffic`]: (req: GetStaticSiteTrafficRequest) => shopStaticSiteTraffic(req),
  });
}
