/**
 * A Get It Right run, seen through the workflow viewer.
 *
 * The run is a loop ("attempt") whose body runs lint/test/build in parallel
 * and then a reviewer. Two things the viewer got wrong (gtm/reviews/
 * product-ux-findings.md A-2, A-3):
 *
 *  - while iteration 2's checks ran, the body's nodes stayed neutral: child
 *    status came only from step rows, which are written when a step FINISHES;
 *  - clicking the reviewer inside the loop said "Not yet executed", because
 *    the loop-scoped node id ("attempt:review") matched no root step.
 *
 * The real viewer renders here against the real chat store; only the RPC
 * clients and the router are stubbed.
 */

import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";

import { renderWithQuery } from "@/test/renderWithQuery";
import { useChatStore } from "@/store/chatStore";
import type { NodeExecutionUpdate } from "@/types/streaming";
import {
  NodeExecutionEventType,
  NodeExecutionStatus as ProtoNodeExecutionStatus,
} from "@/gen/reliant/v1/streaming_pb";
import { ContentBlockType, MessageRole } from "@/gen/reliant/v1/chat_pb";
import type { Workflow } from "@/types/workflow";
import type { StepExecution, WorkflowExecution } from "../../Chat/ExecutionSidebar/types";

const getMessage = vi.fn();

vi.mock("@/api/grpc-client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/grpc-client")>()),
  getMessageClient: () => ({ getMessage }),
}));

vi.mock("@tanstack/react-router", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-router")>()),
  useNavigate: () => vi.fn(),
}));

import { WorkflowViewer } from "../WorkflowViewer";

const CHAT_ID = "chat-get-it-right";
const WF_ID = "wf-get-it-right";

beforeAll(() => {
  // React Flow measures its pane and nodes; jsdom has no layout engine.
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
  // React Flow reads the viewport transform back through DOMMatrixReadOnly.
  // Only the scale is consulted, and the viewer's zoom is not under test.
  globalThis.DOMMatrixReadOnly ??= class {
    m22 = 1;
    constructor(_transform?: string) {}
  } as unknown as typeof DOMMatrixReadOnly;
});

const getItRight: Workflow = {
  name: "builtin://get-it-right",
  nodes: [
    {
      id: "attempt",
      type: "loop",
      args: {
        case: "loop",
        value: {
          inline: {
            name: "attempt-body",
            nodes: [
              { id: "lint", type: "run", command: "npm run lint" },
              { id: "test", type: "run", command: "npm test" },
              { id: "build", type: "run", command: "npm run build" },
              { id: "review", type: "workflow", ref: "builtin://structured-agent" },
            ],
            edges: [
              { from: "lint", to: "review" },
              { from: "test", to: "review" },
              { from: "build", to: "review" },
            ],
            entry: ["lint", "test", "build"],
          },
        },
      },
    },
  ],
  edges: [],
  entry: ["attempt"],
} as unknown as Workflow;

let uid = 0;
function step(stepId: string, iteration: number, extra: Partial<StepExecution> = {}): StepExecution {
  uid += 1;
  return {
    id: `step-${uid}`,
    stepId,
    activityName: "ExecuteRunStep",
    status: "completed",
    createdAt: 1_000 + uid,
    loopNodeId: "attempt",
    loopIteration: iteration,
    ...extra,
  };
}

/** Iteration 1 of 2 finished: checks green, the reviewer asked for a refactor. */
function firstIterationRows(): StepExecution[] {
  return [
    step("lint", 0),
    step("test", 0),
    step("build", 0),
    step("review-save", 0, { activityName: "SaveMessage", savedMessageId: "msg-review-1" }),
  ];
}

function execution(status: WorkflowExecution["status"], steps: StepExecution[]): WorkflowExecution {
  return {
    id: WF_ID,
    workflowName: "builtin://get-it-right",
    thread: WF_ID,
    status,
    createdAt: 0,
    messageCount: 0,
    children: [],
    steps,
  };
}

