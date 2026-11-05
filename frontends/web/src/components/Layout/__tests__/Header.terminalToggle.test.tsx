/**
 * New workspaces start with the terminal closed, so the Header's terminal
 * toggle is how it is found again: a labelled button whose tooltip names the
 * keyboard shortcut.
 *
 * (Mocks below mirror Header.settings.test.tsx.)
 */
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";

const SHORTCUTS = vi.hoisted(() => ({ current: {} as Record<string, unknown> }));
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
    selector({ shortcuts: SHORTCUTS.current }),
  effectiveBinding: (s: { defaultBinding: string }) => s.defaultBinding,
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

describe("Header terminal toggle", () => {
  beforeEach(() => {
    vi.stubGlobal(
      "matchMedia",
      vi.fn(() => ({
        matches: false,
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
      })),
    );
    SHORTCUTS.current = {
      toggleTerminal: { handler: "onToggleTerminal", defaultBinding: "ctrl+j" },
    };
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("is a labelled button that calls the toggle handler", async () => {
    const onToggleTerminal = vi.fn();
    render(<Header onToggleTerminal={onToggleTerminal} />);

    await userEvent.click(screen.getByRole("button", { name: "Toggle Terminal" }));

    expect(onToggleTerminal).toHaveBeenCalledTimes(1);
  });

  it("describes itself with the keyboard shortcut", () => {
    render(<Header onToggleTerminal={vi.fn()} />);

    const button = screen.getByRole("button", { name: "Toggle Terminal" });
    const hint = document.getElementById(button.getAttribute("aria-describedby") ?? "");
    expect(hint?.textContent).toMatch(/^Toggle Terminal \(.*J.*\)$/);
  });

  it("drops the empty parentheses when no shortcut is bound", () => {
    SHORTCUTS.current = {};
    render(<Header onToggleTerminal={vi.fn()} />);

    const button = screen.getByRole("button", { name: "Toggle Terminal" });
    const hint = document.getElementById(button.getAttribute("aria-describedby") ?? "");
    expect(hint?.textContent).toBe("Toggle Terminal");
  });
});
