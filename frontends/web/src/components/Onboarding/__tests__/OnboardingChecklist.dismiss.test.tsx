/**
 * Setup Guide dismissal + retirement contract.
 *
 * Two bugs made the guide feel like it "pops up too much even after I dismiss
 * it", and both are pinned here:
 *
 *   1. The header ✕ called setPanelState("collapsed"), so the universal
 *      close gesture left a permanent pill in the corner. The only real
 *      dismiss was a grey text link in the footer.
 *   2. Nothing consulted allRequiredComplete(), so a finished guide kept
 *      rendering at 9/9 forever.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";
import React from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { REQUIRED_ITEMS } from "../constants";

// ─── Checklist store double ──────────────────────────────────────────────────

const setPanelState = vi.fn(async () => undefined);

const checklistState = vi.hoisted(() => ({
  current: {} as any,
}));

vi.mock("../../../store/onboardingChecklistStore", () => ({
  useOnboardingChecklistStore: Object.assign(
    (selector?: any) =>
      selector ? selector(checklistState.current) : checklistState.current,
    {
      getState: () => checklistState.current,
      setState: vi.fn(),
      subscribe: vi.fn(() => () => undefined),
    },
  ),
}));

vi.mock("../../../store/tourStore", () => ({
  useTourStore: {
    getState: () => ({ resetTourProgress: vi.fn(async () => undefined) }),
  },
}));

vi.mock("../../../store/apiKeySetupStore", () => ({
  useApiKeySetupStore: { getState: () => ({ openModal: vi.fn() }) },
}));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => vi.fn(),
}));

vi.mock("../useTourNavigation", () => ({
  useTourNavigation: () => ({ goToStep: vi.fn() }),
}));

vi.mock("../../../lib/analytics", () => ({ trackEvent: vi.fn() }));

import { OnboardingChecklist } from "../OnboardingChecklist";

function setChecklistState(overrides: Record<string, unknown> = {}) {
  const completedItems: Set<string> =
    (overrides.completedItems as Set<string>) ?? new Set();
  checklistState.current = {
    completedItems,
    panelState: "expanded",
    setPanelState,
    totalCompleted: () => completedItems.size,
    completionPercentage: () => 0,
    allRequiredComplete: () =>
      REQUIRED_ITEMS.every((i) => completedItems.has(i.id)),
    ...overrides,
  };
}

describe("OnboardingChecklist dismissal", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    setChecklistState();
  });

  it("dismisses the guide when the header close button is clicked", async () => {
    render(React.createElement(OnboardingChecklist));

    const close = screen.getByRole("button", { name: /dismiss setup guide/i });
    await userEvent.click(close);

    expect(setPanelState).toHaveBeenCalledWith("dismissed");
    expect(setPanelState).not.toHaveBeenCalledWith("collapsed");
  });

  it("offers a separate control for collapsing to the pill", async () => {
    render(React.createElement(OnboardingChecklist));

    const collapse = screen.getByRole("button", {
      name: /collapse setup guide/i,
    });
    await userEvent.click(collapse);

    expect(setPanelState).toHaveBeenCalledWith("collapsed");
  });

  it("renders nothing once the panel state is dismissed", () => {
    setChecklistState({ panelState: "dismissed" });
    const { container } = render(React.createElement(OnboardingChecklist));
    expect(container).toBeEmptyDOMElement();
  });

  it("renders nothing once every required item is complete", () => {
    setChecklistState({
      completedItems: new Set(REQUIRED_ITEMS.map((i) => i.id)),
    });
    const { container } = render(React.createElement(OnboardingChecklist));
    expect(container).toBeEmptyDOMElement();
  });

  it("still renders while required work is outstanding", () => {
    setChecklistState({
      completedItems: new Set(REQUIRED_ITEMS.slice(1).map((i) => i.id)),
    });
    render(React.createElement(OnboardingChecklist));
    expect(screen.getByText("Setup Guide")).toBeInTheDocument();
  });
});
