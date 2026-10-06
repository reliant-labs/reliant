import { describe, expect, it } from "vitest";

import { buildLoopChildStatus, type LoopIterationInfo } from "./useExecutionStatus";
import type { LoopScopedNodeExecution } from "./useNodeExecutionStatus";
import type { StepExecution } from "../../Chat/ExecutionSidebar/types";

const WF = "wf-1";
const BODY = ["implement", "lint", "test", "build", "review"];

let uid = 0;
function row(stepId: string, iteration: number, status: StepExecution["status"] = "completed"): StepExecution {
  uid += 1;
  return {
    id: `s${uid}`,
    stepId,
    activityName: "ExecuteRunStep",
    status,
    createdAt: uid,
    loopNodeId: "attempt",
    loopIteration: iteration,
  };
}

function iteration(n: number, steps: StepExecution[]): LoopIterationInfo {
  return { iteration: n, steps, status: "completed", earliestCreatedAt: 0, latestCreatedAt: 0 };
}

function entry(
  nodeId: string,
  loopNodeId: string,
  iter: number,
  nodePath: string,
  status: LoopScopedNodeExecution["status"],
  sequence: number,
): LoopScopedNodeExecution {
  return { workflowId: WF, nodeId, loopNodeId, iteration: iter, nodePath, status, sequence };
}

describe("buildLoopChildStatus", () => {
  it("places the reviewer's own agent turns in the open iteration", () => {
    // Iteration 0's checks are done. The reviewer runs as a sub-workflow
    // with its OWN agent loop, so its events name that inner loop, not ours.
    const result = buildLoopChildStatus({
      workflowId: WF,
      loopNodeId: "attempt",
      childNodeIds: BODY,
      iterations: [iteration(0, [row("implement-save", 0), row("lint", 0), row("test", 0), row("build", 0)])],
      loopScoped: [
        entry("call_llm", "agent_loop", 3, "attempt.review.agent_loop.call_llm", "running", 40),
      ],
      loopIsRunning: true,
      latestSequence: 40,
    });

    expect(result.byIteration.get(0)?.review).toBe("running");
    expect(result.iterationStatuses).toEqual(["running"]);
  });

  it("keeps a body node running between two of its activities", () => {
    // The reviewer's call_llm just completed and nothing newer has happened:
    // the reviewer is still the node the run is in.
    const result = buildLoopChildStatus({
      workflowId: WF,
      loopNodeId: "attempt",
      childNodeIds: BODY,
      iterations: [iteration(0, [row("lint", 0)])],
      loopScoped: [entry("call_llm", "agent_loop", 3, "attempt.review.agent_loop.call_llm", "completed", 41)],
      loopIsRunning: true,
      latestSequence: 41,
    });
    expect(result.byIteration.get(0)?.review).toBe("running");
  });

  it("starts the next iteration when the implementer works after the reviewer finished", () => {
    const result = buildLoopChildStatus({
      workflowId: WF,
      loopNodeId: "attempt",
      childNodeIds: BODY,
      iterations: [
        iteration(0, [row("implement-save", 0), row("lint", 0), row("review-save", 0)]),
      ],
      loopScoped: [
        entry("call_llm", "agent_loop", 0, "attempt.implement.agent_loop.call_llm", "running", 90),
      ],
      loopIsRunning: true,
      latestSequence: 90,
    });

    expect(result.latestIteration).toBe(1);
    expect(result.byIteration.get(1)?.implement).toBe("running");
    expect(result.iterationStatuses).toEqual(["completed", "running"]);
  });

  it("does not show a check running once the loop has stopped", () => {
    const result = buildLoopChildStatus({
      workflowId: WF,
      loopNodeId: "attempt",
      childNodeIds: BODY,
      iterations: [iteration(0, [row("lint", 0)])],
      loopScoped: [entry("test", "attempt", 0, "attempt.test", "running", 5)],
      loopIsRunning: false,
      latestSequence: 5,
    });
    expect(result.byIteration.get(0)?.test).toBeUndefined();
  });

  it("keeps a check's failure from its row over the stream's lifecycle 'completed'", () => {
    // A run step whose command exits non-zero completes its ACTIVITY; only
    // the row carries the exit code.
    const result = buildLoopChildStatus({
      workflowId: WF,
      loopNodeId: "attempt",
      childNodeIds: BODY,
      iterations: [iteration(0, [row("lint", 0, "failed")])],
      loopScoped: [entry("lint", "attempt", 0, "attempt.lint", "completed", 7)],
      loopIsRunning: true,
      latestSequence: 9,
    });
    expect(result.byIteration.get(0)?.lint).toBe("failed");
  });

  it("does not guess an iteration for a parallel loop's deep activity", () => {
    const result = buildLoopChildStatus({
      workflowId: WF,
      loopNodeId: "implementations",
      childNodeIds: ["create_wt", "impl"],
      iterations: [],
      loopScoped: [
        entry("call_llm", "agent_loop", 0, "implementations.impl.agent_loop.call_llm", "running", 3),
      ],
      loopIsRunning: true,
      latestSequence: 3,
      parallel: true,
    });
    expect(result.byIteration.size).toBe(0);
  });
});
