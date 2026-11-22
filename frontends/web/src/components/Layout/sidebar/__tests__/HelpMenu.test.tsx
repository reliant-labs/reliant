/**
 * The nav footer's Help row: Take a tour (the journey picker), Docs, Keyboard
 * shortcuts, and "Show setup guide" only while a dismissed guide has work left.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import React from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const revivable = vi.hoisted(() => ({ current: false }));
vi.mock("@/components/Onboarding/OnboardingChecklist", () => ({ useSetupGuideRevivable: () => revivable.current }));

const storage = vi.hoisted(() => ({ kind: "settings" as "settings" | "local" }));
vi.mock("@/components/Onboarding/tourShell", () => ({ getTourStorage: () => ({ kind: storage.kind }) }));

const revive = vi.fn(async () => undefined);
const markComplete = vi.fn(async () => undefined);
vi.mock("@/store/onboardingChecklistStore", () => ({
  useOnboardingChecklistStore: { getState: () => ({ revive, markComplete }) },
}));

import { HelpMenu } from "../HelpMenu";

const onNavigate = vi.fn();
const onOpenTours = vi.fn();

async function openMenu() {
  render(React.createElement(HelpMenu, { onNavigate, onOpenTours }));
  await userEvent.click(screen.getByTestId("nav-help"));
}

describe("Help in the nav footer", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    revivable.current = false;
    storage.kind = "settings";
  });

  it("opens the journey picker", async () => {
    await openMenu();
    await userEvent.click(screen.getByTestId("help-menu-tour"));
    expect(onOpenTours).toHaveBeenCalledTimes(1);
  });

  it("opens the docs in a new tab and counts as reading them", async () => {
    const open = vi.spyOn(window, "open").mockReturnValue(null);
    await openMenu();
    await userEvent.click(screen.getByTestId("help-menu-docs"));
    expect(open).toHaveBeenCalledWith("https://docs.reliantlabs.io/", "_blank", "noopener,noreferrer");
    expect(markComplete).toHaveBeenCalledWith("read-docs");
    open.mockRestore();
  });

  it("opens Settings › Keyboard shortcuts through the nav's navigate", async () => {
    await openMenu();
    await userEvent.click(screen.getByTestId("help-menu-shortcuts"));
    expect(onNavigate).toHaveBeenCalledWith("/settings/shortcuts");
  });

  it("offers Show setup guide only while a dismissed guide can come back", async () => {
    await openMenu();
    expect(screen.queryByTestId("help-menu-setup-guide")).toBeNull();
  });

  it("brings a dismissed guide back", async () => {
    revivable.current = true;
    await openMenu();
    await userEvent.click(screen.getByTestId("help-menu-setup-guide"));
    expect(revive).toHaveBeenCalledTimes(1);
  });
});
