import { render, screen } from "@testing-library/react";
import { SpawnToolRenderer } from "./SpawnToolRenderer";
import type { ToolRenderContext } from "./types";
import { ChatWorkflowStatus, MessageRole, ContentBlockType } from "../../../types/chat";

// Regression guard for spawn: the tool call's own result arrives the moment
// the child is dispatched — a handle, not the agent's output — while the
// agent it started keeps running for minutes. The child workflow, not
// ctx.isCompleted/hasFailed, must be the authority on what the preview shows.

let mockAllWorkflows: Array<{ id: string; status: ChatWorkflowStatus; children: unknown[] }> = [];
let mockThreadMessages: unknown[] = [];
let mockIsPending = false;

vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({ allWorkflows: mockAllWorkflows }),
}));

vi.mock("../../../hooks/message-queries", () => ({
  useThreadMessages: () => ({ data: mockThreadMessages, isPending: mockIsPending }),
}));

vi.mock("../../../store/chatStoreHooks", () => ({
  useToolResultsByCallId: () => ({}),
}));

function assistantMessage(id: string, text: string) {
  return {
    id,
    role: MessageRole.ASSISTANT,
    seq: id,
    contentBlocks: [
      { type: ContentBlockType.TEXT, content: text },
    ],
  };
}

function createContext(overrides: Partial<ToolRenderContext> = {}): ToolRenderContext {
  return {
    toolName: "spawn",
    toolCallId: "tool-1",
    childWorkflowId: "wf-child-1",
    input: { title: "reviewer" },
    result: undefined,
    chatId: "chat-1",
    isExpanded: true,
    isCompleted: false,
    isExecuting: true,
    isPreparing: false,
    hasFailed: false,
    ...overrides,
  };
}

describe("SpawnToolRenderer", () => {
  beforeEach(() => {
    mockAllWorkflows = [];
    mockThreadMessages = [];
    mockIsPending = false;
  });

  it("child RUNNING: shows live content, offers the message form, does not pop the last message", () => {
    mockAllWorkflows = [{ id: "wf-child-1", status: ChatWorkflowStatus.RUNNING, children: [] }];
    mockThreadMessages = [
      assistantMessage("m1", "First update from the agent"),
      assistantMessage("m2", "Second update from the agent"),
    ];

    render(<SpawnToolRenderer ctx={createContext()} />);

    expect(screen.getByText("First update from the agent")).toBeInTheDocument();
    expect(screen.getByText("Second update from the agent")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("Message this agent…")).toBeInTheDocument();
  });

  it("child COMPLETED: does not offer the message form", () => {
    mockAllWorkflows = [{ id: "wf-child-1", status: ChatWorkflowStatus.COMPLETED, children: [] }];
    mockThreadMessages = [
      assistantMessage("m1", "Working on it"),
      assistantMessage("m2", "Final result"),
    ];

    render(<SpawnToolRenderer ctx={createContext({ isCompleted: true, isExecuting: false })} />);

    expect(screen.queryByPlaceholderText("Message this agent…")).not.toBeInTheDocument();
  });

  it("child FAILED: does not offer the message form, shows a terminal failed state", () => {
    mockAllWorkflows = [{ id: "wf-child-1", status: ChatWorkflowStatus.FAILED, children: [] }];
    mockThreadMessages = [assistantMessage("m1", "Ran into trouble")];

    render(<SpawnToolRenderer ctx={createContext({ isCompleted: false, hasFailed: true })} />);

    expect(screen.queryByPlaceholderText("Message this agent…")).not.toBeInTheDocument();
    expect(screen.getByText("Agent failed")).toBeInTheDocument();
  });

  it("child CANCELLED: does not offer the message form, shows a terminal cancelled state", () => {
    mockAllWorkflows = [{ id: "wf-child-1", status: ChatWorkflowStatus.CANCELLED, children: [] }];
    mockThreadMessages = [assistantMessage("m1", "Stopped partway")];

    render(<SpawnToolRenderer ctx={createContext({ isCompleted: false, hasFailed: true })} />);

    expect(screen.queryByPlaceholderText("Message this agent…")).not.toBeInTheDocument();
    expect(screen.getByText("Agent cancelled")).toBeInTheDocument();
  });

  it("spawn dispatched, child still RUNNING: does not render as done, does not pop the last message", () => {
    mockAllWorkflows = [{ id: "wf-child-1", status: ChatWorkflowStatus.RUNNING, children: [] }];
    mockThreadMessages = [
      assistantMessage("m1", "Doing background work"),
      assistantMessage("m2", "More background work"),
    ];

    render(
      <SpawnToolRenderer
        ctx={createContext({
          input: { title: "reviewer" },
          // The tool call itself resolves (dispatch handle) almost
          // immediately, well before the agent finishes.
          isCompleted: true,
          isExecuting: false,
        })}
      />,
    );

    // The child workflow, not ctx.isCompleted, is authoritative: still running.
    expect(screen.getByPlaceholderText("Message this agent…")).toBeInTheDocument();
    expect(screen.getByText("Doing background work")).toBeInTheDocument();
    expect(screen.getByText("More background work")).toBeInTheDocument();
    expect(screen.queryByText("Starting…")).not.toBeInTheDocument();
  });

  it("spawn dispatched, child COMPLETED: does not pop the last message (result lives in the parent's mailbox, not the tool output)", () => {
    mockAllWorkflows = [{ id: "wf-child-1", status: ChatWorkflowStatus.COMPLETED, children: [] }];
    mockThreadMessages = [
      assistantMessage("m1", "Doing background work"),
      assistantMessage("m2", "Background result"),
    ];

    render(
      <SpawnToolRenderer
        ctx={createContext({
          input: { title: "reviewer" },
          isCompleted: true,
          isExecuting: false,
        })}
      />,
    );

    expect(screen.getByText("Doing background work")).toBeInTheDocument();
    expect(screen.getByText("Background result")).toBeInTheDocument();
    expect(screen.queryByPlaceholderText("Message this agent…")).not.toBeInTheDocument();
  });

  it("no child workflow row yet, pre-dispatch failure: falls back to ctx's own verdict", () => {
    mockAllWorkflows = [];
    mockThreadMessages = [];

    render(
      <SpawnToolRenderer
        ctx={createContext({
          childWorkflowId: undefined,
          isCompleted: false,
          hasFailed: true,
        })}
      />,
    );

    expect(screen.queryByPlaceholderText("Message this agent…")).not.toBeInTheDocument();
  });
});
