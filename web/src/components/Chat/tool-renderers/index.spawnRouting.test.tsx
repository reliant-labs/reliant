import { render, screen } from "@testing-library/react";
import { ToolContentArea } from "./index";
import type { ToolRenderContext } from "./types";
import { ChatWorkflowStatus } from "../../../types/chat";

// Regression guard for the spawn substring bug: only the literal "spawn" tool
// should route to SpawnToolRenderer. spawn_status and spawn_send are ordinary
// tools on an existing spawn and must fall through to GenericToolRenderer.

let mockAllWorkflows: Array<{ id: string; status: ChatWorkflowStatus; children: unknown[] }> = [];

vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({ allWorkflows: mockAllWorkflows }),
}));

vi.mock("../../../hooks/message-queries", () => ({
  useThreadMessages: () => ({ data: [], isPending: false }),
}));

vi.mock("../../../store/chatStoreHooks", () => ({
  useToolResultsByCallId: () => ({}),
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
          result: { content: "status: running" },
          isCompleted: true,
          isExecuting: false,
        })}
      />,
    );
    expect(screen.getByText("Input")).toBeInTheDocument();
  });

  it("routes spawn_send to GenericToolRenderer, not SpawnToolRenderer", () => {
    render(
      <ToolContentArea
        ctx={createContext({
          toolName: "spawn_send",
          input: { agent_id: "wf-child-1", message: "keep going" },
          result: { content: "queued" },
          isCompleted: true,
          isExecuting: false,
        })}
      />,
    );
    expect(screen.getByText("Input")).toBeInTheDocument();
  });
});
