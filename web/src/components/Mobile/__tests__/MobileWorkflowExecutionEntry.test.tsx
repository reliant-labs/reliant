import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { ChatWorkflowStatus } from "../../../gen/reliant/v1/chat_pb";
import type { WorkflowExecutionData } from "../../../types/chat";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, ...props }: { children?: React.ReactNode }) => (
    <a {...props}>{children}</a>
  ),
}));

let execution: WorkflowExecutionData | null = null;
vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({ data: execution }),
}));

const { MobileWorkflowExecutionEntry } = await import("../MobileWorkflowExecutionEntry");

function workflowExecution(
  overrides: Partial<WorkflowExecutionData> = {},
): WorkflowExecutionData {
  return {
    id: "wf-1",
    workflowName: "builtin://agent",
    thread: "wf-1",
    status: ChatWorkflowStatus.RUNNING,
    createdAt: "",
    messageCount: 0,
    children: [],
    steps: [],
    ...overrides,
  } as WorkflowExecutionData;
}

describe("MobileWorkflowExecutionEntry", () => {
  it("renders nothing when the chat has no workflow execution", () => {
    execution = null;
    const { container } = render(<MobileWorkflowExecutionEntry chatId="c1" />);
    expect(container).toBeEmptyDOMElement();
  });

  it("shows the workflow name when an execution exists", () => {
    execution = workflowExecution({ workflowName: "builtin://forge-one-shot" });
    render(<MobileWorkflowExecutionEntry chatId="c1" />);
    expect(screen.getByText("Forge One Shot")).toBeInTheDocument();
  });

  it("links to the chat's workflow view", () => {
    execution = workflowExecution();
    const { container } = render(<MobileWorkflowExecutionEntry chatId="c1" />);
    expect(container.querySelector("a")).toHaveAttribute(
      "to",
      "/m/chats/$chatId/workflow",
    );
  });
});
