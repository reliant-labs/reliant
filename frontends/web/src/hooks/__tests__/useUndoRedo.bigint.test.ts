/**
 * Undo history snapshots the builder's canvas, whose nodes carry their step
 * proto. A step's int64 fields are bigints (a CelInt literal such as
 * call_llm's max_tokens), and the snapshot used to be a
 * JSON.parse(JSON.stringify(...)) copy, which throws on a bigint: the first
 * edit or drag of such a step threw "Do not know how to serialize a BigInt".
 */
import { describe, expect, it } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { fromJson } from "@bufbuild/protobuf";

import { WorkflowSchema } from "../../gen/reliant/v1/workflow_v2_pb";
import type { Workflow } from "../../types/workflow";
import { workflowToFlowElements } from "../../lib/workflow-flow";
import { useUndoRedo } from "../useUndoRedo";

function canvasWithMaxTokens(maxTokens: string) {
  const workflow = fromJson(WorkflowSchema, {
    name: "ask",
    entry: ["ask"],
    nodes: [{ id: "ask", type: "call_llm", callLlm: { maxTokens: { literal: maxTokens } } }],
  } as never) as Workflow;
  return workflowToFlowElements(workflow);
}

function maxTokensOf(nodes: Array<{ id: string; data: unknown }>): unknown {
  const step = (nodes.find((n) => n.id === "ask")!.data as { step: { args: { value: { maxTokens: { value: { value: unknown } } } } } }).step;
  return step.args.value.maxTokens.value.value;
}

describe("useUndoRedo with int64 fields on the canvas", () => {
  it("snapshots, undoes and redoes a step holding a bigint, keeping it exact", () => {
    const before = canvasWithMaxTokens("9007199254740993"); // 2^53 + 1
    const after = canvasWithMaxTokens("4096");
    expect(maxTokensOf(before.nodes)).toBe(9007199254740993n);

    const { result } = renderHook(() => useUndoRedo());
    act(() => result.current.takeSnapshot(before.nodes, before.edges));
    expect(result.current.canUndo).toBe(true);

    let undone: ReturnType<typeof result.current.undo> = null;
    act(() => {
      undone = result.current.undo(after.nodes, after.edges);
    });
    expect(maxTokensOf(undone!.nodes)).toBe(9007199254740993n);

    let redone: ReturnType<typeof result.current.redo> = null;
    act(() => {
      redone = result.current.redo();
    });
    expect(maxTokensOf(redone!.nodes)).toBe(4096n);
  });

  it("keeps a snapshot independent of later edits to the live canvas", () => {
    const canvas = canvasWithMaxTokens("100");
    const { result } = renderHook(() => useUndoRedo());
    act(() => result.current.takeSnapshot(canvas.nodes, canvas.edges));
    // Mutate the live canvas in place; history must not see it.
    (canvas.nodes[0].data as { label: string }).label = "edited";

    let undone: ReturnType<typeof result.current.undo> = null;
    act(() => {
      undone = result.current.undo(canvas.nodes, canvas.edges);
    });
    expect((undone!.nodes[0].data as { label: string }).label).not.toBe("edited");
  });
});
