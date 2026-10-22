import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MobileWorkflowStepList } from "../MobileWorkflowStepList";
import type { Workflow, Step } from "../../../types/workflow";

function step(id: string, type = "call_llm"): Step {
  return { id, type } as Step;
}

function workflow(): Workflow {
  return {
    name: "demo",
    entry: ["first"],
    nodes: [step("first"), step("second", "run")],
    edges: [{ from: "first", cases: [{ to: ["second"] }] }],
  } as Workflow;
}

describe("MobileWorkflowStepList", () => {
  it("renders every node in order with a status label", () => {
    render(
      <MobileWorkflowStepList
        workflow={workflow()}
        statusMap={{ first: "completed", second: "running" }}
        onSelectNode={() => {}}
      />,
    );

    const rows = screen.getAllByRole("button");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent("first");
    expect(rows[0]).toHaveTextContent("Done");
    expect(rows[1]).toHaveTextContent("second");
    expect(rows[1]).toHaveTextContent("Running");
  });

  it("shows pending status for nodes with no execution yet", () => {
    render(
      <MobileWorkflowStepList workflow={workflow()} statusMap={{}} onSelectNode={() => {}} />,
    );
    expect(screen.getAllByText("Pending")).toHaveLength(2);
  });

  it("calls onSelectNode with the tapped node's id", async () => {
    const onSelectNode = vi.fn();
    render(
      <MobileWorkflowStepList
        workflow={workflow()}
        statusMap={{}}
        onSelectNode={onSelectNode}
      />,
    );
    await userEvent.click(screen.getByRole("button", { name: /second/i }));
    expect(onSelectNode).toHaveBeenCalledWith("second");
  });

  it("renders an empty state for a workflow with no nodes", () => {
    render(
      <MobileWorkflowStepList
        workflow={{ name: "empty", nodes: [] } as Workflow}
        statusMap={{}}
        onSelectNode={() => {}}
      />,
    );
    expect(screen.getByText(/no steps/i)).toBeInTheDocument();
  });

  it("never renders an edit affordance — this view is read-only", () => {
    render(
      <MobileWorkflowStepList workflow={workflow()} statusMap={{}} onSelectNode={() => {}} />,
    );
    expect(screen.queryByRole("button", { name: /edit/i })).not.toBeInTheDocument();
    expect(screen.queryByText(/save/i)).not.toBeInTheDocument();
  });
});
