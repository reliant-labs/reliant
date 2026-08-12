/**
 * Regression guard for async spawn: the group's running/completed rollup
 * must key a spawn entry off its child workflow's status, not the spawn tool
 * call's own status/result — which under async spawn arrives in milliseconds
 * while the agent it started keeps running for minutes.
 */
import { describe, it, expect, vi } from "vitest";
import { render } from "@testing-library/react";
import { ChatWorkflowStatus } from "../../../gen/reliant/v1/chat_pb";

let mockAllWorkflows: Array<{ id: string; status: ChatWorkflowStatus; children: unknown[] }> = [];

vi.mock("../../../store/activityStore", () => ({
  useActivityStore: () => undefined,
  ChatActivity: { IDLE: "idle" },
}));
vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({ allWorkflows: mockAllWorkflows }),
}));
vi.mock("../ToolExecution", () => ({
  ToolExecution: () => <div data-testid="tool-row" />,
  durableStatusToDisplayStatus: () => undefined,
  workflowStatusToDisplayStatus: (status: ChatWorkflowStatus | undefined) => {
    switch (status) {
      case ChatWorkflowStatus.COMPLETED:
        return "completed";
      case ChatWorkflowStatus.CANCELLED:
        return "cancelled";
      case ChatWorkflowStatus.FAILED:
        return "failed";
      default:
        return "executing";
    }
  },
}));

import { ToolExecutionGroup } from "../ToolExecutionGroup";

const spawnCall = (id: string, childWorkflowId: string) => ({
  call: { id, name: "spawn", input: { title: "reviewer" }, childWorkflowId },
  result: { content: "Spawned agent_id: " + childWorkflowId, is_error: false } as never,
  status: "completed" as const, // the dispatch itself is "completed" under async spawn
});

// The running pill is styled `text-primary`, the completed pill `text-success/60`
// (see ToolExecutionGroup.tsx summary render) — the color class is what
// distinguishes which bucket a count landed in, since both render "1".
function pillText(container: HTMLElement, colorClass: string): string | undefined {
  const el = container.querySelector(`span.${colorClass}`);
  return el ? el.textContent?.trim() : undefined;
}

describe("ToolExecutionGroup rollup counts a spawn by its child workflow's status", () => {
  it("counts a spawn whose child is still running as running, not completed", () => {
    mockAllWorkflows = [{ id: "wf-1", status: ChatWorkflowStatus.RUNNING, children: [] }];

    const { container } = render(<ToolExecutionGroup executions={[spawnCall("1", "wf-1")]} />);

    expect(pillText(container, "text-primary")).toBe("1");
    expect(pillText(container, "text-success\\/60")).toBeUndefined();
  });

  it("counts a spawn whose child has completed as completed, not running", () => {
    mockAllWorkflows = [{ id: "wf-2", status: ChatWorkflowStatus.COMPLETED, children: [] }];

    const { container } = render(<ToolExecutionGroup executions={[spawnCall("2", "wf-2")]} />);

    expect(pillText(container, "text-primary")).toBeUndefined();
    expect(pillText(container, "text-success\\/60")).toBe("1");
  });
});
