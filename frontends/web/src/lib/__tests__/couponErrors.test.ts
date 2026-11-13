import { describe, expect, it } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";

import { describeCouponError } from "../couponErrors";

// The shapes control-plane's coupon.RedeemError produces (internal/coupon/errors.go).
const refused = (message: string, code: Code, reason?: string) =>
  new ConnectError(message, code, reason ? new Headers({ "x-forge-error-reason": reason }) : undefined);

describe("describeCouponError — each refusal says what happened and what to do", () => {
  it("not valid", () => {
    const text = describeCouponError(refused("that coupon code isn't valid", Code.NotFound, "coupon_not_found"));
    expect(text).toMatch(/isn't valid/i);
    expect(text).toMatch(/typo/i);
  });

  it("expired", () => {
    expect(describeCouponError(refused("that coupon has expired", Code.FailedPrecondition, "coupon_expired"))).toMatch(/expired/i);
  });

  it("no longer active", () => {
    expect(describeCouponError(refused("that coupon is no longer active", Code.FailedPrecondition, "coupon_inactive"))).toMatch(
      /no longer active/i,
    );
  });

  it("fully redeemed by others", () => {
    expect(describeCouponError(refused("that coupon has been fully redeemed", Code.ResourceExhausted, "coupon_exhausted"))).toMatch(
      /fully redeemed/i,
    );
  });

  it("already used by this org — keeps the server's once-vs-maximum wording", () => {
    expect(
      describeCouponError(refused("you've already redeemed that coupon", Code.AlreadyExists, "coupon_already_redeemed")),
    ).toBe("You've already redeemed that coupon.");
    expect(
      describeCouponError(
        refused("you've already redeemed that coupon the maximum number of times", Code.AlreadyExists, "coupon_already_redeemed"),
      ),
    ).toMatch(/maximum number of times/i);
  });

  it("too many attempts says to wait", () => {
    expect(
      describeCouponError(refused("too many failed attempts — please try again later", Code.ResourceExhausted, "coupon_too_many_attempts")),
    ).toMatch(/wait/i);
  });

  it("branches on the reason, not the prose: reworded server copy still gets the right sentence", () => {
    expect(describeCouponError(refused("code gone", Code.FailedPrecondition, "coupon_expired"))).toMatch(/expired/i);
  });

  it("an older server with no reason still tells a bad code from a used one by its Connect code", () => {
    expect(describeCouponError(refused("coupon not found", Code.NotFound))).toMatch(/isn't valid/i);
    expect(describeCouponError(refused("coupon already redeemed", Code.AlreadyExists))).toMatch(/already redeemed/i);
  });

  it("a reason this web does not know shows the server's sentence, capitalised", () => {
    expect(describeCouponError(refused("this code only works in the EU", Code.FailedPrecondition, "coupon_region_locked"))).toBe(
      "This code only works in the EU.",
    );
  });

  it("an unreachable or crashed server is 'couldn't check', not a raw status", () => {
    for (const err of [
      refused("internal error", Code.Internal),
      refused("unavailable", Code.Unavailable),
      refused("deadline", Code.DeadlineExceeded),
      new TypeError("Failed to fetch"),
    ]) {
      expect(describeCouponError(err)).toMatch(/couldn't check/i);
    }
  });

  it("never shows the [code] framing", () => {
    expect(describeCouponError(new Error("[invalid_argument] code is required"))).toBe("Code is required.");
  });

  it("falls back to generic words when there is nothing to show", () => {
    expect(describeCouponError(undefined)).toBe("Could not redeem that code.");
  });
});
