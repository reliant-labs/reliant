// Copyright (c) 2025 Reliant Labs

/**
 * What the Enable dialog asks for and says, as pure logic: the questions a
 * workflow's triggers raise, how a refusal reads, and where a workflow stands.
 */

import { describe, expect, it } from "vitest";

import type { EnableWorkflowResult, WorkflowTriggerResult } from "@/hooks/trigger-model";
import type { DeclaredTrigger } from "@/lib/declaredTriggers";
import { planEnable, refusalHeadline, summarizeEnable, triggersNeedingPrompt, workflowEnableState } from "../enableWorkflow";

const trigger = (name: string, source: unknown, extra: Record<string, unknown> = {}) =>
  ({ name, filter: "", inputs: {}, source, ...extra }) as unknown as DeclaredTrigger;

const schedule = trigger("nightly", { case: "schedule", value: { cron: ["0 9 * * *"] } }, { prompt: "Sweep." });
const hook = trigger("hook", { case: "webhook", value: {} });
const issue = trigger("new-issue", { case: "integration", value: { integration: "github", events: ["issues.opened"], match: {} } });
const pr = trigger("new-pr", { case: "integration", value: { integration: "github", events: ["pull_request.opened"], match: {} } });
const slack = trigger("mention", { case: "integration", value: { integration: "slack", events: ["app_mention"], match: {} } });

const result = (trigger: string, outcome: WorkflowTriggerResult["outcome"], message = ""): WorkflowTriggerResult => ({ trigger, outcome, message });

describe("planEnable", () => {
  it("asks for one connection per integration, however many triggers use it", () => {
    const plan = planEnable([schedule, issue, pr, slack, hook]);
    expect(plan.integrations).toEqual(["github", "slack"]);
    expect(plan.triggersByIntegration).toEqual({ github: ["new-issue", "new-pr"], slack: ["mention"] });
    expect(plan.webhookTriggers).toEqual(["hook"]);
  });

  it("asks for no connection when no trigger needs one", () => {
    expect(planEnable([schedule, hook]).integrations).toEqual([]);
  });
});

describe("triggersNeedingPrompt", () => {
  it("lists only the triggers whose author wrote no prompt", () => {
    expect(triggersNeedingPrompt([schedule, hook, issue])).toEqual(["hook", "new-issue"]);
  });
});

describe("summarizeEnable", () => {
  it("is enabled when the server says every trigger is running, and carries the webhook credentials", () => {
    const withToken: WorkflowTriggerResult = { ...result("hook", "enabled"), webhook: { token: "tok", url: "/hooks/1/tok" } };
    const verdict = summarizeEnable({ enabled: true, results: [result("nightly", "enabled"), withToken] });
    expect(verdict).toEqual({ kind: "enabled", webhooks: [withToken] });
  });

  it("is a refusal when one trigger failed: that one is the cause and the rest were held back", () => {
    const verdict = summarizeEnable({
      enabled: false,
      results: [result("nightly", "notEnabled"), result("new-issue", "failed", "connect github first"), result("hook", "notEnabled")],
    });
    expect(verdict.kind).toBe("refused");
    if (verdict.kind !== "refused") throw new Error("unreachable");
    expect(verdict.failed.map((r) => r.trigger)).toEqual(["new-issue"]);
    expect(verdict.held.map((r) => r.trigger)).toEqual(["nightly", "hook"]);
  });

  it("is partial, and says what is still running, when an undo failed", () => {
    const verdict = summarizeEnable({
      enabled: false,
      results: [result("nightly", "leftEnabled", "Disable it from Automations."), result("hook", "failed", "no token")],
    } satisfies EnableWorkflowResult);
    expect(verdict.kind).toBe("partial");
    if (verdict.kind !== "partial") throw new Error("unreachable");
    expect(verdict.running.map((r) => r.trigger)).toEqual(["nightly"]);
    expect(verdict.failed.map((r) => r.trigger)).toEqual(["hook"]);
  });
});

describe("refusalHeadline", () => {
  it("names the one trigger that could not be enabled", () => {
    expect(refusalHeadline([result("new-issue", "failed")])).toBe("Nothing was turned on, because “new-issue” could not be.");
  });
  it("counts several", () => {
    expect(refusalHeadline([result("a", "failed"), result("b", "failed")])).toBe("Nothing was turned on, because 2 triggers could not be.");
  });
});

describe("workflowEnableState", () => {
  const names = ["nightly", "hook"];
  it("is off with no activations", () => expect(workflowEnableState(names, [])).toBe("off"));
  it("ignores ad hoc automations, which declare nothing", () => {
    expect(workflowEnableState(names, [{ enabled: true }])).toBe("off");
  });
  it("is on when every declared trigger runs", () => {
    expect(workflowEnableState(names, [{ workflowTrigger: "nightly", enabled: true }, { workflowTrigger: "hook", enabled: true }])).toBe("on");
  });
  it("is partial when one trigger was added since, or one is paused", () => {
    expect(workflowEnableState(names, [{ workflowTrigger: "nightly", enabled: true }])).toBe("partial");
    expect(workflowEnableState(names, [{ workflowTrigger: "nightly", enabled: true }, { workflowTrigger: "hook", enabled: false }])).toBe("partial");
  });
  it("is paused when activations exist and none run", () => {
    expect(workflowEnableState(names, [{ workflowTrigger: "nightly", enabled: false }])).toBe("paused");
  });
});
