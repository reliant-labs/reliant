/**
 * Where a new step lands (WORKFLOW_EDITOR_UX_REVIEW §1 issue 3): connected,
 * after the "+" that asked for it, the selection, or the end of the main
 * path; placed in the next column and clear of every other node; spliced
 * into an edge by making room downstream. A step lands unconnected only on a
 * canvas with no nodes.
 */

import { describe, expect, it } from "vitest";
import type { Edge, Node } from "@xyflow/react";

import { actionNodeIdBase, uniqueNodeId } from "@/lib/actionNodeArgs";
import { nodesEdgesToWorkflow } from "@/lib/nodes-edges-to-workflow";
import {
  INSERT_GAP_X,
  connectionOrder,
  connectionProblem,
  describeCase,
  mainPathTail,
  nodeSize,
  planInsert,
  readableStepId,
  resolveAnchor,
  withNodeAriaLabels,
} from "../insertPlacement";

const SIZE = { width: 200, height: 80 };

function node(id: string, x: number, y: number, extra: Partial<Node> = {}): Node {
  return { id, type: "actionNode", position: { x, y }, measured: SIZE, data: { label: id, step: { id, type: "call_llm" } }, ...extra };
}
const start = (x = 0, y = 0): Node => ({
  id: "workflow",
  type: "eventNode",
  position: { x, y },
  measured: { width: 150, height: 60 },
  data: { eventType: "started", label: "Start" },
});
const switchNode = (id: string, x: number, y: number, cases: string[]): Node => ({
  id,
  type: "switchNode",
  position: { x, y },
  measured: { width: 200, height: 44 + cases.length * 40 + 8 },
  data: { label: "Switch", cases: cases.map((caseId, i) => ({ id: caseId, condition: i < cases.length - 1 ? "x" : "", label: "" })) },
});
const edge = (source: string, target: string, extra: Partial<Edge> = {}): Edge => ({ id: `${source}->${target}`, source, target, type: "custom", ...extra });
const fresh = (id: string): Node => ({ id, type: "actionNode", position: { x: 0, y: 0 }, data: { label: id, step: { id, type: "run" } } });

function overlapping(a: Node, b: Node): boolean {
  const sa = nodeSize(a);
  const sb = nodeSize(b);
  return a.position.x < b.position.x + sb.width && b.position.x < a.position.x + sa.width && a.position.y < b.position.y + sb.height && b.position.y < a.position.y + sa.height;
}

describe("readableStepId", () => {
  it("names a step for its type, numbering repeats", () => {
    expect(readableStepId("call_llm", ["workflow"])).toBe("call_llm");
    expect(readableStepId("call_llm", ["workflow", "call_llm"])).toBe("call_llm_2");
    expect(readableStepId("call_llm", ["call_llm", "call_llm_2"])).toBe("call_llm_3");
  });

  it("calls the Agent step agent, since its type is the start node's id", () => {
    expect(readableStepId("workflow", ["workflow"])).toBe("agent");
  });

  it("is always CEL-safe and never a timestamp", () => {
    expect(readableStepId("run", [])).toBe("run");
    expect(readableStepId("3d-render", [])).toBe("step_3d_render");
    expect(readableStepId("", [])).toBe("step");
    expect(readableStepId("call_llm", [])).not.toMatch(/\d{6,}/);
  });

  it("names an integration action for its integration and action", () => {
    expect(uniqueNodeId(actionNodeIdBase("slack/message.post@1"), [])).toBe("slack_message_post");
    expect(uniqueNodeId(actionNodeIdBase("slack/message.post@1"), ["slack_message_post"])).toBe("slack_message_post_2");
    expect(actionNodeIdBase("github/issue.create@1")).toBe("github_issue_create");
    expect(actionNodeIdBase("http/request@1")).toBe("http_request");
    expect(actionNodeIdBase("3rd-party/do.it@2")).toBe("action_3rd_party_do_it");
  });
});

