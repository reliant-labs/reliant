import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { act, type ReactNode } from "react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { EventBusProvider } from "../../../lib/event-context";
import { WorkflowBuilderChat } from "../WorkflowBuilderChat";
import { ContentBlockType, MessageRole } from "../../../gen/reliant/v1/chat_pb";
import type { Workflow } from "../../../types/workflow";

const mocks = vi.hoisted(() => {
  const chatParamsState = {
    chatParams: {} as Record<string, Record<string, unknown>>,
    tempNewChatParams: {} as Record<string, unknown>,
    updateChatParams: vi.fn(),
    updateTempNewChatParams: vi.fn(),
    transferTempToChat: vi.fn(),
  };

  return {
    chatGrpc: {
      create: vi.fn(),
      get: vi.fn(),
      cancel: vi.fn(),
    },
    workflowGrpc: {
      associateChatWithWorkflowDraft: vi.fn(),
    },
    getWorkflowByDraftId: vi.fn(),
    getWorkflowWithDraftId: vi.fn(),
    chatStoreState: {
      initChatState: vi.fn(),
      sendMessage: vi.fn(),
      loadMessages: vi.fn(),
    },
    chatMessages: {} as Record<string, unknown[]>,
    chatParamsState,
    globalDataState: {
      models: [] as Array<{
        id: string;
        name: string;
        provider: string;
        supportedThinkingLevels?: string[];
        capabilities: string[];
        tags: string[];
      }>,
      modelsLoading: false,
      modelsError: null as string | null,
      isInitialized: true,
      isPrefetching: false,
      refetchModels: vi.fn(),
    },
    activityState: {
      setActivity: vi.fn(),
    },
    subscribeToChatDetails: vi.fn(),
    unsubscribeFromChatDetails: vi.fn(),
    updateWorkflowParams: vi.fn(),
    listApprovalsByChat: vi.fn(async () => [] as unknown[]),
    logger: {
      error: vi.fn(),
      debug: vi.fn(),
      info: vi.fn(),
      warn: vi.fn(),
    },
  };
});

vi.mock("../../Chat/BaseChatInput", async () => {
  const { forwardRef } = await vi.importActual<typeof import("react")>("react");

  interface MockBaseChatInputProps {
    value: string;
    onChange: (value: string) => void;
    onSend: () => void;
    disabled?: boolean;
    isLoading?: boolean;
    placeholder?: string;
  }

  const BaseChatInput = forwardRef<HTMLTextAreaElement, MockBaseChatInputProps>(
    function MockBaseChatInput(
      { value, onChange, onSend, disabled = false, isLoading = false, placeholder },
      ref,
    ) {
      return (
        <div>
          <textarea
            ref={ref}
            data-testid="workflow-chat-input"
            value={value}
            placeholder={placeholder}
            disabled={disabled || isLoading}
            onChange={(event) => onChange(event.currentTarget.value)}
          />
          <button
            type="button"
            data-testid="send-button"
            disabled={disabled || isLoading || value.trim().length === 0}
            onClick={onSend}
          >
            Send
          </button>
        </div>
      );
    },
  );

  return { BaseChatInput };
});

vi.mock("../../Chat/thread-views", () => ({
  InterleavedTimeline: () => <div data-testid="timeline" />,
}));

vi.mock("../../Chat/ChatMessagesContainer", () => ({
  ChatMessagesContainer: ({ children }: { children: ReactNode }) => (
    <div data-testid="messages-container">{children}</div>
  ),
}));

vi.mock("../../Chat/ChatThinkingIndicator", () => ({
  ChatThinkingIndicator: () => <div data-testid="thinking-indicator" />,
}));

vi.mock("../../../api/chat-grpc", () => ({
  chatGrpc: mocks.chatGrpc,
}));

vi.mock("../../../api/workflow-grpc", () => ({
  workflowGrpc: mocks.workflowGrpc,
  getWorkflowByDraftId: mocks.getWorkflowByDraftId,
  getWorkflowWithDraftId: mocks.getWorkflowWithDraftId,
}));

vi.mock("../../../api/client", () => ({
  api: {
    chatsV2: {
      updateWorkflowParams: mocks.updateWorkflowParams,
    },
    approvals: {
      listByChat: mocks.listApprovalsByChat,
    },
  },
}));

vi.mock("../../../store/chatStore", () => ({
  useChatStore: Object.assign(
    (selector: (state: typeof mocks.chatStoreState) => unknown) =>
      selector(mocks.chatStoreState),
    { getState: () => mocks.chatStoreState },
  ),
}));

