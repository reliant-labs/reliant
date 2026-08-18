import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ToolExecution } from "../ToolExecution";
import { ContentBlockType, MessageRole, ToolCallStatus, WorkflowState, WorkflowStopReason } from "../../../gen/reliant/v1/chat_pb";
import type { Message } from "../../../types/chat";

// Regression guard for the spawn_status / spawn_send substring bug: these are
// ordinary tools on an EXISTING spawn, not spawns themselves. Their status
// must come from their own tool call, not from a (nonexistent) child workflow.

let mockAllWorkflows: Array<{ id: string; thread?: string; threadTitle?: string; workflowName?: string; state: WorkflowState; stopReason: WorkflowStopReason; children: unknown[] }> = [
  { id: "wf-child-1", thread: "wf-child-1", threadTitle: "Research Stripe Billing", state: WorkflowState.ACTIVE, stopReason: WorkflowStopReason.UNSPECIFIED, children: [] },
];
let mockChatMessages: Message[] = [];
let mockStreamingMessages: Message[] = [];

vi.mock("../../../store/chatStoreHooks", () => ({
  useChat: () => undefined,
  useChatMessages: () => mockChatMessages,
  useStreamingMessages: () => mockStreamingMessages,
  useToolCallStates: () => new Map(),
}));
vi.mock("../../../hooks/approval-queries", () => ({
  useApproveToolRequest: () => ({ mutate: vi.fn() }),
  useDenyToolRequest: () => ({ mutate: vi.fn() }),
}));
vi.mock("../../../hooks/task-queries", () => ({
  useTasksForChat: () => ({ data: undefined }),
}));
vi.mock("../../../store/threadActivityStore", () => ({
  useActiveThreads: () => [],
}));
vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({ allWorkflows: mockAllWorkflows }),
}));

function assistantMessage(contentBlocks: Message["contentBlocks"]): Message {
  return {
    id: "msg-1",
    chatId: "chat-1",
    seq: BigInt(1),
    thread: "chat-1",
    role: MessageRole.ASSISTANT,
    contentBlocks,
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    sequenceNumber: BigInt(1),
  } as Message;
}

