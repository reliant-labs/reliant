import { describe, expect, it } from "vitest";
import { orderedWorkflowNodeIds } from "../workflowStepOrder";
import type { Workflow, Step } from "../../../types/workflow";

function step(id: string, type = "call_llm"): Step {
  return { id, type } as Step;
}

function workflow(overrides: Partial<Workflow> = {}): Workflow {
  return {
    name: "demo",
    nodes: [],
    edges: [],
    ...overrides,
  } as Workflow;
}

describe("orderedWorkflowNodeIds", () => {
  it("orders nodes breadth-first from the entry point", () => {
    const wf = workflow({
      entry: ["a"],
      nodes: [step("a"), step("b"), step("c")],
      edges: [
        { from: "a", cases: [{ to: ["b"] }] },
        { from: "b", cases: [{ to: ["c"] }] },
      ],
    });
    expect(orderedWorkflowNodeIds(wf)).toEqual(["a", "b", "c"]);
  });

  it("appends nodes unreachable from entry in declaration order", () => {
    const wf = workflow({
      entry: ["a"],
      nodes: [step("a"), step("orphan"), step("b")],
      edges: [{ from: "a", cases: [{ to: ["b"] }] }],
    });
    expect(orderedWorkflowNodeIds(wf)).toEqual(["a", "b", "orphan"]);
  });

  it("falls back to declaration order when there is no entry or edges", () => {
    const wf = workflow({ nodes: [step("x"), step("y"), step("z")] });
    expect(orderedWorkflowNodeIds(wf)).toEqual(["x", "y", "z"]);
  });

  it("returns an empty list for a workflow with no nodes", () => {
    expect(orderedWorkflowNodeIds(workflow())).toEqual([]);
  });
});
