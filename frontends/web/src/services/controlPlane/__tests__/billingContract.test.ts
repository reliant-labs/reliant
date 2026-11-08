// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  create,
  fromBinary,
  fromJson,
  toBinary,
  toJson,
} from "@bufbuild/protobuf";

import {
  GetCurrentUserUsageBillingResponseSchema,
  HostedUsageRateSchema,
} from "@/gen/controlplane/services/billing/v1/billing_pb";

/**
 * The wire contract of the hosting allowance (HostedUsageRate.included_monthly,
 * tag 5) and the monthly hosting minimum
 * (GetCurrentUserUsageBillingResponse.minimum_monthly_cents, tag 5) that
 * control-plane serves. Both are additive: a response from a control plane
 * that predates them must still decode, to "no allowance" and "no minimum".
 */
describe("usage billing contract", () => {
  it("keeps the allowance and minimum on their vendored tags and JSON names", () => {
    const included = HostedUsageRateSchema.field.includedMonthly;
    expect(included.number).toBe(5);
    expect(included.jsonName).toBe("includedMonthly");
    const minimum =
      GetCurrentUserUsageBillingResponseSchema.field.minimumMonthlyCents;
    expect(minimum.number).toBe(5);
    expect(minimum.jsonName).toBe("minimumMonthlyCents");
  });

  it("round-trips a nonzero minimum and an exact-decimal allowance through binary and JSON", () => {
    const msg = create(GetCurrentUserUsageBillingResponseSchema, {
      rates: [
        create(HostedUsageRateSchema, {
          resourceKind: "egress_gib",
          displayName: "Internet egress",
          unit: "GiB",
          priceCents: "15",
          includedMonthly: "10.5",
        }),
      ],
      minimumMonthlyCents: 500n,
    });

    const decoded = fromBinary(
      GetCurrentUserUsageBillingResponseSchema,
      toBinary(GetCurrentUserUsageBillingResponseSchema, msg),
    );
    expect(decoded.minimumMonthlyCents).toBe(500n);
    expect(decoded.rates[0]?.includedMonthly).toBe("10.5");

    const json = toJson(GetCurrentUserUsageBillingResponseSchema, msg);
    // int64 travels as a JSON string; the allowance stays an exact decimal string.
    expect(json).toMatchObject({
      minimumMonthlyCents: "500",
      rates: [{ includedMonthly: "10.5" }],
    });
    const fromWire = fromJson(GetCurrentUserUsageBillingResponseSchema, json);
    expect(fromWire.minimumMonthlyCents).toBe(500n);
    expect(fromWire.rates[0]?.includedMonthly).toBe("10.5");
  });

  it("reads an older control plane's response, which carries neither field, as no allowance and no minimum", () => {
    const older = create(GetCurrentUserUsageBillingResponseSchema, {
      rates: [
        create(HostedUsageRateSchema, {
          resourceKind: "cpu_milli",
          displayName: "Hosted CPU",
          unit: "vCPU-hour",
          priceCents: "7.0",
        }),
      ],
    });

    const decoded = fromBinary(
      GetCurrentUserUsageBillingResponseSchema,
      toBinary(GetCurrentUserUsageBillingResponseSchema, older),
    );
    expect(decoded.minimumMonthlyCents).toBe(0n);
    expect(decoded.rates[0]?.includedMonthly).toBe("");
    expect(decoded.rates[0]?.priceCents).toBe("7.0");

    const fromWire = fromJson(GetCurrentUserUsageBillingResponseSchema, {
      rates: [
        {
          resourceKind: "cpu_milli",
          displayName: "Hosted CPU",
          unit: "vCPU-hour",
          priceCents: "7.0",
        },
      ],
    });
    expect(fromWire.minimumMonthlyCents).toBe(0n);
    expect(fromWire.rates[0]?.includedMonthly).toBe("");
  });
});
