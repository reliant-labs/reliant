/**
 * The chat screen has one top bar (owner, 2026-11-23): the open chat's header
 * renders inside the app bar's slot (Layout/topBar.ts), and says nothing the
 * bar already says (the machine, the project name).
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, within } from "@testing-library/react";
import { SurfaceProvider } from "../../../lib/surfaceContext";

const state = vi.hoisted(() => ({
  chat: {} as Record<string, unknown>,
  activeDaemon: null as null | { daemonId: string; name: string },
}));

vi.mock("../../../hooks/useDaemonStatus", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../hooks/useDaemonStatus")>()),
  useDaemonStatus: () => ({ daemons: [], activeDaemon: state.activeDaemon, loading: false }),
}));
vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) => selector({ currentProject: { id: "p1", name: "shopfront" } }),
}));
vi.mock("../../../services/controlPlane/config", () => ({ hasControlPlane: true }));
vi.mock("../../../hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: [
    { daemonId: "other", name: "Other machine" },
    { daemonId: "assigned", name: "Build workstation" },
  ] }),
}));
vi.mock("../thread-views/useChatThreadMode", () => ({
  useChatThreadMode: () => ({ mode: "detailed", preference: "auto" }),
  setChatThreadMode: vi.fn(),
}));
vi.mock("../../../hooks/chat-queries", () => ({
  useChat: () => ({ data: state.chat }),
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
vi.mock("../../../hooks/run-queries", () => ({ useChildRuns: () => ({ data: undefined }) }));
// Who has access is its own test (ChatHeader.sharing); no server answers here.
vi.mock("../../../hooks/chat-member-queries", () => ({ useChatMembers: () => ({ data: undefined }) }));
vi.mock("../NoMachine", () => ({ NoMachinePill: () => <span>no machine</span> }));
vi.mock("../WorkspaceRecoveryMenuItems", () => ({ WorkspaceRecoveryMenuItems: () => null }));
vi.mock("../MoveChatToProjectDialog", () => ({ MoveChatToProjectDialog: () => null }));
vi.mock("../../Sharing/ShareChatDialog", () => ({
  ShareChatDialog: ({ open }: { open: boolean }) => (open ? <div role="dialog">share dialog</div> : null),
}));

// ChatAppTag links to the app (a router), which this suite does not mount.
vi.mock("../../Projects/ChatAppTag", () => ({ ChatAppTag: ({ name }: { name: string }) => <span>{name}</span> }));

const { ChatHeader } = await import("../ChatHeader");
const { useTopBarSlot } = await import("../../Layout/topBar");

function header(id = "chat-1") {
  return (
    <SurfaceProvider surface="desktop">
      <ChatHeader chatId={id} />
    </SurfaceProvider>
  );
}

let slot: HTMLElement;
beforeEach(() => {
  state.chat = { id: "chat-1", title: "Deploy fix", orgId: "acme", worktreeId: "wt-1", activeDaemonId: "assigned" };
  state.activeDaemon = null;
  slot = document.createElement("div");
  document.body.appendChild(slot);
  useTopBarSlot.setState({ el: slot, owner: null });
});
afterEach(() => {
  useTopBarSlot.setState({ el: null, owner: null });
  slot.remove();
});

describe("ChatHeader in the app bar", () => {
  it("renders into the bar's slot, not a row of its own", () => {
    render(header());
    expect(within(slot).getByRole("heading", { name: "Deploy fix" })).toBeInTheDocument();
    expect(within(slot).getByRole("button", { name: "More options" })).toBeInTheDocument();
    expect(document.querySelector("header[data-testid='chat-top-bar']")).toBeNull();
  });

  it("gives a second header its own row, and the slot once the first leaves", () => {
    const first = render(header());
    const second = render(header("chat-2"));
    expect(within(slot).getAllByTestId("chat-top-bar")).toHaveLength(1);
    expect(second.container.querySelector("header[data-testid='chat-top-bar']")).not.toBeNull();
    act(() => first.unmount());
    expect(within(slot).getAllByTestId("chat-top-bar")).toHaveLength(1);
    expect(second.container.querySelector("header[data-testid='chat-top-bar']")).toBeNull();
  });

  it("keeps its own row when there is no bar", () => {
    useTopBarSlot.setState({ el: null, owner: null });
    const { container } = render(header());
    expect(container.querySelector("header[data-testid='chat-top-bar']")).not.toBeNull();
  });
});

describe("ChatHeader says nothing the bar says", () => {
  it("leaves the machine to the bar when the chat runs on the active one", () => {
    state.activeDaemon = { daemonId: "assigned", name: "Build workstation" };
    render(header());
    expect(screen.queryByText(/^Machine:/)).not.toBeInTheDocument();
  });

  it("names the chat's machine when it is not the bar's", () => {
    state.activeDaemon = { daemonId: "other", name: "Other machine" };
    render(header());
    expect(screen.getByText("Machine: Build workstation")).toBeInTheDocument();
  });

  it("drops the app tag that repeats the project name, keeps one that does not", () => {
    state.chat = { ...state.chat, appName: "shopfront" };
    const { unmount } = render(header());
    expect(within(slot).queryByText("shopfront")).not.toBeInTheDocument();
    unmount();
    state.chat = { ...state.chat, appName: "billing-api" };
    render(header());
    expect(within(slot).getByText("billing-api")).toBeInTheDocument();
  });
});

describe("ChatHeader's scrolled-away prompt slot", () => {
  function withWidth(wide: boolean) {
    vi.stubGlobal(
      "matchMedia",
      vi.fn((query: string) => ({ matches: wide && query === "(min-width: 1536px)", addEventListener: vi.fn(), removeEventListener: vi.fn() })),
    );
  }
  afterEach(() => vi.unstubAllGlobals());

  it("is offered to the timeline in a wide bar", () => {
    withWidth(true);
    const ref = vi.fn();
    render(
      <SurfaceProvider surface="desktop">
        <ChatHeader chatId="chat-1" pinnedPromptSlotRef={ref} />
      </SurfaceProvider>,
    );
    expect(ref).toHaveBeenCalledWith(within(slot).getByTestId("chat-pinned-prompt-slot"));
  });

  it("is not mounted in a narrower bar, so the timeline keeps the prompt in its own strip", () => {
    withWidth(false);
    const ref = vi.fn();
    render(
      <SurfaceProvider surface="desktop">
        <ChatHeader chatId="chat-1" pinnedPromptSlotRef={ref} />
      </SurfaceProvider>,
    );
    expect(screen.queryByTestId("chat-pinned-prompt-slot")).not.toBeInTheDocument();
    // Its first render precedes claiming the bar (its own row, briefly); settled, the timeline holds no slot.
    expect(ref.mock.calls.at(-1)?.[0]).toBeNull();
  });
});
