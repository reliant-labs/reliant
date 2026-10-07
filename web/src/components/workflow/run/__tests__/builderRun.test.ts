// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from "vitest";
import type { Edge, Node } from "@xyflow/react";

import { NodeExecutionEventType, NodeExecutionStatus } from "../../../../gen/reliant/v1/streaming_pb";
import type { NodeExecutionUpdate } from "../../../../types/streaming";
import { deriveBuilderRun, runErrorFieldKey, type BuilderRunInput } from "../builderRun";
import { withBuilderRun, withBuilderRunEdges } from "../../nodes/runMarkers";

function decided(nodeId: string, overrides: Partial<NodeExecutionUpdate> = {}): NodeExecutionUpdate {
  return {
    update_type: "node_execution",
    event_type: NodeExecutionEventType.COMPLETED,
    node_id: nodeId,
    node_type: "action",
    status: NodeExecutionStatus.COMPLETED,
    workflow_id: "chat-1",
    chat_id: "chat-1",
    sequence_number: 1,
    ...overrides,
  } as NodeExecutionUpdate;
}

function input(overrides: Partial<BuilderRunInput>): BuilderRunInput {
  return {
    nodes: [],
    edges: [],
    statusMap: {},
    loopInfo: {},
    decidingEvents: {},
    latestSequence: undefined,
    live: true,
    awaitingInput: false,
    isEntryType: (type) => type === "eventNode",
    ...overrides,
  };
}

// start → check → (switch) → yes | no ; yes → post
const graph = {
  nodes: [
    { id: "workflow", type: "eventNode" },
    { id: "check" },
    { id: "route", type: "switchNode" },
    { id: "yes" },
    { id: "no" },
    { id: "post" },
  ],
  edges: [
    { id: "e1", source: "workflow", target: "check" },
    { id: "e2", source: "check", target: "route" },
    { id: "e3", source: "route", target: "yes" },
    { id: "e4", source: "route", target: "no" },
    { id: "e5", source: "yes", target: "post" },
  ],
};

describe("deriveBuilderRun", () => {
  it("highlights the branch the run took, through a switch, and not the one it did not", () => {
    const view = deriveBuilderRun(
      input({ ...graph, statusMap: { workflow: "completed", check: "completed", yes: "completed", post: "running" } }),
    );
    expect([...view.takenEdges].sort()).toEqual(["e1", "e2", "e3", "e5"]);
    expect(view.nodes.no).toBeUndefined();
    expect(view.ended).toBe(false);
  });

  it("marks a step whose condition was false as skipped, not done", () => {
    const view = deriveBuilderRun(
      input({
        ...graph,
        statusMap: { check: "completed" },
        decidingEvents: { check: decided("check", { status: NodeExecutionStatus.SKIPPED }) },
      }),
    );
    expect(view.nodes.check?.status).toBe("skipped");
  });

  it("reads the persisted (string) shape of a skip too", () => {
    const view = deriveBuilderRun(
      input({
        ...graph,
        statusMap: { check: "completed" },
        decidingEvents: { check: decided("check", { status: "NODE_EXECUTION_STATUS_SKIPPED" as unknown as NodeExecutionStatus }) },
      }),
    );
    expect(view.nodes.check?.status).toBe("skipped");
  });

  it("shows an approval waiting only while the run is live and nothing has happened since", () => {
    const approval = decided("check", { node_type: "ApprovalCreate", sequence_number: 9 });
    const waiting = input({ ...graph, statusMap: { check: "completed" }, decidingEvents: { check: approval }, latestSequence: 9 });
    expect(deriveBuilderRun(waiting).nodes.check?.status).toBe("waiting");
    // Approved: the run moved on.
    expect(deriveBuilderRun({ ...waiting, latestSequence: 12 }).nodes.check?.status).toBe("completed");
    // ...unless the run says it is waiting on a person (a parallel branch moved on).
    expect(deriveBuilderRun({ ...waiting, latestSequence: 12, awaitingInput: true }).nodes.check?.status).toBe("waiting");
    // Over: never waiting.
    expect(deriveBuilderRun({ ...waiting, live: false }).nodes.check?.status).toBe("completed");
  });

  it("carries a failure's error and lists failed steps in canvas order", () => {
    const view = deriveBuilderRun(
      input({
        ...graph,
        live: false,
        statusMap: { post: "failed", check: "failed" },
        decidingEvents: { post: decided("post", { error_message: "channel_not_found" }) },
      }),
    );
    expect(view.nodes.post).toEqual({ status: "failed", error: "channel_not_found" });
    expect(view.failed).toEqual(["check", "post"]);
    expect(view.ended).toBe(true);
  });

  it("gives a loop its iterations", () => {
    const loop = { nodeId: "check", currentIteration: 2, completedIterations: 2, iterationStatuses: ["completed", "completed", "running"] as const };
    const view = deriveBuilderRun(
      input({ ...graph, statusMap: { check: "running" }, loopInfo: { check: { ...loop, iterationStatuses: [...loop.iterationStatuses] } } }),
    );
    expect(view.nodes.check?.loop?.currentIteration).toBe(2);
  });
});

