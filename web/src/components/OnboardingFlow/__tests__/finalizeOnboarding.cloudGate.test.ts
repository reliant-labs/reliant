/**
 * finalizeOnboardingSideEffects must not navigate away from /onboarding while
 * a cloud daemon is still provisioning.
 *
 * THE BUG THIS CLOSES: all three terminal steps (ProjectChoiceStep,
 * ProjectPickerStep, GitHubConnectStep) do
 *
 *     await finalizeOnboardingSideEffects(...)
 *     if (isCloud) setShowDaemonGate(true);
 *
 * but finalizeOnboardingSideEffects itself ends with an UNCONDITIONAL
 * `router.navigate({ to: "/" })`. For a cloud user the router leaves
 * /onboarding before the gate can render, so DaemonConnectingGate — the screen
 * that exists to say "your machine is still starting" — is never seen.
 *
 * The user lands on `/` with onboarding_completed=true and no ACTIVE daemon,
 * which is exactly the state ModernApp renders ProjectPicker for. That is why
 * a brand-new user reported being dropped onto "Connect a daemon" and
 * "Resume a daemon — Starting" instead of finishing onboarding.
 *
 * The tour param must still be set for the local path, which has no gate and
 * genuinely is finished at this point.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

const mockNavigate = vi.fn();
const mockResetTourProgress = vi.fn().mockResolvedValue(undefined);

vi.mock("@/routes", () => ({ router: { navigate: mockNavigate } }));

vi.mock("@/store/tourStore", () => ({
  useTourStore: { getState: () => ({ resetTourProgress: mockResetTourProgress }) },
}));

vi.mock("@/services/controlPlane/onboarding", () => ({
  onboardingService: { provisionManagedKey: vi.fn().mockResolvedValue({ synced: true }) },
}));

vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}));

import {
  finalizeOnboardingSideEffects,
  navigateAfterOnboarding,
} from "../useOnboardingComplete";

beforeEach(() => {
  vi.clearAllMocks();
});

describe("finalizeOnboardingSideEffects — cloud daemon gate", () => {
  it("does NOT navigate away when the caller still has to show the daemon gate", async () => {
    await finalizeOnboardingSideEffects("reliant_credits", { navigate: false });

    // The caller (a cloud terminal step) owns navigation from here: it renders
    // DaemonConnectingGate and navigates on the gate's Continue.
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it("still resets tour progress when navigation is deferred", async () => {
    await finalizeOnboardingSideEffects("reliant_credits", { navigate: false });

    expect(mockResetTourProgress).toHaveBeenCalled();
  });

  it("navigates to / with the first tour step on the local path", async () => {
    await finalizeOnboardingSideEffects("anthropic");

    expect(mockNavigate).toHaveBeenCalledWith(
      expect.objectContaining({ to: "/" }),
    );
  });

  // The deferred half. Cloud's finalize never touched the URL, so the tour
  // param has to be SET here — preserving it from `prev` would silently drop
  // the post-onboarding tour for every cloud user.
  it("navigateAfterOnboarding sets the tour param on the way out", async () => {
    await navigateAfterOnboarding();

    expect(mockNavigate).toHaveBeenCalledWith({
      to: "/",
      search: { tour: "chat-and-sidebars" },
    });
  });
});
