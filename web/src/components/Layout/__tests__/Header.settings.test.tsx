/**
 * The Header's Settings button must survive project-picker mode.
 *
 * The app's only other Settings link lives in the Sidebar, which renders
 * exclusively inside ModernApp's main app shell — and that shell requires a
 * selected project. A user whose daemon never provisions never gets a project,
 * so if this button is gated on `!projectPickerMode` (as every other control
 * on the row is) there is no route to /settings at all, and therefore no way
 * to sign out.
 */
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) =>
    selector({ currentProject: null }),
}));

vi.mock("../../../store/worktreeStore", () => ({
  useWorktreeStore: (selector: (s: unknown) => unknown) =>
    selector({ currentWorktree: null, worktrees: [] }),
}));

vi.mock("../../../store/shortcutsStore", () => ({
  useShortcutsStore: (selector: (s: unknown) => unknown) =>
    selector({ shortcuts: {} }),
}));

vi.mock("../../../hooks/useTitleBarChrome", () => ({
  useTitleBarChrome: () => ({
    isElectron: false,
    trafficLightPadding: 0,
    dragRegionStyle: {},
    noDragRegionStyle: {},
    showWindowControls: false,
  }),
}));

// Chrome that only renders outside picker mode — stubbed so the Header can
// mount without their data dependencies.
vi.mock("../ConfigHealthIndicator", () => ({
  ConfigHealthIndicator: () => null,
}));
vi.mock("../DaemonStatusDot", () => ({ DaemonStatusDot: () => null }));
vi.mock("../DetectedPortsChip", () => ({ DetectedPortsChip: () => null }));

import { Header } from "../Header";

describe("Header settings escape hatch", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // The Header's theme effect reads matchMedia, which jsdom does not
    // implement. Irrelevant to what these tests assert.
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({
        matches: false,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      })),
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("renders the Settings button in project-picker mode", async () => {
    const onNavigateToSettings = vi.fn();
    render(
      <Header projectPickerMode onNavigateToSettings={onNavigateToSettings} />,
    );

    const button = screen.getByTestId("header-settings-button");
    await userEvent.click(button);

    expect(onNavigateToSettings).toHaveBeenCalledTimes(1);
  });

  it("renders it outside picker mode too", () => {
    render(
      <Header projectPickerMode={false} onNavigateToSettings={vi.fn()} />,
    );
    expect(screen.getByTestId("header-settings-button")).toBeInTheDocument();
  });
});
