/**
 * The Chat card writes the definition's `automation_only`. A builder save
 * rebuilds the definition from the canvas plus the fields it holds, so the
 * flag (and each declared trigger's prompt template) must come back out,
 * or switching Chat off would silently switch itself back on at the next
 * save. Switching it back on leaves no field at all: on is the default.
 */

import { describe, expect, it } from "vitest";
import type { Node } from "@xyflow/react";
import type { Workflow } from "../../types/workflow";
import { workflowToFlowElements } from "../workflow-flow";
import { nodesEdgesToWorkflow } from "../nodes-edges-to-workflow";

function save(source: Workflow): Workflow {
  const { nodes, edges } = workflowToFlowElements(source, { entryNode: "triggerRail" });
  return nodesEdgesToWorkflow(nodes as Node[], edges, {
    name: source.name ?? "",
    description: source.description ?? "",
    inputs: {},
    outputs: {},
    entry: source.entry,
    tag: undefined,
    presetDefault: undefined,
    apiVersion: undefined,
    isLocked: false,
    definition: source,
  });
}

const base: Workflow = {
  name: "nightly-digest",
  description: "",
  entry: ["digest"],
  nodes: [{ id: "digest", type: "call_llm" }],
  triggers: [
    { name: "nightly", filter: "", inputs: {}, prompt: "Summarise {{ trigger.scheduled_for }}", source: { case: "schedule", value: { cron: ["0 9 * * *"] } } },
  ] as unknown as Workflow["triggers"],
};

describe("the Chat trigger and prompt templates survive a builder save", () => {
  it("keeps Chat off (automation_only) and the declared prompt", () => {
    const saved = save({ ...base, automationOnly: true } as Workflow);
    expect(saved.automationOnly).toBe(true);
    expect((saved.triggers?.[0] as { prompt?: string }).prompt).toBe("Summarise {{ trigger.scheduled_for }}");
  });

  it("Chat on writes no field", () => {
    expect(save({ ...base, automationOnly: false } as Workflow)).not.toHaveProperty("automationOnly");
  });
});
