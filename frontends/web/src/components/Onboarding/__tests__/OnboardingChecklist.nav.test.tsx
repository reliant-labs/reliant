/**
 * The Setup Guide, docked in the nav footer: a "Get started · n/9" row that
 * opens its checklist in place.
 *
 * Pinned here:
 *   - who sees it: signed in, Reliant's settings storage, not dismissed, and
 *     required work left (it floated over the login form once, and a finished
 *     guide sat at 9/9 forever);
 *   - ✕ dismisses (the store's "dismissed"; Help › Show setup guide revives
 *     it), the row opens and closes it ("expanded" / "collapsed");
 *   - each item goes somewhere of its own: three used to land on the same
 *     Workflows library page.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";
import React from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

import { CHECKLIST_ITEMS, REQUIRED_ITEMS } from "../constants";

const authState = vi.hoisted(() => ({ current: { user: { id: "user-1" } as unknown } }));
vi.mock("../../../store/authStore", () => ({
  useAuthStore: Object.assign((selector?: any) => (selector ? selector(authState.current) : authState.current), {
    getState: () => authState.current,
  }),
}));

const setPanelState = vi.fn(async () => undefined);
const markComplete = vi.fn(async () => undefined);
const checklistState = vi.hoisted(() => ({ current: {} as any }));
vi.mock("../../../store/onboardingChecklistStore", () => ({
  useOnboardingChecklistStore: Object.assign(
    (selector?: any) => (selector ? selector(checklistState.current) : checklistState.current),
    { getState: () => checklistState.current, setState: vi.fn(), subscribe: vi.fn(() => () => undefined) },
  ),
}));

const resetTourProgress = vi.fn(async () => undefined);
vi.mock("../../../store/tourStore", () => ({
  useTourStore: { getState: () => ({ resetTourProgress }) },
}));

const openModal = vi.fn();
vi.mock("../../../store/apiKeySetupStore", () => ({
  useApiKeySetupStore: { getState: () => ({ openModal }) },
}));

const storage = vi.hoisted(() => ({ kind: "settings" as "settings" | "local" }));
vi.mock("../tourShell", () => ({ getTourStorage: () => ({ kind: storage.kind }) }));

vi.mock("../../../lib/analytics", () => ({ trackEvent: vi.fn() }));

import { trackEvent } from "../../../lib/analytics";
import { OnboardingChecklist } from "../OnboardingChecklist";

function setChecklistState(overrides: Record<string, unknown> = {}) {
  checklistState.current = {
    isInitialized: true,
    completedItems: new Set<string>(),
    panelState: "collapsed",
    setPanelState,
    markComplete,
    ...overrides,
  };
}

const onNavigate = vi.fn();
const onOpenTours = vi.fn();
const renderGuide = () => render(React.createElement(OnboardingChecklist, { onNavigate, onOpenTours }));

describe("the Setup Guide in the nav", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    authState.current = { user: { id: "user-1" } };
    storage.kind = "settings";
    setChecklistState();
  });

  it("is one row, Get started with its count, closed until opened", () => {
    setChecklistState({ completedItems: new Set(["add-api-key", "start-chat"]) });
    renderGuide();
    const toggle = screen.getByTestId("setup-guide-toggle");
    expect(toggle).toHaveTextContent("Get started");
    expect(toggle).toHaveTextContent(`2/${CHECKLIST_ITEMS.length}`);
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByRole("group", { name: "Setup guide" })).toBeNull();
  });

  it("opens and closes in place through the store", async () => {
    renderGuide();
    await userEvent.click(screen.getByTestId("setup-guide-toggle"));
    expect(setPanelState).toHaveBeenCalledWith("expanded");

    setChecklistState({ panelState: "expanded" });
    renderGuide();
    expect(screen.getByRole("group", { name: "Setup guide" })).toBeInTheDocument();
    await userEvent.click(screen.getAllByTestId("setup-guide-toggle")[1]);
    expect(setPanelState).toHaveBeenCalledWith("collapsed");
  });

  it("dismisses with ✕, never just collapses", async () => {
    renderGuide();
    await userEvent.click(screen.getByRole("button", { name: /dismiss setup guide/i }));
    expect(setPanelState).toHaveBeenCalledWith("dismissed");
    expect(setPanelState).not.toHaveBeenCalledWith("collapsed");
  });

  it.each([
    ["dismissed", () => setChecklistState({ panelState: "dismissed" })],
    ["every required item done", () => setChecklistState({ completedItems: new Set(REQUIRED_ITEMS.map((i) => i.id)) })],
    ["signed out", () => (authState.current = { user: null })],
    ["its state not loaded yet", () => setChecklistState({ isInitialized: false })],
    ["the forge shell (local tour storage)", () => (storage.kind = "local")],
  ])("draws nothing when %s", (_why, arrange) => {
    arrange();
    const { container } = renderGuide();
    expect(container).toBeEmptyDOMElement();
  });

  it("sends each item somewhere of its own, through the nav's navigate, and records the click", async () => {
    setChecklistState({ panelState: "expanded" });
    renderGuide();
    await userEvent.click(screen.getByTestId("setup-guide-item-use-custom-workflow"));
    await userEvent.click(screen.getByTestId("setup-guide-item-create-workflow"));
    await userEvent.click(screen.getByRole("button", { name: /bonus/i }));
    await userEvent.click(screen.getByTestId("setup-guide-item-create-preset"));
    await userEvent.click(screen.getByTestId("setup-guide-item-install-mcp"));
    const hrefs = onNavigate.mock.calls.map(([href]) => href);
    expect(hrefs).toEqual(["/workflows/library", "/workflow/new", "/settings/presets", "/settings/mcp"]);
    expect(new Set(hrefs).size).toBe(hrefs.length);
    expect(trackEvent).toHaveBeenCalledWith("checklist_step_completed", expect.objectContaining({ stepId: "create-workflow" }));
  });

  it("opens the journey picker and the API key modal from their items", async () => {
    setChecklistState({ panelState: "expanded" });
    renderGuide();
    await userEvent.click(screen.getByTestId("setup-guide-item-take-product-tour"));
    expect(resetTourProgress).toHaveBeenCalled();
    expect(onOpenTours).toHaveBeenCalledTimes(1);
    await userEvent.click(screen.getByTestId("setup-guide-item-add-api-key"));
    expect(openModal).toHaveBeenCalledTimes(1);
  });

  it("shows a done item as done, with no action", () => {
    setChecklistState({ panelState: "expanded", completedItems: new Set(["add-api-key"]) });
    renderGuide();
    const done = screen.getByTestId("setup-guide-item-add-api-key");
    expect(done).toHaveAttribute("data-complete", "true");
    expect(done.tagName).toBe("LI");
  });
});
