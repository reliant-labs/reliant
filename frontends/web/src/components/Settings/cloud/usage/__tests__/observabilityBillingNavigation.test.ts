// @vitest-environment node
import { describe, expect, it } from "vitest";
import { BILLING_TAB_IDS, billingTabOrder, resolveBillingTab, VISIBLE_BILLING_TABS } from "@/routeSchemas";

describe("observability billing deep links", () => {
  it("accepts quota-banner tab=limits without stripping it or losing existing invoice links", () => {
    expect(BILLING_TAB_IDS).toContain("limits");
    expect(resolveBillingTab("limits")).toBe("usage");
    expect(resolveBillingTab("invoices")).toBe("usage");
    expect(resolveBillingTab("ai")).toBe("ai");
    expect(VISIBLE_BILLING_TABS).toEqual(["overview", "plans", "usage", "hosting", "ai"]);
  });
  it("leads with app hosting in the forge console, and opens there by default", () => {
    expect(billingTabOrder(true)[0]).toBe("hosting");
    expect(resolveBillingTab(undefined, true)).toBe("hosting");
    expect(resolveBillingTab(undefined, false)).toBe("overview");
    expect(resolveBillingTab("plans", true)).toBe("plans");
  });
});
