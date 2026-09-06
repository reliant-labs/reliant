/**
 * Onboarding must always render an escape hatch.
 *
 * This is the regression guard that matters: `OnboardingEscapeHatch` existing
 * is worth nothing if the page stops mounting it. Onboarding suppresses the
 * app Header and renders above every other surface, so if this row goes away a
 * user whose daemon never provisions has no route to settings and no way to
 * sign out — the exact dead end this change exists to close.
 */
/* eslint-disable @typescript-eslint/no-explicit-any */
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

vi.mock("../useOnboardingPlan", () => ({
  useOnboardingPlan: () => ({ plan: {}, updatePlan: vi.fn() }),
}));

vi.mock("../analytics", () => ({
  useOnboardingTracking: vi.fn(),
  markOnboardingFinalized: vi.fn(),
}));

vi.mock("@/hooks/useTitleBarChrome", () => ({
  useTitleBarChrome: () => ({ isElectron: false, dragRegionStyle: {} }),
}));

// The page reads compute eligibility and the wallet balance to derive the
// step. Both are react-query reads and this test mounts no client; stepConfig
// is stubbed below anyway, so the facts never reach a real decision here.
vi.mock("../useOnboardingFacts", () => ({
  useOnboardingFacts: () => ({
    computeEligible: true,
    walletFunded: true,
    loading: false,
    refetch: vi.fn(),
  }),
}));

// The real step registry drags in the whole compute/model/GitHub graph. A
// single stub step is enough — this test is about the chrome around it.
vi.mock("../stepConfig", () => ({
  BACK_CLEARS: { compute: [] },
  deriveStep: () => "compute",
  getStepsForPlan: () => ["compute"],
  visibleStepsForPlan: () => ["compute"],
  stepMaxWidth: () => "max-w-2xl",
  STEP_COMPONENTS: { compute: () => <div data-testid="step" /> },
  STEP_LABELS: { compute: "Compute" },
}));

vi.mock("../steps", () => ({}));

vi.mock("../OnboardingEscapeHatch", () => ({
  OnboardingEscapeHatch: () => <div data-testid="onboarding-escape-hatch" />,
}));

import { OnboardingPage } from "../OnboardingPage";

describe("OnboardingPage escape hatch", () => {
  it("mounts the escape hatch alongside the step content", () => {
    render(<OnboardingPage />);

    expect(screen.getByTestId("step")).toBeInTheDocument();
    expect(
      screen.getByTestId("onboarding-escape-hatch"),
    ).toBeInTheDocument();
  });
});
