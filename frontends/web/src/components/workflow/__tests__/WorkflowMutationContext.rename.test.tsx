/**
 * Renaming a step in the config panel (WorkflowMutations.renameNode) moves
 * everything that named it: edges, and every `nodes.<old>` expression in
 * other steps, Switch cases and the workflow's outputs. Before, only edges
 * moved, so a rename silently broke every step that read the old id.
 */

import { describe, expect, it } from "vitest";
import { act, render } from "@testing-library/react";
import type { Edge, Node } from "@xyflow/react";
import { useState } from "react";

import { celString } from "@/lib/celAdapter";
import type { Workflow } from "@/types/workflow";
import { WorkflowMutationProvider, useWorkflowMutations, type WorkflowMutations } from "../WorkflowMutationContext";

const initialNodes: Node[] = [
  { id: "workflow", type: "eventNode", position: { x: 0, y: 0 }, data: { eventType: "started" } },
  { id: "think", type: "actionNode", position: { x: 300, y: 0 }, data: { label: "think", step: { id: "think", type: "call_llm" } } },
  {
    id: "act",
    type: "actionNode",
    position: { x: 600, y: 0 },
    data: { label: "act", step: { id: "act", type: "execute_tools", args: { case: "executeTools", value: { toolCalls: celString("{{nodes.think.tool_calls}}") } } } },
  },
  { id: "route", type: "switchNode", position: { x: 900, y: 0 }, data: { cases: [{ id: "c1", condition: "nodes.think.tool_calls.size() > 0" }] } },
];
const initialEdges: Edge[] = [
  { id: "workflow-think", source: "workflow", target: "think" },
  { id: "think-act", source: "think", target: "act" },
];

function renderProvider() {
  const state: { nodes: Node[]; edges: Edge[]; workflow: Workflow; mutations?: WorkflowMutations } = {
    nodes: initialNodes,
    edges: initialEdges,
    workflow: { name: "w", outputs: { answer: "{{nodes.think.content}}" } } as unknown as Workflow,
  };
  function Capture() {
    state.mutations = useWorkflowMutations();
    return null;
  }
  function Harness() {
    const [nodes, setNodes] = useState(initialNodes);
    const [edges, setEdges] = useState(initialEdges);
    const [workflow, setWorkflow] = useState(state.workflow);
    state.nodes = nodes;
    state.edges = edges;
    state.workflow = workflow;
    return (
      <WorkflowMutationProvider
        nodes={nodes}
        edges={edges}
        setNodes={setNodes}
        setEdges={setEdges}
        setHasModifications={() => undefined}
        takeSnapshot={() => undefined}
        setSelectedNodeId={() => undefined}
        setSelectedEdgeId={() => undefined}
        setWorkflow={setWorkflow}
      >
        <Capture />
      </WorkflowMutationProvider>
    );
  }
  render(<Harness />);
  return state;
}

describe("renameNode", () => {
  it("rewrites edges and every expression that named the old id", () => {
    const state = renderProvider();
    act(() => state.mutations!.renameNode("think", "plan"));

    expect(state.nodes.map((n) => n.id)).toEqual(["workflow", "plan", "act", "route"]);
    expect(state.edges.map((e) => `${e.source}>${e.target}`)).toEqual(["workflow>plan", "plan>act"]);

    const act_ = state.nodes.find((n) => n.id === "act")!.data as { step: { args: { value: { toolCalls: unknown } } } };
    expect(act_.step.args.value.toolCalls).toEqual(celString("{{nodes.plan.tool_calls}}"));
    const route = state.nodes.find((n) => n.id === "route")!.data as { cases: Array<{ condition: string }> };
    expect(route.cases[0]!.condition).toBe("nodes.plan.tool_calls.size() > 0");
    expect(state.workflow.outputs).toEqual({ answer: "{{nodes.plan.content}}" });
  });
});
