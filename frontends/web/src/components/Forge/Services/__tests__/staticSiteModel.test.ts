// Copyright (c) 2026 Reliant Labs

/**
 * staticSiteModel: the meters against the free caps (under, near and over),
 * paid hosting showing no free cap, the 404-mode copy (SPA fallback against a
 * real 404.html), the runtime config list, and the traffic series.
 */

import { create } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";
import { describe, expect, it } from "vitest";

import {
  GetStaticSiteTrafficResponseSchema,
  StaticSiteConfigurationSchema,
  StaticSiteHosting,
  StaticSiteNotFoundMode,
  StaticSiteUsageSchema,
} from "@/gen/controlplane/services/static_site/v1/static_site_pb";

import {
  capNote,
  formatBinaryBytes,
  historyNote,
  HOSTING_BILLING_HREF,
  meterState,
  NEAR_CAP,
  notFoundModeCopy,
  ruleCount,
  runtimeConfigEntries,
  trafficView,
  usageView,
} from "../staticSiteModel";

const MIB = 2 ** 20;
const GIB = 2 ** 30;

const freeUsage = (over: Partial<{ storage: number; org: number; transfer: number; site: number }> = {}) =>
  create(StaticSiteUsageSchema, {
    hosting: StaticSiteHosting.FREE,
    storageBytes: BigInt(over.storage ?? 180 * MIB),
    siteStorageLimitBytes: BigInt(GIB),
    orgStorageBytes: BigInt(over.org ?? 236 * MIB),
    orgStorageLimitBytes: BigInt(10 * GIB),
    orgStorageMeasured: true,
    orgTransferBytes: BigInt(over.transfer ?? Math.round(2.1 * GIB)),
    orgTransferLimitBytes: BigInt(10 * GIB),
    periodEnd: timestampFromMs(Date.UTC(2026, 11, 1)),
    siteTransferBytes: BigInt(over.site ?? Math.round(1.9 * GIB)),
  });

describe("meterState", () => {
  it("is under below the near line, near from it, over at and past the cap", () => {
    expect(meterState(0, 100)).toBe("under");
    expect(meterState(79, 100)).toBe("under");
    expect(meterState(NEAR_CAP * 100, 100)).toBe("near");
    expect(meterState(99.9, 100)).toBe("near");
    expect(meterState(100, 100)).toBe("over");
    expect(meterState(140, 100)).toBe("over");
  });

  it("is uncapped with no cap", () => {
    expect(meterState(5 * GIB, 0)).toBe("uncapped");
  });
});

describe("usageView", () => {
  it("draws the free caps, this site's share of the transfer, and the reset", () => {
    const view = usageView(freeUsage());
    expect(view.meters.map((m) => [m.id, m.usedLabel, m.limitLabel, m.state])).toEqual([
      ["site-storage", "180 MiB", "of 1 GiB", "under"],
      ["org-storage", "236 MiB", "of 10 GiB", "under"],
      ["org-transfer", "2.1 GiB", "of 10 GiB", "under"],
    ]);
    expect(view.meters[2]!.note).toMatch(/^This site: 1\.9 GiB\. Resets (1 December|December 1)\.$/);
    expect(view.billing).toBe(false);
    expect(capNote(view)).toBeNull();
  });

  it("warns near a cap, without the billing link", () => {
    const view = usageView(freeUsage({ storage: 900 * MIB }));
    expect(view.meters[0]!.state).toBe("near");
    expect(view.worst).toBe("near");
    expect(view.billing).toBe(false);
    expect(capNote(view)).toBe("Close to the free allowance.");
  });

  it("links billing when capped", () => {
    const view = usageView(freeUsage({ transfer: 10 * GIB }));
    expect(view.meters[2]!.state).toBe("over");
    expect(view.billing).toBe(true);
    expect(capNote(view)).toMatch(/needs billing/);
    expect(HOSTING_BILLING_HREF).toBe("/settings/billing?tab=hosting");
  });

  it("shows no free cap under paid hosting", () => {
    const view = usageView(create(StaticSiteUsageSchema, { hosting: StaticSiteHosting.PAID, storageBytes: BigInt(3 * GIB), orgStorageBytes: BigInt(40 * GIB), orgStorageMeasured: true, orgTransferBytes: BigInt(80 * GIB) }));
    expect(view.meters.every((m) => m.limit === 0 && m.state === "uncapped" && m.limitLabel === "")).toBe(true);
    expect(view.summary).toMatch(/no free caps/);
    expect(view.billing).toBe(false);
    expect(capNote(view)).toBeNull();
  });

  it("asks for billing when nothing covers the org's hosting", () => {
    const view = usageView(create(StaticSiteUsageSchema, { hosting: StaticSiteHosting.NOT_COVERED, orgStorageMeasured: true }));
    expect(view.billing).toBe(true);
    expect(capNote(view)).toMatch(/billing/);
  });

  it("says what it does not know: an unmeasured site, an org figure that is a lower bound", () => {
    const usage = freeUsage();
    usage.storageBytes = undefined;
    usage.orgStorageMeasured = false;
    usage.siteTransferBytes = undefined;
    const view = usageView(usage);
    expect(view.meters[0]!.usedLabel).toBe("Not measured yet");
    expect(view.meters[1]!.usedLabel).toBe("at least 236 MiB");
    expect(view.meters[2]!.note).toMatch(/^Resets (1 December|December 1)\.$/);
  });
});

