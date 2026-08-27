import { describe, it, expect } from "vitest";
import { getActivitySteps } from "../activityIndicators";
import type { StepExecution, WorkflowExecution } from "../../ExecutionSidebar/types";

function step(overrides: Partial<StepExecution> & { activityName: string }): StepExecution {
  return {
    id: overrides.stepId ?? overrides.activityName,
    stepId: overrides.activityName.toLowerCase(),
    status: "completed",
    createdAt: 1_000,
    ...overrides,
  } as StepExecution;
}

function workflow(steps: StepExecution[]): WorkflowExecution {
  return {
    workflowId: "wf-1",
    workflowName: "builtin://agent",
    thread: "thread-1",
    status: "running",
    steps,
    children: [],
  } as unknown as WorkflowExecution;
}

describe("getActivitySteps", () => {
  // The regression. activity_name is the Temporal registration name —
  // "CallLLM" — but the filter list carried a "V2_" prefix no activity has
  // ever been recorded under, so the entry matched nothing and a "Call Llm"
  // block rendered in the timeline beside the message that step had saved.
  it("never surfaces CallLLM, even before its -save step exists", () => {
    // Mid-stream: the call_llm step is written, its -save counterpart is not.
    // This is the exact window the stray block appeared in, which is why a
    // refresh made it disappear.
    const activities = getActivitySteps(
      workflow([step({ activityName: "CallLLM", stepId: "call_llm", status: "running" })])
    );

    expect(activities).toEqual([]);
  });

  it("never surfaces CallLLM once its -save step has landed", () => {
    const activities = getActivitySteps(
      workflow([
        step({ activityName: "CallLLM", stepId: "call_llm" }),
        step({
          activityName: "SaveMessage",
          stepId: "call_llm-save",
          outputJson: { message_id: "msg-1" },
        }),
      ])
    );

    expect(activities).toEqual([]);
  });

  // Tool calls render from message content blocks, so an indicator would be a
  // second copy of the same event.
  it("does not surface ExecuteTools, which ToolExecution already renders", () => {
    const activities = getActivitySteps(
      workflow([step({ activityName: "ExecuteTools", stepId: "execute_tools", status: "running" })])
    );

    expect(activities).toEqual([]);
  });

  it("still surfaces activities that produce no message", () => {
    const activities = getActivitySteps(
      workflow([step({ activityName: "ExecuteRunStep", stepId: "lint" })])
    );

    expect(activities).toHaveLength(1);
    expect(activities[0].step.activityName).toBe("ExecuteRunStep");
  });

  it("still surfaces user-defined activities it has never heard of", () => {
    const activities = getActivitySteps(
      workflow([step({ activityName: "DeployToStaging", stepId: "deploy" })])
    );

    expect(activities).toHaveLength(1);
  });

  it("hides workflow plumbing", () => {
    const activities = getActivitySteps(
      workflow([
        step({ activityName: "WorkflowStatus", stepId: "status" }),
        step({ activityName: "Cleanup", stepId: "cleanup" }),
        step({ activityName: "FailStep", stepId: "fail" }),
      ])
    );

    expect(activities).toEqual([]);
  });
});
