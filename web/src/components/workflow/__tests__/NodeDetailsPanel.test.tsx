import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { NodeDetailsPanel } from "../NodeDetailsPanel";
import type { FlowNodeData } from "../../../lib/workflow-flow";
import type { StepExecution } from "../../Chat/ExecutionSidebar/types";

describe("NodeDetailsPanel", () => {
  it("renders router candidate and fallback summary", () => {
    const nodeData: FlowNodeData = {
      label: "Router",
      executionStatus: "completed",
      step: {
        id: "router-1",
        type: "router",
        args: {
          case: "router",
          value: {
            workflows: [
              {
                ref: "builtin://agent",
                presets: ["fast", "safe"],
                description: "Default agent route",
              },
              {
                ref: "workflow://triage",
              },
            ],
            fallback: "workflow://fallback-handler",
          },
        },
      },
    };

    const stepExecutions: StepExecution[] = [];

    render(
      <NodeDetailsPanel
        nodeData={nodeData}
        nodeId="router-1"
        stepExecutions={stepExecutions}
        onClose={vi.fn()}
      />,
    );

    expect(screen.getByText("Candidates:")).toBeInTheDocument();
    expect(screen.getAllByText("agent").length).toBeGreaterThan(0);
    expect(screen.getByText("triage")).toBeInTheDocument();
    expect(screen.getByText("Presets: fast, safe")).toBeInTheDocument();
    expect(screen.getByText("Default agent route")).toBeInTheDocument();
    expect(screen.getByText("Fallback:")).toBeInTheDocument();
    expect(screen.getByText("workflow://fallback-handler")).toBeInTheDocument();
  });
});