/**
 * The step opens on the free path — and opens on it PASSIVELY.
 *
 * Two rules, and the second is the one that is easy to break while satisfying
 * the first:
 *
 *  1. Claude Code is pre-selected, so a user who accepts the default finishes
 *     onboarding without being charged. They bring a Claude subscription they
 *     already pay for.
 *
 *  2. Pre-selecting must write NOTHING. `deriveStep` routes on
 *     `plan.modelProvider`, so writing it on mount would satisfy the step's
 *     own precondition and derive the user straight past the question — the
 *     step would flash and vanish, having "chosen" on their behalf. A default
 *     SELECTION is a starting position for a choice the user still makes.
 */

import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

import { ModelStep } from "../steps/ModelStep";
import type { LaunchPlan } from "../types";

const mockUseWalletOverview = vi.fn();

vi.mock("@/hooks/useCloudBillingQueries", () => ({
  useWalletOverview: () => mockUseWalletOverview(),
  useCloudEligibility: () => ({ data: { eligible: false }, isLoading: false }),
}));

vi.mock("@/hooks/useOAuthAvailability", () => ({
  useOAuthAvailability: () => ({ available: true, isLoading: false }),
}));

vi.mock("@/lib/claude-oauth", () => ({ useClaudeOAuth: () => ({}) }));
vi.mock("@/lib/codex-oauth", () => ({ useCodexOAuth: () => ({}) }));
vi.mock("@/lib/copilot-oauth", () => ({
  useCopilotOAuth: () => ({ reset: vi.fn() }),
}));

function wrapper({ children }: { children: React.ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

describe("ModelStep — default selection", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockUseWalletOverview.mockReturnValue({
      data: { wallet: { balanceUsdNanos: 0n } },
      isLoading: false,
    });
  });

  it("opens with Claude Code selected, so the free path is the default", () => {
    render(
      <ModelStep
        plan={{ compute: "cloud_free_trial" } as LaunchPlan}
        updatePlan={vi.fn()}
        onNext={vi.fn()}
        onBack={vi.fn()}
      />,
      { wrapper },
    );

    // aria-pressed is not set on these tiles, so selection is read the way a
    // user reads it: the selected provider is the one whose panel is showing.
    expect(
      screen.getByRole("button", { name: /connect claude code/i }),
    ).toBeInTheDocument();
  });

  it("writes nothing on mount — a default selection is not a choice", () => {
    const updatePlan = vi.fn();
    const onNext = vi.fn();

    render(
      <ModelStep
        plan={{ compute: "cloud_free_trial" } as LaunchPlan}
        updatePlan={updatePlan}
        onNext={onNext}
        onBack={vi.fn()}
      />,
      { wrapper },
    );

    expect(updatePlan).not.toHaveBeenCalled();
    // The decisive one: derivation routes on modelProvider, so a write here
    // would skip the step entirely rather than default it.
    expect(onNext).not.toHaveBeenCalled();
  });
});
