/**
 * Every departure from `/onboarding` goes through `leaveOnboarding(reason)`.
 *
 * THE ASYMMETRY THIS CLOSES: onboarding used to have three exits using three
 * different mechanisms — the route's `useNavigate` for "already complete", the
 * same for the returning-user heal, and the imported global `router` singleton,
 * called un-awaited from inside an async helper, for the terminal steps.
 *
 * That third mechanism is where `458a830c` lived. `finalizeOnboardingSideEffects`
 * ended with an unconditional `router.navigate({to: "/"})`, so a cloud user left
 * `/onboarding` before `DaemonConnectingGate` could render and landed on `/`
 * with onboarding complete and no ACTIVE daemon. The fix threaded a
 * `navigate: false` flag down to the singleton, which then had to be passed
 * correctly at four call sites — each re-deriving cloud-ness with its own copy
 * of the same string literal. Getting one wrong reproduces the original bug on
 * one path only, which is exactly how it would ship again.
 *
 * With one funnel the flag is unnecessary rather than merely well-tested: the
 * cloud path cannot exit before the gate because the gate's Continue is the
 * only cloud exit reason that exists.
 *
 * These tests assert on the RESULTING DESTINATION, not on "did navigate get
 * called". Ten of the fourteen unit files in this directory mock the router
 * wholesale and assert `mockNavigate` fired, which proves a component asked to
 * navigate but never that the user ended up somewhere sensible — and is blind
 * to the singleton path entirely. That mock boundary is drawn exactly along the
 * seam the bug lived in.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

// vi.mock factories are hoisted above the module body, so the spies they close
// over have to be created in a hoisted block too.
const { mockTrackEvent, mockLoggerInfo } = vi.hoisted(() => ({
  mockTrackEvent: vi.fn(),
  mockLoggerInfo: vi.fn(),
}));

vi.mock("@/lib/analytics", () => ({ trackEvent: mockTrackEvent }));

vi.mock("@/lib/logger", () => ({
  logger: { info: mockLoggerInfo, warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));

import {
  ONBOARDING_EXIT_REASONS,
  leaveOnboarding,
  onboardingExitTarget,
  type OnboardingExitReason,
} from "../leaveOnboarding";
import { START_APP, START_CHAT, START_REPO } from "@/components/Apps/appStart";

beforeEach(() => {
  vi.clearAllMocks();
});

/** Resolve a target's `search` whether it is a literal or a reducer. */
function resolveSearch(
  reason: OnboardingExitReason,
  prev: Record<string, unknown> = {},
): unknown {
  const { search } = onboardingExitTarget(reason);
  return typeof search === "function" ? search(prev) : search;
}

