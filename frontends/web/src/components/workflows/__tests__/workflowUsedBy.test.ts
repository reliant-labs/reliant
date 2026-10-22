// Copyright (c) 2025 Reliant Labs

/**
 * "Used by" (WORKFLOW_UI.md §2.3): which workflows reference a workflow
 * through `ref:`, scanned from the definitions ListWorkflows already returns.
 * Refs resolve the way the runtime loads them: `builtin://x` exactly, anything
 * else by slug.
 */

import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";

import type { WorkflowResponse } from "@/api/workflow-grpc";
import {
  CelStringSchema,
  LoopArgsSchema,
  NodeSchema,
  RouterArgsSchema,
  RouterWorkflowCandidateSchema,
  SubWorkflowArgsSchema,
  WorkflowSchema,
  type Node,
} from "@/gen/reliant/v1/workflow_v2_pb";
import { refKey, workflowSlug, workflowsUsing } from "../detail/workflowUsedBy";

const literal = (value: string) => create(CelStringSchema, { value: { case: "literal", value } });
const expr = (value: string) => create(CelStringSchema, { value: { case: "expr", value } });

function callNode(id: string, ref: ReturnType<typeof literal>): Node {
  return create(NodeSchema, { id, type: "workflow", args: { case: "workflow", value: create(SubWorkflowArgsSchema, { ref }) } });
}

function loopNode(id: string, body: { ref?: string; inline?: Node[] }): Node {
  return create(NodeSchema, {
    id,
    type: "loop",
    args: {
      case: "loop",
      value: create(LoopArgsSchema, {
        ref: body.ref ? literal(body.ref) : undefined,
        inline: body.inline ? create(WorkflowSchema, { name: "body", nodes: body.inline }) : undefined,
      }),
    },
  });
}

function routerNode(id: string, refs: string[]): Node {
  return create(NodeSchema, {
    id,
    type: "router",
    args: {
      case: "router",
      value: create(RouterArgsSchema, { workflows: refs.map((ref) => create(RouterWorkflowCandidateSchema, { ref })) }),
    },
  });
}

function workflow(name: string, nodes: Node[], source: WorkflowResponse["source"] = "project"): WorkflowResponse {
  return {
    name,
    filename: name.replace("builtin://", ""),
    source,
    stepCount: nodes.length,
    status: "complete",
    validationErrors: [],
    nodes,
    edges: [],
  } as WorkflowResponse;
}

describe("workflowSlug / refKey", () => {
  it("slugs like the runtime's generateWorkflowSlug", () => {
    expect(workflowSlug("  My Flow_v2! ")).toBe("my-flow-v2");
    expect(workflowSlug("a--b")).toBe("a-b");
  });

  it("keeps builtin refs exact and slugs the rest, ignoring project://", () => {
    expect(refKey("builtin://agent")).toBe("builtin://agent");
    expect(refKey("Nightly Triage")).toBe("slug:nightly-triage");
    expect(refKey("project://nightly-triage")).toBe("slug:nightly-triage");
  });

  it("cannot resolve a CEL-computed ref", () => {
    expect(refKey("{{ inputs.flow }}")).toBeNull();
    expect(refKey("")).toBeNull();
  });
});

describe("workflowsUsing", () => {
  const library = [
    workflow("builtin://agent", [], "builtin"),
    workflow("builtin://implement-review", [callNode("implement", literal("builtin://agent"))], "builtin"),
    workflow("triage", [loopNode("each", { inline: [callNode("inner", literal("builtin://agent"))] })]),
    workflow("router-flow", [routerNode("route", ["triage", "builtin://agent"])]),
    workflow("dynamic", [callNode("pick", expr("{{ inputs.flow }}"))]),
    workflow("loops-triage", [loopNode("each", { ref: "project://triage" })], "user"),
    workflow("unrelated", [callNode("x", literal("builtin://structured-agent"))]),
  ];

  it("finds direct refs, refs inside inline bodies, and router candidates", () => {
    expect(workflowsUsing("builtin://agent", library).map((w) => w.name)).toEqual([
      "builtin://implement-review",
      "router-flow",
      "triage",
    ]);
  });

  it("matches a project workflow by slug whatever the ref's spelling", () => {
    expect(workflowsUsing("triage", library).map((w) => w.name)).toEqual(["loops-triage", "router-flow"]);
  });

  it("never lists the workflow itself, and is empty when nothing calls it", () => {
    const selfCalling = [workflow("recurse", [callNode("again", literal("recurse"))])];
    expect(workflowsUsing("recurse", selfCalling)).toEqual([]);
    expect(workflowsUsing("unrelated", library)).toEqual([]);
  });

  it("a builtin and a project workflow with the same short name are different workflows", () => {
    const shadow = [
      workflow("agent", [], "project"),
      workflow("calls-project-agent", [callNode("x", literal("agent"))]),
    ];
    expect(workflowsUsing("builtin://agent", shadow)).toEqual([]);
    expect(workflowsUsing("agent", shadow).map((w) => w.name)).toEqual(["calls-project-agent"]);
  });
});
