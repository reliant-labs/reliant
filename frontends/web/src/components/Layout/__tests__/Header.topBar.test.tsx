/**
 * The one top bar (owner, 2026-11-23): a left-aligned project / branch path,
 * then the slot the open chat's header renders into; Settings remains
 * accessible even when the project nav is absent; and in the macOS app a
 * docked nav's own toggle stands in for the bar's.
 */
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, within } from "@testing-library/react";

const state = vi.hoisted(() => ({
  chrome: { isMac: false, isFullscreen: false },
}));

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) =>
    selector({ currentProject: { id: "p1", name: "shopfront", default_branch: "main" } }),
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
    isElectron: state.chrome.isMac,
    isMac: state.chrome.isMac,
    isFullscreen: state.chrome.isFullscreen,
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
import { useTopBarSlot } from "../topBar";

describe("Header: the one top bar", () => {
  beforeEach(() => {
    state.chrome = { isMac: false, isFullscreen: false };
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() })),
    );
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    useTopBarSlot.setState({ el: null, owner: null });
  });

  it("leads with the project / branch path, the project opening the switcher", () => {
    render(<Header onToggleChatSidebar={vi.fn()} />);
    const path = screen.getByRole("navigation", { name: "Project" });
    expect(within(path).getByTestId("header-project-name")).toHaveTextContent("shopfront");
    expect(within(path).getByText("main")).toBeInTheDocument();
  });

  it("offers the chat's header a slot after the path", () => {
    render(<Header />);
    expect(useTopBarSlot.getState().el).toBe(screen.getByTestId("topbar-chat-slot"));
  });

  it("keeps Settings accessible while a project is open without the nav", () => {
    render(<Header onNavigateToSettings={vi.fn()} />);
    expect(screen.getByTestId("header-settings-button")).toBeInTheDocument();
  });

  it("keeps its sidebar toggle beside a collapsed nav, and in a browser", () => {
    const { unmount } = render(<Header windowAligned={false} onToggleChatSidebar={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Toggle Chat Sidebar" })).toBeInTheDocument();
    unmount();
    state.chrome = { isMac: true, isFullscreen: false };
    render(<Header windowAligned onToggleChatSidebar={vi.fn()} />);
    expect(screen.getByRole("button", { name: "Toggle Chat Sidebar" })).toBeInTheDocument();
  });

  it("drops its toggle in the macOS app while the docked nav has its own by the traffic lights", () => {
    state.chrome = { isMac: true, isFullscreen: false };
    render(<Header windowAligned={false} onToggleChatSidebar={vi.fn()} />);
    expect(screen.queryByRole("button", { name: "Toggle Chat Sidebar" })).not.toBeInTheDocument();
  });
});
