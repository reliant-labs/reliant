/**
 * An async ask_user (input async: true) never takes over the composer.
 *
 * Nothing is parked on an async question — the agent kept working — so the
 * composer must stay the composer: no question prompt, and a typed message
 * still goes to the agent. Only the timeline's card answers it
 * (research/ASYNC_ASK_USER.md). This runs the real question cache and stream
 * routing (applyQuestionUpdate); only the RPCs are faked.
 */

import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { queryClient } from "../../../lib/query-client";

const CHAT_ID = "chat-async";

globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

const { sendAgentMessageMock } = vi.hoisted(() => ({ sendAgentMessageMock: vi.fn() }));

vi.mock("../../../api/chat-grpc", () => ({
  chatGrpc: { sendAgentMessage: sendAgentMessageMock },
  QUEUED_SENDER_KIND_HUMAN: 5,
}));
vi.mock("../../../api/question-grpc", () => ({
  questionGrpc: {
    // The server leaves async questions out of the pending list.
    listPendingQuestions: vi.fn().mockResolvedValue([]),
    listAsyncQuestions: vi.fn().mockResolvedValue([]),
    resolveQuestion: vi.fn().mockResolvedValue({ success: true }),
  },
}));
vi.mock("../../../lib/toast-manager", () => ({
  toast: { error: vi.fn(), info: vi.fn(), success: vi.fn(), warning: vi.fn() },
}));
vi.mock("../../../hooks/chat-queries", async () => {
  const actual = await vi.importActual<typeof import("../../../hooks/chat-queries")>(
    "../../../hooks/chat-queries",
  );
  return {
    ...actual,
    // The main thread's id is the chat id.
    useChat: () => ({ data: { id: CHAT_ID, workflowId: CHAT_ID }, isLoading: false }),
    getChatFromCache: () => undefined,
  };
});


const NO_PRESETS = { presets: [] as never[], loading: false };
const NO_MODELS = { models: [] as never[] };
const NO_SLASH_COMMANDS: never[] = [];
vi.mock("../../../store/globalDataStore", () => ({
  usePresetsForWorkflow: () => NO_PRESETS,
  useModels: () => NO_MODELS,
  useGlobalDataStore: Object.assign(() => undefined, { getState: () => ({ models: [] }) }),
}));
vi.mock("../../Settings/ModelPreferences", () => ({ loadTagModelConfigs: vi.fn().mockResolvedValue({}) }));
vi.mock("../../../hooks/useSlashCommands", () => ({ useSlashCommands: () => NO_SLASH_COMMANDS }));
vi.mock("../../../api/workflow-grpc", () => ({
  workflowGrpc: { getWorkflow: vi.fn().mockResolvedValue({ workflow: null }) },
}));
vi.mock("../../../api/preset-grpc", () => ({
  presetGrpc: { getDefaultPresets: vi.fn().mockResolvedValue({}), listPresets: vi.fn().mockResolvedValue([]) },
}));
vi.mock("../settings", () => ({ ChatSettingsPopover: () => null }));
vi.mock("../WorkflowSelector", () => ({ WorkflowSelector: () => null }));

import { ChatInput } from "../ChatInput";
import { applyQuestionUpdate } from "../../../hooks/approval-queries";

function questionEvent(id: string, prompt: string, async: boolean) {
  return {
    update_type: "question" as const,
    question_id: id,
    chat_id: CHAT_ID,
    workflow_id: CHAT_ID,
    thread_id: CHAT_ID,
    step_id: "ask_user",
    status: "pending" as const,
    async,
    tool_call_id: `call-${id}`,
    metadata: JSON.stringify({
      type: "ask_user",
      ...(async ? { async: true } : {}),
      questions: [{ question: prompt, options: [{ label: "Yes", description: "" }] }],
    }),
  };
}

function renderComposer() {
  return render(
    <QueryClientProvider client={queryClient}>
      <ChatInput onSend={vi.fn()} chatId={CHAT_ID} isStreaming={true} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  queryClient.clear();
  sendAgentMessageMock.mockResolvedValue({ success: true, message: "" });
});
afterEach(() => queryClient.clear());

describe("an async question does not take over the composer", () => {
  it("keeps the composer, and a typed message still goes to the agent", async () => {
    renderComposer();
    await waitFor(() => expect(screen.getByTestId("chat-input")).toBeInTheDocument());

    act(() => applyQuestionUpdate(CHAT_ID, questionEvent("q-async", "Which database? (async)", true)));

    expect(screen.queryByText("Which database? (async)")).toBeNull();
    expect(screen.queryByPlaceholderText("Or type your own answer...")).toBeNull();

    const editor = screen.getByTestId("chat-input");
    editor.textContent = "keep going";
    fireEvent.input(editor);
    fireEvent.keyDown(editor, { key: "Enter", shiftKey: false, metaKey: false, ctrlKey: false });
    expect(sendAgentMessageMock).toHaveBeenCalledWith(CHAT_ID, CHAT_ID, "keep going", [], expect.any(String));
  });

  it("while a blocking question from the same stream still does (control)", async () => {
    renderComposer();
    await waitFor(() => expect(screen.getByTestId("chat-input")).toBeInTheDocument());

    act(() => applyQuestionUpdate(CHAT_ID, questionEvent("q-blocking", "Ship it?", false)));

    expect(await screen.findByText("Ship it?")).toBeInTheDocument();
  });
});
