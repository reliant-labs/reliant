/**
 * The composer's "Waking <machine>…" line (research/WORKFLOW_UI.md §9.2).
 *
 * The server wakes the chat's daemon INSIDE the send RPC
 * (wakeDaemonForAttendedTurn, bounded at 30 s) and only then answers. So a
 * wake is underway exactly while a send is in flight AND the machine that
 * send wakes — the chat's pinned daemon — is asleep or starting in the
 * registry. Those are the only inputs; nothing is inferred from timing.
 *
 * An unpinned chat wakes whatever default resolution picks on the server,
 * which the web cannot see, so it shows no line rather than guess a machine.
 */

import { act, screen, fireEvent, waitFor } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { DaemonInfoSchema, DaemonStatus, type DaemonInfo } from "../../../gen/reliant/v1/daemon_registry_pb";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { ChatPresenter } from "../ChatPresenter";

const state = vi.hoisted(() => ({
  chat: undefined as { id: string; activeDaemonId?: string; launchKind?: string } | undefined,
  daemons: [] as DaemonInfo[],
}));

vi.mock("../../../hooks/chat-queries", () => ({
  useChat: () => ({ data: state.chat }),
}));
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: state.daemons, isLoading: false }),
}));
vi.mock("../../../hooks/queued-agent-messages", () => ({
  useQueuedAgentMessages: () => ({ messages: [], refresh: vi.fn(), forget: vi.fn() }),
}));
// The composer is a single Send button here: this suite is about the line
// beside it, and the real composer pulls in the whole input stack.
vi.mock("../ChatInputWrapper", () => ({
  ChatInputWrapper: ({ onSend }: { onSend: (message: string) => Promise<void> }) => (
    <button type="button" onClick={() => void onSend("hello").catch(() => {})}>
      Send
    </button>
  ),
}));
vi.mock("../ChatThinkingIndicator", () => ({ ChatThinkingIndicator: () => null }));
vi.mock("../ChatMessagesContainer", () => ({
  ChatMessagesContainer: ({ children }: { children?: React.ReactNode }) => <div>{children}</div>,
}));
vi.mock("../ScrollToBottomButton", () => ({ ScrollToBottomButton: () => null }));
vi.mock("../PermissionsPanelWrapper", () => ({
  PermissionsPanelWrapper: ({ children }: { children?: React.ReactNode }) => <>{children}</>,
}));
vi.mock("../PermissionsPanel", () => ({ PermissionsPanel: () => null }));
vi.mock("../ChatHeader", () => ({ ChatHeader: () => null }));
vi.mock("../ResumeDaemonPill", () => ({ ResumeDaemonPill: () => null }));
vi.mock("../OomKillBanner", () => ({ OomKillBanner: () => null }));
vi.mock("../BackgroundWorkPill", () => ({ BackgroundWorkPill: () => null }));
vi.mock("../QueuedMessages", () => ({ QueuedMessages: () => null }));
vi.mock("../thread-views", () => ({ InterleavedTimeline: () => <div data-testid="timeline" /> }));
vi.mock("../../workflow/WorkflowViewerPanel", () => ({ WorkflowViewerPanel: () => null }));

function daemon(daemonId: string, hostname: string, status: DaemonStatus): DaemonInfo {
  return create(DaemonInfoSchema, { daemonId, hostname, status });
}

/** A send the test settles by hand, so "in flight" lasts as long as it needs. */
function deferredSend() {
  let resolve!: () => void;
  let reject!: (error: Error) => void;
  const promise = new Promise<void>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { send: vi.fn(() => promise), resolve, reject };
}

function renderPresenter(onSendMessage: () => Promise<void>) {
  return renderWithQuery(
    <SurfaceProvider surface="desktop">
      <ChatPresenter
        messages={[]}
        approvals={[]}
        errorEvents={[]}
        infoEvents={[]}
        runOutputs={[]}
        chatId="chat-1"
        isChatBusy={false}
        pendingApprovals={[]}
        connectionStatus="connected"
        projectId="project-1"
        onSendMessage={onSendMessage}
        onStopStreaming={vi.fn(async () => {})}
      />
    </SurfaceProvider>,
  );
}

beforeEach(() => {
  state.chat = { id: "chat-1", activeDaemonId: "d-1", launchKind: "chat.start" };
  state.daemons = [daemon("d-1", "MacBook", DaemonStatus.SUSPENDED)];
});

describe("ChatPresenter: Waking <machine>… while a send wakes the chat's machine", () => {
  it("shows while the send is in flight and the pinned machine is asleep, and clears when it returns", async () => {
    const pending = deferredSend();
    renderPresenter(pending.send);
    expect(screen.queryByRole("status", { name: /Waking/ })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    const line = await screen.findByRole("status", { name: "Waking MacBook…" });
    expect(line).toHaveTextContent("Waking MacBook…");

    await act(async () => pending.resolve());
    await waitFor(() => expect(screen.queryByRole("status", { name: /Waking/ })).toBeNull());
  });

  it("clears when the send fails too", async () => {
    const pending = deferredSend();
    renderPresenter(pending.send);
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await screen.findByRole("status", { name: "Waking MacBook…" });
    await act(async () => pending.reject(new Error("boom")));
    await waitFor(() => expect(screen.queryByRole("status", { name: /Waking/ })).toBeNull());
  });

  it("says Waking for a machine that is already starting", async () => {
    state.daemons = [daemon("d-1", "Cloud box", DaemonStatus.PENDING)];
    renderPresenter(deferredSend().send);
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    expect(await screen.findByRole("status", { name: "Waking Cloud box…" })).toBeInTheDocument();
  });

  it("shows nothing when the machine is awake: there is nothing to wake", async () => {
    state.daemons = [daemon("d-1", "MacBook", DaemonStatus.ACTIVE)];
    renderPresenter(deferredSend().send);
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await act(async () => {});
    expect(screen.queryByRole("status", { name: /Waking/ })).toBeNull();
  });

  it("shows nothing for an unpinned chat: the server picks the machine, the web cannot name it", async () => {
    state.chat = { id: "chat-1", launchKind: "chat.start" };
    renderPresenter(deferredSend().send);
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await act(async () => {});
    expect(screen.queryByRole("status", { name: /Waking/ })).toBeNull();
  });

  it("shows nothing when no send is in flight, even with the machine asleep", () => {
    renderPresenter(deferredSend().send);
    expect(screen.queryByRole("status", { name: /Waking/ })).toBeNull();
  });
});
