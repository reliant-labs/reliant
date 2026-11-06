import { beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SurfaceProvider } from "../../../lib/surfaceContext";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, params: _params, to, ...props }: { children?: React.ReactNode; params?: unknown; to: string }) => (
    <a href={to} {...props}>{children}</a>
  ),
}));

const mocks = vi.hoisted(() => ({
  mode: "detailed" as "simplified" | "detailed",
  setChatThreadMode: vi.fn(),
}));

vi.mock("../../Chat/thread-views/useChatThreadMode", () => ({
  useChatThreadMode: () => ({ mode: mocks.mode, preference: "auto" }),
  setChatThreadMode: mocks.setChatThreadMode,
}));
vi.mock("../../../hooks/chat-queries", () => ({
  useChat: () => ({ data: { id: "chat-1", title: "Chat", worktreeId: "wt-1" } }),
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
vi.mock("../../Chat/WorkspaceRecoveryMenuItems", () => ({ WorkspaceRecoveryMenuItems: () => null }));

const { MobileChatSheet } = await import("../MobileChatSheet");

function renderSheet() {
  const onClose = vi.fn();
  render(
    <SurfaceProvider surface="mobile">
      <MobileChatSheet
        chatId="chat-1"
        title="Chat"
        worktreeId="wt-1"
        threads={[]}
        hasMultipleThreads={false}
        selectedThreadId={null}
        onClose={onClose}
        onSelectThread={vi.fn()}
      />
    </SurfaceProvider>,
  );
  return onClose;
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.mode = "detailed";
});

describe("MobileChatSheet Simplified view toggle", () => {
  it("switches to simplified and closes the sheet", async () => {
    const user = userEvent.setup();
    const onClose = renderSheet();
    const row = screen.getByRole("button", { name: /Simplified view/ });
    expect(row).toHaveAttribute("aria-pressed", "false");
    await user.click(row);
    expect(mocks.setChatThreadMode).toHaveBeenCalledWith("simplified");
    expect(onClose).toHaveBeenCalled();
  });

  it("switches to detailed when already simplified", async () => {
    mocks.mode = "simplified";
    const user = userEvent.setup();
    renderSheet();
    const row = screen.getByRole("button", { name: /Simplified view/ });
    expect(row).toHaveAttribute("aria-pressed", "true");
    await user.click(row);
    expect(mocks.setChatThreadMode).toHaveBeenCalledWith("detailed");
  });
});
