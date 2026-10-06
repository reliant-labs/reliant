// Copyright (c) 2025 Reliant Labs

/**
 * Run LLM Tool Calls (`execute_tools`) runs the tool calls a Call LLM step
 * returned; its `tool_calls` input is almost always
 * `{{nodes.<that call_llm>.tool_calls}}`. When the author wires it downstream
 * of a Call LLM, the builder writes that in for them instead of leaving an
 * empty box whose expected value nothing on screen explains.
 *
 * "Downstream" sees through Switch nodes, which exist only on the canvas (they
 * compile into edge conditions), because the usual agent loop branches on
 * "did the model ask for tools?" between the two steps.
 */

import type { Edge, Node } from "@xyflow/react";

import { getActionArgValue, withActionArg } from "../../lib/actionStepArgs";
import { normalizeCelString } from "../../lib/celAdapter";
import type { Step } from "../../types/workflow";

const SWITCH_NODE_TYPE = "switchNode";

function stepOf(node: Node | undefined): Step | undefined {
  return (node?.data as { step?: Step } | undefined)?.step;
}

/**
 * The Call LLM step feeding `sourceId`: the node itself, or the nearest one
 * behind a chain of Switch nodes. Undefined when there is none, or when two
 * different Call LLM steps are equally near (a guess would be wrong half the
 * time, and an empty field is honest).
 */
export function upstreamCallLlm(sourceId: string, nodes: readonly Node[], edges: readonly Edge[]): string | undefined {
  const byId = new Map(nodes.map((node) => [node.id, node]));
  const seen = new Set<string>();
  let frontier = [sourceId];
  while (frontier.length > 0) {
    const found = new Set<string>();
    const next: string[] = [];
    for (const id of frontier) {
      if (seen.has(id)) continue;
      seen.add(id);
      const node = byId.get(id);
      if (stepOf(node)?.type === "call_llm") {
        found.add(id);
        continue;
      }
      if (node?.type !== SWITCH_NODE_TYPE) continue;
      for (const edge of edges) if (edge.target === id) next.push(edge.source);
    }
    if (found.size === 1) return [...found][0];
    if (found.size > 1) return undefined;
    frontier = next;
  }
  return undefined;
}

/**
 * The step with `tool_calls` pointing at `callLlmId`, or undefined when the
 * step is not Run LLM Tool Calls or already has a value: the author's
 * expression always wins over the default.
 */
export function withDefaultToolCalls(step: Step, callLlmId: string): Step | undefined {
  if (step.type !== "execute_tools") return undefined;
  if (normalizeCelString(getActionArgValue(step, "tool_calls")).trim() !== "") return undefined;
  return withActionArg(step, "tool_calls", `{{nodes.${callLlmId}.tool_calls}}`, "string", true);
}

/**
 * What connecting `sourceId` → `targetId` does to the target step: the
 * prefilled step, or undefined when nothing changes.
 */
export function toolCallsDefaultForEdge(
  sourceId: string,
  targetId: string,
  nodes: readonly Node[],
  edges: readonly Edge[],
): Step | undefined {
  const target = stepOf(nodes.find((node) => node.id === targetId));
  if (target?.type !== "execute_tools") return undefined;
  const callLlm = upstreamCallLlm(sourceId, nodes, edges);
  return callLlm ? withDefaultToolCalls(target, callLlm) : undefined;
}