vi.mock("../../../store/chatStoreHooks", () => ({
  useChatMessages: (chatId?: string) => (chatId ? (mocks.chatMessages[chatId] ?? []) : []),
  useStreamingMessages: () => [],
  useErrorEvents: () => [],
  useInfoEvents: () => [],
  useRunOutputs: () => [],
}));

vi.mock("../../../store/activityStore", () => ({
  ChatActivity: {
    IDLE: 0,
    RUNNING: 1,
    AWAITING_INPUT: 2,
  },
  useIsChatRunning: () => false,
  useActivityStore: Object.assign(
    (selector: (state: typeof mocks.activityState) => unknown) =>
      selector(mocks.activityState),
    { getState: () => mocks.activityState },
  ),
}));

vi.mock("../../../store/globalUpdatesStore", () => ({
  useGlobalUpdatesStore: (selector: (state: {
    subscribeToChatDetails: typeof mocks.subscribeToChatDetails;
    unsubscribeFromChatDetails: typeof mocks.unsubscribeFromChatDetails;
  }) => unknown) =>
    selector({
      subscribeToChatDetails: mocks.subscribeToChatDetails,
      unsubscribeFromChatDetails: mocks.unsubscribeFromChatDetails,
    }),
}));

vi.mock("../../../store/globalDataStore", () => ({
  useModels: () => ({
    models: mocks.globalDataState.models,
    loading: mocks.globalDataState.modelsLoading,
    error: mocks.globalDataState.modelsError,
  }),
  useGlobalDataStore: (selector: (state: typeof mocks.globalDataState) => unknown) =>
    selector(mocks.globalDataState),
}));

vi.mock("../../../store/chatParamsStore", () => ({
  useChatParamsStore: Object.assign(
    (selector: (state: typeof mocks.chatParamsState) => unknown) =>
      selector(mocks.chatParamsState),
    { getState: () => mocks.chatParamsState },
  ),
}));

vi.mock("../../../lib/logger", () => ({
  logger: mocks.logger,
}));

const workflow: Workflow = {
  name: "test-workflow",
  description: "Test workflow",
  nodes: [],
  edges: [],
};

function renderWorkflowBuilderChat(props: Partial<Parameters<typeof WorkflowBuilderChat>[0]> = {}) {
  // Mirrors the provider nesting in App.tsx: the panel reads approvals through
  // React Query (useApprovals) and subscribes to "api-key:saved" through the
  // event bus, so it only mounts under both. Retries and caching are off so a
  // query never outlives the test that started it.
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0, staleTime: 0 },
    },
  });

  return render(
    <EventBusProvider>
      <QueryClientProvider client={queryClient}>
        <WorkflowBuilderChat
          workflow={workflow}
          onWorkflowChange={vi.fn()}
          projectId="project-1"
          isOpen
          onOpenChange={vi.fn()}
          panelSize="normal"
          onPanelSizeChange={vi.fn()}
          {...props}
        />
      </QueryClientProvider>
    </EventBusProvider>,
  );
}

