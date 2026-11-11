/**
 * useChatActions is the one implementation behind the desktop ChatHeader menu
 * and the mobile Chat sheet. These pin the capability gate (no terminal where
 * the surface has none) and that the desktop menu fires the shared mutations.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, renderHook, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { QueryClientProvider } from "@tanstack/react-query";
import { createTestQueryClient } from "@/test/renderWithQuery";
import { mockTransport } from "@/lib/test-utils";
import { setTransport } from "@/lib/connect";
import { useAuthStore } from "@/store/authStore";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import type { Surface } from "../../../lib/surface";

const mocks = vi.hoisted(() => ({
  archive: vi.fn(async () => {}),
  unarchive: vi.fn(),
  rename: vi.fn(async () => {}),
  notify: vi.fn(),
  createSession: vi.fn(),
  showTerminal: vi.fn(),
  resolveWorktree: vi.fn(),
  resolveMainWorktree: vi.fn(),
  loadMainWorktree: vi.fn(),
}));

const mainRow = { id: "wt-main", is_main: true, name: "main", path: "/repo", project_id: "p1" };
const branchRow = { id: "wt-1", is_main: false, name: "feature", path: "/repo-feature", project_id: "p1" };

vi.mock("../../../hooks/chat-queries", () => ({
  useChat: () => ({ data: { id: "chat-1", title: "Old title", worktreeId: "wt-1" } }),
  useRenameChat: () => ({ mutateAsync: mocks.rename }),
  useArchiveChat: () => ({ mutateAsync: mocks.archive }),
  useUnarchiveChat: () => ({ mutate: mocks.unarchive }),
  useMoveChatToProject: () => ({ mutateAsync: vi.fn(), isPending: false }),
}));
vi.mock("../../../hooks/message-queries", () => ({
  useMessages: () => ({ data: { messages: [] } }),
}));
vi.mock("../../../hooks/chat-actions", () => ({ compactChat: vi.fn() }));
vi.mock("../../../hooks/worktree-queries", () => ({
  resolveWorktree: mocks.resolveWorktree,
  resolveMainWorktree: mocks.resolveMainWorktree,
}));
// Reading main for the recovery items must never select a workspace: the
// chat's own open is still switching to its workspace while this runs.
vi.mock("../../../hooks/worktree-actions", () => ({ loadMainWorktree: mocks.loadMainWorktree }));
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
  // ChatHeader mounts the real project picker even while closed. Match the
  // app's query context, with a fresh client for every render/hook fixture.
  const client = createTestQueryClient();
  return ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={client}>
      <SurfaceProvider surface={surface}>{children}</SurfaceProvider>
    </QueryClientProvider>
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  useAuthStore.setState({ session: { access_token: "t" } as never });
  setTransport(mockTransport({
    "ProjectService.ListProjects": { response: { projects: [{ id: "p2", name: "Other project" }] } },
    "ProjectService.ListProjectDaemons": { response: { projectDaemons: [] } },
  }));
  mocks.resolveWorktree.mockResolvedValue({ ...mainRow, id: "wt-1" });
  mocks.resolveMainWorktree.mockResolvedValue(mainRow);
});

describe("useChatActions workspace", () => {
  it("reads a branch chat's main workspace without selecting it", async () => {
    mocks.resolveWorktree.mockResolvedValue(branchRow);
    const { result } = renderHook(() => useChatActions("chat-1"), { wrapper: wrapperFor("desktop") });
    await waitFor(() => expect(result.current.mainWorktree).toEqual(mainRow));
    expect(result.current.worktree).toEqual(branchRow);
    expect(mocks.resolveMainWorktree).toHaveBeenCalledWith("p1");
    expect(mocks.loadMainWorktree).not.toHaveBeenCalled();
  });
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

  it("opens the real project picker with the menu's query context", async () => {
    const user = userEvent.setup();
    render(<ChatHeader chatId="chat-1" />, { wrapper: wrapperFor("desktop") });

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "More options" }));
    await user.click(screen.getByRole("button", { name: "Move to project…" }));

    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(await screen.findByRole("radio", { name: /Other project/ })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Move to project…" })).not.toBeInTheDocument();
    expect(mocks.archive).not.toHaveBeenCalled();
  });
});
