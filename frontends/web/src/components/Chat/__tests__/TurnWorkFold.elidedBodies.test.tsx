import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TurnWorkFold } from "../TurnWorkFold";
import { ContentBlockType, MessageRole, StreamingState } from "../../../types/chat";
import { ToolCallStatus, type Message } from "../../../gen/reliant/v1/chat_pb";
import type { TurnWork } from "../thread-views/simplifiedTurns";
import { queryClient } from "../../../lib/query-client";
import { setTransport } from "../../../lib/connect";
import { mockTransport } from "../../../lib/test-utils";
import type { ToolResultsByCallId } from "../../../lib/messageProcessor";

// A simplified-mode fold opened from a stub read: its header (the headline,
// the counts) reads only what a stub keeps, so it says the same as from the
// full transcript and fetches nothing. Opening it reads every elided body of
// its rows in one batch.

vi.mock("../ChatMessage", () => ({
  ChatMessage: ({ message }: { message: Message }) => <div data-testid="row-work">{message.id}</div>,
}));
vi.mock("../AgentMessageCard", () => ({ isAgentMessage: () => false, AgentMessageCard: () => null }));
vi.mock("../SystemNotificationMessage", () => ({ SystemNotificationMessage: () => null }));
const results = vi.hoisted(() => ({ value: {} as ToolResultsByCallId }));
vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
  useChat: () => ({ worktreeId: "worktree-1" }),
  useToolResultsByCallId: () => results.value,
  useToolCallStates: () => new Map(),
}));
vi.mock("../../../store/chatStore", () => ({ useChatStore: { getState: () => ({ branchChat: vi.fn() }) } }));
vi.mock("../../../store/projectStore", () => ({ useProjectStore: () => ({ id: "project-1" }) }));
vi.mock("../BranchOptionsMenu", () => ({ BranchOptionsMenu: () => null }));
vi.mock("../BranchToWorktreeModal", () => ({ BranchToWorktreeModal: () => null }));
vi.mock("../BranchToExistingWorktreeModal", () => ({ BranchToExistingWorktreeModal: () => null }));
vi.mock("../../../hooks/useWorkflowExecutions", () => ({ useWorkflowExecutions: () => ({ allWorkflows: [] }) }));

const EDIT_INPUT = { file_path: "src/cart/totals.ts", old_string: "a".repeat(3000), new_string: "b".repeat(3000) };
const EDIT_SKELETON = { file_path: "src/cart/totals.ts", old_string: `${"a".repeat(256)}…`, new_string: `${"b".repeat(256)}…` };
const SHELL_INPUT = { command: "go test ./...", description: "Run the cart tests", stdin: "x".repeat(4000) };
const SHELL_SKELETON = { command: "go test ./...", description: "Run the cart tests", stdin: `${"x".repeat(256)}…` };

function message(id: string, blocks: Message["contentBlocks"]): Message {
  return {
    id,
    chatId: "chat-1",
    thread: "chat-1",
    role: MessageRole.ASSISTANT,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: blocks,
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
  } as unknown as Message;
}

function call(id: string, name: string, input: object, elided: boolean) {
  const raw = JSON.stringify(input);
  return {
    id: `call-${id}`,
    index: 0,
    type: ContentBlockType.TOOL_CALL,
    toolName: name,
    toolCallId: id,
    input: raw,
    toolCallStatus: ToolCallStatus.COMPLETED,
    completedAt: "2024-01-01T00:01:00.000Z",
    ...(elided ? { bodyElided: true, bodySize: 7000n } : {}),
  };
}

function work(elided: boolean): TurnWork {
  // Fresh message objects per variant: processMessage's memo is keyed by them.
  return {
    key: `work-${elided}`,
    thread: "chat-1",
    parts: [
      { message: message(`m1-${elided}`, [call("c1", "edit", elided ? EDIT_SKELETON : EDIT_INPUT, elided)] as Message["contentBlocks"]), segmentIndexes: [0] },
      { message: message(`m2-${elided}`, [call("c2", "shell", elided ? SHELL_SKELETON : SHELL_INPUT, elided)] as Message["contentBlocks"]), segmentIndexes: [0] },
    ],
  };
}

const getContentBlockBodies = vi.fn();

describe("a fold opened from a stub read", () => {
  beforeEach(() => {
    queryClient.clear();
    getContentBlockBodies.mockReset();
    getContentBlockBodies.mockImplementation(async (request: { blockIds: string[] }) => ({
      bodies: request.blockIds.map((blockId) => ({ blockId, input: "{}" })),
      notFound: [],
      deferred: [],
    }));
    setTransport(mockTransport({ "ChatService.GetContentBlockBodies": (req: unknown) => getContentBlockBodies(req) }));
    results.value = {
      c1: { content: "ok", block_id: "res-c1" },
      c2: { content: '{"exit_code":0', truncated: true, elided: true, block_id: "res-c2" },
    };
  });
  afterEach(() => {
    setTransport(null);
    queryClient.clear();
  });

  it("says the same in its header as from the full transcript, and fetches nothing", async () => {
    const full = render(<TurnWorkFold work={work(false)} chatId="chat-1" />);
    const fullLabel = screen.getAllByRole("button")[0].getAttribute("aria-label");
    full.unmount();

    render(<TurnWorkFold work={work(true)} chatId="chat-1" />);
    expect(screen.getAllByRole("button")[0].getAttribute("aria-label")).toBe(fullLabel);
    expect(fullLabel).toContain("1 file edited");
    await new Promise((r) => setTimeout(r, 80));
    expect(getContentBlockBodies).not.toHaveBeenCalled();
  });

  it("reads every elided body of its rows in one batch when opened", async () => {
    render(<TurnWorkFold work={work(true)} chatId="chat-1" />);
    fireEvent.click(screen.getAllByRole("button")[0]);

    await waitFor(() => expect(getContentBlockBodies).toHaveBeenCalledTimes(1));
    expect(getContentBlockBodies.mock.calls[0][0].blockIds.sort()).toEqual(["call-c1", "call-c2", "res-c2"]);
  });
});
