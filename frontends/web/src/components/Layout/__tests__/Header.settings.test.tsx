/**
 * The Header's Settings button must be there with no project.
 *
 * The app's only other Settings link lives in the Sidebar, which renders
 * exclusively inside ModernApp's main app shell — and that shell requires a
 * project. A user with none yet (the first-run state, or a machine that never
 * provisions) would otherwise have no route to /settings at all, and
 * therefore no way to sign out.
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
// Workspace rows are read through hooks/worktree-queries; answer them from the
// same fixture.
vi.mock("../../../hooks/worktree-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../hooks/worktree-queries")>()),
  ...(await import("../../../test/worktreeQueriesMock")).worktreeQueriesMock(() => ({ currentWorktree: null, worktrees: [] })),
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
// Reads the wallet through react-query, and this suite mounts the Header bare
// (no QueryClientProvider). The real app always has one — App.tsx wraps the
// whole router tree — so stubbing here matches the neighbours above rather than
// papering over a missing provider.
vi.mock("../../Billing/LowCreditSurface", () => ({
  LowCreditSurface: () => null,
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

  it("renders the Settings button with no project (the first-run state)", async () => {
    const onNavigateToSettings = vi.fn();
    render(
      <Header onNavigateToSettings={onNavigateToSettings} />,
    );

    const button = screen.getByTestId("header-settings-button");
    await userEvent.click(button);

    expect(onNavigateToSettings).toHaveBeenCalledTimes(1);
  });

});
