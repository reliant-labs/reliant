/**
 * Route policy for globally-mounted modals.
 *
 * ModalLayer is mounted at the root route, so it renders on every screen with
 * no idea what is underneath it. `/onboarding` is a full-screen z-40 overlay
 * and modals are z-50, so a modal opened while onboarding is active covers the
 * setup flow. These tests pin which modals each route tolerates.
 */
import { describe, expect, it } from "vitest";
import { isModalAllowedOnRoute } from "../modalRoutePolicy";

describe("isModalAllowedOnRoute", () => {
  it("suppresses the api-key-setup modal on /onboarding", () => {
    expect(isModalAllowedOnRoute("api-key-setup", "/onboarding")).toBe(false);
  });

  it("suppresses it on nested onboarding paths and trailing slashes", () => {
    expect(isModalAllowedOnRoute("api-key-setup", "/onboarding/")).toBe(false);
    expect(isModalAllowedOnRoute("api-key-setup", "/onboarding/step-2")).toBe(
      false,
    );
  });

  it("allows it everywhere else", () => {
    for (const pathname of ["/", "/project/p1", "/settings", "/m/chats"]) {
      expect(isModalAllowedOnRoute("api-key-setup", pathname)).toBe(true);
    }
  });

  it("does not match a sibling route that merely shares the prefix", () => {
    expect(isModalAllowedOnRoute("api-key-setup", "/onboarding-help")).toBe(
      true,
    );
  });

  it("still allows billing modals on /onboarding", () => {
    // These are raised in direct response to a request the user just made and
    // explain why it failed. Suppressing them would turn a clear error into a
    // silent no-op, which is worse than the overlap.
    expect(isModalAllowedOnRoute("upgrade-required", "/onboarding")).toBe(true);
    expect(isModalAllowedOnRoute("billing-email-required", "/onboarding")).toBe(
      true,
    );
  });
});
