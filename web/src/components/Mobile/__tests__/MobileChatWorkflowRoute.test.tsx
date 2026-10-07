/**
 * `/m/chats/$chatId/workflow` shows each step's live status from the same
 * sources as the desktop viewer: the chat's node_execution stream and the FULL
 * execution tree (step rows and child workflows).
 *
 * Regression (gtm/media/v2 README, R6): a run in iteration 2 of its loop
 * showed "Loop · attempt · Pending" under a "Running" header. The route
 * stripped the tree's steps and children and never subscribed to the chat's
 * stream, so on a direct load nothing could say the loop was running.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, render, screen, within } from "@testing-library/react";

import type { Workflow, Step } from "../../../types/workflow";
import type { WorkflowExecutionData } from "../../../types/chat";
import type { NodeExecutionUpdate } from "../../../types/streaming";
import { WorkflowExecutionView, WorkflowState, WorkflowStopReason } from "../../../gen/reliant/v1/chat_pb";
import { useChatStore } from "../../../store/chatStore";
import { useGlobalUpdatesStore } from "../../../store/globalUpdatesStore";

const CHAT_ID = "chat-1";
const RUN_ID = "wf-root";

vi.mock("@tanstack/react-router", () => ({
  useParams: () => ({ chatId: CHAT_ID }),
  Link: ({ children, ...props }: { children?: React.ReactNode }) => <a {...props}>{children}</a>,
}));

function step(id: string, type: string): Step {
  return { id, type } as Step;
}

const workflow = {
  name: "builtin://get-it-right",
  entry: ["plan"],
  nodes: [step("plan", "call_llm"), step("attempt", "loop"), step("ship", "workflow")],
  edges: [
    { from: "plan", cases: [{ to: ["attempt"] }] },
    { from: "attempt", cases: [{ to: ["ship"] }] },
  ],
} as Workflow;

vi.mock("../../../store/globalDataStore", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../store/globalDataStore")>()),
  useWorkflows: () => ({ workflows: [workflow], loading: false }),
}));

/** Step rows are written when a step finishes, so they only say completed/failed. */
function stepRow(stepId: string, extra: Record<string, unknown> = {}) {
  return {
    id: `s-${stepId}-${String(extra.loopIteration ?? "")}`,
    stepId,
    activityName: "run",
    success: true,
    createdAt: "2026-10-06T10:00:00Z",
    ...extra,
  };
}

/** The BASIC tree: what the chat timeline reads, with only a few steps. */
const basicRun = {
  id: RUN_ID,
  workflowName: "builtin://get-it-right",
  thread: CHAT_ID,
  state: WorkflowState.ACTIVE,
  stopReason: WorkflowStopReason.UNSPECIFIED,
  createdAt: "2026-10-06T10:00:00Z",
  messageCount: 12,
  children: [],
  steps: [],
} as unknown as WorkflowExecutionData;

let fullRun: WorkflowExecutionData;

const useWorkflowExecutions = vi.fn((chatId: string | null, view?: WorkflowExecutionView) => {
  const latest = chatId ? (view === WorkflowExecutionView.FULL ? fullRun : basicRun) : null;
  return {
    data: latest,
    allWorkflows: latest ? [latest] : [],
    hasRunningWorkflow: !!latest,
    isLoading: false,
    error: null,
    refetch: async () => undefined,
  };
});

vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: (chatId: string | null, view?: WorkflowExecutionView) =>
    useWorkflowExecutions(chatId, view),
}));

const { MobileChatWorkflowRoute } = await import("../MobileWorkflowDetailRoute");

/** A node event of an activity inside the loop, as the server stamps it. */
function loopEvent(nodeId: string, iteration: number, sequence: number): NodeExecutionUpdate {
  return {
    update_type: "node_execution",
    event_type: "started" as unknown as NodeExecutionUpdate["event_type"],
    node_id: nodeId,
    node_type: "run",
    status: 0 as NodeExecutionUpdate["status"],
    workflow_id: RUN_ID,
    chat_id: CHAT_ID,
    parent_node_id: "attempt",
    iteration,
    metadata: { node_path: `attempt.${nodeId}` },
    sequence_number: sequence,
  };
}

function statusOf(nodeId: string): string {
  const row = screen.getByText(nodeId).closest("button");
  if (!row) throw new Error(`no row for ${nodeId}`);
  return within(row).getAllByText(/^(Pending|Running|Done|Failed)$/)[0]!.textContent ?? "";
}

describe("MobileChatWorkflowRoute", () => {
  const reconcileChatSubscription = vi.fn();

  beforeEach(() => {
    useWorkflowExecutions.mockClear();
    reconcileChatSubscription.mockReset();
    useChatStore.setState({ nodeExecutions: {} });
    useGlobalUpdatesStore.setState({ reconcileChatSubscription });
    fullRun = {
      ...basicRun,
      steps: [
        stepRow("plan", { activityName: "V2_CallLLM" }),
        // Iteration 1 finished; iteration 2 has started but nothing in it has
        // finished, so no row of iteration 2 exists yet.
        stepRow("implement", { loopNodeId: "attempt", loopIteration: 0 }),
        stepRow("lint", { loopNodeId: "attempt", loopIteration: 0 }),
      ],
    } as unknown as WorkflowExecutionData;
  });

  it("shows a loop mid-run as Running, from the chat's stream, on a direct load", async () => {
    // Subscribing is what brings the chat's node events in (the snapshot
    // replays them); nothing else on this route would.
    reconcileChatSubscription.mockImplementation((chatId: string | null) => {
      if (chatId !== CHAT_ID) return;
      useChatStore.setState({
        nodeExecutions: { [CHAT_ID]: [loopEvent("implement", 1, 40)] },
      });
    });

    await act(async () => {
      render(<MobileChatWorkflowRoute />);
    });

    expect(statusOf("attempt")).toBe("Running");
    expect(reconcileChatSubscription).toHaveBeenCalledWith(CHAT_ID);
  });

  it("shows a loop mid-run as Running from its history while the stream has not connected", async () => {
    // No stream events: the subscription is asserted, but nothing arrives.
    // The FULL tree's rows for the loop all say completed (rows are written
    // as steps finish), and nothing after the loop has run, so the loop is
    // still what the run is doing.
    await act(async () => {
      render(<MobileChatWorkflowRoute />);
    });

    expect(statusOf("attempt")).toBe("Running");
    expect(statusOf("plan")).toBe("Done");
  });

  it("reads finished steps and child workflows from the FULL tree, as the desktop viewer does", async () => {
    fullRun = {
      ...fullRun,
      children: [
        {
          ...basicRun,
          id: "wf-ship",
          spawnedByNodeId: "ship",
          parentId: RUN_ID,
          children: [],
          steps: [],
        },
      ],
    } as unknown as WorkflowExecutionData;

    await act(async () => {
      render(<MobileChatWorkflowRoute />);
    });

    // A step row says the step finished.
    expect(statusOf("plan")).toBe("Done");
    // A sub-workflow node is running while its child workflow is.
    expect(statusOf("ship")).toBe("Running");
    expect(useWorkflowExecutions).toHaveBeenCalledWith(CHAT_ID, WorkflowExecutionView.FULL);
  });
});
