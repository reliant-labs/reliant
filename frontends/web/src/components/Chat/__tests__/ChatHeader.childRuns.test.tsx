/**
 * The runs an agent started from this chat, in the top bar. The header used
 * to carry a strip with a link to each run; the restyle folded it into one
 * quiet "N runs" control, which must still open each run (and the filtered
 * Runs list), not only the list.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { RunDisplayState } from "../../../gen/reliant/v1/run_pb";
import { renderRunsAt } from "../../runs/__tests__/runTestUtils";

const state = vi.hoisted(() => ({
  runs: [] as Array<{ chatId: string; title: string; displayState: number }>,
  hasMore: false,
}));

vi.mock("../../../services/controlPlane/config", () => ({ hasControlPlane: true }));
vi.mock("../thread-views/useChatThreadMode", () => ({
  useChatThreadMode: () => ({ mode: "detailed", preference: "auto" }),
  setChatThreadMode: vi.fn(),
}));
vi.mock("../../../hooks/chat-queries", () => ({
  useChat: () => ({ data: { id: "chat-1", title: "Deploy fix", worktreeId: "wt-1" } }),
  useRenameChat: () => ({ mutateAsync: vi.fn() }),
  useArchiveChat: () => ({ mutateAsync: vi.fn() }),
  useUnarchiveChat: () => ({ mutate: vi.fn() }),
}));
vi.mock("../../../hooks/message-queries", () => ({ useMessages: () => ({ data: { messages: [] } }) }));
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
vi.mock("../../../hooks/run-queries", () => ({
  useChildRuns: () => ({ data: state.runs.length ? { runs: state.runs, hasMore: state.hasMore } : undefined }),
}));
vi.mock("../NoMachine", () => ({ NoMachinePill: () => null }));
vi.mock("../WorkspaceRecoveryMenuItems", () => ({ WorkspaceRecoveryMenuItems: () => null }));
vi.mock("../MoveChatToProjectDialog", () => ({ MoveChatToProjectDialog: () => null }));
vi.mock("../../Sharing/ShareChatDialog", () => ({ ShareChatDialog: () => null }));

const { ChatHeader } = await import("../ChatHeader");

function renderHeader() {
  return renderRunsAt(
    <SurfaceProvider surface="desktop">
      <ChatHeader chatId="chat-1" />
    </SurfaceProvider>,
    "/workflows/runs/chat-1",
  );
}

beforeEach(() => {
  state.runs = [];
  state.hasMore = false;
});

describe("ChatHeader runs started here", () => {
  it("opens each run, with its state, and the Runs list filtered to them", async () => {
    state.runs = [
      { chatId: "c1", title: "Fix flaky test", displayState: RunDisplayState.RUNNING },
      { chatId: "c2", title: "", displayState: RunDisplayState.FAILED },
    ];
    renderHeader();
    const user = userEvent.setup();

    const trigger = await screen.findByRole("button", { name: "2 runs started here" });
    await user.click(trigger);

    const first = await screen.findByRole("menuitem", { name: /Fix flaky test/ });
    expect(first).toHaveAttribute("href", "/workflows/runs/c1");
    expect(first.querySelector('[data-run-status="running"]')).not.toBeNull();
    const second = screen.getByRole("menuitem", { name: /Untitled run/ });
    expect(second).toHaveAttribute("href", "/workflows/runs/c2");
    expect(second.querySelector('[data-run-status="failed"]')).not.toBeNull();
    expect(screen.getByRole("menuitem", { name: "All runs started here" })).toHaveAttribute(
      "href",
      "/workflows/runs?parent=chat-1",
    );
  });

  it("renders nothing for a chat that started no runs", async () => {
    renderHeader();
    await screen.findByRole("button", { name: "More options" });
    expect(screen.queryByTestId("child-runs-link")).not.toBeInTheDocument();
  });
});