describe("ToolExecution status for spawn_status / spawn_send", () => {
  beforeEach(() => {
    mockAllWorkflows = [
      { id: "wf-child-1", thread: "wf-child-1", threadTitle: "Research Stripe Billing", state: WorkflowState.ACTIVE, stopReason: WorkflowStopReason.UNSPECIFIED, children: [] },
    ];
    mockChatMessages = [];
    mockStreamingMessages = [];
  });
  it("shows completed for a completed spawn_status call, not a spawn-derived 'Executing...'", () => {
    render(
      <ToolExecution
        toolCall={{
          id: "toolu_spawn_status_1",
          name: "spawn_status",
          input: { agent_id: "wf-child-1" },
          finished: true,
          durableStatus: ToolCallStatus.COMPLETED,
        }}
        toolResult={{ name: "spawn_status", content: "status: running", is_error: false }}
        chatId="chat-1"
      />,
    );

    expect(screen.getByText("Completed")).toBeInTheDocument();
    expect(screen.queryByText("Executing...")).not.toBeInTheDocument();
  });

  it("shows executing for an in-flight spawn_status call, driven by its own tool status", () => {
    render(
      <ToolExecution
        toolCall={{
          id: "toolu_spawn_status_2",
          name: "spawn_status",
          input: { agent_id: "wf-child-1" },
          finished: false,
          durableStatus: ToolCallStatus.EXECUTING,
        }}
        chatId="chat-1"
      />,
    );

    expect(screen.getByText("Executing...")).toBeInTheDocument();
  });

  it("shows completed for a completed spawn_send call", () => {
    render(
      <ToolExecution
        toolCall={{
          id: "toolu_spawn_send_1",
          name: "spawn_send",
          input: { agent_id: "wf-child-1", message: "keep going" },
          finished: true,
          durableStatus: ToolCallStatus.COMPLETED,
        }}
        toolResult={{ name: "spawn_send", content: "queued", is_error: false }}
        chatId="chat-1"
      />,
    );

    expect(screen.getByText("Completed")).toBeInTheDocument();
    expect(screen.queryByText("Executing...")).not.toBeInTheDocument();
  });

  it("labels spawn_status with the agent title and can open the full thread", () => {
    const onSelectThread = vi.fn();

    render(
      <ToolExecution
        toolCall={{
          id: "toolu_spawn_status_title",
          name: "spawn_status",
          input: { agent_id: "wf-child-1", wait: true },
          finished: true,
          durableStatus: ToolCallStatus.COMPLETED,
        }}
        toolResult={{ name: "spawn_status", content: "status: running", is_error: false }}
        chatId="chat-1"
        onSelectThread={onSelectThread}
      />,
    );

    expect(screen.getByText(/Research Stripe Billing/)).toBeInTheDocument();
    expect(screen.queryByText(/wf-child-1/)).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Open full thread view" }));
    expect(onSelectThread).toHaveBeenCalledWith("wf-child-1");
  });

  it("labels spawn_status from the original spawn title when workflow metadata is generic", () => {
    mockAllWorkflows = [
      { id: "wf-generic", thread: "wf-generic", workflowName: "builtin://agent", state: WorkflowState.ACTIVE, stopReason: WorkflowStopReason.UNSPECIFIED, children: [] },
    ];
    mockChatMessages = [
      assistantMessage([
        {
          id: "spawn-call-block",
          index: 0,
          type: ContentBlockType.TOOL_CALL,
          toolName: "spawn",
          toolCallId: "toolu_spawn_generic",
          input: JSON.stringify({ preset: "researcher", title: "Map streaming thread titles" }),
          matchedResult: {
            toolCallId: "toolu_spawn_generic",
            type: "tool_result",
            content: JSON.stringify({ agent_id: "wf-generic" }),
            isError: false,
          },
        },
      ]),
    ];

    render(
      <ToolExecution
        toolCall={{
          id: "toolu_spawn_status_generic",
          name: "spawn_status",
          input: { agent_id: "wf-generic", wait: true },
          finished: true,
          durableStatus: ToolCallStatus.COMPLETED,
        }}
        toolResult={{ name: "spawn_status", content: "status: running", is_error: false }}
        chatId="chat-1"
      />,
    );

    expect(screen.getByText(/Map streaming thread titles/)).toBeInTheDocument();
    expect(screen.queryByText("(agent)")).not.toBeInTheDocument();
    expect(screen.queryByText(/wf-generic/)).not.toBeInTheDocument();
  });

  it("does not label spawn_status as a generic agent when no title is available", () => {
    mockAllWorkflows = [
      { id: "wf-untitled", thread: "wf-untitled", workflowName: "builtin://agent", state: WorkflowState.ACTIVE, stopReason: WorkflowStopReason.UNSPECIFIED, children: [] },
    ];

    render(
      <ToolExecution
        toolCall={{
          id: "toolu_spawn_status_untitled",
          name: "spawn_status",
          input: { agent_id: "wf-untitled" },
          finished: true,
          durableStatus: ToolCallStatus.COMPLETED,
        }}
        toolResult={{ name: "spawn_status", content: "status: running", is_error: false }}
        chatId="chat-1"
      />,
    );

    expect(screen.getByText(/this agent/)).toBeInTheDocument();
    expect(screen.queryByText("(agent)")).not.toBeInTheDocument();
    expect(screen.queryByText(/wf-untitled/)).not.toBeInTheDocument();
  });

  it("labels bash_wait from the original bash description instead of the process id", () => {
    mockChatMessages = [
      assistantMessage([
        {
          id: "bash-call-block",
          index: 0,
          type: ContentBlockType.TOOL_CALL,
          toolName: "bash",
          toolCallId: "toolu_bash_1",
          input: JSON.stringify({ command: "npm test -- --runInBand", description: "Run focused web tests" }),
          matchedResult: {
            toolCallId: "toolu_bash_1",
            type: "tool_result",
            content: JSON.stringify({ process_id: "proc-123", backgrounded: true }),
            isError: false,
          },
        },
      ]),
    ];

    render(
      <ToolExecution
        toolCall={{
          id: "toolu_bash_wait_1",
          name: "bash_wait",
          input: { process_id: "proc-123" },
          finished: true,
          durableStatus: ToolCallStatus.COMPLETED,
        }}
        toolResult={{ name: "bash_wait", content: "done", is_error: false }}
        chatId="chat-1"
      />,
    );

    expect(screen.getByText(/Run focused web tests/)).toBeInTheDocument();
    expect(screen.queryByText(/proc-123/)).not.toBeInTheDocument();
  });
});
