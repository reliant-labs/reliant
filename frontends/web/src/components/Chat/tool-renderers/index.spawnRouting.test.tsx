import { render, screen } from "@testing-library/react";
import { ToolContentArea } from "./index";
import type { ToolRenderContext } from "./types";
import { WorkflowState, WorkflowStopReason } from "../../../types/chat";

// Regression guard for the spawn substring bug: only the literal "spawn" tool
// should route to SpawnToolRenderer. spawn_status is an ordinary tool on an
// existing spawn and falls through to GenericToolRenderer; spawn_send has its
// own renderer (the message as markdown), and neither is SpawnToolRenderer.

let mockAllWorkflows: Array<{ id: string; state: WorkflowState; stopReason: WorkflowStopReason; children: unknown[] }> = [];

vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({ allWorkflows: mockAllWorkflows }),
}));

vi.mock("../../../hooks/message-queries", () => ({
  useThreadMessages: () => ({ data: [], isPending: false }),
}));

vi.mock("../../../store/chatStoreHooks", () => ({
  useToolResultsByCallId: () => ({}),
  useChatMessages: () => [],
  useStreamingMessages: () => [],
}));

function createContext(overrides: Partial<ToolRenderContext> = {}): ToolRenderContext {
  return {
    toolName: "spawn",
    toolCallId: "tool-1",
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

describe("ToolContentArea spawn routing", () => {
  beforeEach(() => {
    mockAllWorkflows = [];
  });

  it("routes the literal 'spawn' tool to SpawnToolRenderer", () => {
    render(<ToolContentArea ctx={createContext({ toolName: "spawn", childWorkflowId: "wf-child-1" })} />);
    // SpawnToolRenderer surfaces child-agent language; generic input/output
    // panels are the tell for the fallback renderer.
    expect(screen.queryByText("Input")).not.toBeInTheDocument();
  });

  it("routes spawn_status to GenericToolRenderer, not SpawnToolRenderer", () => {
    render(
      <ToolContentArea
        ctx={createContext({
          toolName: "spawn_status",
          input: { agent_id: "wf-child-1" },
          result: { name: "spawn_status", content: "status: running" },
          isCompleted: true,
          isExecuting: false,
        })}
      />,
    );
    expect(screen.getByText("Input")).toBeInTheDocument();
  });

  it("routes spawn_send to SpawnSendToolRenderer: the message, not a JSON input dump", () => {
    render(
      <ToolContentArea
        ctx={createContext({
          toolName: "mcp__reliant__spawn_send",
          input: { agent_id: "wf-child-1", subject: "Keep going", message: "keep going on the **auth** refactor" },
          result: { name: "spawn_send", content: "queued" },
          isCompleted: true,
          isExecuting: false,
        })}
      />,
    );
    expect(screen.getByTestId("spawn-send-body")).toBeInTheDocument();
    expect(screen.getByText("auth")).toBeInTheDocument();
    expect(screen.getByText("queued")).toBeInTheDocument();
    expect(screen.queryByText("Input")).not.toBeInTheDocument();
  });
});
