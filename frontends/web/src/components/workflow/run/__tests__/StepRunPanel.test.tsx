// Copyright (c) 2025 Reliant Labs

/**
 * A step's Run tab reads the run's record (ListStepExecutions) and shows what
 * the step did: its resolved inputs, output, error and attempts; an agent's
 * tool calls; and, for an error that names a field, a way to that field. Only
 * the RPC is mocked.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import type { ReactNode } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";

import type { StepRecord } from "../../../../api/step-executions-grpc";
import type { BuilderRun } from "../../hooks/useBuilderTestRun";
import type { RunNodeState } from "../builderRun";

const listStepExecutions = vi.fn();
vi.mock("../../../../api/step-executions-grpc", () => ({
  listStepExecutions: (...args: unknown[]) => listStepExecutions(...args),
}));

import { BuilderRunProvider, RunSample, sampleValue } from "../BuilderRunContext";
import { StepRunPanel } from "../StepRunPanel";

function record(overrides: Partial<StepRecord>): StepRecord {
  return {
    id: crypto.randomUUID(),
    workflowId: "chat-1",
    stepId: "post",
    activityName: "slack/message.post@1",
    nodePath: "post",
    attempt: 1,
    createdAtMs: 1_000,
    durationMs: 250,
    ...overrides,
  };
}

function runWith(nodes: Record<string, RunNodeState>): BuilderRun {
  return {
    chatId: "chat-1",
    view: { nodes, takenEdges: new Set(), ended: true, failed: Object.keys(nodes).filter((id) => nodes[id]!.status === "failed") },
    status: null,
    settledVersion: 1,
  };
}

function renderWithRun(ui: ReactNode, run: BuilderRun, focusField = vi.fn()) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const rootRoute = createRootRoute({ component: () => <Outlet /> });
  const routes = [
    createRoute({
      getParentRoute: () => rootRoute,
      path: "/workflow",
      component: () => (
        <BuilderRunProvider run={run} inputs={{ label: "bug" }} focus={null} focusField={focusField}>
          {ui}
        </BuilderRunProvider>
      ),
    }),
    createRoute({ getParentRoute: () => rootRoute, path: "/workflows/runs/$runId", component: () => null }),
  ];
  const router = createRouter({
    routeTree: rootRoute.addChildren(routes),
    history: createMemoryHistory({ initialEntries: ["/workflow"] }),
  });
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { focusField };
}

describe("StepRunPanel", () => {
  beforeEach(() => listStepExecutions.mockReset());

  it("shows a failed step's resolved inputs, error and every attempt, with long values cut until Show all", async () => {
    const user = userEvent.setup();
    const longText = "Summary: " + "x".repeat(900);
    listStepExecutions.mockResolvedValue({
      truncated: false,
      records: [
        record({ attempt: 2, createdAtMs: 2_000, error: "slack: channel_not_found", success: false, input: { channel: "C123", text: longText } }),
        record({ attempt: 1, createdAtMs: 1_000, error: "slack: rate limited", success: false, input: { channel: "C123", text: longText } }),
      ],
    });
    renderWithRun(<StepRunPanel nodeId="post" />, runWith({ post: { status: "failed", error: "slack: channel_not_found" } }));

    // The record arrives after the stream's state: wait for it.
    expect(await screen.findByText("2 attempts")).toBeInTheDocument();
    expect(screen.getByText("slack: channel_not_found")).toBeInTheDocument();
    const summary = screen.getByTestId("step-run-summary");
    expect(summary).toHaveTextContent("Failed");
    expect(summary).toHaveTextContent("2 attempts");
    expect(screen.getByRole("button", { name: "Attempt 2 · failed", pressed: true })).toBeInTheDocument();

    const inputs = screen.getAllByTestId("run-value")[0]!;
    expect(inputs).toHaveTextContent('"channel": "C123"');
    expect(inputs.textContent!.length).toBeLessThan(longText.length);
    await user.click(screen.getByRole("button", { name: /Show all/ }));
    expect(screen.getAllByTestId("run-value")[0]!.textContent).toContain(longText);
    expect(screen.getAllByRole("button", { name: "Copy inputs" })).toHaveLength(1);

    // The first attempt failed differently.
    await user.click(screen.getByRole("button", { name: "Attempt 1 · failed" }));
    expect(screen.getByText("slack: rate limited")).toBeInTheDocument();
    expect(listStepExecutions).toHaveBeenCalledWith("chat-1");
  });

  it("takes the author to the field a {{ }} expression failed in", async () => {
    const user = userEvent.setup();
    const message = 'workflow validation failed: CEL evaluation failed for step post: action.with[channel]: evaluating "{{ inputs.ch }}": no such key: ch';
    listStepExecutions.mockResolvedValue({
      truncated: false,
      records: [record({ activityName: "FailStep", error: message, success: false })],
    });
    const { focusField } = renderWithRun(<StepRunPanel nodeId="post" />, runWith({ post: { status: "failed", error: message } }));

    await screen.findByText("Inputs (resolved)");
    await user.click(screen.getByRole("button", { name: "Go to Channel" }));
    expect(focusField).toHaveBeenCalledWith("post", "channel");
  });

  it("lists the tool calls an Agent step made and the turns it ran", async () => {
    listStepExecutions.mockResolvedValue({
      truncated: false,
      records: [
        record({
          stepId: "call_llm",
          nodePath: "agent.agent_loop.call_llm",
          activityName: "CallLLM",
          loopNodeId: "agent_loop",
          loopIteration: 0,
          output: { response_text: "Looking", tool_calls: [{ id: "t1", name: "view", input: '{"path":"main.go"}' }] },
        }),
        record({ stepId: "call_llm-save", nodePath: "agent.agent_loop.call_llm", activityName: "SaveMessage" }),
      ],
    });
    renderWithRun(<StepRunPanel nodeId="agent" />, runWith({ agent: { status: "completed" } }));

    expect(await screen.findByText("Tool calls (1)")).toBeInTheDocument();
    expect(screen.getByText("view")).toBeInTheDocument();
    expect(screen.getAllByTestId("run-value")[0]).toHaveTextContent('"path": "main.go"');
    expect(screen.getByText("Steps inside (1)")).toBeInTheDocument();
  });

  it("puts the test run's value beside a field", async () => {
    listStepExecutions.mockResolvedValue({
      truncated: false,
      records: [record({ stepId: "summarize", nodePath: "summarize", activityName: "CallLLM", output: { response_text: "All good" } })],
    });
    renderWithRun(<RunSample path="nodes.summarize.response_text" />, runWith({ summarize: { status: "completed" } }));

    expect(await screen.findByTestId("run-sample")).toHaveTextContent('In the test run: "All good"');
  });
});

describe("sampleValue", () => {
  const records = [
    record({ stepId: "summarize", nodePath: "summarize", activityName: "CallLLM", createdAtMs: 1, output: { tool_calls: [{ name: "view" }] } }),
    record({ stepId: "summarize", nodePath: "summarize", activityName: "CallLLM", createdAtMs: 2, success: false, error: "429" }),
  ];

  it("reads a step's newest successful output, through lists", () => {
    expect(sampleValue("nodes.summarize.tool_calls[0].name", records, {})).toBe("view");
  });

  it("reads the run's inputs", () => {
    expect(sampleValue("inputs.label", records, { label: "bug" })).toBe("bug");
  });

  it("has nothing for what the run never produced", () => {
    expect(sampleValue("nodes.summarize.message.text", records, {})).toBeUndefined();
    expect(sampleValue("nodes.other.response_text", records, {})).toBeUndefined();
    expect(sampleValue("trigger.kind", records, {})).toBeUndefined();
  });
});
