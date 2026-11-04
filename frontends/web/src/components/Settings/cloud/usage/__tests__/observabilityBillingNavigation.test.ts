import { describe, expect, it } from "vitest";
import { BILLING_TAB_IDS, resolveBillingTab, VISIBLE_BILLING_TABS } from "@/routeSchemas";

describe("observability billing deep links", () => {
  it("accepts quota-banner tab=limits without stripping it or losing existing invoice links", () => {
    expect(BILLING_TAB_IDS).toContain("limits");
    expect(resolveBillingTab("limits")).toBe("usage");
    expect(resolveBillingTab("invoices")).toBe("usage");
    expect(resolveBillingTab("ai")).toBe("ai");
    expect(VISIBLE_BILLING_TABS).toEqual(["overview", "plans", "usage", "ai"]);
  });
});
