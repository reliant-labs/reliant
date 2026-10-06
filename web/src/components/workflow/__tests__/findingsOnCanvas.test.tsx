/**
 * Problems are shown ON what they are about (research/WORKFLOW_EDITOR_UX_REVIEW.md
 * issue 4): the field's own error under the field, outlined, and focused when
 * the problem is picked; a count badge and ring on the step on the canvas.
 */
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import type { Node, NodeProps } from "@xyflow/react";

import type { ValidationError } from "../../../api/workflow-grpc";
import type { ProtoFieldSchema } from "../../../types/workflowFieldSchema";
import { ProtoFieldRenderer } from "../ProtoFieldRenderer";
import { NodeFindingsScope, WorkflowFindingsProvider, type FindingFocus } from "../WorkflowFindingsContext";
import { findingsByNode, locateFindings } from "../workflowFindings";
import { NODE_PROBLEM_CLASS, withFindingMarkers, withProblemMarker } from "../nodes/problemMarkers";

vi.mock("../../../lib/cel-completion-service", async () => ({
  ...(await vi.importActual<typeof import("../../../lib/cel-completion-service")>("../../../lib/cel-completion-service")),
  ensureCELCompletionsCached: async () => {},
}));

const modelFinding = {
  type: "structure",
  message: "wf.nodes.[1](summarize).model: call_llm node requires a model",
  suggestion: "pick a model",
  nodeId: "summarize",
  field: "model",
  detail: "call_llm node requires a model",
  path: "wf.nodes.[1](summarize).model",
} as ValidationError;

const byNode = findingsByNode(locateFindings([modelFinding]));
const modelSchema: ProtoFieldSchema = { key: "model", label: "Model", widget: "text" };

function renderField(nodeId: string, focus: FindingFocus | null = null) {
  return render(
    <WorkflowFindingsProvider byNode={byNode} focus={focus}>
      <NodeFindingsScope nodeId={nodeId}>
        <ProtoFieldRenderer schema={modelSchema} value="" onChange={() => {}} />
      </NodeFindingsScope>
    </WorkflowFindingsProvider>,
  );
}

describe("a field's own problem", () => {
  it("is shown under the field, with its remedy, and outlines it", () => {
    const { container } = renderField("summarize");
    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("call_llm node requires a model");
    expect(alert).toHaveTextContent("pick a model");
    expect(container.querySelector(".cpv2-field--invalid")).not.toBeNull();
  });

  it("belongs to its step only", () => {
    renderField("another_step");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("takes focus when the problem is picked in the problems list", () => {
    renderField("summarize", { nodeId: "summarize", fieldKey: "model", seq: 1 });
    expect(screen.getByLabelText("Model")).toHaveFocus();
  });

  it("renders nothing extra outside the builder (no provider)", () => {
    render(<ProtoFieldRenderer schema={modelSchema} value="" onChange={() => {}} />);
    expect(screen.queryByRole("alert")).toBeNull();
  });
});

describe("a step's problems on the canvas", () => {
  it("marks only the steps with problems, with a count", () => {
    const nodes = [
      { id: "summarize", position: { x: 0, y: 0 }, data: {}, ariaLabel: "Call LLM, summarize, not connected" },
      { id: "ask", position: { x: 0, y: 0 }, data: {} },
    ] as Node[];
    const [summarize, ask] = withFindingMarkers(nodes, byNode);
    expect(summarize!.className).toContain(NODE_PROBLEM_CLASS);
    expect(summarize!.data).toMatchObject({ problemCount: 1 });
    // The problems are read as part of the step's own name.
    expect(summarize!.ariaLabel).toBe("Call LLM, summarize, not connected, 1 problem");
    expect(ask).toBe(nodes[1]); // untouched

    const Inner = () => <div>step</div>;
    const Marked = withProblemMarker(Inner as unknown as React.ComponentType<NodeProps>);
    render(<Marked {...({ id: "summarize", data: summarize!.data } as unknown as NodeProps)} />);
    expect(screen.getByTestId("node-problem-badge")).toHaveTextContent("1");
  });
});