describe("resolveAnchor", () => {
  const nodes = [start(), node("think", 300, 0), node("act", 600, 0)];
  const edges = [edge("workflow", "think"), edge("think", "act")];

  it("uses the anchor a + asked for", () => {
    expect(resolveAnchor({ pending: { kind: "splice", edgeId: "think->act" }, nodes, edges, selectedNodeId: "think" })).toEqual({
      kind: "splice",
      edgeId: "think->act",
    });
  });

  it("falls back to the selection when the + target is gone", () => {
    expect(resolveAnchor({ pending: { kind: "splice", edgeId: "nope" }, nodes, edges, selectedNodeId: "think" })).toMatchObject({
      kind: "after",
      nodeId: "think",
    });
  });

  it("prefers React Flow's selection, which keyboard selection updates", () => {
    const selected = nodes.map((n) => (n.id === "act" ? { ...n, selected: true } : n));
    expect(resolveAnchor({ pending: null, nodes: selected, edges, selectedNodeId: "think" })).toMatchObject({ nodeId: "act" });
  });

  it("with nothing selected, goes after the end of the main path", () => {
    expect(resolveAnchor({ pending: null, nodes, edges, selectedNodeId: null })).toMatchObject({ kind: "after", nodeId: "act" });
  });

  it("is null only for an empty canvas", () => {
    expect(resolveAnchor({ pending: null, nodes: [], edges: [], selectedNodeId: null })).toBeNull();
    expect(resolveAnchor({ pending: null, nodes: [start()], edges: [], selectedNodeId: null })).toMatchObject({ nodeId: "workflow" });
  });

  it("attaches after a Switch through its first case that leads nowhere", () => {
    const sw = switchNode("route", 300, 0, ["yes", "no"]);
    expect(resolveAnchor({ pending: null, nodes: [start(), { ...sw, selected: true }], edges: [edge("route", "x", { sourceHandle: "yes" })], selectedNodeId: null })).toEqual({
      kind: "after",
      nodeId: "route",
      sourceHandle: "no",
    });
  });
});

describe("mainPathTail", () => {
  it("is the furthest step with nothing after it, ignoring loop-backs", () => {
    // An agent loop: think → route → (yes) act → think; (default) done.
    const nodes = [start(), node("think", 300, 0), switchNode("route", 600, 0, ["yes", "no"]), node("act", 900, 200), node("done", 900, 0)];
    const edges = [
      edge("workflow", "think"),
      edge("think", "route"),
      edge("route", "act", { sourceHandle: "yes" }),
      edge("act", "think"),
      edge("route", "done", { sourceHandle: "no" }),
    ];
    expect(mainPathTail(nodes, edges)?.id).toBe("done");
  });

  it("is the rightmost reachable node when everything loops", () => {
    const nodes = [start(), node("a", 300, 0), node("b", 600, 0)];
    expect(mainPathTail(nodes, [edge("workflow", "a"), edge("a", "b"), edge("b", "a")])?.id).toBe("b");
  });
});

describe("planInsert: after a node", () => {
  it("lands in the next column, level with the output, and wired to it", () => {
    const nodes = [start(), node("think", 300, 100)];
    const plan = planInsert({ nodes, edges: [edge("workflow", "think")], node: fresh("run"), anchor: { kind: "after", nodeId: "think" }, fallbackPosition: { x: 0, y: 0 } });
    expect(plan.inserted.position).toEqual({ x: 300 + SIZE.width + INSERT_GAP_X, y: 100 + SIZE.height / 2 - nodeSize(fresh("run")).height / 2 });
    expect(plan.edges.at(-1)).toMatchObject({ source: "think", target: "run", type: "custom" });
    expect(plan.nodes).toHaveLength(3);
  });

  it("puts a second branch below the first, and never on top of a node", () => {
    const nodes = [start(), node("think", 300, 100), node("act", 580, 100), node("blocker", 580, 220)];
    const plan = planInsert({ nodes, edges: [edge("think", "act")], node: fresh("run"), anchor: { kind: "after", nodeId: "think" }, fallbackPosition: { x: 0, y: 0 } });
    expect(plan.inserted.position.y).toBeGreaterThan(220 + SIZE.height);
    for (const other of nodes) expect(overlapping(plan.inserted, other)).toBe(false);
  });

  it("wires a Switch case, and the start node's edge carries its event", () => {
    const sw = switchNode("route", 300, 0, ["yes", "no"]);
    const fromCase = planInsert({ nodes: [start(), sw], edges: [], node: fresh("run"), anchor: { kind: "after", nodeId: "route", sourceHandle: "no" }, fallbackPosition: { x: 0, y: 0 } });
    expect(fromCase.edges.at(-1)).toMatchObject({ source: "route", target: "run", sourceHandle: "no" });

    const fromStart = planInsert({ nodes: [start()], edges: [], node: fresh("run"), anchor: { kind: "after", nodeId: "workflow" }, fallbackPosition: { x: 0, y: 0 } });
    expect(fromStart.edges.at(-1)).toMatchObject({ source: "workflow", target: "run", data: { sourceEvent: "started" } });
  });
});

