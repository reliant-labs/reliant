/**
 * The floating Setup Guide: a "Get started · n/8" pill on every shell page
 * that opens into a panel of the app's first steps.
 *
 * Pinned here:
 *   - who sees it: signed in, Reliant's settings storage, not dismissed, and
 *     work left (it floated over the login form once, and a finished guide sat
 *     at full progress forever);
 *   - the pill opens the panel; Escape, a press outside, or running an item
 *     puts it back; ✕ dismisses (Help › Show setup guide revives it);
 *   - each item goes somewhere of its own, and clicking one is not completing it;
 *   - where it sits: above the composer, never over it (guideBottom).
 */
import { describe, it, expect, vi, beforeEach } from "vitest";
import React from "react";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const authState = vi.hoisted(() => ({ current: { user: { id: "user-1" } as unknown } }));
vi.mock("../../../store/authStore", () => ({
  useAuthStore: Object.assign((selector?: any) => (selector ? selector(authState.current) : authState.current), {
    getState: () => authState.current,
  }),
}));

const setPanelState = vi.fn(async (panelState: string) => {
  checklistState.current = { ...checklistState.current, panelState };
});
const markComplete = vi.fn(async () => undefined);
const checklistState = vi.hoisted(() => ({ current: {} as any }));
vi.mock("../../../store/onboardingChecklistStore", () => ({
  useOnboardingChecklistStore: Object.assign(
    (selector?: any) => (selector ? selector(checklistState.current) : checklistState.current),
    { getState: () => checklistState.current, setState: vi.fn(), subscribe: vi.fn(() => () => undefined) },
  ),
}));

const openModal = vi.fn();
vi.mock("../../../store/apiKeySetupStore", () => ({
  useApiKeySetupStore: { getState: () => ({ openModal }) },
}));

const openAddProject = vi.fn();
vi.mock("../../../store/addProjectDialogStore", () => ({ openAddProject: (...args: unknown[]) => openAddProject(...args) }));

const storage = vi.hoisted(() => ({ kind: "settings" as "settings" | "local" }));
vi.mock("../tourShell", () => ({ getTourStorage: () => ({ kind: storage.kind }) }));

vi.mock("../../../lib/mobileRedirect", () => ({ isPhoneViewport: () => false }));
vi.mock("../../../lib/analytics", () => ({ trackEvent: vi.fn() }));

import { trackEvent } from "../../../lib/analytics";
import { homePath, sandboxesPath, workflowLibraryPath } from "@/lib/paths";
import { activeChecklistItems } from "../checklistItems";
import { guideBottom, OnboardingChecklist } from "../OnboardingChecklist";

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