async function enterAndSend(message: string) {
  const input = await screen.findByTestId("workflow-chat-input");
  await waitFor(() => expect(input).not.toBeDisabled());
  await act(async () => {
    fireEvent.change(input, { target: { value: message } });
    fireEvent.click(screen.getByTestId("send-button"));
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();

  mocks.chatMessages = {};
  mocks.chatParamsState.chatParams = {};
  mocks.chatParamsState.tempNewChatParams = {
    thinking_level: "high",
    model: { id: "gpt-5.3-codex@codex" },
  };
  mocks.chatParamsState.transferTempToChat.mockImplementation((chatId: string) => {
    mocks.chatParamsState.chatParams[chatId] = {
      ...mocks.chatParamsState.tempNewChatParams,
    };
    mocks.chatParamsState.tempNewChatParams = {};
  });

  mocks.globalDataState.models = [
    {
      id: "gpt-5.3-codex@codex",
      name: "GPT 5.3 Codex",
      provider: "codex",
      supportedThinkingLevels: ["medium", "high"],
      capabilities: [],
      tags: ["flagship"],
    },
  ];
  mocks.globalDataState.modelsLoading = false;
  mocks.globalDataState.modelsError = null;
  mocks.globalDataState.isInitialized = true;
  mocks.globalDataState.isPrefetching = false;

  mocks.chatGrpc.create.mockResolvedValue({
    chat: { id: "chat-new" },
  });
  mocks.chatGrpc.get.mockResolvedValue({ id: "chat-existing" });
  mocks.chatStoreState.sendMessage.mockResolvedValue(undefined);
  mocks.chatStoreState.loadMessages.mockResolvedValue(undefined);
  mocks.updateWorkflowParams.mockResolvedValue({});
});

afterEach(() => {
  vi.useRealTimers();
});

describe("WorkflowBuilderChat send workflow params", () => {
  it("creates the first builder chat with the preset and provider-qualified selected model", async () => {
    renderWorkflowBuilderChat({ isNewWorkflow: true });

    await enterAndSend("Build a workflow");

    await waitFor(() => expect(mocks.chatGrpc.create).toHaveBeenCalledTimes(1));
    expect(mocks.chatGrpc.create).toHaveBeenCalledWith(
      expect.objectContaining({
        project_id: "project-1",
        workflow: "builtin://agent",
        selected_presets: { "": "workflow_builder" },
        workflow_params: {
          mode: "auto",
          model: { id: "gpt-5.3-codex@codex", thinking_level: "high" },
        },
      }),
    );
  });

  it("sends subsequent builder messages with the preset and provider-qualified selected model", async () => {
    mocks.chatParamsState.tempNewChatParams = {};
    mocks.chatParamsState.chatParams = {
      "chat-existing": {
        thinking_level: "medium",
        model: { id: "gpt-5.3-codex@codex" },
      },
    };

    renderWorkflowBuilderChat({ builderChatId: "chat-existing" });

    await waitFor(() => expect(mocks.chatGrpc.get).toHaveBeenCalledWith("chat-existing"));
    await enterAndSend("Update the workflow");

    await waitFor(() => expect(mocks.chatStoreState.sendMessage).toHaveBeenCalledTimes(1));
    expect(mocks.chatStoreState.sendMessage).toHaveBeenCalledWith(
      "chat-existing",
      "Update the workflow",
      undefined,
      expect.objectContaining({
        selectedPresets: { "": "workflow_builder" },
        workflowParams: {
          mode: "auto",
          model: { id: "gpt-5.3-codex@codex", thinking_level: "medium" },
        },
      }),
    );
  });

  it("updates running chat thinking under the model selector", async () => {
    mocks.chatParamsState.tempNewChatParams = {};
    mocks.chatParamsState.chatParams = {
      "chat-existing": {
        thinking_level: "medium",
        model: { id: "gpt-5.3-codex@codex" },
      },
    };

    renderWorkflowBuilderChat({ builderChatId: "chat-existing" });

    await waitFor(() => expect(mocks.chatGrpc.get).toHaveBeenCalledWith("chat-existing"));
    await act(async () => {
      fireEvent.change(screen.getByTitle("Thinking level"), { target: { value: "high" } });
    });

    expect(mocks.chatParamsState.updateChatParams).toHaveBeenCalledWith(
      "chat-existing",
      { thinking_level: "high" },
    );
    expect(mocks.updateWorkflowParams).toHaveBeenCalledWith("chat-existing", {
      mode: "auto",
      model: { id: "gpt-5.3-codex@codex", thinking_level: "high" },
    });
  });

  it("renders create errors inline and restores the draft message for retry", async () => {
    mocks.chatGrpc.create.mockRejectedValueOnce(
      new Error("workflow input validation failed: model unavailable"),
    );

    renderWorkflowBuilderChat({ isNewWorkflow: true });

    await enterAndSend("Build a workflow");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "workflow input validation failed: model unavailable",
    );
    await waitFor(() =>
      expect(screen.getByTestId("workflow-chat-input")).toHaveValue("Build a workflow"),
    );
    expect(screen.getByTestId("send-button")).toBeEnabled();
  });

  // The draft used to be handed to the agent as an injected system message
  // ("You are operating on workflow <uuid>"). The workflow tools now resolve it
  // from the chat<->draft association, so nothing may be injected into the
  // conversation on either the create or the send path.
  it("sends no injected system message on the create path", async () => {
    renderWorkflowBuilderChat({ isNewWorkflow: true, draftId: "draft-1" });

    await enterAndSend("Build a workflow");

    await waitFor(() => expect(mocks.chatGrpc.create).toHaveBeenCalledTimes(1));
    const [createArgs] = mocks.chatGrpc.create.mock.calls[0] as [
      { messages: Array<{ role: number; content: string }> },
    ];
    expect(createArgs.messages).toEqual([
      { role: MessageRole.USER, content: "Build a workflow" },
    ]);
  });

  it("sends no injected system message on the send path", async () => {
    renderWorkflowBuilderChat({ builderChatId: "chat-existing", draftId: "draft-1" });

    await waitFor(() => expect(mocks.chatGrpc.get).toHaveBeenCalledWith("chat-existing"));
    await enterAndSend("Update the workflow");

    await waitFor(() => expect(mocks.chatStoreState.sendMessage).toHaveBeenCalledTimes(1));
    const sendOptions = mocks.chatStoreState.sendMessage.mock.calls[0][3] as Record<
      string,
      unknown
    >;
    expect(sendOptions).not.toHaveProperty("systemMessages");
  });
});

