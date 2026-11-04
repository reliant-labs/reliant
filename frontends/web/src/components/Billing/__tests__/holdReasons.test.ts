import { describe, expect, it } from "vitest";

import { friendlyCheckoutHolds, friendlyUsageHold } from "../holdReasons";

describe("friendlyCheckoutHolds", () => {
  it("never shows the server's operator vocabulary", () => {
    // Verbatim from control-plane svcbilling/machine_acceptance.go.
    const reasons = [
      "machine route machine-pooled-v1/l has not been commissioned",
      "proposed route economics do not clear the complete cost guard",
      "immutable future prices have not been published and verified",
      "the existing subscription period must be verified before next-period consent",
    ];
    const out = friendlyCheckoutHolds(reasons);
    expect(out.join(" ")).not.toMatch(/route|commission|economics|guard|immutable|machine-pooled/i);
    expect(out).toEqual([
      "This machine size isn't available to buy yet.",
      "Machine prices are being updated. Checkout reopens shortly.",
      "We're still confirming your current billing period.",
    ]);
  });

  it("collapses repeats and falls back to one plain sentence", () => {
    expect(friendlyCheckoutHolds(["machine route a has not been commissioned", "machine route b has not been commissioned"])).toHaveLength(1);
    expect(friendlyCheckoutHolds(["something new"])).toEqual(["Machine checkout is paused right now."]);
    expect(friendlyCheckoutHolds([])).toEqual(["Machine checkout is paused right now."]);
  });
});

describe("friendlyUsageHold", () => {
  it("reassures rather than alarms, and hides the raw reason", () => {
    const line = friendlyUsageHold("machine allowance snapshot is unresolved; conversations and stored data are preserved");
    expect(line).toMatch(/aren't affected/);
    expect(line).not.toMatch(/snapshot|unresolved/);
    expect(friendlyUsageHold(undefined)).toBeNull();
  });
});
