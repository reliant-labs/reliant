/**
 * finalizeOnboardingSideEffects must not navigate at all.
 *
 * THE BUG THIS CLOSES: all three terminal steps (ProjectChoiceStep,
 * ProjectPickerStep, GitHubConnectStep) do
 *
 *     await finalizeOnboardingSideEffects(...)
 *     if (isCloud) setShowDaemonGate(true);
 *
 * but finalizeOnboardingSideEffects itself used to end with an UNCONDITIONAL
 * `router.navigate({ to: "/" })` through the global router singleton — not the
 * router the route is mounted under. For a cloud user the app left /onboarding
 * before the gate could render, so DaemonConnectingGate — the screen that
 * exists to say "your machine is still starting" — was never seen. The user
 * landed on `/` with onboarding_completed=true and no ACTIVE daemon, which is
 * exactly the state ModernApp renders ProjectPicker for. That is why a
 * brand-new user reported being dropped onto "Connect a daemon" and "Resume a
 * daemon — Starting" instead of finishing onboarding.
 *
 * THE ORIGINAL FIX AND WHY IT WAS REPLACED: a `navigate: false` option,
 * threaded down here from four call sites, each re-deriving cloud-ness with
 * its own copy of the `=== "cloud_free_trial"` literal. That made the bug one
 * forgotten argument away on any new path — and the literal itself was wrong
 * for `cloud_paid`.
 *
 * Now: this function does side effects and NOTHING else. Navigation belongs to
 * `leaveOnboarding`, so there is no flag to get wrong. A caller that forgets
 * to exit strands the user visibly on /onboarding rather than exiting at the
 * wrong moment invisibly. See leaveOnboarding.test.ts for the exits themselves.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

const mockNavigate = vi.fn();
const mockResetTourProgress = vi.fn().mockResolvedValue(undefined);
const mockProvisionManagedKey = vi.fn().mockResolvedValue({ synced: true });

// Still mocked, and deliberately so: this asserts the singleton is not reached
// by ANY route out of this function, which is the property that broke.
vi.mock("@/routes", () => ({ router: { navigate: mockNavigate } }));

vi.mock("@/store/tourStore", () => ({
  useTourStore: { getState: () => ({ resetTourProgress: mockResetTourProgress }) },
}));

vi.mock("@/services/controlPlane/onboarding", () => ({
  onboardingService: { provisionManagedKey: () => mockProvisionManagedKey() },
}));

vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}));

import * as onboardingComplete from "../useOnboardingComplete";
import { finalizeOnboardingSideEffects } from "../useOnboardingComplete";

beforeEach(() => {
  vi.clearAllMocks();
});

describe("finalizeOnboardingSideEffects — side effects only", () => {
  it("never navigates", async () => {
    // The cloud path's whole reason for existing: the caller still has to
    // render DaemonConnectingGate, and it cannot if the router has already
    // unmounted the step.
    await finalizeOnboardingSideEffects();

    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it("resets tour progress so the exit can start the tour cleanly", async () => {
    await finalizeOnboardingSideEffects();

    expect(mockResetTourProgress).toHaveBeenCalled();
  });

  // The managed-key provision MOVED, and its absence here is the point.
  //
  // It used to be a fire-and-forget `.then()` in this function, so a failed
  // grant produced a log line and nothing else — and the user found out at
  // their first message, which failed on a zero balance with no explanation.
  // It is now `grant_ai_access` inside `commitLaunchPlan`, where it is awaited,
  // reported as a task, and surfaced by ProvisioningGate. See
  // commitLaunchPlan.test.ts for the grant's own coverage.
  it("no longer grants AI access as an unobserved side effect", async () => {
    await finalizeOnboardingSideEffects();

    expect(mockProvisionManagedKey).not.toHaveBeenCalled();
  });

  it("no longer exposes a navigate flag or a deferred navigate half", () => {
    // The flag and its deferred twin were the machinery the single exit funnel
    // replaced. Their absence is the point of the change, so it is asserted
    // rather than assumed. Zero parameters now: the modelProvider argument went
    // with the AI grant it selected on.
    expect(finalizeOnboardingSideEffects).toHaveLength(0);
    expect("navigateAfterOnboarding" in onboardingComplete).toBe(false);
  });
});
