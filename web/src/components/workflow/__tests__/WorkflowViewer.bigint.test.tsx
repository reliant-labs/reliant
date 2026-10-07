/**
 * The workflow viewer re-renders a node when its data changes, and used to
 * detect that by comparing JSON.stringify(node.data). Node data carries the
 * step proto, whose int64 fields are bigints — a CelInt literal such as
 * call_llm's max_tokens — so the first status update of a run with such a
 * step threw "Do not know how to serialize a BigInt" from an effect.
 */

import { beforeAll, describe, expect, it, vi } from "vitest";
import { waitFor } from "@testing-library/react";
import { fromJson } from "@bufbuild/protobuf";

import { renderWithQuery } from "@/test/renderWithQuery";
import { WorkflowSchema } from "@/gen/reliant/v1/workflow_v2_pb";
import type { Workflow } from "@/types/workflow";
import type { StepExecution, WorkflowExecution } from "../../Chat/ExecutionSidebar/types";

vi.mock("@/api/grpc-client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/grpc-client")>()),
  getMessageClient: () => ({ getMessage: vi.fn() }),
}));

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

import { WorkflowViewer } from "../WorkflowViewer";

beforeAll(() => {
  // React Flow measures its pane and nodes; jsdom has no layout engine.
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
  globalThis.DOMMatrixReadOnly ??= class {
    m22 = 1;
    constructor(_transform?: string) {}
  } as unknown as typeof DOMMatrixReadOnly;
});

// As GetWorkflow decodes it: max_tokens' int64 literal is a bigint.
const workflow = fromJson(WorkflowSchema, {
  name: "ask-then-check",
  entry: ["ask"],
  nodes: [
    { id: "ask", type: "call_llm", callLlm: { maxTokens: { literal: "4096" } } },
    { id: "check", type: "run", run: { command: { literal: "npm test" } } },
  ],
  edges: [{ from: "ask", default: ["check"] }],
} as never) as Workflow;

function step(stepId: string, status: StepExecution["status"]): StepExecution {
  return { id: `step-${stepId}`, stepId, activityName: "Execute", status, createdAt: 1_000 } as StepExecution;
}

function execution(steps: StepExecution[]): WorkflowExecution {
  return {
    id: "wf-1",
    workflowName: "ask-then-check",
    thread: "wf-1",
    status: "running",
    createdAt: 0,
    messageCount: 0,
    children: [],
    steps,
  };
}

function viewer(exec: WorkflowExecution) {
  return (
    <div style={{ width: 800, height: 600 }}>
      <WorkflowViewer workflow={workflow} execution={exec} projectId="proj-1" />
    </div>
  );
}

function nodeCard(container: HTMLElement, nodeId: string): HTMLElement | null {
  return container.querySelector<HTMLElement>(`.react-flow__node[data-id="${nodeId}"] .workflow-node-card`);
}

describe("WorkflowViewer with int64 fields on a step", () => {
  it("applies a status update to a run whose step holds a bigint", async () => {
    expect(typeof (workflow.nodes[0].args.value as { maxTokens: { value: { value: unknown } } }).maxTokens.value.value).toBe(
      "bigint",
    );
    const errors: unknown[] = [];
    const onError = (event: ErrorEvent) => errors.push(event.error);
    window.addEventListener("error", onError);
    const consoleError = vi.spyOn(console, "error").mockImplementation((...args) => errors.push(args[0]));

    const { container, rerender } = renderWithQuery(viewer(execution([step("ask", "completed")])));
    await waitFor(() => expect(nodeCard(container, "check")).not.toBeNull());

    // Only "check" changes; "ask" (the bigint step) is compared unchanged.
    rerender(viewer(execution([step("ask", "completed"), step("check", "running")])));
    await waitFor(() => expect(nodeCard(container, "check")!.className).toContain("animate-pulse-border"));
    expect(nodeCard(container, "ask")).not.toBeNull();

    window.removeEventListener("error", onError);
    consoleError.mockRestore();
    expect(errors.map(String).filter((e) => /BigInt/.test(e))).toEqual([]);
  });
});
