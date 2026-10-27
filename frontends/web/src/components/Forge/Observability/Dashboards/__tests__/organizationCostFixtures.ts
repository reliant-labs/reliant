// Copyright (c) 2026 Reliant Labs
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { GetOrganizationCostSummaryResponseSchema } from "@/gen/controlplane/services/billing/v1/billing_pb";

const timestamp = (value: string) => timestampFromDate(new Date(value));

export function organizationCostFixture() {
  return create(GetOrganizationCostSummaryResponseSchema, {
    orgId: "org-example",
    currency: "USD",
    generatedAt: timestamp("2026-10-25T12:00:00Z"),
    periods: [
      { id: "month_to_date", start: timestamp("2026-10-01T00:00:00Z"), end: timestamp("2026-11-01T00:00:00Z") },
      { id: "compute_billing_period", start: timestamp("2026-10-12T00:00:00Z"), end: timestamp("2026-11-12T00:00:00Z") },
    ],
    sources: [
      { category: "managed_llm", periodId: "month_to_date", actualChargesUsdNanos: 12_340_000_000n, creditsUsdNanos: -2_500_000_000n, pendingCount: 2n, completeness: "partial", coverageStart: timestamp("2026-10-01T00:00:00Z"), coverageEnd: timestamp("2026-10-25T12:00:00Z"), limitations: ["Pending calls do not yet have a reliable dollar estimate."] },
      { category: "byok", periodId: "month_to_date", completeness: "not_measured", limitations: ["External provider charges are not measured."] },
      { category: "hosted_infrastructure", periodId: "month_to_date", listEquivalentUsdNanos: 4_000_000_001n, completeness: "partial", coverageStart: timestamp("2026-10-18T12:00:00Z"), measuredThrough: timestamp("2026-10-25T11:55:00Z"), allowances: [{ id: "cpu", unit: "vCPU-hours", included: 100, consumed: 12.5 }], limitations: ["Only seven days of retained resource buckets are available; not complete month-to-date coverage."] },
      { category: "observability", periodId: "month_to_date", pendingEstimateUsdNanos: 0n, completeness: "complete", measuredThrough: timestamp("2026-10-25T11:59:00Z"), allowances: [{ id: "ingest", unit: "bytes", included: 1_000_000_000 }] },
      { category: "static_hosting", periodId: "month_to_date", completeness: "unavailable", limitations: ["Metering is temporarily unavailable."] },
      { category: "machines", periodId: "compute_billing_period", actualChargesUsdNanos: 20_000_000_000n, completeness: "partial", measuredThrough: timestamp("2026-10-25T11:58:00Z") },
    ],
  });
}
