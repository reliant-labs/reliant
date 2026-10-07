/**
 * useCanvasInsertion: a step added from the palette lands where the "+" that
 * opened it asked; with no "+", after the selection or the end of the main
 * path. Every insert is undoable, marks the workflow dirty, becomes the
 * selection, and — for Run LLM Tool Calls after a Call LLM — gets its
 * tool_calls.
 */

import { describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { ReactFlowProvider, type Edge, type Node } from "@xyflow/react";
import type { ReactNode } from "react";

import { getActionArgValue } from "@/lib/actionStepArgs";
import { celString } from "@/lib/celAdapter";
import { initStepArgs, type Step } from "@/types/workflow";
import { useCanvasInsertion, type UseCanvasInsertionOptions } from "../CanvasInsertContext";

const start: Node = { id: "workflow", type: "eventNode", position: { x: 0, y: 0 }, measured: { width: 150, height: 60 }, data: { eventType: "started" } };
const stepNode = (id: string, type: string, x: number, extra: Partial<Node> = {}): Node => ({
  id,
  type: "actionNode",
  position: { x, y: 0 },
  measured: { width: 200, height: 80 },
  data: { label: id, step: { id, type, args: initStepArgs(type) } },
  ...extra,
});
const fresh = (id: string, type: string): Node => ({ id, type: "actionNode", position: { x: 0, y: 0 }, data: { label: id, step: { id, type, args: initStepArgs(type) } } });

function setup(nodes: Node[], edges: Edge[], overrides: Partial<UseCanvasInsertionOptions> = {}) {
  const calls = { nodes: [] as Node[][], edges: [] as Edge[][] };
  const options: UseCanvasInsertionOptions = {
    enabled: true,
    nodes,
    edges,
    setNodes: (next) => calls.nodes.push(next),
    setEdges: (next) => calls.edges.push(next),
    takeSnapshot: vi.fn(),
    markDirty: vi.fn(),
    selectedNodeId: null,
    paletteOpen: false,
    openPalette: vi.fn(),
    createEdge: vi.fn(),
    fallbackPosition: () => ({ x: 10, y: 10 }),
    ...overrides,
  };
  const wrapper = ({ children }: { children: ReactNode }) => <ReactFlowProvider>{children}</ReactFlowProvider>;
  const hook = renderHook((props: UseCanvasInsertionOptions) => useCanvasInsertion(props), { wrapper, initialProps: options });
  return { hook, options, calls };
}

describe("useCanvasInsertion", () => {
  it("lands the palette's step at the + that opened it, connected", () => {
    const nodes = [start, stepNode("think", "call_llm", 300), stepNode("done", "run", 900)];
    const edges: Edge[] = [{ id: "e1", source: "workflow", target: "think" }, { id: "e2", source: "think", target: "done" }];
    const { hook, options, calls } = setup(nodes, edges);

    act(() => hook.result.current.api!.openPaletteAt({ kind: "splice", edgeId: "e2" }));
    expect(options.openPalette).toHaveBeenCalled();
    hook.rerender({ ...options, paletteOpen: true });
    act(() => {
      hook.result.current.insertNode(fresh("run", "run"));
    });

    const finalEdges = calls.edges.at(-1)!;
    expect(finalEdges.map((e) => `${e.source}>${e.target}`)).toEqual(["workflow>think", "think>run", "run>done"]);
    expect(options.takeSnapshot).toHaveBeenCalledWith(nodes, edges);
    expect(options.markDirty).toHaveBeenCalled();
  });

  it("forgets the + once the palette closes without a choice", () => {
    const nodes = [start, stepNode("think", "call_llm", 300), stepNode("done", "run", 900)];
    const edges: Edge[] = [{ id: "e1", source: "workflow", target: "think" }, { id: "e2", source: "think", target: "done" }];
    const { hook, options, calls } = setup(nodes, edges);

    act(() => hook.result.current.api!.openPaletteAt({ kind: "splice", edgeId: "e2" }));
    hook.rerender({ ...options, paletteOpen: true });
    hook.rerender({ ...options, paletteOpen: false });
    act(() => {
      hook.result.current.insertNode(fresh("run", "run"));
    });
    // Not spliced: after the end of the main path instead.
    expect(calls.edges.at(-1)!.map((e) => `${e.source}>${e.target}`)).toEqual(["workflow>think", "think>done", "done>run"]);
  });

  it("goes after the selected step, and makes the new step the selection", () => {
    const nodes = [start, stepNode("think", "call_llm", 300, { selected: true }), stepNode("done", "run", 900)];
    const { hook, calls } = setup(nodes, [{ id: "e1", source: "workflow", target: "think" }, { id: "e2", source: "think", target: "done" }]);
    let inserted: Node | undefined;
    act(() => {
      inserted = hook.result.current.insertNode(fresh("run", "run"));
    });
    expect(calls.edges.at(-1)!.at(-1)).toMatchObject({ source: "think", target: "run" });
    expect(inserted?.selected).toBe(true);
    expect(calls.nodes.at(-1)!.filter((n) => n.selected).map((n) => n.id)).toEqual(["run"]);
  });

  it("fills in Run LLM Tool Calls' tool_calls when it lands after a Call LLM", () => {
    const nodes = [start, stepNode("think", "call_llm", 300, { selected: true })];
    const { hook } = setup(nodes, [{ id: "e1", source: "workflow", target: "think" }]);
    let inserted: Node | undefined;
    act(() => {
      inserted = hook.result.current.insertNode(fresh("execute_tools", "execute_tools"));
    });
    expect(getActionArgValue((inserted!.data as { step: Step }).step, "tool_calls")).toEqual(celString("{{nodes.think.tool_calls}}"));
  });

  it("connects two steps through the builder's edge drawing, and refuses a bad pair", () => {
    const nodes = [start, stepNode("think", "call_llm", 300), stepNode("done", "run", 900)];
    const { hook, options } = setup(nodes, [{ id: "e1", source: "workflow", target: "think" }]);
    expect(hook.result.current.api!.connect("think", "done")).toBe(true);
    expect(options.createEdge).toHaveBeenCalledWith("think", "done", undefined);
    expect(hook.result.current.api!.connect("done", "workflow")).toBe(false);
    expect(options.createEdge).toHaveBeenCalledTimes(1);
  });

  it("offers no commands on a read-only canvas", () => {
    const { hook } = setup([start], [], { enabled: false });
    expect(hook.result.current.api).toBeNull();
  });
});