describe("the 404 mode", () => {
  it("names a real 404.html and the SPA fallback for what a visitor gets", () => {
    expect(notFoundModeCopy(StaticSiteNotFoundMode.NOT_FOUND_PAGE).label).toBe("404.html: real 404s");
    const spa = notFoundModeCopy(StaticSiteNotFoundMode.SPA_FALLBACK);
    expect(spa.label).toBe("SPA fallback: unknown paths serve index.html with 200");
    expect(spa.explainer).toMatch(/missing asset is still a 404/);
    expect(notFoundModeCopy(StaticSiteNotFoundMode.UNSPECIFIED).label).toBe("Unknown");
  });
});

describe("configuration", () => {
  it("lists literal values and workload URLs together, by key", () => {
    const config = create(StaticSiteConfigurationSchema, { runtimeConfig: { SITE_NAME: "Shop", ANALYTICS: "on" }, runtimeConfigWorkloadUrls: { API_URL: "api" } });
    expect(runtimeConfigEntries(config)).toEqual([
      { key: "ANALYTICS", value: "on", workloadUrl: false },
      { key: "API_URL", value: "api", workloadUrl: true },
      { key: "SITE_NAME", value: "Shop", workloadUrl: false },
    ]);
  });

  it("counts rules, and says when the build could not be read", () => {
    expect(ruleCount(undefined, "rule")).toBe("Not read");
    expect(ruleCount(0, "rule")).toBe("None");
    expect(ruleCount(1, "rule")).toBe("1 rule");
    expect(ruleCount(3, "rule")).toBe("3 rules");
  });

  it("formats bytes in binary units", () => {
    expect(formatBinaryBytes(180 * MIB)).toBe("180 MiB");
    expect(formatBinaryBytes(2.1 * GIB)).toBe("2.1 GiB");
    expect(formatBinaryBytes(512)).toBe("512 B");
  });
});

describe("trafficView", () => {
  const point = (ms: number, requests: number, hits: number) => ({
    start: timestampFromMs(ms),
    counts: { requests: BigInt(requests), cacheHits: BigInt(hits), cacheMisses: BigInt(requests - hits), cacheHitRatio: requests ? hits / requests : undefined, bytes: BigInt(requests * 10) },
  });

  it("turns points into series, a step with no request a gap in the ratio", () => {
    const res = create(GetStaticSiteTrafficResponseSchema, {
      points: [point(Date.UTC(2026, 10, 26, 0), 100, 90), point(Date.UTC(2026, 10, 26, 1), 0, 0)],
      totals: { requests: 100n, cacheHits: 90n, cacheMisses: 10n, cacheHitRatio: 0.9, bytes: 1000n, requests4xx: 2n, rate4xx: 0.02 },
      historyDays: 7,
    });
    const view = trafficView(res);
    expect(view.series.timestamps).toEqual([Date.UTC(2026, 10, 26, 0) / 1000, Date.UTC(2026, 10, 26, 1) / 1000]);
    expect(view.series.requests).toEqual([100, 0]);
    expect(view.series.hitRatio).toEqual([0.9, null]);
    expect(view.totals).toMatchObject({ requests: 100, hitRatio: 0.9, bytes: 1000, rate4xx: 0.02, rate5xx: 0 });
    expect(view.empty).toBe(false);
    expect(historyNote(view)).toBeNull();
  });

  it("notes a range the plan's history clamped", () => {
    const view = trafficView(create(GetStaticSiteTrafficResponseSchema, { historyDays: 7, historyClamped: true }));
    expect(view.empty).toBe(true);
    expect(historyNote(view)).toMatch(/keeps 7 days .* Paid hosting keeps 30\./);
  });
});
