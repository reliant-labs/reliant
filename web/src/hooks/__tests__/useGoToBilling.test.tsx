/**
 * useGoToBilling — where "View plans" actually sends people.
 *
 * This hook used to fork on anonymity and route anonymous sessions through
 * /upgrade first. That guarantee (no subscription against a browser-session
 * identity) has NOT been dropped — it moved into the checkout mutation, which
 * is the only call that spends money and therefore the only chokepoint that
 * cannot be bypassed by adding a sixth navigation call site. See
 * `useCloudBillingQueries.identity.test.tsx`, which pins it there.
 *
 * What is left here is navigation, and it is the same for everyone: straight to
 * billing, on the Plans tab, because that is what the user asked for.
 */
import { renderHook, act } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mockNavigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => mockNavigate,
}));

import { useGoToBilling } from "../useGoToBilling";

beforeEach(() => {
  vi.clearAllMocks();
});

describe("useGoToBilling", () => {
  it("sends an anonymous user straight to billing, no identity detour", () => {
    const { result } = renderHook(() => useGoToBilling());
    act(() => result.current());

    expect(mockNavigate).toHaveBeenCalledWith({
      to: "/settings/$section",
      params: { section: "billing" },
      search: { tab: "plans", from: undefined, returnTo: undefined },
    });
    // The /upgrade interstitial is gone: the ask now happens at purchase.
    expect(mockNavigate).not.toHaveBeenCalledWith(
      expect.objectContaining({ to: "/upgrade" }),
    );
  });

  it("deep-links to the Plans tab, not the Overview dashboard", () => {
    const { result } = renderHook(() => useGoToBilling());
    act(() => result.current());

    expect(mockNavigate).toHaveBeenCalledWith(
      expect.objectContaining({ search: expect.objectContaining({ tab: "plans" }) }),
    );
  });

  it("carries the originating surface so billing can offer a route back", () => {
    // Without this, a user who detoured from the compute step landed on billing
    // with no way back into the wizard.
    const { result } = renderHook(() => useGoToBilling("onboarding"));
    act(() => result.current());

    expect(mockNavigate).toHaveBeenCalledWith(
      expect.objectContaining({
        to: "/settings/$section",
        params: { section: "billing" },
        search: expect.objectContaining({ tab: "plans", from: "onboarding" }),
      }),
    );
  });

  it("captures the FULL onboarding URL, so the detour is resumable", () => {
    // The regression this pins is the one that made the billing detour feel
    // punitive. Onboarding's entire state — every answer the user has given —
    // lives in the `plan` search param (useOnboardingPlan). A route back that
    // knows only "/onboarding" therefore lands on an EMPTY plan, and
    // deriveStep's first line (`if (!plan.compute) return 'compute'`) restarts
    // the wizard at step one. The user is punished for a detour the flow
    // required of them.
    //
    // Asserting on the exact query string rather than just "returnTo is set" is
    // the point: dropping the `search` half would still satisfy a truthiness
    // check and would still lose every answer.
    window.history.pushState(
      {},
      "",
      "/onboarding?plan=%7B%22compute%22%3A%22cloud_free_trial%22%7D",
    );

    const { result } = renderHook(() => useGoToBilling("onboarding"));
    act(() => result.current());

    expect(mockNavigate).toHaveBeenCalledWith(
      expect.objectContaining({
        search: expect.objectContaining({
          returnTo: "/onboarding?plan=%7B%22compute%22%3A%22cloud_free_trial%22%7D",
        }),
      }),
    );
  });

  it("does not capture a return URL when the caller is not onboarding", () => {
    // Settings and the chat shell are already where the user wants to be; a
    // returnTo would be noise in the URL and a needless open-redirect surface.
    window.history.pushState({}, "", "/settings/general");

    const { result } = renderHook(() => useGoToBilling());
    act(() => result.current());

    expect(mockNavigate).toHaveBeenCalledWith(
      expect.objectContaining({
        search: expect.objectContaining({ returnTo: undefined }),
      }),
    );
  });
});
