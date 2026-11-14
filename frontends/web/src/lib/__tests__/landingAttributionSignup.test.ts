import { beforeEach, describe, expect, it } from "vitest";
import {
  HOLD_MS,
  SIGNUP_ATTRIBUTION_KEY,
  dropLandingAttribution,
  dropSignupAttribution,
  handoffQuery,
  holdLandingAttribution,
  readSignupAttribution,
} from "../landingAttribution";

beforeEach(() => localStorage.clear());

describe("signup attribution (the control plane's copy of the handoff)", () => {
  it("keeps the landing visit's referrer host and entry page, shaped", () => {
    expect(handoffQuery("?utm_source=hn&ref=News.YCombinator.com&lp=/products/forge")).toBe(
      "?utm_source=hn&ref=news.ycombinator.com&lp=%2Fproducts%2Fforge",
    );
    expect(handoffQuery("?ref=https://evil.example/x&lp=/a?b=c")).toBe("");
    expect(handoffQuery("?ref=localhost&lp=relative")).toBe("");
  });

  it("is held beside the runtime's copy and outlives it", () => {
    holdLandingAttribution("?rid=v_landing_123456&utm_source=reddit&ref=reddit.com&lp=/", 1_000);
    dropLandingAttribution(); // telemetry started with the runtime's copy, or consent is off
    expect(readSignupAttribution(2_000)).toBe("rid=v_landing_123456&utm_source=reddit&ref=reddit.com&lp=%2F");
    dropSignupAttribution();
    expect(readSignupAttribution(2_000)).toBeUndefined();
  });

  it("expires after HOLD_MS and removes a stale record", () => {
    holdLandingAttribution("?utm_source=hn", 1_000);
    expect(readSignupAttribution(1_000 + HOLD_MS + 1)).toBeUndefined();
    expect(localStorage.getItem(SIGNUP_ATTRIBUTION_KEY)).toBeNull();
  });
});
