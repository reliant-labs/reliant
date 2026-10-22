/**
 * `useArrivedComplete` — the latch that separates "completion recorded" from
 * "flow finished".
 *
 * `OnboardingRoute.completionGate.test.tsx` proves the SEAM: a terminal step
 * survives its own completion call and gets its provisioning gate on screen.
 * This file pins the latch's own contract, which that test cannot observe —
 * a route-level test mounts a fresh component per render, and the identity
 * rule below is about one mounted instance outliving a change of user.
 */
import { renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { useArrivedComplete } from "../useArrivedComplete";

describe("useArrivedComplete", () => {
  it("is false for a user who arrives mid-flow", () => {
    const { result } = renderHook(() =>
      useArrivedComplete({ userId: "user-1", isComplete: false }),
    );
    expect(result.current).toBe(false);
  });

  it("is true for a user who arrives already complete", () => {
    const { result } = renderHook(() =>
      useArrivedComplete({ userId: "user-1", isComplete: true }),
    );
    expect(result.current).toBe(true);
  });

  // THE BUG. The completion mutation writes `onboardingCompleted: true` into
  // the query this reads, in the middle of a terminal step's own work. That is
  // this flow's doing, not evidence the user should never have been here.
  it("stays false when completion is recorded while the flow is mounted", () => {
    const { result, rerender } = renderHook(
      (props: { isComplete: boolean }) =>
        useArrivedComplete({ userId: "user-1", isComplete: props.isComplete }),
      { initialProps: { isComplete: false } },
    );
    expect(result.current).toBe(false);

    rerender({ isComplete: true });

    expect(result.current).toBe(false);
  });

  // Sign-out clears the user cache and the next sign-in resolves a different
  // account. A latch that outlived the identity would answer the new user's
  // arrival question with the previous user's state — the class of bug
  // `authStore.signOutClearsCache` exists to prevent.
  it("re-asks the question when the signed-in user changes", () => {
    const { result, rerender } = renderHook(
      (props: { userId: string; isComplete: boolean }) =>
        useArrivedComplete({ userId: props.userId, isComplete: props.isComplete }),
      { initialProps: { userId: "user-1", isComplete: false } },
    );
    expect(result.current).toBe(false);

    // A different account, already complete, resolves into the same mounted
    // route. They must be sent on rather than inheriting user-1's answer.
    rerender({ userId: "user-2", isComplete: true });

    expect(result.current).toBe(true);
  });

  // And the reverse, which is the one that actually strands someone: a
  // completed user's answer must not follow a new incomplete account into the
  // flow, or that account is redirected out of an onboarding it never did.
  it("does not carry a completed answer onto the next user", () => {
    const { result, rerender } = renderHook(
      (props: { userId: string; isComplete: boolean }) =>
        useArrivedComplete({ userId: props.userId, isComplete: props.isComplete }),
      { initialProps: { userId: "user-1", isComplete: true } },
    );
    expect(result.current).toBe(true);

    rerender({ userId: "user-2", isComplete: false });

    expect(result.current).toBe(false);
  });

  // Nothing is decided while the user query is in flight. A one-frame
  // `currentUser === undefined` must not latch "incomplete" for a user who is
  // in fact complete, nor redirect one who is not.
  it("decides nothing until the user query has settled", () => {
    const { result, rerender } = renderHook(
      (props: { isUserLoading: boolean; isComplete: boolean }) =>
        useArrivedComplete({
          // The identity IS the loading signal: undefined while in flight.
          userId: props.isUserLoading ? undefined : "user-1",
          isComplete: props.isComplete,
        }),
      { initialProps: { isUserLoading: true, isComplete: false } },
    );
    expect(result.current).toBe(false);

    // The query settles, and the user WAS already complete.
    rerender({ isUserLoading: false, isComplete: true });

    expect(result.current).toBe(true);
  });

  // A refetch flipping isLoading back on must not re-open the question and let
  // a later completion be mistaken for an arrival.
  it("holds its answer across a refetch that re-enters loading", () => {
    const { result, rerender } = renderHook(
      (props: { isUserLoading: boolean; isComplete: boolean }) =>
        useArrivedComplete({ userId: "user-1", isComplete: props.isComplete }),
      { initialProps: { isUserLoading: false, isComplete: false } },
    );
    expect(result.current).toBe(false);

    rerender({ isUserLoading: true, isComplete: false });
    expect(result.current).toBe(false);

    // Completion lands, then the refetch settles carrying it.
    rerender({ isUserLoading: false, isComplete: true });
    expect(result.current).toBe(false);
  });
});
