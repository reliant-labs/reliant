// Copyright (c) 2026 Reliant Labs
//
// Typed ListOrganizationUsage responses (cp#1602's shape) for the usage,
// billing and LLM views. Test data only: never rendered by a product route.

import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

import {
  ListOrganizationUsageResponseSchema,
  OrganizationUsageGranularity,
  OrganizationUsageGroupBy,
  type ListOrganizationUsageResponse,
} from "@/gen/controlplane/services/billing/v1/billing_pb";
import type { OrganizationUsageRequest } from "@/services/controlPlane/observability/organizationUsageApi";

const ts = (value: string) => timestampFromDate(new Date(value));
const usd = (dollars: number) => BigInt(Math.round(dollars * 1e9));

/** 2026-10-25 12:00 UTC: the fixed clock every view test runs at. */
export const NOW_MS = Date.parse("2026-10-25T12:00:00Z");
export const ORG = "org-example";

const MANAGED_RATE = { category: "managed_llm", basis: "actual", last24hUsdNanos: usd(1), averageDaily7dUsdNanos: usd(2), measuredHours: 168 };
const HOSTED_RATE = { category: "hosted_apps", basis: "list_equivalent", last24hUsdNanos: usd(0.5), averageDaily7dUsdNanos: usd(0.4), measuredHours: 12, unpricedDimensions: ["infra_memory"], lowerBound: true };
const DATABASE_RATE = { category: "databases", basis: "list_equivalent", last24hUsdNanos: usd(0.2), averageDaily7dUsdNanos: usd(0.2), measuredHours: 168, unpricedDimensions: [], lowerBound: false };

const INVENTORY = [
  { category: "machines", size: "small", state: "running", count: 2n, storageGib: 50n },
  { category: "machines", size: "large", state: "suspended", count: 1n, storageGib: 100n },
  { category: "hosted_apps", state: "running", environmentId: "env-prod", environmentName: "prod", count: 3n },
  { category: "databases", state: "running", environmentId: "env-prod", environmentName: "prod", count: 1n },
  { category: "static_hosting", state: "running", environmentId: "env-prod", environmentName: "prod", count: 1n },
];

const COVERAGE = [
  { category: "managed_llm", completeness: "complete", coverageStart: ts("2026-09-26T00:00:00Z"), coverageEnd: ts("2026-10-26T00:00:00Z"), measuredThrough: ts("2026-10-25T11:00:00Z") },
  { category: "machines", completeness: "partial", coverageStart: ts("2026-09-26T00:00:00Z"), coverageEnd: ts("2026-10-26T00:00:00Z"), measuredThrough: ts("2026-10-25T10:00:00Z"), limitations: ["The newest dev-box meter tick is behind the range end."] },
  { category: "hosted_infrastructure", completeness: "partial", coverageStart: ts("2026-10-15T00:00:00Z"), coverageEnd: ts("2026-10-26T00:00:00Z"), measuredThrough: ts("2026-10-25T11:00:00Z"), limitations: ["Hosted usage is retained for 7 days."] },
  { category: "observability", completeness: "unavailable", limitations: ["Observability metering could not be read."] },
  { category: "inventory", completeness: "complete" },
];

function base(granularity: OrganizationUsageGranularity, groupBy: OrganizationUsageGroupBy, start: string, end: string) {
  return {
    orgId: ORG,
    currency: "USD",
    generatedAt: ts("2026-10-25T12:00:00Z"),
    start: ts(start),
    end: ts(end),
    granularity,
    groupBy,
    coverage: COVERAGE,
    spendRates: [MANAGED_RATE, HOSTED_RATE, DATABASE_RATE],
    inventory: INVENTORY,
    completeness: "partial",
  };
}

/** 30 days by category: managed LLM settled, hosting list-priced, dev boxes and static egress unpriced, observability unavailable. */
export function usageByCategory(): ListOrganizationUsageResponse {
  return create(ListOrganizationUsageResponseSchema, {
    ...base(OrganizationUsageGranularity.DAY, OrganizationUsageGroupBy.CATEGORY, "2026-09-26T00:00:00Z", "2026-10-26T00:00:00Z"),
    points: [
      { bucketStart: ts("2026-10-20T00:00:00Z"), category: "managed_llm", actualChargesUsdNanos: usd(12.34), quantities: [{ id: "llm_requests", unit: "count", value: 40 }] },
      { bucketStart: ts("2026-10-21T00:00:00Z"), category: "managed_llm", actualChargesUsdNanos: usd(0.004), quantities: [{ id: "llm_requests", unit: "count", value: 1 }] },
      { bucketStart: ts("2026-10-20T00:00:00Z"), category: "machines", detail: "small", quantities: [{ id: "running_seconds", unit: "seconds", value: 7200 }, { id: "running_machines", unit: "count", value: 2 }] },
      { bucketStart: ts("2026-10-21T00:00:00Z"), category: "machines", detail: "large", quantities: [{ id: "running_seconds", unit: "seconds", value: 3600 }] },
      { bucketStart: ts("2026-10-20T00:00:00Z"), category: "hosted_apps", listEquivalentUsdNanos: usd(4), unpricedDimensions: ["infra_memory"], quantities: [{ id: "infra_cpu", unit: "vcpu_hours", value: 12.5 }, { id: "infra_memory", unit: "gib_hours", value: 25 }] },
      { bucketStart: ts("2026-10-20T00:00:00Z"), category: "databases", listEquivalentUsdNanos: usd(1), quantities: [{ id: "infra_storage", unit: "gib_hours", value: 240 }] },
      { bucketStart: ts("2026-10-20T00:00:00Z"), category: "static_hosting", quantities: [{ id: "egress_kib", unit: "kib", value: 2048 }] },
    ],
  });
}

