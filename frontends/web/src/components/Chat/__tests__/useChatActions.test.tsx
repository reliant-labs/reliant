/**
 * useChatActions is the one implementation behind the desktop ChatHeader menu
 * and the mobile Chat sheet. These pin the capability gate (no terminal where
 * the surface has none) and that the desktop menu fires the shared mutations.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import type { Surface } from "../../../lib/surface";

const mocks = vi.hoisted(() => ({
  archive: vi.fn(async () => {}),
  unarchive: vi.fn(),
  rename: vi.fn(async () => {}),
  notify: vi.fn(),
  createSession: vi.fn(),
  showTerminal: vi.fn(),
}));

vi.mock("../../../hooks/chat-queries", () => ({
  useChat: () => ({ data: { id: "chat-1", title: "Old title", worktreeId: "wt-1" } }),
  useRenameChat: () => ({ mutateAsync: mocks.rename }),
  useArchiveChat: () => ({ mutateAsync: mocks.archive }),
  useUnarchiveChat: () => ({ mutate: mocks.unarchive }),
}));
vi.mock("../../../hooks/message-queries", () => ({
  useMessages: () => ({ data: { messages: [] } }),
}));
vi.mock("../../../hooks/chat-actions", () => ({ compactChat: vi.fn() }));
vi.mock("../../../hooks/worktree-queries", () => ({
  resolveWorktree: async () => ({ id: "wt-1", is_main: true, name: "main", path: "/repo", project_id: "p1" }),
  getCachedMainWorktree: () => undefined,
}));
vi.mock("../../../hooks/worktree-actions", () => ({ loadMainWorktree: vi.fn() }));
vi.mock("../../../store/terminalStore", () => ({
  useTerminalStore: {
    getState: () => ({
      getWorktreeSessions: () => [],
      setActiveSession: vi.fn(),
      createSession: mocks.createSession,
      showTerminal: mocks.showTerminal,
    }),
  },
}));
vi.mock("../../../lib/toast-manager", () => ({
  toast: { notify: mocks.notify, success: vi.fn(), error: vi.fn(), info: vi.fn() },
}));

// ChatHeader's own collaborators, for the desktop-menu case below.
vi.mock("../../../store/chatStoreHooks", () => ({
  useContextUsage: () => null,
  useContextUsageByThread: () => ({}),
}));
vi.mock("../thread-views", () => ({ useThreads: () => [], ThreadTabs: () => null }));
vi.mock("../../runs/ChildRunsStrip", () => ({ ChildRunsStrip: () => null }));
vi.mock("../NoMachine", () => ({ NoMachinePill: () => null }));
vi.mock("../WorkspaceRecoveryMenuItems", () => ({ WorkspaceRecoveryMenuItems: () => null }));

const { useChatActions } = await import("../useChatActions");
const { ChatHeader } = await import("../ChatHeader");

function wrapperFor(surface: Surface) {
  return ({ children }: { children: ReactNode }) => <SurfaceProvider surface={surface}>{children}</SurfaceProvider>;
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("useChatActions terminal capability", () => {
  it("offers Open Terminal on desktop once the workspace resolves", async () => {
    const { result } = renderHook(() => useChatActions("chat-1"), { wrapper: wrapperFor("desktop") });
    await waitFor(() => expect(result.current.openTerminal).toBeDefined());
    result.current.openTerminal!();
    expect(mocks.createSession).toHaveBeenCalledWith("/repo", "p1", "wt-1");
    expect(mocks.showTerminal).toHaveBeenCalled();
  });

  it.each(["mobile", "embed"] as const)("offers no terminal on %s", async (surface) => {
    const { result } = renderHook(() => useChatActions("chat-1"), { wrapper: wrapperFor(surface) });
    await waitFor(() => expect(result.current.worktree).not.toBeNull());
    expect(result.current.openTerminal).toBeUndefined();
  });
});

describe("useChatActions rename", () => {
  it("skips the mutation when nothing changed", async () => {
    const { result } = renderHook(() => useChatActions("chat-1"), { wrapper: wrapperFor("desktop") });
    expect(await result.current.rename("  Old title ")).toBe(true);
    expect(mocks.rename).not.toHaveBeenCalled();
  });
});

describe("ChatHeader menu runs the shared actions", () => {
  it("archives with Undo and offers Open Terminal on desktop", async () => {
    const user = userEvent.setup();
    render(<ChatHeader chatId="chat-1" />, { wrapper: wrapperFor("desktop") });

    await user.click(screen.getByRole("button", { name: "More options" }));
    expect(await screen.findByRole("button", { name: "Open Terminal" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Archive chat" }));
    expect(mocks.archive).toHaveBeenCalledWith("chat-1");
    await waitFor(() => expect(mocks.notify).toHaveBeenCalledWith("Chat archived", expect.anything()));
  });
});
