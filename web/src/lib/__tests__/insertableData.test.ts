import { describe, expect, it } from "vitest";

import { insertableData, matchesSearch, upstreamStepIds, type InsertableDataInput } from "../insertableData";

const outputs: Record<string, Array<{ name: string; type: string; description: string }>> = {
  call_llm: [
    { name: "response_text", type: "string", description: "The reply text" },
    { name: "tool_calls", type: "message", description: "" },
  ],
  run: [{ name: "stdout", type: "string", description: "" }],
};

// entry → plan (call_llm) → switch → [tools (execute_tools), done (run)]; tools → plan.
const edges = [
  { source: "__start__", target: "plan" },
  { source: "plan", target: "switch_plan" },
  { source: "switch_plan", target: "tools" },
  { source: "switch_plan", target: "done" },
  { source: "tools", target: "plan" },
  { source: "lint", target: "done" },
];

function input(overrides: Partial<InsertableDataInput> = {}): InsertableDataInput {
  return {
    context: {
      nodeIds: ["plan", "tools", "done", "lint", "unrelated"],
      nodeTypeMap: { plan: "call_llm", tools: "execute_tools", done: "run", lint: "run", unrelated: "run" },
      inputParams: { topic: { type: "string", description: "What to research" } },
      edges,
      nodeOutputSchemas: { lint: { type: "object", properties: { issues: { type: "array", items: { type: "string" }, description: "Lint findings" } } } },
    },
    currentNodeId: "done",
    nodeOutputFields: (type) => outputs[type] ?? [],
    nodeTypeLabel: (type) => ({ call_llm: "Call LLM", run: "Run Command" })[type] ?? type,
    ...overrides,
  };
}

describe("upstreamStepIds", () => {
  it("walks back through switch and entry nodes, keeping only steps, in workflow order", () => {
    expect(upstreamStepIds("done", edges, ["plan", "tools", "done", "lint", "unrelated"])).toEqual(["plan", "tools", "lint"]);
  });

  it("terminates on a cycle and never lists the step itself", () => {
    expect(upstreamStepIds("plan", edges, ["plan", "tools", "done"])).toEqual(["tools"]);
  });

  it("is empty for a step nothing connects to", () => {
    expect(upstreamStepIds("unrelated", edges, ["plan", "unrelated"])).toEqual([]);
  });
});

describe("insertableData", () => {
  it("offers inputs, the trigger, and each upstream step's outputs as paths", () => {
    const groups = insertableData(input());
    expect(groups.map((g) => g.id)).toEqual(["inputs", "trigger", "node:plan", "node:tools", "node:lint"]);

    expect(groups[0].fields).toEqual([{ path: "inputs.topic", type: "string", description: "What to research" }]);
    expect(groups[1].fields.map((f) => f.path)).toContain("trigger.kind");

    const plan = groups.find((g) => g.id === "node:plan")!;
    expect(plan.detail).toBe("Call LLM");
    expect(plan.fields).toEqual([
      { path: "nodes.plan.response_text", type: "string", description: "The reply text" },
      { path: "nodes.plan.tool_calls", type: "object", description: "" },
    ]);

    // An integration action's typed data, one level deep.
    const lint = groups.find((g) => g.id === "node:lint")!;
    expect(lint.fields.map((f) => f.path)).toEqual(["nodes.lint.stdout", "nodes.lint.data", "nodes.lint.data.issues"]);
    expect(lint.fields[2]).toMatchObject({ type: "list of string", description: "Lint findings" });

    // `unrelated` is not upstream at all.
    expect(groups.some((g) => g.id === "node:unrelated")).toBe(false);
  });

  // The same fields the step's Outputs tab lists (catalogToOutputFields of
  // ListNodes): sub-fields one level in, a list's through its first item, and
  // the debug plumbing marked advanced left out.
  it("offers an output's sub-fields and leaves advanced fields out", () => {
    const plan = insertableData(
      input({
        nodeOutputFields: (type) =>
          type === "call_llm"
            ? [
                { name: "tool_calls", type: "array", description: "", children: [{ name: "name", type: "string", description: "Tool name" }] },
                { name: "message", type: "object", description: "", children: [{ name: "text", type: "string", description: "" }, { name: "seq", type: "integer", description: "", advanced: true }] },
                { name: "upstream_proxyman_id", type: "string", description: "", advanced: true },
              ]
            : [],
      }),
    ).find((g) => g.id === "node:plan")!;
    expect(plan.fields.map((f) => f.path)).toEqual(["nodes.plan.tool_calls", "nodes.plan.tool_calls[0].name", "nodes.plan.message", "nodes.plan.message.text"]);
    expect(plan.fields[1]).toMatchObject({ type: "string", description: "Tool name" });
  });

  // When the catalog's output schemas have not loaded (or a type declares
  // none), dropping the step made the picker say nothing runs before this one.
  it("still offers an upstream step whose outputs are unknown, as its whole output", () => {
    const tools = insertableData(input()).find((g) => g.id === "node:tools")!;
    expect(tools.fields).toEqual([{ path: "nodes.tools", type: "object", description: "Everything this step produced" }]);

    const unloaded = insertableData(input({ nodeOutputFields: () => [] }));
    expect(unloaded.find((g) => g.id === "node:plan")!.fields.map((f) => f.path)).toEqual(["nodes.plan"]);
  });

  it("offers no step outputs outside a step, or where the context has no nodes namespace", () => {
    expect(insertableData(input({ currentNodeId: null })).map((g) => g.id)).toEqual(["inputs", "trigger"]);
    expect(insertableData(input({ celContext: "loop_while" })).some((g) => g.id.startsWith("node:"))).toBe(false);
  });

  it("lists the declared trigger's payload fields, two levels deep", () => {
    const groups = insertableData(
      input({
        context: {
          ...input().context,
          triggerPayloadSchema: {
            type: "object",
            properties: { data: { type: "object", properties: { issue: { type: "object", description: "The issue" } } } },
          },
        },
      }),
    );
    const trigger = groups.find((g) => g.id === "trigger")!;
    expect(trigger.fields.map((f) => f.path)).toEqual(expect.arrayContaining(["trigger.payload.data", "trigger.payload.data.issue"]));
  });

  it("searches paths, types, descriptions and the step's name", () => {
    const groups = insertableData(input());
    const plan = groups.find((g) => g.id === "node:plan")!;
    expect(matchesSearch(plan.fields[0], plan, "reply")).toBe(true);
    expect(matchesSearch(plan.fields[0], plan, "call llm")).toBe(true);
    expect(matchesSearch(plan.fields[0], plan, "stdout")).toBe(false);
  });
});