describe("onboardingExitTarget — where each reason lands the user", () => {
  it("defines a destination for every declared reason", () => {
    // Guards the guard: proves the table below is exhaustive, so adding a
    // reason without a destination fails here rather than navigating nowhere.
    const offenders: string[] = [];
    for (const reason of ONBOARDING_EXIT_REASONS) {
      const target = onboardingExitTarget(reason);
      if (!target?.to) offenders.push(reason);
    }
    expect(offenders).toEqual([]);
  });

  it("opens a chat with a first message when the user actually finished, and not the tour", () => {
    // In the first week none of 12 external signups sent a chat: the two who
    // finished were dropped on a tour / the forge journey. Finishing now lands
    // on the composer; the tour is one click away from there.
    for (const reason of ["completed_local", "completed_cloud_gate_continue"] as const) {
      expect(onboardingExitTarget(reason).to).toBe("/");
      const search = resolveSearch(reason) as Record<string, unknown>;
      expect(search.start).toBe(START_CHAT);
      // Explicitly cleared: the root route retains `?tour=` otherwise.
      expect("tour" in search && search.tour === undefined).toBe(true);
    }
  });

  it("picks the first message from what the user said they came to do", () => {
    const startFor = (landing: Parameters<typeof onboardingExitTarget>[1]) =>
      (onboardingExitTarget("completed_cloud_gate_continue", landing).search as Record<string, unknown>).start;

    expect(startFor("build_app")).toBe(START_APP);
    expect(startFor("existing_codebase")).toBe(START_REPO);
    expect(startFor("chat")).toBe(START_CHAT);
    expect(startFor(undefined)).toBe(START_CHAT);
  });

  it("never carries a tour into the chat it opens", () => {
    for (const landing of ["build_app", "existing_codebase", "chat", undefined] as const) {
      for (const reason of ["completed_local", "completed_cloud_gate_continue"] as const) {
        const search = onboardingExitTarget(reason, landing).search as Record<string, unknown>;
        expect(search.tour).toBeUndefined();
      }
    }
  });

  it("forwards an in-flight tour handoff when leaving without finishing", () => {
    // These redirects used to pass `search: {}`, which erases everything
    // including `?tour=` — a redirect racing the handoff ended the tour before
    // it rendered.
    for (const reason of ["already_complete", "returning_user_heal"] as const) {
      expect(resolveSearch(reason, { tour: "chat-and-sidebars" })).toEqual({
        tour: "chat-and-sidebars",
      });
    }
  });

  it("does not leak onboarding-local params into the app", () => {
    // `plan` and the `github_*` return params are flow-local. A spread of
    // `prev` would carry the whole plan onto `/`.
    const leaked = resolveSearch("already_complete", {
      tour: "chat-and-sidebars",
      plan: { compute: "cloud_paid" },
      github_connected: "true",
    });
    expect(leaked).toEqual({ tour: "chat-and-sidebars" });
  });

  it("sends a signed-out user to auth, not into the app", () => {
    expect(onboardingExitTarget("signed_out").to).toBe("/auth");
  });
});

describe("leaveOnboarding — the single exit", () => {
  it("navigates to the reason's destination", async () => {
    const nav = vi.fn();
    await leaveOnboarding("completed_local", nav);

    expect(nav).toHaveBeenCalledTimes(1);
    const [options] = nav.mock.calls[0];
    expect(options.to).toBe("/");
    expect(
      typeof options.search === "function" ? options.search({}) : options.search,
    ).toMatchObject({ start: START_CHAT });
  });

  it("logs the reason, so an exit nobody asked for is greppable", async () => {
    // Runtime detector for the whole class. console/logger output in the
    // renderer lands in frontend_reliant-web.log prefixed [browser:…], in
    // Electron and a browser tab alike — so an e2e run can grep for an exit
    // reason no user triggered. Today "something ended onboarding" is invisible.
    await leaveOnboarding("returning_user_heal", vi.fn());

    const logged = mockLoggerInfo.mock.calls.map((args) => JSON.stringify(args)).join("\n");
    expect(logged).toContain("returning_user_heal");
  });

  it("emits onboarding_exited with the reason and steps viewed", async () => {
    await leaveOnboarding("already_complete", vi.fn());

    expect(mockTrackEvent).toHaveBeenCalledWith(
      "onboarding_exited",
      expect.objectContaining({
        reason: "already_complete",
        steps_viewed_count: expect.any(Number),
      }),
    );
  });

  it("exits exactly once per reason across all declared reasons", async () => {
    // A reason that forgets to navigate strands the user on /onboarding, which
    // is the `63b18468` failure. A reason that navigates twice races itself.
    for (const reason of ONBOARDING_EXIT_REASONS) {
      const nav = vi.fn();
      await leaveOnboarding(reason, nav);
      expect(nav, `reason ${reason}`).toHaveBeenCalledTimes(1);
    }
  });
});

describe("the navigate flag is gone", () => {
  it("finalizeOnboardingSideEffects no longer decides whether to navigate", async () => {
    // The flag existed only because side effects and navigation were fused.
    // With one funnel, finalize does side effects and nothing else, so there is
    // no flag to pass wrongly at a fifth call site added tomorrow.
    const mod = await import("../useOnboardingComplete");
    expect(mod.finalizeOnboardingSideEffects).toHaveLength(0);
    expect("navigateAfterOnboarding" in mod).toBe(false);
  });
});
