/**
 * Renaming a step rewrites what names it: `nodes.<old>` in templates, raw CEL
 * conditions and bracket form, and the workflow fields that hold step ids —
 * without touching a longer id that merely starts the same, or an inline
 * sub-workflow, whose `nodes.x` names its own steps.
 */

import { describe, expect, it } from "vitest";

import { rewriteNodeReferences, rewriteNodeReferencesInText, rewriteWorkflowNodeReferences } from "../nodeReferences";

describe("rewriteNodeReferencesInText", () => {
  it.each([
    ["{{nodes.think.content}}", "{{nodes.plan.content}}"],
    ["nodes.think.tool_calls.size() > 0", "nodes.plan.tool_calls.size() > 0"],
    ["{{ nodes . think . content }}", "{{ nodes . plan . content }}"],
    ['nodes["think"].content', 'nodes["plan"].content'],
    ["nodes['think']", "nodes['plan']"],
    ["has(nodes.think) && nodes.think.ok", "has(nodes.plan) && nodes.plan.ok"],
  ])("%s", (before, after) => {
    expect(rewriteNodeReferencesInText(before, "think", "plan")).toBe(after);
  });

  it("leaves ids that only share a prefix, and other namespaces, alone", () => {
    for (const text of ["{{nodes.thinker.content}}", "{{inputs.think}}", "{{trigger.payload.nodes.think}}", "think about it"]) {
      expect(rewriteNodeReferencesInText(text, "think", "plan")).toBe(text);
    }
  });
});

describe("rewriteNodeReferences", () => {
  it("rewrites every string in a step, keeping everything else", () => {
    const step = {
      id: "act",
      type: "execute_tools",
      args: { case: "executeTools", value: { toolCalls: { value: { case: "expr", value: "{{nodes.think.tool_calls}}" } }, limit: 3n } },
    };
    const rewritten = rewriteNodeReferences(step, "think", "plan");
    expect(rewritten.args.value.toolCalls.value.value).toBe("{{nodes.plan.tool_calls}}");
    expect(rewritten.args.value.limit).toBe(3n);
    expect(rewritten.id).toBe("act");
  });

  it("returns the same object when nothing named the step", () => {
    const step = { id: "act", args: { value: { prompt: "{{nodes.other.content}}" } } };
    expect(rewriteNodeReferences(step, "think", "plan")).toBe(step);
  });

  it("does not reach into an inline sub-workflow, which has its own step ids", () => {
    const loop = {
      id: "repeat",
      args: {
        case: "loop",
        value: {
          while: { expr: "nodes.think.again" },
          inline: { name: "body", nodes: [{ id: "think", args: { value: { p: "{{nodes.think.x}}" } } }], edges: [] },
        },
      },
    };
    const rewritten = rewriteNodeReferences(loop, "think", "plan");
    expect(rewritten.args.value.while.expr).toBe("nodes.plan.again");
    expect(rewritten.args.value.inline).toBe(loop.args.value.inline);
  });

  it("rewrites Switch case conditions held in node data", () => {
    const data = { label: "Switch", cases: [{ id: "c1", condition: "nodes.think.tool_calls.size() > 0" }, { id: "c2", condition: "" }] };
    expect(rewriteNodeReferences(data, "think", "plan").cases[0]!.condition).toBe("nodes.plan.tool_calls.size() > 0");
  });
});

describe("rewriteWorkflowNodeReferences", () => {
  it("rewrites outputs and the fields that hold step ids", () => {
    const workflow = {
      name: "w",
      outputs: { answer: "{{nodes.think.content}}", other: "{{nodes.act.result}}" },
      entry: ["think", "act"],
      resumeNode: "think",
      transitionTo: "act",
    };
    expect(rewriteWorkflowNodeReferences(workflow, "think", "plan")).toEqual({
      name: "w",
      outputs: { answer: "{{nodes.plan.content}}", other: "{{nodes.act.result}}" },
      entry: ["plan", "act"],
      resumeNode: "plan",
      transitionTo: "act",
    });
  });

  it("returns the same workflow when nothing named the step", () => {
    const workflow = { name: "w", outputs: { a: "{{nodes.act.x}}" } };
    expect(rewriteWorkflowNodeReferences(workflow, "think", "plan")).toBe(workflow);
  });
});
