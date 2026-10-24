/**
 * The composer answers the question of the thread IN VIEW.
 *
 * Each spawned agent is a thread that can park on its own ask_user, so a chat
 * can have several questions pending at once. In chat adcee226 spawn A asked
 * first and spawn B asked later; the composer only ever had "the chat's
 * question" (the newest, B's), so with A in view the user's reply was queued
 * into A's mailbox — which a thread parked on a question never drains — and A
 * stayed blocked for hours.
 *
 * Pinned here:
 *  - viewing A shows A's question and answering it resolves A's question id,
 *    never queueing to the mailbox;
 *  - viewing B shows B's;
 *  - viewing the main thread, with only spawns waiting, leaves the ordinary
 *    composer in place, and a message while running queues to the main thread.
 */

import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderWithQuery } from "../../../test/renderWithQuery";
import type { QuestionInfo } from "../../../gen/reliant/v1/question_pb";

const CHAT_ID = "chat-abc";
const SPAWN_A = "thread-spawn-a";
const SPAWN_B = "thread-spawn-b";

globalThis.ResizeObserver ??= class {
  observe() {}
  unobserve() {}
  disconnect() {}
} as unknown as typeof ResizeObserver;

const { sendAgentMessageMock, answerQuestionMock, pending } = vi.hoisted(() => ({
  sendAgentMessageMock: vi.fn(),
  answerQuestionMock: vi.fn(),
  pending: { questions: [] as QuestionInfo[] },
}));

vi.mock("../../../api/chat-grpc", () => ({
  chatGrpc: { sendAgentMessage: sendAgentMessageMock },
  QUEUED_SENDER_KIND_HUMAN: 5,
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
vi.mock("../../../hooks/question-queries", () => ({
  usePendingQuestions: () => ({ data: { questions: pending.questions }, isLoading: false }),
  answerQuestion: answerQuestionMock,
}));

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

function askUser(id: string, threadId: string, prompt: string): QuestionInfo {
  return {
    $typeName: "reliant.v1.QuestionInfo",
    async: false,
    toolCallId: "",
    questionId: id,
    chatId: CHAT_ID,
    workflowId: `wf-${threadId}`,
    threadId: threadId,
    stepId: "ask_user",
    status: "pending",
    createdAt: "",
    metadata: JSON.stringify({
      type: "ask_user",
      questions: [{ question: prompt, options: [{ label: "Yes", description: "" }] }],
    }),
  };
}

beforeEach(() => {
  vi.clearAllMocks();
  sendAgentMessageMock.mockResolvedValue({ success: true, message: "" });
  answerQuestionMock.mockResolvedValue(undefined);
  // Oldest first: A asked, then B asked a newer one.
  pending.questions = [
    askUser("qA", SPAWN_A, "Spawn A: which schema?"),
    askUser("qB", SPAWN_B, "Spawn B: ship it?"),
  ];
});

describe("the composer answers the question of the thread in view", () => {
  it("shows spawn A's older question while A is in view, and a typed reply answers it", async () => {
    renderWithQuery(
      <ChatInput onSend={vi.fn()} chatId={CHAT_ID} isStreaming={true} selectedThreadId={SPAWN_A} />,
    );

    expect(screen.getByText("Spawn A: which schema?")).toBeInTheDocument();
    expect(screen.queryByText("Spawn B: ship it?")).toBeNull();

    fireEvent.change(screen.getByPlaceholderText("Or type your own answer..."), {
      target: { value: "continue" },
    });
    fireEvent.click(screen.getByRole("button", { name: /submit/i }));

    await waitFor(() => expect(answerQuestionMock).toHaveBeenCalledTimes(1));
    expect(answerQuestionMock).toHaveBeenCalledWith(CHAT_ID, "qA", {
      answers: [{ question: "Spawn A: which schema?", selected: [], freetext: "continue" }],
    });
    // Answering is not queueing: nothing went to A's mailbox.
    expect(sendAgentMessageMock).not.toHaveBeenCalled();
  });

  it("shows spawn B's question while B is in view", () => {
    renderWithQuery(
      <ChatInput onSend={vi.fn()} chatId={CHAT_ID} isStreaming={true} selectedThreadId={SPAWN_B} />,
    );

    expect(screen.getByText("Spawn B: ship it?")).toBeInTheDocument();
    expect(screen.queryByText("Spawn A: which schema?")).toBeNull();
  });

  it("leaves the main thread's composer alone when only spawns are waiting", () => {
    renderWithQuery(<ChatInput onSend={vi.fn()} chatId={CHAT_ID} isStreaming={true} />);

    expect(screen.queryByText("Spawn A: which schema?")).toBeNull();
    expect(screen.queryByText("Spawn B: ship it?")).toBeNull();

    // The main thread is running, so Enter queues to ITS mailbox.
    const editor = screen.getByTestId("chat-input");
    editor.textContent = "keep going";
    fireEvent.input(editor);
    fireEvent.keyDown(editor, { key: "Enter", shiftKey: false, metaKey: false, ctrlKey: false });

    expect(sendAgentMessageMock).toHaveBeenCalledWith(
      CHAT_ID,
      CHAT_ID,
      "keep going",
      [],
      expect.any(String),
    );
    expect(answerQuestionMock).not.toHaveBeenCalled();
  });

  it("shows the main thread's own question there, and not on a spawn", () => {
    pending.questions = [...pending.questions, askUser("qMain", CHAT_ID, "Main: pick a plan?")];

    const { unmount } = renderWithQuery(<ChatInput onSend={vi.fn()} chatId={CHAT_ID} />);
    expect(screen.getByText("Main: pick a plan?")).toBeInTheDocument();
    unmount();

    renderWithQuery(<ChatInput onSend={vi.fn()} chatId={CHAT_ID} selectedThreadId={SPAWN_A} />);
    expect(screen.queryByText("Main: pick a plan?")).toBeNull();
    expect(screen.getByText("Spawn A: which schema?")).toBeInTheDocument();
  });
});
