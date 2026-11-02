import { describe, expect, it } from "vitest";

import { parseRational } from "@/lib/exact-rational";
import {
  computeCouponRedeemedLine,
  formatPrice,
  formatRateDollars,
  hostedRateLine,
  hostedRateLines,
  hostingMinimumLine,
  type HostedRateLike,
} from "../priceFormat";

/**
 * The hosting rates exactly as control-plane's usagePolicyAndRates serves them
 * from pricing.yaml (internal/billing/svcbilling/usage_billing.go): display
 * names, units and exact decimal cents per unit.
 */
const SERVER_RATES: HostedRateLike[] = [
  { resourceKind: "cpu_milli", displayName: "Hosted CPU", unit: "vCPU-hour", priceCents: "7.0" },
  { resourceKind: "mem_mib", displayName: "Hosted memory", unit: "GiB-hour", priceCents: "1.2" },
  { resourceKind: "storage_gib", displayName: "Hosted storage", unit: "GiB-hour", priceCents: "0.0342465753424658" },
  { resourceKind: "egress_gib", displayName: "Internet egress", unit: "GiB", priceCents: "15" },
  { resourceKind: "static_egress_kib", displayName: "Static-site egress", unit: "GiB", priceCents: "15" },
  { resourceKind: "static_site", displayName: "Extra static sites", unit: "site-month", priceCents: "500" },
];

describe("formatPrice", () => {
  it("prints whole dollars bare, as the price list does", () => {
    expect(formatPrice(2500)).toBe("$25");
    expect(formatPrice(48900n)).toBe("$489");
    expect(formatPrice(124900)).toBe("$1,249");
    expect(formatPrice(0)).toBe("$0");
  });

  it("keeps both cent digits when there are cents", () => {
    expect(formatPrice(1250)).toBe("$12.50");
    expect(formatPrice(1005)).toBe("$10.05");
  });
});

describe("formatRateDollars", () => {
  it("uses only the decimals a rate needs", () => {
    expect(formatRateDollars(parseRational("7.0")!)).toBe("$0.07");
    expect(formatRateDollars(parseRational("1.2")!)).toBe("$0.012");
    expect(formatRateDollars(parseRational("15")!)).toBe("$0.15");
    expect(formatRateDollars(parseRational("500")!)).toBe("$5");
    expect(formatRateDollars(parseRational("30")!)).toBe("$0.30");
  });
});

describe("hosted rate disclosure", () => {
  it("reads like the price list, in its units", () => {
    expect(SERVER_RATES.map(hostedRateLine)).toEqual([
      "Hosted CPU: $0.07/vCPU-hr",
      "Hosted memory: $0.012/GiB-hr",
      "Hosted storage: $0.25/GiB-month",
      "Internet egress: $0.15/GB",
      "Static-site egress: $0.15/GB",
      "Extra static sites: $5/month each (your first site is free)",
    ]);
  });

  it("never prints cents or a sixteen-digit fraction", () => {
    for (const line of hostedRateLines(SERVER_RATES)) {
      expect(line).not.toMatch(/cents|\d{5,}/);
    }
  });

  it("states a free monthly allowance when the server sends one", () => {
    expect(hostedRateLine({ ...SERVER_RATES[3], includedMonthly: "10" })).toBe(
      "Internet egress: $0.15/GB after 10 GB free each month",
    );
    expect(hostedRateLine({ ...SERVER_RATES[3], includedMonthly: "0" })).toBe("Internet egress: $0.15/GB");
  });

  it("refuses the whole list when any price is unreadable", () => {
    expect(hostedRateLine({ ...SERVER_RATES[0], priceCents: "NaN" })).toBeNull();
    expect(hostedRateLines([...SERVER_RATES, { ...SERVER_RATES[0], priceCents: "" }])).toEqual([]);
  });

  it("passes an unknown unit through rather than guess it", () => {
    expect(hostedRateLine({ resourceKind: "x", displayName: "Thing", unit: "widget", priceCents: "100" })).toBe("Thing: $1/widget");
  });

  it("states the monthly minimum only when there is one", () => {
    expect(hostingMinimumLine(500n)).toBe("A $5 minimum applies in any month with hosting usage.");
    expect(hostingMinimumLine(0n)).toBeNull();
    expect(hostingMinimumLine(undefined)).toBeNull();
  });
});

describe("coupon hours", () => {
  it("says how many hours a compute coupon added, on the machine it runs", () => {
    expect(computeCouponRedeemedLine(6000)).toBe(
      "Added 100 hours on an M machine. They're one-time, and used after any hours your plan includes.",
    );
    expect(computeCouponRedeemedLine(90)).toMatch(/^Added 1\.5 hours on an M machine\./);
  });

  it("does not claim zero hours from a missing figure", () => {
    expect(computeCouponRedeemedLine(0)).not.toMatch(/0 hours/);
  });
});