/** This month as one bucket. */
export function usageMonthToDate(): ListOrganizationUsageResponse {
  return create(ListOrganizationUsageResponseSchema, {
    ...base(OrganizationUsageGranularity.MONTH, OrganizationUsageGroupBy.CATEGORY, "2026-10-01T00:00:00Z", "2026-11-01T00:00:00Z"),
    points: [
      { bucketStart: ts("2026-10-01T00:00:00Z"), category: "managed_llm", actualChargesUsdNanos: usd(20), quantities: [] },
      { bucketStart: ts("2026-10-01T00:00:00Z"), category: "machines", detail: "small", quantities: [{ id: "running_seconds", unit: "seconds", value: 36000 }] },
      { bucketStart: ts("2026-10-01T00:00:00Z"), category: "hosted_apps", listEquivalentUsdNanos: usd(6), quantities: [] },
      { bucketStart: ts("2026-10-01T00:00:00Z"), category: "static_hosting", quantities: [{ id: "egress_kib", unit: "kib", value: 4096 }] },
    ],
  });
}

export function usageByEnv(): ListOrganizationUsageResponse {
  return create(ListOrganizationUsageResponseSchema, {
    ...base(OrganizationUsageGranularity.DAY, OrganizationUsageGroupBy.ENV, "2026-09-26T00:00:00Z", "2026-10-26T00:00:00Z"),
    points: [
      { bucketStart: ts("2026-10-20T00:00:00Z"), category: "hosted_apps", groupKey: "env-prod", groupLabel: "prod", listEquivalentUsdNanos: usd(3) },
      { bucketStart: ts("2026-10-20T00:00:00Z"), category: "hosted_apps", groupKey: "env-staging", groupLabel: "staging", listEquivalentUsdNanos: usd(1) },
      { bucketStart: ts("2026-10-20T00:00:00Z"), category: "databases", groupKey: "env-prod", groupLabel: "prod", listEquivalentUsdNanos: usd(1) },
      { bucketStart: ts("2026-10-20T00:00:00Z"), category: "managed_llm", actualChargesUsdNanos: usd(12.34) },
    ],
  });
}

export function usageByModel(): ListOrganizationUsageResponse {
  return create(ListOrganizationUsageResponseSchema, {
    ...base(OrganizationUsageGranularity.DAY, OrganizationUsageGroupBy.MODEL, "2026-09-26T00:00:00Z", "2026-10-26T00:00:00Z"),
    points: [
      { bucketStart: ts("2026-10-20T00:00:00Z"), category: "managed_llm", groupKey: "claude-sonnet-5", groupLabel: "", actualChargesUsdNanos: usd(10) },
      { bucketStart: ts("2026-10-21T00:00:00Z"), category: "managed_llm", groupKey: "gemini-pro", groupLabel: "", actualChargesUsdNanos: usd(2.34) },
    ],
  });
}

/** Every source unavailable: no points, nothing priced, nothing in inventory. */
export function usageUnavailable(groupBy = OrganizationUsageGroupBy.CATEGORY, month = false): ListOrganizationUsageResponse {
  return create(ListOrganizationUsageResponseSchema, {
    ...base(month ? OrganizationUsageGranularity.MONTH : OrganizationUsageGranularity.DAY, groupBy, month ? "2026-10-01T00:00:00Z" : "2026-09-26T00:00:00Z", month ? "2026-11-01T00:00:00Z" : "2026-10-26T00:00:00Z"),
    coverage: ["managed_llm", "machines", "hosted_infrastructure", "observability", "inventory"].map((category) => ({ category, completeness: "unavailable", limitations: ["Source could not be read."] })),
    spendRates: [],
    inventory: [],
    points: [],
    completeness: "unavailable",
  });
}

/** Route a request to the fixture of its shape. */
export function respond(req: OrganizationUsageRequest): ListOrganizationUsageResponse {
  if (req.granularity === "month" && req.groupBy === "category") return usageMonthToDate();
  if (req.groupBy === "env") return usageByEnv();
  if (req.groupBy === "model") return usageByModel();
  return usageByCategory();
}

export function respondUnavailable(req: OrganizationUsageRequest): ListOrganizationUsageResponse {
  const groupBy = req.groupBy === "env" ? OrganizationUsageGroupBy.ENV : req.groupBy === "model" ? OrganizationUsageGroupBy.MODEL : OrganizationUsageGroupBy.CATEGORY;
  return usageUnavailable(groupBy, req.granularity === "month");
}

/** History reads fine, the inventory source does not: current counts are unknown, usage is not. */
export function respondInventoryUnavailable(req: OrganizationUsageRequest): ListOrganizationUsageResponse {
  const ok = respond(req);
  return create(ListOrganizationUsageResponseSchema, {
    ...ok,
    inventory: [],
    coverage: ok.coverage.map((c) => (c.category === "inventory" ? { ...c, completeness: "unavailable", limitations: ["Current deployments could not be listed."] } : c)),
  });
}
