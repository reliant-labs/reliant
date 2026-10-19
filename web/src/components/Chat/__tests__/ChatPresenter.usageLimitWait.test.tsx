/**
 * A run held until a provider's usage limit resets (server-side timer; it
 * retries by itself). The transcript footer says who it waits on, when it
 * carries on, with a live countdown, and offers Retry now and Stop. The reset
 * time rides on the chat; the activity says whether it applies.
 */

import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { ChatActivity, MessageRole } from "../../../gen/reliant/v1/chat_pb";
import { useActivityStore } from "../../../store/activityStore";
import { renderWithQuery } from "../../../test/renderWithQuery";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import type { Message } from "../../../api/client";
import { ChatPresenter } from "../ChatPresenter";

const state = vi.hoisted(() => ({
  chat: undefined as Record<string, unknown> | undefined,
  retry: vi.fn(async (_chatId: string, _useModel?: string) => true),
  selection: null as Record<string, unknown> | null,
  models: [] as Array<Record<string, unknown>>,
  tiers: {} as Record<string, unknown>,
  accounts: [] as unknown[],
}));

vi.mock("../../../hooks/chat-queries", () => ({
  useChat: () => ({ data: state.chat }),
  useChatSelection: () => ({ data: state.selection }),
}));
vi.mock("../../../store/globalDataStore", () => ({
  useModels: () => ({ models: state.models, tiers: state.tiers, loading: false, error: null }),
}));
vi.mock("../../../hooks/provider-account-queries", () => ({
  useProviderAccounts: () => ({ data: { kind: "ready", accounts: state.accounts } }),
}));
vi.mock("../../../api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../../api/client")>();
  return { ...actual, api: { ...actual.api, chatsV2: { ...actual.api.chatsV2, retryUsageLimitWait: state.retry } } };
});
vi.mock("@/hooks/useOnboardingQueries", () => ({
  useDaemonList: () => ({ data: [], isLoading: false }),
  useResumeDaemon: () => ({ mutate: vi.fn(), isPending: false }),
}));
vi.mock("../../../hooks/queued-agent-messages", () => ({
  useQueuedAgentMessages: () => ({ messages: [], refresh: vi.fn(), forget: vi.fn() }),
}));
vi.mock("../ChatInputWrapper", () => ({ ChatInputWrapper: () => null }));
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
vi.mock("../DiskPressureBanner", () => ({ DiskPressureBanner: () => null }));
vi.mock("../BackgroundWorkPill", () => ({ BackgroundWorkPill: () => null }));
vi.mock("../QueuedMessages", () => ({ QueuedMessages: () => null }));
vi.mock("../thread-views", () => ({
  InterleavedTimeline: ({ footer }: { footer?: React.ReactNode }) => <div data-testid="timeline">{footer}</div>,
}));
vi.mock("../../workflow/WorkflowViewerPanel", () => ({ WorkflowViewerPanel: () => null }));

function message(id: string, role: MessageRole): Message {
  return { id, role, content: id, thread: "chat-1" } as unknown as Message;
}

const onStop = vi.fn(async () => {});

function renderPresenter() {
  return renderWithQuery(
    <SurfaceProvider surface="desktop">
      <ChatPresenter
        messages={[message("m1", MessageRole.ASSISTANT), message("m2", MessageRole.USER)]}
        approvals={[]}
        errorEvents={[]}
        infoEvents={[]}
        runOutputs={[]}
        chatId="chat-1"
        // WAITING_FOR_USAGE_LIMIT is not "running".
        isChatBusy={false}
        pendingApprovals={[]}
        connectionStatus="connected"
        projectId="project-1"
        onSendMessage={vi.fn(async () => {})}
        onStopStreaming={onStop}
      />
    </SurfaceProvider>,
  );
}

beforeEach(() => {
  state.retry.mockClear();
  onStop.mockClear();
  state.selection = null;
  state.models = [];
  state.tiers = {};
  state.accounts = [];
  state.chat = {
    id: "chat-1",
    workflowId: "chat-1",
    activity: ChatActivity.RUNNING,
    launchKind: "chat.start",
    usageLimitResumesAt: new Date(Date.now() + 72 * 60_000).toISOString(),
    usageLimitProvider: "Anthropic",
    usageLimitProbing: false,
  };
  useActivityStore.getState().setActivity("chat-1", ChatActivity.WAITING_FOR_USAGE_LIMIT);
});

