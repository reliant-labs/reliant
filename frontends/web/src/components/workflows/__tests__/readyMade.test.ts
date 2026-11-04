import { describe, expect, it } from "vitest";
import type { WorkflowResponse } from "@/hooks/workflow-queries";
import { integrationsNeeded, isReadyMadeAutomation } from "../list/readyMade";

const literal = (value: string) => ({ value: { case: "literal", value } });

describe("ready-made automations", () => {
  it("reads what a workflow needs from its triggers and its actions, at any depth", () => {
    const workflow = {
      triggers: [{ name: "alert", source: { case: "webhook", value: {} } }],
      nodes: [
        { id: "loop", args: { case: "loop", value: { nodes: [{ id: "file", args: { case: "action", value: { uses: literal("github/issue.create@1") } } }] } } },
        { id: "call", args: { case: "action", value: { uses: literal("http/request@1") } } },
      ],
    } as unknown as Pick<WorkflowResponse, "triggers" | "nodes">;
    expect(integrationsNeeded(workflow)).toEqual(["GitHub"]);
  });

  it("is a visible built-in that declares triggers and is not started from a chat", () => {
    const base = { source: "builtin" as const, isHidden: false, automationOnly: true, triggers: [{}] as WorkflowResponse["triggers"] };
    expect(isReadyMadeAutomation(base)).toBe(true);
    expect(isReadyMadeAutomation({ ...base, isHidden: true })).toBe(false);
    expect(isReadyMadeAutomation({ ...base, automationOnly: false })).toBe(false);
    expect(isReadyMadeAutomation({ ...base, source: "project" })).toBe(false);
    expect(isReadyMadeAutomation({ ...base, triggers: [] })).toBe(false);
  });
});
