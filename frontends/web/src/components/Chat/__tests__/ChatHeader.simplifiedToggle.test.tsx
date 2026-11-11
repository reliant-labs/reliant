import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SurfaceProvider } from "../../../lib/surfaceContext";

const mocks = vi.hoisted(() => ({
  mode: "detailed" as "simplified" | "detailed",
  setChatThreadMode: vi.fn(),
}));

vi.mock("../thread-views/useChatThreadMode", () => ({
  useChatThreadMode: () => ({ mode: mocks.mode, preference: "auto" }),
  setChatThreadMode: mocks.setChatThreadMode,
}));
vi.mock("../../../hooks/chat-queries", () => ({
  useChat: () => ({ data: { id: "chat-1", title: "Old title", worktreeId: "wt-1" } }),
  useRenameChat: () => ({ mutateAsync: vi.fn() }),
  useArchiveChat: () => ({ mutateAsync: vi.fn() }),
  useUnarchiveChat: () => ({ mutate: vi.fn() }),
}));
vi.mock("../../../hooks/message-queries", () => ({
  useMessages: () => ({ data: { messages: [] } }),
}));
vi.mock("../../../hooks/chat-actions", () => ({ compactChat: vi.fn() }));
vi.mock("../../../hooks/worktree-queries", () => ({
  resolveWorktree: async () => null,
  getCachedMainWorktree: () => undefined,
}));
vi.mock("../../../hooks/worktree-actions", () => ({ loadMainWorktree: vi.fn() }));
vi.mock("../../../lib/toast-manager", () => ({
  toast: { notify: vi.fn(), success: vi.fn(), error: vi.fn(), info: vi.fn() },
}));
vi.mock("../../../store/chatStoreHooks", () => ({
  useContextUsage: () => null,
  useContextUsageByThread: () => ({}),
}));
vi.mock("../thread-views", () => ({ useThreads: () => [], ThreadTabs: () => null }));
vi.mock("../../runs/ChildRunsStrip", () => ({ ChildRunsStrip: () => null }));
vi.mock("../NoMachine", () => ({ NoMachinePill: () => null }));
vi.mock("../WorkspaceRecoveryMenuItems", () => ({ WorkspaceRecoveryMenuItems: () => null }));
vi.mock("../MoveChatToProjectDialog", () => ({
  MoveChatToProjectDialog: ({ open, chatId }: { open: boolean; chatId: string }) =>
    open ? <div role="dialog">Move {chatId}</div> : null,
}));

const { ChatHeader } = await import("../ChatHeader");

async function openMenu() {
  const user = userEvent.setup();
  render(
    <SurfaceProvider surface="desktop">
      <ChatHeader chatId="chat-1" />
    </SurfaceProvider>,
  );
  await user.click(screen.getByRole("button", { name: "More options" }));
  return user;
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.mode = "detailed";
});

describe("ChatHeader Simplified view toggle", () => {
  it("is unchecked in detailed mode and switches to simplified, closing the menu", async () => {
    const user = await openMenu();
    const item = screen.getByRole("button", { name: "Simplified view" });
    expect(item).toHaveAttribute("aria-pressed", "false");
    await user.click(item);
    expect(mocks.setChatThreadMode).toHaveBeenCalledWith("simplified");
    expect(screen.queryByRole("button", { name: "Simplified view" })).not.toBeInTheDocument();
  });

  it("is checked in simplified mode and switches to detailed", async () => {
    mocks.mode = "simplified";
    const user = await openMenu();
    const item = screen.getByRole("button", { name: "Simplified view" });
    expect(item).toHaveAttribute("aria-pressed", "true");
    await user.click(item);
    expect(mocks.setChatThreadMode).toHaveBeenCalledWith("detailed");
  });
});

describe("ChatHeader Move to project", () => {
  it("opens the project picker for this chat, closing the menu", async () => {
    const user = await openMenu();
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Move to project…" }));
    expect(screen.getByRole("dialog")).toHaveTextContent("Move chat-1");
    expect(screen.queryByRole("button", { name: "Move to project…" })).not.toBeInTheDocument();
  });
});
