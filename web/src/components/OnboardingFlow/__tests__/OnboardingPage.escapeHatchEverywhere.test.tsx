/**
 * The escape hatch must be reachable from EVERY state, including the one
 * where the step component is missing.
 *
 * `OnboardingEscapeHatch` existing is worth nothing if the page can stop
 * mounting it. Onboarding is a `fixed inset-0 z-40` overlay on its own route:
 * the app Header is suppressed and the Sidebar needs a selected project, which
 * is precisely what a stuck user does not have. Without this row a user whose
 * setup cannot complete has no route to settings and no way to sign out —
 * and sign-out is the one repair a stranded user can perform unaided.
 *
 * THE LIVE DEFECT THIS CLOSES: `OnboardingPage` did `if (!StepComponent)
 * return null` BEFORE rendering the card that contains the hatch.
 * `STEP_COMPONENTS` is populated by a module-scope side effect of importing
 * `./steps`, and it is typed through an assertion, so nothing type-checks the
 * registry being complete. If registration has not run — a bundling or module
 * ordering fault in a packaged build, the shape CI never exercises — the user
 * gets a blank screen with no Back, no Settings and no Sign out. That is
 * `63b18468`'s dead end reintroduced through a different door.
 *
 * A missing step component is a bug either way. The question this test settles
 * is whether it is a bug the user can walk away from.
 */
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

const mockUpdatePlan = vi.fn();
let currentPlan: Record<string, unknown> = {};

vi.mock("../useOnboardingPlan", () => ({
  useOnboardingPlan: () => ({ plan: currentPlan, updatePlan: mockUpdatePlan }),
}));

vi.mock("../analytics", () => ({
  useOnboardingTracking: vi.fn(),
  markOnboardingFinalized: vi.fn(),
}));

vi.mock("@/hooks/useTitleBarChrome", () => ({
  useTitleBarChrome: () => ({ isElectron: false, dragRegionStyle: {} }),
}));

// The page reads the two server facts step derivation depends on, which are
// react-query reads and need a client this test does not mount. Facts are
// driven per-fixture below instead: `currentFacts` is what each PLAN_FOR_STEP
// entry needs to actually land on the step it claims.
let currentFacts = { computeEligible: true, walletFunded: true };
vi.mock("../useOnboardingFacts", () => ({
  useOnboardingFacts: () => ({
    ...currentFacts,
    loading: false,
    refetch: vi.fn(),
  }),
}));

// The real step registry drags in the whole compute/model/GitHub graph. Stub
// components are enough — this test is about the chrome around them, and about
// what happens when that chrome has no step to wrap.
vi.mock("../steps", () => ({}));

vi.mock("../OnboardingEscapeHatch", () => ({
  OnboardingEscapeHatch: () => <div data-testid="onboarding-escape-hatch" />,
}));

vi.mock("../stepConfig", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../stepConfig")>();
  return {
    ...actual,
    // Every declared step gets a stub; the "unregistered" case is driven by
    // deleting from this object inside the test that needs it.
    STEP_COMPONENTS: Object.fromEntries(
      actual.ONBOARDING_STEPS.map((id) => [
        id,
        () => <div data-testid="step">{id}</div>,
      ]),
    ),
  };
});

import { OnboardingPage } from "../OnboardingPage";
import { ONBOARDING_STEPS, STEP_COMPONENTS, deriveStep } from "../stepConfig";
import type { LaunchPlan } from "../types";

/**
 * A plan (and the server facts) that derives to each step, so "every step"
 * means every step.
 *
 * Facts are part of the fixture because they are part of derivation: the
 * checkout step is reachable only when something is owed, and every other step
 * is reachable only once nothing is. Baking "entitled" in as a default and
 * hoping would silently stop covering a step the day payment started mattering
 * to it.
 */
const ENTITLED = { computeEligible: true, walletFunded: true };
const OWES_MONEY = { computeEligible: false, walletFunded: false };

const PLAN_FOR_STEP: Record<
  string,
  { plan: Partial<LaunchPlan>; facts: typeof ENTITLED }
> = {
  "compute": { plan: {}, facts: ENTITLED },
  "model": { plan: { compute: "local_daemon" }, facts: ENTITLED },
  "checkout": {
    // A new user on cloud compute: no subscription, no trial, so payment is
    // owed and this is the step.
    plan: { compute: "cloud_paid", modelProvider: "anthropic" },
    facts: OWES_MONEY,
  },
  "project-picker": {
    plan: { compute: "local_daemon", modelProvider: "anthropic" },
    facts: ENTITLED,
  },
  "project-choice": {
    plan: { compute: "cloud_free_trial", modelProvider: "anthropic" },
    facts: ENTITLED,
  },
  "github-connect": {
    plan: {
      compute: "cloud_free_trial",
      modelProvider: "anthropic",
      intent: "existing_codebase",
    },
    facts: ENTITLED,
  },
};

describe("OnboardingPage — escape hatch on every step", () => {
  it("covers every declared step with a fixture", () => {
    // Guards the guard: a step added without a fixture would otherwise be
    // silently untested by the loop below.
    expect(Object.keys(PLAN_FOR_STEP).sort()).toEqual([...ONBOARDING_STEPS].sort());
  });

  it("renders the escape hatch on each step", () => {
    for (const [step, { plan, facts }] of Object.entries(PLAN_FOR_STEP)) {
      currentPlan = plan;
      currentFacts = facts;
      // The fixture must actually reach the step it claims to.
      expect(deriveStep(plan, facts), `fixture for ${step}`).toBe(step);

      const { unmount } = render(<OnboardingPage />);
      expect(
        screen.getByTestId("onboarding-escape-hatch"),
        `escape hatch on ${step}`,
      ).toBeInTheDocument();
      unmount();
    }
  });

  it("renders the escape hatch even when the step component is missing", () => {
    // The blank-screen case. Registration has not run for the derived step.
    const registry = STEP_COMPONENTS as Record<string, unknown>;
    const saved = registry["compute"];
    delete registry["compute"];
    currentPlan = {};

    try {
      render(<OnboardingPage />);

      // No step to show — but the user is not trapped.
      expect(screen.queryByTestId("step")).not.toBeInTheDocument();
      expect(screen.getByTestId("onboarding-escape-hatch")).toBeInTheDocument();
    } finally {
      registry["compute"] = saved;
    }
  });

  it("tells the user something went wrong rather than showing an empty card", () => {
    const registry = STEP_COMPONENTS as Record<string, unknown>;
    const saved = registry["compute"];
    delete registry["compute"];
    currentPlan = {};

    try {
      render(<OnboardingPage />);
      expect(screen.getByRole("alert")).toBeInTheDocument();
    } finally {
      registry["compute"] = saved;
    }
  });
});
