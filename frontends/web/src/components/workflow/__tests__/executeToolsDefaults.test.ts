/**
 * Wiring Run LLM Tool Calls (`execute_tools`) after a Call LLM fills in
 * `tool_calls` with that step's tool calls — through a Switch, which is how
 * an agent loop usually branches between the two — and never overwrites what
 * the author already wrote.
 */

import { describe, expect, it } from "vitest";
import type { Edge, Node } from "@xyflow/react";

import { getActionArgValue } from "@/lib/actionStepArgs";
import { celString } from "@/lib/celAdapter";
import { initStepArgs, type Step } from "@/types/workflow";
import { toolCallsDefaultForEdge, upstreamCallLlm, withDefaultToolCalls } from "../executeToolsDefaults";

function stepNode(id: string, type: string, step?: Partial<Step>): Node {
  return { id, type: "actionNode", position: { x: 0, y: 0 }, data: { step: { id, type, args: initStepArgs(type), ...step } } };
}
const switchNode = (id: string): Node => ({ id, type: "switchNode", position: { x: 0, y: 0 }, data: { cases: [] } });
const edge = (source: string, target: string): Edge => ({ id: `${source}->${target}`, source, target });

const toolCallsOf = (step: Step | undefined) => getActionArgValue(step!, "tool_calls");

describe("Run LLM Tool Calls defaults", () => {
  it("points tool_calls at the Call LLM it is wired after", () => {
    const nodes = [stepNode("think", "call_llm"), stepNode("act", "execute_tools")];
    const step = toolCallsDefaultForEdge("think", "act", nodes, []);
    expect(toolCallsOf(step)).toEqual(celString("{{nodes.think.tool_calls}}"));
  });

  it("sees through a Switch between the two", () => {
    const nodes = [stepNode("think", "call_llm"), switchNode("has_tools"), switchNode("again"), stepNode("act", "execute_tools")];
    const edges = [edge("think", "has_tools"), edge("has_tools", "again")];
    expect(upstreamCallLlm("again", nodes, edges)).toBe("think");
    expect(toolCallsOf(toolCallsDefaultForEdge("again", "act", nodes, edges))).toEqual(celString("{{nodes.think.tool_calls}}"));
  });

  it("leaves it alone when the source is not a Call LLM", () => {
    const nodes = [stepNode("cmd", "run"), stepNode("act", "execute_tools")];
    expect(toolCallsDefaultForEdge("cmd", "act", nodes, [])).toBeUndefined();
  });

  it("does not guess between two equally near Call LLMs", () => {
    const nodes = [stepNode("a", "call_llm"), stepNode("b", "call_llm"), switchNode("pick"), stepNode("act", "execute_tools")];
    const edges = [edge("a", "pick"), edge("b", "pick")];
    expect(upstreamCallLlm("pick", nodes, edges)).toBeUndefined();
  });

  it("never overwrites tool_calls the author set", () => {
    const authored = withDefaultToolCalls(
      { id: "act", type: "execute_tools", args: { case: "executeTools", value: { toolCalls: celString("{{nodes.other.tool_calls}}") } } } as Step,
      "think",
    );
    expect(authored).toBeUndefined();
  });

  it("only touches Run LLM Tool Calls", () => {
    const nodes = [stepNode("think", "call_llm"), stepNode("save", "save_message")];
    expect(toolCallsDefaultForEdge("think", "save", nodes, [])).toBeUndefined();
  });

  it("survives a cycle of Switches", () => {
    const nodes = [switchNode("s1"), switchNode("s2"), stepNode("act", "execute_tools")];
    const edges = [edge("s1", "s2"), edge("s2", "s1")];
    expect(upstreamCallLlm("s1", nodes, edges)).toBeUndefined();
  });
});