// The canvas used to refetch only for a hardcoded ["edit_workflow",
// "write_workflow"] allowlist, which silently omitted create_workflow and every
// scenario tool. The rule is now generic: any successful tool result triggers a
// refetch, and the draft's version decides whether the canvas is replaced.
describe("WorkflowBuilderChat canvas sync", () => {
  function toolResultMessage(seq: number, toolName: string) {
    return {
      role: MessageRole.TOOL,
      seq,
      contentBlocks: [
        {
          type: ContentBlockType.TOOL_RESULT,
          toolName,
          isError: false,
        },
      ],
    };
  }

  // Creates a chat (so the component is not on the "restored chat" path), then
  // delivers tool results and re-renders so the watcher effect observes them.
  async function sendThenDeliver(
    onWorkflowChange: ReturnType<typeof vi.fn>,
    messages: unknown[],
  ) {
    renderWorkflowBuilderChat({
      isNewWorkflow: true,
      draftId: "draft-1",
      onWorkflowChange,
    });
    await enterAndSend("Build a workflow");
    await waitFor(() => expect(mocks.chatGrpc.create).toHaveBeenCalledTimes(1));

    mocks.chatMessages["chat-new"] = messages;
    // Any state change re-runs the effect with the new store messages.
    await act(async () => {
      fireEvent.change(await screen.findByTestId("workflow-chat-input"), {
        target: { value: "x" },
      });
    });
  }

  it("refetches the draft for a tool that is not edit_workflow/write_workflow", async () => {
    const onWorkflowChange = vi.fn();
    mocks.getWorkflowByDraftId.mockResolvedValue({
      workflow: { ...workflow, description: "updated by create_workflow" },
      version: 2,
    });

    await sendThenDeliver(onWorkflowChange, [toolResultMessage(1, "create_workflow")]);

    await waitFor(() =>
      expect(mocks.getWorkflowByDraftId).toHaveBeenCalledWith("project-1", "draft-1"),
    );
    await waitFor(() =>
      expect(onWorkflowChange).toHaveBeenCalledWith(
        expect.objectContaining({ description: "updated by create_workflow" }),
      ),
    );
  });

  it("refetches the draft for a scenario tool", async () => {
    const onWorkflowChange = vi.fn();
    mocks.getWorkflowByDraftId.mockResolvedValue({
      workflow: { ...workflow, description: "updated by write_scenario" },
      version: 3,
    });

    await sendThenDeliver(onWorkflowChange, [toolResultMessage(1, "write_scenario")]);

    await waitFor(() =>
      expect(mocks.getWorkflowByDraftId).toHaveBeenCalledWith("project-1", "draft-1"),
    );
    await waitFor(() =>
      expect(onWorkflowChange).toHaveBeenCalledWith(
        expect.objectContaining({ description: "updated by write_scenario" }),
      ),
    );
  });

  // A read-only tool (view, grep, get_workflow) costs one cheap refetch but must
  // NOT replace the canvas — otherwise the generic rule would clobber unsaved
  // canvas edits on every turn.
  it("does not replace the canvas when the draft version is unchanged", async () => {
    const onWorkflowChange = vi.fn();
    mocks.getWorkflowByDraftId.mockResolvedValue({
      workflow: { ...workflow, description: "v2" },
      version: 2,
    });

    await sendThenDeliver(onWorkflowChange, [toolResultMessage(1, "edit_workflow")]);
    await waitFor(() => expect(onWorkflowChange).toHaveBeenCalledTimes(1));

    // A later read-only tool result: refetch happens, canvas does not change.
    mocks.chatMessages["chat-new"] = [
      toolResultMessage(1, "edit_workflow"),
      toolResultMessage(2, "get_workflow"),
    ];
    await act(async () => {
      fireEvent.change(await screen.findByTestId("workflow-chat-input"), {
        target: { value: "xy" },
      });
    });

    await waitFor(() => expect(mocks.getWorkflowByDraftId).toHaveBeenCalledTimes(2));
    expect(onWorkflowChange).toHaveBeenCalledTimes(1);
  });
});