describe("the floating Setup Guide", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    authState.current = { user: { id: "user-1" } };
    storage.kind = "settings";
    setChecklistState();
  });

  it("is a small pill, Get started with its count, until opened", () => {
    setChecklistState({ completedItems: new Set(["connect-machine", "add-api-key", "start-chat"]) });
    renderGuide();
    const toggle = screen.getByTestId("setup-guide-toggle");
    expect(toggle).toHaveTextContent(`Get started· 3/${activeChecklistItems().length}`);
    expect(screen.getByTestId("setup-guide")).toHaveAttribute("data-state", "collapsed");
    expect(screen.queryByRole("region", { name: "Setup guide" })).toBeNull();
  });

  it("opens into a panel listing every item that applies, in order", async () => {
    renderGuide();
    await userEvent.click(screen.getByTestId("setup-guide-toggle"));
    expect(setPanelState).toHaveBeenCalledWith("expanded");

    setChecklistState({ panelState: "expanded" });
    const { container } = renderGuide();
    const rows = container.querySelectorAll("[data-testid^='setup-guide-item-']");
    expect([...rows].map((row) => row.getAttribute("data-testid")!.replace("setup-guide-item-", ""))).toEqual(
      activeChecklistItems().map((item) => item.id),
    );
  });

  it("goes back to the pill on Escape and on a press outside it", () => {
    setChecklistState({ panelState: "expanded" });
    renderGuide();
    fireEvent.keyDown(window, { key: "Escape" });
    expect(setPanelState).toHaveBeenLastCalledWith("collapsed");

    setPanelState.mockClear();
    setChecklistState({ panelState: "expanded" });
    renderGuide();
    fireEvent.pointerDown(document.body);
    expect(setPanelState).toHaveBeenCalledWith("collapsed");
  });

  it("dismisses with ✕, from the pill as from the panel", async () => {
    renderGuide();
    await userEvent.click(screen.getByRole("button", { name: /dismiss setup guide/i }));
    expect(setPanelState).toHaveBeenCalledWith("dismissed");
  });

  it.each([
    ["dismissed", () => setChecklistState({ panelState: "dismissed" })],
    ["every item done", () => setChecklistState({ completedItems: new Set(activeChecklistItems().map((i) => i.id)) })],
    ["signed out", () => (authState.current = { user: null })],
    ["its state not loaded yet", () => setChecklistState({ isInitialized: false })],
    ["the forge shell (local tour storage)", () => (storage.kind = "local")],
  ])("draws nothing when %s", (_why, arrange) => {
    arrange();
    const { container } = renderGuide();
    expect(container).toBeEmptyDOMElement();
  });

  it("sends each item somewhere of its own, and a click completes nothing", async () => {
    const ids = activeChecklistItems().map((item) => item.id);
    for (const id of ids) {
      setChecklistState({ panelState: "expanded" });
      const { unmount } = renderGuide();
      await userEvent.click(screen.getByTestId(`setup-guide-item-${id}`));
      unmount();
    }
    const hrefs = onNavigate.mock.calls.map(([href]) => href);
    expect(new Set(hrefs).size).toBe(hrefs.length);
    expect(hrefs).toEqual(expect.arrayContaining([sandboxesPath(), homePath(), workflowLibraryPath()]));
    expect(openModal).toHaveBeenCalledTimes(1);
    expect(openAddProject).toHaveBeenCalledWith("clone");
    expect(onOpenTours).toHaveBeenCalledTimes(1);
    expect(markComplete).not.toHaveBeenCalled();
    expect(trackEvent).not.toHaveBeenCalledWith("checklist_step_completed", expect.anything());
    expect(trackEvent).toHaveBeenCalledWith("checklist_step_clicked", expect.objectContaining({ stepId: "connect-machine" }));
  });

  it("shows a done item as done, with no action", () => {
    setChecklistState({ panelState: "expanded", completedItems: new Set(["add-api-key"]) });
    renderGuide();
    const done = screen.getByTestId("setup-guide-item-add-api-key");
    expect(done).toHaveAttribute("data-complete", "true");
    expect(done.tagName).toBe("LI");
  });
});

describe("where the guide sits (guideBottom)", () => {
  const viewport = { width: 1440, height: 900 };
  const box = (left: number, top: number, right: number, bottom: number) => ({
    left,
    top,
    right,
    bottom,
    width: right - left,
    height: bottom - top,
  });

  it("is 16px off the bottom with nothing to clear", () => {
    expect(guideBottom([], viewport)).toBe(16);
  });

  it("sits 12px above the composer when the composer reaches its column", () => {
    expect(guideBottom([box(286, 805, 1410, 896)], viewport)).toBe(900 - 805 + 12);
  });

  it("follows the composer up as it grows", () => {
    expect(guideBottom([box(286, 600, 1410, 896)], viewport)).toBe(900 - 600 + 12);
  });

  it("ignores a composer that stops short of its column, and a bar that is not on the bottom edge", () => {
    expect(guideBottom([box(286, 805, 900, 896)], viewport)).toBe(16);
    expect(guideBottom([box(286, 300, 1410, 400)], viewport)).toBe(16);
  });
});
