/**
 * The Header's Settings button is there only while there is no project.
 *
 * With no project yet (the first-run state, or a machine that never
 * provisions) there is no nav, so the header gear is the only route to
 * /settings, and so to signing out.
 *
 * With a project open there is no header gear: owner decision, 2026-11-24
 * (the one top bar, Variant A). Settings is the nav's footer row and ⌘,.
 * A review that asks for "Settings in the header" is answered by that
 * decision, not by restoring the gear: #3309's pr-fix did, and the owner
 * reverted it. The tests below hold both halves — no gear, and Settings
 * still reachable.
 */
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

const projectState = vi.hoisted(() => ({
  currentProject: null as null | { id: string; name: string },
}));

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) =>
    selector(projectState),
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
import { hrefOf, hybridNav, type NavCtx } from "../sidebar/navConfig";
import { defaultShortcuts } from "../../../store/shortcutsData.generated";
import { settingsPath } from "@/lib/paths";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";

describe("Header settings escape hatch", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    projectState.currentProject = null;
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

  it.each([false, true])("has no Settings gear with a project open (owner decision), sidebar expanded=%s", (expanded) => {
    projectState.currentProject = { id: "project-1", name: "Project" };
    useWorkspaceStateStore.setState({ leftSidebarExpanded: expanded });
    render(<Header onNavigateToSettings={vi.fn()} />);

    expect(screen.queryByTestId("header-settings-button")).toBeNull();
    expect(screen.queryByRole("button", { name: "Settings" })).toBeNull();
  });

  it("keeps Settings reachable with a project open: the nav's footer row and ⌘,", () => {
    // The nav's footer, drawn on every page of the shell, ends with Settings.
    const footerSettings = hybridNav.footer.find((item) => item.id === "settings");
    expect(footerSettings?.kind).toBe("row");
    expect(footerSettings && hrefOf(footerSettings, { pathname: "/c/chat-1", search: {} } as NavCtx)).toBe(settingsPath());

    // ⌘, opens Settings on desktop and on the web (bindings.test.ts proves
    // the chord fires on every surface).
    const shortcut = defaultShortcuts.toggleSettings;
    expect(shortcut.handler).toBe("onToggleSettings");
    expect(shortcut.defaultBinding).toBe("Cmd+,");
    expect(shortcut.defaultWebBinding).toBe("Cmd+,");
  });

  it("leads with the Reliant mark, a link home that stays in the SPA on a plain click", async () => {
    // The mark is the header's while the sidebar is collapsed; open, the
    // sidebar's top row carries it.
    useWorkspaceStateStore.setState({ leftSidebarExpanded: false });
    const onNavigateHome = vi.fn();
    render(<Header onNavigateHome={onNavigateHome} />);

    const mark = screen.getByRole("link", { name: "Reliant home" });
    expect(mark).toHaveAttribute("href", "/");
    expect(mark.querySelector("svg")).not.toBeNull();

    await userEvent.click(mark);
    expect(onNavigateHome).toHaveBeenCalledTimes(1);

    // A modifier-click is the browser's (new tab), not the router's.
    fireEvent.click(mark, { metaKey: true });
    expect(onNavigateHome).toHaveBeenCalledTimes(1);
  });

  it("leaves the mark to the sidebar while the sidebar is open", () => {
    useWorkspaceStateStore.setState({ leftSidebarExpanded: true });
    render(<Header onNavigateHome={vi.fn()} />);
    expect(screen.queryByRole("link", { name: "Reliant home" })).toBeNull();
  });
});
