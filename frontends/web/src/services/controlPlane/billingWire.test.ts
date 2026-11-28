// Copyright (c) 2026 Reliant Labs

import { create, fromBinary, fromJson, toBinary, toJson } from "@bufbuild/protobuf";
import { describe, expect, it } from "vitest";

import { GetCurrentUserUsageBillingResponseSchema } from "@/gen/controlplane/services/billing/v1/billing_pb";

describe("hosting billing wire contract", () => {
  it.each([
    { granted: true, applied: true, expired: false, refusedReason: "" },
    { granted: true, applied: true, expired: true, refusedReason: "" },
    { granted: false, applied: false, expired: false, refusedReason: "card_already_credited" },
  ])("preserves monetary values, dates and credit state: %j", (state) => {
    const response = create(GetCurrentUserUsageBillingResponseSchema, {
      enabled: true,
      policyVersion: "hosting-v1",
      agreement: {},
      rates: [{ resourceKind: "egress", displayName: "Transfer", unit: "GiB", priceCents: "12.5", includedMonthly: "10" }],
      minimumMonthlyCents: 500n,
      planDisplayName: "Hobby",
      includedUsageCents: 500n,
      launchCredit: {
        offerCents: 2500n, grantedCents: 2500n, remainingCents: 1701n, appliedCents: 799n,
        grantedAt: { seconds: 1800000000n, nanos: 123000000 },
        expiresAt: { seconds: 1802592000n, nanos: 456000000 },
        ...state,
      },
    });
    const decoded = fromBinary(GetCurrentUserUsageBillingResponseSchema, toBinary(GetCurrentUserUsageBillingResponseSchema, response));
    expect(decoded).toEqual(response);
    expect(fromJson(GetCurrentUserUsageBillingResponseSchema, toJson(GetCurrentUserUsageBillingResponseSchema, decoded))).toEqual(response);
    expect(decoded.launchCredit?.remainingCents).toBe(1701n);
    expect(decoded.includedUsageCents).toBe(500n);
    expect(decoded.rates[0]?.priceCents).toBe("12.5");
  });
});