describe("ChatPresenter: a run held for a usage limit", () => {
  it("says who it waits on and when it carries on, instead of thinking", () => {
    renderPresenter();
    const note = screen.getByTestId("usage-limit-wait-note");
    expect(note).toHaveTextContent("Waiting for Anthropic usage limit");
    expect(screen.getByTestId("usage-limit-resume-line")).toHaveTextContent(/resumes .+ \(in 1h 1[12]m\)/);
    expect(screen.queryByTestId("thinking-indicator")).toBeNull();
  });

  it("Retry now wakes the server's timer for this chat", async () => {
    renderPresenter();
    fireEvent.click(screen.getByRole("button", { name: "Retry now" }));
    await waitFor(() => expect(state.retry).toHaveBeenCalledWith("chat-1"));
  });

  it("offers no model switch when nothing else can serve", () => {
    renderPresenter();
    expect(screen.queryByRole("button", { name: /^Use .* now$/ })).toBeNull();
  });

  describe("with another provider connected", () => {
    beforeEach(() => {
      state.selection = { params: { model: { id: "claude-5.5-opus@claude" } }, providerPolicy: {} };
      state.models = [
        { id: "claude-5.5-opus@claude", name: "Claude 5.5 Opus", provider: "Claude subscription", driverId: "claude", tags: ["flagship"] },
        { id: "gpt-5.6-sol@codex", name: "GPT-5.6 Sol", provider: "Codex", driverId: "codex", tags: ["flagship"] },
      ];
    });

    it("\"Use <model> now\" sends use_model, not a plain retry", async () => {
      renderPresenter();
      fireEvent.click(screen.getByRole("button", { name: "Use GPT-5.6 Sol · Codex now" }));
      await waitFor(() => expect(state.retry).toHaveBeenCalledWith("chat-1", "gpt-5.6-sol@codex"));
    });

    it("Retry now stays a plain retry beside it", async () => {
      renderPresenter();
      fireEvent.click(screen.getByRole("button", { name: "Retry now" }));
      await waitFor(() => expect(state.retry).toHaveBeenCalledWith("chat-1"));
      expect(state.retry).not.toHaveBeenCalledWith("chat-1", expect.anything());
    });

    it("names the account of the limited provider that returns first", () => {
      const at = (min: number) => new Date(Date.now() + min * 60_000).toISOString();
      const acct = (id: string, until: string) => ({ id, provider: "claude", label: `${id}@acme.com`, status: "limited", limitedUntil: until });
      state.accounts = [acct("alice", at(90)), acct("bob", at(30))];
      renderPresenter();
      expect(screen.getByTestId("usage-limit-earliest-account")).toHaveTextContent("bob@acme.com is back");
    });
  });

  it("Stop is the run's ordinary stop", () => {
    renderPresenter();
    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    expect(onStop).toHaveBeenCalledTimes(1);
  });

  it("does not promise a reset the provider never named", () => {
    state.chat = { ...state.chat, usageLimitProbing: true, usageLimitProvider: "" };
    renderPresenter();
    expect(screen.getByTestId("usage-limit-wait-note")).toHaveTextContent("Waiting for usage limit");
    expect(screen.getByTestId("usage-limit-resume-line")).toHaveTextContent("no reset time given");
  });

  it("says nothing once the run is running again", () => {
    useActivityStore.getState().setActivity("chat-1", ChatActivity.RUNNING);
    renderPresenter();
    expect(screen.queryByTestId("usage-limit-wait-note")).toBeNull();
  });

  it("shows no stale countdown when the activity moved on but the chat snapshot did not", () => {
    useActivityStore.getState().setActivity("chat-1", ChatActivity.IDLE);
    renderPresenter();
    expect(screen.queryByTestId("usage-limit-wait-note")).toBeNull();
  });
});
