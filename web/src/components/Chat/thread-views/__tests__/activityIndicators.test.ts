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

/**
 * The shape of a real long-running agent workflow: every turn records a
 * CallLLM, an ExecuteTools and their "-save" steps, and almost nothing is a
 * user-facing activity. Measured in a dev database, one workflow held 27,765
 * steps of which exactly 2 surfaced as indicators.
 */
function longAgentWorkflow(turns: number): WorkflowExecution {
  const steps: StepExecution[] = [];
  for (let turn = 0; turn < turns; turn++) {
    steps.push(
      step({ activityName: "CallLLM", stepId: `call_llm_${turn}` }),
      step({
        activityName: "SaveMessage",
        stepId: `call_llm_${turn}-save`,
        savedMessageId: `llm-${turn}`,
      }),
      step({ activityName: "ExecuteTools", stepId: `execute_tools_${turn}` }),
      step({
        activityName: "SaveMessage",
        stepId: `execute_tools_${turn}-save`,
        savedMessageId: `tools-${turn}`,
      }),
    );
  }
  steps.push(step({ activityName: "Compact", stepId: "compact" }));
  return workflow(steps);
}

describe("getActivitySteps", () => {
  // The timeline recomputes this on every message and streamed delta. It used
  // to resolve each step's "-save" counterpart with a linear find() over the
  // whole step list — O(steps²) — which for a 28k-step workflow is ~385M
  // comparisons and multi-second main-thread stalls (typing lagged by 2s).
  it("stays linear on a long-running workflow", () => {
    const turns = 5_000; // 20,001 steps
    const wf = longAgentWorkflow(turns);

    const started = performance.now();
    const activities = getActivitySteps(wf);
    const elapsedMs = performance.now() - started;

    expect(activities.map((a) => a.step.activityName)).toEqual(["Compact"]);
    // Linear is ~single-digit ms here; the quadratic version takes seconds.
    expect(elapsedMs).toBeLessThan(250);
  });

  it("matches a -save step only within the same loop iteration", () => {
    const activities = getActivitySteps(
      workflow([
        step({ activityName: "ExecuteRunStep", stepId: "lint", loopNodeId: "loop", loopIteration: 0 }),
        step({ activityName: "ExecuteRunStep", stepId: "lint", loopNodeId: "loop", loopIteration: 1 }),
        step({
          activityName: "SaveMessage",
          stepId: "lint-save",
          loopNodeId: "loop",
          loopIteration: 1,
          savedMessageId: "msg-1",
        }),
      ])
    );

    // Iteration 1 saved a message; iteration 0 did not.
    expect(activities).toHaveLength(1);
    expect(activities[0].step.loopIteration).toBe(0);
  });

  it("does not treat a -save step without a saved message id as a saved message", () => {
    const activities = getActivitySteps(
      workflow([
        step({ activityName: "ExecuteRunStep", stepId: "lint" }),
        step({ activityName: "SaveMessage", stepId: "lint-save" }),
      ])
    );

    expect(activities).toHaveLength(1);
    expect(activities[0].step.stepId).toBe("lint");
  });

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
          savedMessageId: "msg-1",
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