let seq = 0;
function loopEvent(nodeId: string, eventType: NodeExecutionEventType, iteration: number): NodeExecutionUpdate {
  seq += 1;
  return {
    update_type: "node_execution",
    event_type: eventType,
    node_id: nodeId,
    node_type: "run",
    status: ProtoNodeExecutionStatus.RUNNING,
    workflow_id: WF_ID,
    chat_id: CHAT_ID,
    parent_node_id: "attempt",
    iteration,
    metadata: { node_path: `attempt.${nodeId}` },
    sequence_number: seq,
  };
}

function nodeCard(container: HTMLElement, nodeId: string): HTMLElement {
  const node = container.querySelector<HTMLElement>(`.react-flow__node[data-id="${nodeId}"]`);
  if (!node) throw new Error(`node ${nodeId} not rendered`);
  const card = node.querySelector<HTMLElement>(".workflow-node-card");
  if (!card) throw new Error(`node ${nodeId} has no card`);
  return card;
}

function renderViewer(exec: WorkflowExecution) {
  return renderWithQuery(
    <div style={{ width: 800, height: 600 }}>
      <WorkflowViewer workflow={getItRight} execution={exec} projectId="proj-1" chatId={CHAT_ID} />
    </div>,
  );
}

beforeEach(() => {
  uid = 0;
  seq = 0;
  getMessage.mockReset();
});

afterEach(() => {
  useChatStore.setState({ nodeExecutions: {} });
});

describe("WorkflowViewer — a loop run", () => {
  it("shows the second iteration's checks running while they run", async () => {
    useChatStore.setState({
      nodeExecutions: {
        [CHAT_ID]: [
          loopEvent("lint", NodeExecutionEventType.STARTED, 0),
          loopEvent("test", NodeExecutionEventType.STARTED, 0),
          loopEvent("build", NodeExecutionEventType.STARTED, 0),
          loopEvent("lint", NodeExecutionEventType.COMPLETED, 0),
          loopEvent("test", NodeExecutionEventType.COMPLETED, 0),
          loopEvent("build", NodeExecutionEventType.COMPLETED, 0),
          // Iteration 2 has started its checks; none has finished, so no
          // step row for them exists yet.
          loopEvent("test", NodeExecutionEventType.STARTED, 1),
          loopEvent("build", NodeExecutionEventType.STARTED, 1),
        ],
      },
    });

    const { container } = renderViewer(execution("running", firstIterationRows()));

    await waitFor(() => {
      expect(nodeCard(container, "attempt:test").className).toContain("animate-pulse-border");
    });
    expect(nodeCard(container, "attempt:build").className).toContain("animate-pulse-border");
    // lint has not started in iteration 2, so it is not running.
    expect(nodeCard(container, "attempt:lint").className).not.toContain("animate-pulse-border");
    // The picker followed the run into iteration 2.
    expect(screen.getByTitle("Select iteration")).toHaveValue("1");
  });

  it("shows the reviewer's verdict when the reviewer inside the loop is clicked", async () => {
    getMessage.mockResolvedValue({
      message: {
        id: "msg-review-1",
        role: MessageRole.ASSISTANT,
        contentBlocks: [
          {
            type: ContentBlockType.TEXT,
            content: "## Review (Attempt 1): refactor\nThe cart total ignores discounts.",
          },
        ],
      },
    });

    const { container } = renderViewer(execution("completed", firstIterationRows()));

    await waitFor(() => nodeCard(container, "attempt:review"));
    fireEvent.click(container.querySelector('.react-flow__node[data-id="attempt:review"]')!);

    expect(await screen.findByText(/The cart total ignores discounts/)).toBeInTheDocument();
    expect(getMessage).toHaveBeenCalledWith({ messageId: "msg-review-1" });
    expect(screen.queryByText("Not yet executed")).not.toBeInTheDocument();
  });
});
