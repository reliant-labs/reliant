// Copyright (c) 2025 Reliant Labs
import { describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { GetOrganizationCostSummaryResponseSchema } from "@/gen/controlplane/services/billing/v1/billing_pb";
import { getOrganizationCostSummary } from "../organizationCostsApi";

const read = vi.hoisted(() => vi.fn());
vi.mock("../../client", () => ({ getControlPlaneClient: () => ({ getOrganizationCostSummary: read }) }));

describe("organization cost adapter", () => {
  it("sends only orgId and preserves optional nanos instead of inventing totals or zeros", async () => {
    const response = create(GetOrganizationCostSummaryResponseSchema, { orgId: "org", currency: "USD", sources: [
      { category: "managed_llm", periodId: "month_to_date", actualChargesUsdNanos: 9007199254740993n, completeness: "partial" },
      { category: "byok", periodId: "month_to_date", completeness: "not_measured" },
    ] });
    read.mockResolvedValue(response);
    expect(await getOrganizationCostSummary("org")).toBe(response);
    expect(read).toHaveBeenCalledWith({ orgId: "org" });
    expect(response.sources[0].actualChargesUsdNanos).toBe(9007199254740993n);
    expect(response.sources[1].actualChargesUsdNanos).toBeUndefined();
  });
});
