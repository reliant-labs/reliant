/**
 * The full-screen read-only workflow view — renders structure and execution
 * status, and never offers to change the workflow.
 */

import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Workflow, Step } from "../../../types/workflow";
import type { WorkflowExecution, StepExecution } from "../../Chat/ExecutionSidebar/types";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, ...props }: { children?: React.ReactNode }) => (
    <a {...props}>{children}</a>
  ),
}));

const { MobileWorkflowScreen } = await import("../MobileWorkflowScreen");

function step(id: string, type = "call_llm"): Step {
  return { id, type } as Step;
}

function workflow(): Workflow {
  return {
    name: "builtin://agent",
    entry: ["plan"],
    nodes: [step("plan"), step("act", "run")],
    edges: [{ from: "plan", cases: [{ to: ["act"] }] }],
  } as Workflow;
}

function execution(overrides: Partial<WorkflowExecution> = {}): WorkflowExecution {
  return {
    id: "wf-1",
    workflowName: "builtin://agent",
    thread: "wf-1",
    status: "running",
    createdAt: 0,
    messageCount: 0,
    children: [],
    steps: [],
    ...overrides,
  };
}

function stepExecution(overrides: Partial<StepExecution> = {}): StepExecution {
  return {
    id: "se-1",
    stepId: "plan",
    activityName: "V2_CallLLM",
    status: "completed",
    createdAt: 0,
    ...overrides,
  };
}

describe("MobileWorkflowScreen", () => {
  it("renders the workflow name and every node", () => {
    render(<MobileWorkflowScreen workflow={workflow()} />);
    expect(screen.getByText("plan")).toBeInTheDocument();
    expect(screen.getByText("act")).toBeInTheDocument();
  });

  it("shows the execution status pill when a live execution is provided", () => {
    render(<MobileWorkflowScreen workflow={workflow()} execution={execution({ status: "running" })} />);
    expect(screen.getByText("running")).toBeInTheDocument();
  });

  it("omits the execution status pill when there is no execution", () => {
    render(<MobileWorkflowScreen workflow={workflow()} />);
    expect(screen.queryByText(/running|completed|failed|cancelled/)).not.toBeInTheDocument();
  });

  it("reflects per-node execution status derived from the execution tree", () => {
    render(
      <MobileWorkflowScreen
        workflow={workflow()}
        execution={execution({ steps: [stepExecution({ stepId: "plan", status: "completed" })] })}
      />,
    );
    expect(screen.getByText("Done")).toBeInTheDocument();
  });

  it("opens a detail sheet on tap and shows an 'edit on desktop' notice instead of edit controls", async () => {
    render(<MobileWorkflowScreen workflow={workflow()} />);
    await userEvent.click(screen.getByRole("button", { name: /plan/i }));

    // The sheet renders its own copy of the notice, and the screen renders
    // one permanently at the bottom — both point at desktop, neither offers
    // an edit control here.
    expect(screen.getAllByText(/edit this workflow on desktop/i).length).toBeGreaterThan(0);
    expect(screen.queryByRole("button", { name: /^edit$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("textbox")).not.toBeInTheDocument();
  });

  it("closes the sheet when the backdrop or close button is tapped", async () => {
    render(<MobileWorkflowScreen workflow={workflow()} />);
    await userEvent.click(screen.getByRole("button", { name: /plan/i }));
    expect(screen.getByRole("button", { name: /^close$/i })).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /^close$/i }));
    expect(screen.queryByRole("button", { name: /^close$/i })).not.toBeInTheDocument();
  });
});