describe("planInsert: into an edge", () => {
  it("splits the edge in two, keeping the route's data on the first half", () => {
    const nodes = [start(), node("think", 300, 0), node("act", 1000, 0)];
    const edges = [edge("think", "act", { sourceHandle: "h", data: { label: "next", siblingIndex: 0, totalSiblings: 1 } })];
    const plan = planInsert({ nodes, edges, node: fresh("run"), anchor: { kind: "splice", edgeId: "think->act" }, fallbackPosition: { x: 0, y: 0 } });
    expect(plan.edges.find((e) => e.id === "think->act")).toBeUndefined();
    expect(plan.edges).toEqual(
      expect.arrayContaining([
        expect.objectContaining({ source: "think", target: "run", sourceHandle: "h", data: { label: "next" } }),
        expect.objectContaining({ source: "run", target: "act" }),
      ]),
    );
    // Enough room between them: centred in the gap, nothing moved.
    expect(plan.inserted.position.x).toBeGreaterThan(500);
    expect(plan.inserted.position.x + nodeSize(plan.inserted).width).toBeLessThan(1000);
    expect(plan.nodes.find((n) => n.id === "act")?.position.x).toBe(1000);
  });

  it("makes room by moving what is downstream right, and leaves loop-backs alone", () => {
    const nodes = [start(), node("think", 300, 0), node("act", 580, 0), node("after", 860, 0), node("other", 300, 300)];
    // after → think loops back; other is upstream-only.
    const edges = [edge("workflow", "think"), edge("think", "act"), edge("act", "after"), edge("after", "think"), edge("workflow", "other")];
    const plan = planInsert({ nodes, edges, node: fresh("run"), anchor: { kind: "splice", edgeId: "think->act" }, fallbackPosition: { x: 0, y: 0 } });
    const at = (id: string) => plan.nodes.find((n) => n.id === id)!.position;
    expect(at("act").x).toBeGreaterThan(580);
    expect(at("after").x - at("act").x).toBe(280);
    expect(at("think").x).toBe(300);
    expect(at("other")).toEqual({ x: 300, y: 300 });
    for (const other of plan.nodes.filter((n) => n.id !== "run")) expect(overlapping(plan.inserted, other)).toBe(false);
  });

  it("opens a gap even when the end starts inside the start's width, and goes below a loop-back", () => {
    // The trigger rail is 300 wide, so a step at x=300 starts inside its span.
    const rail: Node = { ...start(), type: "triggerRailNode", measured: { width: 300, height: 150 } };
    const nodes = [rail, node("think", 300, 0)];
    const forward = planInsert({ nodes, edges: [edge("workflow", "think")], node: fresh("run"), anchor: { kind: "splice", edgeId: "workflow->think" }, fallbackPosition: { x: 0, y: 0 } });
    expect(forward.inserted.position.x).toBe(300 + INSERT_GAP_X);
    expect(forward.nodes.find((n) => n.id === "think")!.position.x).toBeGreaterThan(forward.inserted.position.x + 200);

    const loop = planInsert({
      nodes: [node("a", 300, 0), node("b", 600, 0)],
      edges: [edge("b", "a")],
      node: fresh("run"),
      anchor: { kind: "splice", edgeId: "b->a" },
      fallbackPosition: { x: 0, y: 0 },
    });
    expect(loop.inserted.position.y).toBeGreaterThanOrEqual(80 + 40);
    expect(loop.nodes.find((n) => n.id === "a")!.position).toEqual({ x: 300, y: 0 });
  });
});