describe("runErrorFieldKey", () => {
  it("finds the field a {{ }} expression failed in", () => {
    expect(
      runErrorFieldKey(
        'workflow validation failed: CEL evaluation failed for step summarize: call_llm.system_prompt: evaluating "{{ nodes.x.y }}": no such key: y',
      ),
    ).toBe("system_prompt");
    expect(
      runErrorFieldKey('CEL evaluation failed for step post: action.with[channel]: evaluating "{{ inputs.ch }}": no such key'),
    ).toBe("channel");
  });

  it("names no field when the error is not about one", () => {
    expect(runErrorFieldKey("provider returned 429: rate limited")).toBeUndefined();
    expect(runErrorFieldKey('CEL evaluation failed for step a: call_llm.tools_config.tools: evaluating "x": y')).toBeUndefined();
    expect(runErrorFieldKey(undefined)).toBeUndefined();
  });
});

describe("withBuilderRun / withBuilderRunEdges", () => {
  const nodes = [
    { id: "a", position: { x: 0, y: 0 }, ariaLabel: "Call LLM, a", data: { step: { id: "a" } } },
    { id: "b", position: { x: 0, y: 0 }, ariaLabel: "Run, b", data: { step: { id: "b" } } },
    { id: "c", position: { x: 0, y: 0 }, ariaLabel: "Run, c", data: { step: { id: "c" } } },
  ] as Node[];
  const edges = [
    { id: "a->b", source: "a", target: "b" },
    { id: "a->c", source: "a", target: "c" },
  ] as Edge[];

  it("leaves the canvas untouched without a run", () => {
    expect(withBuilderRun(nodes, null)).toBe(nodes);
    expect(withBuilderRunEdges(edges, null)).toBe(edges);
  });

  it("paints states, names them, and shows what the run never reached once it is over", () => {
    const view = deriveBuilderRun(
      input({
        nodes,
        edges,
        live: false,
        statusMap: { a: "completed", b: "failed" },
        decidingEvents: { b: decided("b", { error_message: "exit 1" }) },
      }),
    );
    const [a, b, c] = withBuilderRun(nodes, view);
    expect(a!.data).toMatchObject({ executionStatus: "completed" });
    expect(a!.ariaLabel).toBe("Call LLM, a, done");
    expect(b!.data).toMatchObject({ executionStatus: "failed", runBadge: "failed" });
    expect(c!.className).toContain("wf-node--run-unreached");
    expect(c!.ariaLabel).toBe("Run, c, not run");

    const [ab, ac] = withBuilderRunEdges(edges, view);
    expect(ab!.data).toMatchObject({ executionStatus: "failed" });
    expect(ac).toBe(edges[1]);
  });

  it("styles skipped and waiting steps with classes, since nodes have no style of their own for them", () => {
    const view = deriveBuilderRun(
      input({
        nodes,
        edges,
        statusMap: { a: "completed", b: "completed" },
        latestSequence: 3,
        decidingEvents: {
          a: decided("a", { status: NodeExecutionStatus.SKIPPED }),
          b: decided("b", { node_type: "QuestionCreate", sequence_number: 3 }),
        },
      }),
    );
    const [a, b] = withBuilderRun(nodes, view);
    expect(a!.className).toContain("wf-node--run-skipped");
    expect(a!.data).toMatchObject({ executionStatus: undefined, runBadge: "skipped" });
    expect(b!.className).toContain("wf-node--run-waiting");
    expect(b!.ariaLabel).toBe("Run, b, waiting for you");
  });
});
