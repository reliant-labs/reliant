/**
 * What the new-chat screen shows between send and the chat existing.
 *
 * A new chat has no id until StartChat answers (p50 2.3s, up to 6.4s
 * measured), and the composer clears on send. Before this, that whole window
 * showed the unchanged welcome screen over an empty box, and a start that
 * failed lost the prompt for good: no chat, so no transcript to recover it
 * from. Now the message is shown as sent, and if the start fails the text is
 * handed back to the composer.
 */
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const startChat = vi.hoisted(() => vi.fn());
const selectChat = vi.hoisted(() => vi.fn());
const toastError = vi.hoisted(() => vi.fn());

function storeMock(state: () => any) {
  return Object.assign((selector?: any) => (selector ? selector(state()) : state()), {
    getState: () => state(),
    setState: vi.fn(),
    subscribe: vi.fn(() => () => undefined),
  });
}

const laptop = { daemonId: "d-laptop", hostname: "laptop", status: 1 };

vi.mock("../../../hooks/chat-queries", () => ({
  useChatList: () => ({ data: [], isSuccess: true }),
}));
vi.mock("../../../store/chatStore", () => ({
  useChatStore: storeMock(() => ({ hasLoaded: true, chats: new Map(), startChat, selectChat })),
}));
vi.mock("../../../store/worktreeStore", () => ({
  useWorktreeStore: storeMock(() => ({
    currentWorktree: { id: "w-main", branch: "main", name: "main", is_main: true },
    worktrees: [{ id: "w-main", branch: "main", name: "main", is_main: true }],
    switchWorktreeContext: vi.fn(),
    loadWorktrees: vi.fn(),
  })),
}));
vi.mock("../../../store/projectStore", () => ({
  useProjectStore: storeMock(() => ({ currentProject: { id: "p1", name: "proj", default_branch: "main" } })),
}));
vi.mock("../../../store/attachmentStore", () => ({
  useAttachmentStore: storeMock(() => ({ clearAttachments: vi.fn() })),
}));
vi.mock("../../../store/workspaceStateStore", () => ({
  useWorkspaceStateStore: storeMock(() => ({ clearNewChatDraft: vi.fn() })),
}));
vi.mock("../../../store/apiKeySetupStore", () => ({
  useApiKeySetupStore: storeMock(() => ({ ensureApiKeyOrShowModal: vi.fn() })),
}));
vi.mock("../../../store/chatParamsStore", () => ({
  useChatParamsStore: storeMock(() => ({ transferTempToChat: vi.fn() })),
}));
vi.mock("@/hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({ activeDaemon: laptop, daemons: [laptop], loading: false, refresh: vi.fn() }),
}));
vi.mock("@/hooks/useDaemonWait", () => ({
  useDaemonWait: () => ({ state: null, retryNow: vi.fn() }),
}));
vi.mock("@/hooks/useBundledDaemonPending", () => ({ useBundledDaemonPending: () => false }));
vi.mock("@/services/controlPlane/capabilities", () => ({ capabilities: { cloudDaemons: true } }));
// The composer: a Send button, plus whatever text a prefill last put in it.
vi.mock("../ChatInput", () => ({
  ChatInput: ({
    onSend,
    prefill,
  }: {
    onSend: (c: string) => Promise<void>;
    prefill?: { text: string; id: number };
  }) => (
    <div>
      <output data-testid="composer-prefill">{prefill?.text ?? ""}</output>
      <button type="button" onClick={() => void onSend("refactor the parser").catch(() => undefined)}>
        Send
      </button>
    </div>
  ),
}));
vi.mock("../ResumeDaemonPill", () => ({ ResumeDaemonPill: () => null }));
vi.mock("../OomKillBanner", () => ({ OomKillBanner: () => null }));
vi.mock("../../DaemonWaitState", () => ({ DaemonWaitState: () => null }));
vi.mock("../../Layout/ConnectDaemonModal", () => ({ ConnectDaemonModal: () => null }));
vi.mock("../../Worktrees/CreateWorktreeModal", () => ({ CreateWorktreeModal: () => null }));
vi.mock("../../Worktrees/DiscoverWorktreesModal", () => ({ DiscoverWorktreesModal: () => null }));
vi.mock("../../ui/Tooltip", () => ({ Tooltip: ({ children }: any) => <>{children}</> }));
vi.mock("../../icons/ReliantIcon", () => ({ ReliantIcon: () => null }));
vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("../../../lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("../../../lib/logger", () => ({ logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() } }));
vi.mock("sonner", () => ({ toast: { error: toastError, success: vi.fn() } }));

import { NewChatView } from "../NewChatView";

let settle: { resolve: (chat: { id: string }) => void; reject: (error: Error) => void };

beforeEach(() => {
  startChat.mockReset();
  selectChat.mockReset();
  toastError.mockReset();
  startChat.mockImplementation(
    () =>
      new Promise((resolve, reject) => {
        settle = { resolve, reject };
      }),
  );
  vi.spyOn(console, "error").mockImplementation(() => undefined);
});

describe("NewChatView while the chat is being created", () => {
  it("shows the message as sent in place of the welcome screen", async () => {
    render(<NewChatView tabId="t1" />);
    expect(screen.queryByTestId("pending-first-message")).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Send" }));

    const pending = await screen.findByTestId("pending-first-message");
    expect(pending).toHaveTextContent("refactor the parser");
    expect(pending).toHaveTextContent("Starting your chat…");
    expect(screen.queryByTestId("machine-picker")).toBeNull();

    await act(async () => settle.resolve({ id: "chat-new" }));
    expect(selectChat).toHaveBeenCalledWith({ id: "chat-new" });
  });

  it("hands the text back to the composer when the start fails", async () => {
    render(<NewChatView tabId="t1" />);
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await screen.findByTestId("pending-first-message");

    await act(async () => settle.reject(new Error("no daemon available")));

    await waitFor(() =>
      expect(screen.getByTestId("composer-prefill")).toHaveTextContent("refactor the parser"),
    );
    expect(screen.queryByTestId("pending-first-message")).toBeNull();
    expect(toastError).toHaveBeenCalledWith(
      "Couldn't start the chat",
      expect.objectContaining({ description: expect.stringContaining("back in the box") }),
    );
  });
});