describe("what an insert saves as", () => {
  const meta = { name: "w", description: "", inputs: {}, outputs: {} };

  it("a first step after the start is the workflow's entry", () => {
    const plan = planInsert({ nodes: [start()], edges: [], node: fresh("run"), anchor: { kind: "after", nodeId: "workflow" }, fallbackPosition: { x: 0, y: 0 } });
    expect(nodesEdgesToWorkflow(plan.nodes, plan.edges, meta).entry).toEqual(["run"]);
  });

  it("a step spliced into an edge sits between its ends", () => {
    const nodes = [start(), node("think", 300, 0), node("done", 1000, 0)];
    const plan = planInsert({
      nodes,
      edges: [edge("workflow", "think", { data: { sourceEvent: "started" } }), edge("think", "done")],
      node: fresh("run"),
      anchor: { kind: "splice", edgeId: "think->done" },
      fallbackPosition: { x: 0, y: 0 },
    });
    expect(nodesEdgesToWorkflow(plan.nodes, plan.edges, meta).edges).toEqual([
      { from: "think", default: ["run"] },
      { from: "run", default: ["done"] },
    ]);
  });

  it("a step after a Switch case runs when that case's condition holds", () => {
    const sw = switchNode("route", 600, 0, ["yes", "no"]);
    const nodes = [start(), node("think", 300, 0), sw];
    const plan = planInsert({
      nodes,
      edges: [edge("workflow", "think", { data: { sourceEvent: "started" } }), edge("think", "route")],
      node: fresh("run"),
      anchor: { kind: "after", nodeId: "route", sourceHandle: "yes" },
      fallbackPosition: { x: 0, y: 0 },
    });
    expect(nodesEdgesToWorkflow(plan.nodes, plan.edges, meta).edges).toEqual([
      { from: "think", cases: [{ to: ["run"], condition: "x", label: undefined }] },
    ]);
  });
});

describe("planInsert: nothing to attach to", () => {
  it("places freely on an empty canvas, clear of anything", () => {
    const plan = planInsert({ nodes: [], edges: [], node: fresh("run"), anchor: null, fallbackPosition: { x: 40, y: 50 } });
    expect(plan.inserted.position).toEqual({ x: 40, y: 50 });
    expect(plan.edges).toEqual([]);
  });
});

describe("describeCase", () => {
  it("never gives two cases of a new Switch the same name", () => {
    const fresh = { ...switchNode("s", 0, 0, ["a", "b", "c"]), data: { cases: [{ id: "a", condition: "" }, { id: "b", condition: "" }, { id: "c", condition: "" }] } };
    expect(["a", "b", "c"].map((h) => describeCase(fresh, h))).toEqual(["1", "2", "default"]);
    const labelled = { ...fresh, data: { cases: [{ id: "a", condition: "x > 1", label: "" }, { id: "b", condition: "", label: "fallback" }] } };
    expect(["a", "b"].map((h) => describeCase(labelled, h))).toEqual(["x > 1", "fallback"]);
    expect(describeCase(fresh, "nope")).toBeUndefined();
  });
});

describe("withNodeAriaLabels", () => {
  const name = (type: string) => (type === "call_llm" ? "Call LLM" : type);

  it("names each node for its kind and id, and says when nothing leads into it", () => {
    const nodes = [start(), node("think", 300, 0), node("orphan", 300, 300), switchNode("route", 600, 0, ["a"])];
    const labelled = withNodeAriaLabels(nodes, [edge("workflow", "think"), edge("think", "route")], name);
    expect(labelled.map((n) => n.ariaLabel)).toEqual(["Start", "Call LLM, think", "Call LLM, orphan, not connected", "Switch, route"]);
  });

  it("returns the same array when every label is current", () => {
    const labelled = withNodeAriaLabels([start(), node("think", 300, 0)], [edge("workflow", "think")], name);
    expect(withNodeAriaLabels(labelled, [edge("workflow", "think")], name)).toBe(labelled);
  });
});

describe("connecting two selected steps", () => {
  it("runs from the start node, else left to right", () => {
    expect(connectionOrder(node("b", 600, 0), start()).map((n) => n.id)).toEqual(["workflow", "b"]);
    expect(connectionOrder(node("b", 600, 0), node("a", 300, 0)).map((n) => n.id)).toEqual(["a", "b"]);
    expect(connectionOrder(node("b", 300, 200), node("a", 300, 0)).map((n) => n.id)).toEqual(["a", "b"]);
  });

  it("refuses an edge into the start, to itself, or one that exists", () => {
    const a = node("a", 0, 0);
    const b = node("b", 300, 0);
    expect(connectionProblem(a, start(), [])).toMatch(/start/);
    expect(connectionProblem(a, a, [])).toMatch(/itself/);
    expect(connectionProblem(a, b, [edge("a", "b")])).toBe("Already connected");
    expect(connectionProblem(a, b, [])).toBeNull();
  });
});
