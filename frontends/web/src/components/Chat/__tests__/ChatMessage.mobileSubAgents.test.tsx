/**
 * On a phone, sub-agents one message started together are one SubAgentGroup,
 * as on desktop: one header counting them by state, open while any runs,
 * placed where the first of them ran, with the message's other tool calls
 * kept in their original order around it.
 */
import { cleanup, render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, describe, expect, it, vi } from "vitest";
import { ChatMessage } from "../ChatMessage";
import { ContentBlockType, MessageRole, StreamingState } from "../../../types/chat";
import type { Message } from "../../../types/chat";
import { SurfaceProvider } from "../../../lib/surfaceContext";
import { WorkflowState, WorkflowStopReason } from "../../../gen/reliant/v1/chat_pb";

const DONE = { state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.COMPLETED };
const FAILED = { state: WorkflowState.STOPPED, stopReason: WorkflowStopReason.FAILED };
const RUNNING = { state: WorkflowState.ACTIVE, stopReason: WorkflowStopReason.UNSPECIFIED };
const STATES = [DONE, DONE, FAILED, RUNNING];
const TITLES = ["Audit checkout", "Port the cart store", "Benchmark cart totals", "Draft the release notes"];

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
  useChatIdOrActive: (chatId?: string | null) => chatId || "chat-1",
  // Getters, not captured consts: vi.mock is hoisted above the declarations.
  useToolResultsByCallId: () => results(),
  useToolCallStates: () => new Map(),
  useChat: () => ({ worktreeId: "worktree-1" }),
  useChatMessages: () => [],
  useStreamingMessages: () => [],
}));
vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({ allWorkflows: STATES.map((s, i) => ({ id: `wf-${i}`, ...s, children: [] })) }),
}));
vi.mock("../../../hooks/approval-queries", () => ({
  useApproveToolRequest: () => ({ mutate: vi.fn() }),
  useDenyToolRequest: () => ({ mutate: vi.fn() }),
}));
vi.mock("../../../hooks/task-queries", () => ({ useTasksForChat: () => ({ data: undefined }) }));
vi.mock("../../../store/threadActivityStore", () => ({ useActiveThreads: () => [] }));
vi.mock("../../../store/projectStore", () => ({ useProjectStore: () => ({ id: "project-1" }) }));
vi.mock("../../../store/chatStore", () => ({ useChatStore: { getState: () => ({ branchChat: vi.fn() }) } }));
vi.mock("../../../api/client", () => ({ api: {} }));
vi.mock("../../../lib/logger", () => ({ logger: { debug: vi.fn(), error: vi.fn(), info: vi.fn(), warn: vi.fn() } }));
vi.mock("../../../lib/toast-manager", () => ({ toast: { error: vi.fn() } }));
vi.mock("../../../lib/tabSwitchProfiler", () => ({ tabSwitchProfiler: { isEnabled: () => false } }));
vi.mock("../MarkdownRenderer", () => ({ MarkdownRenderer: ({ content }: { content: string }) => <div>{content}</div> }));
vi.mock("../BranchOptionsMenu", () => ({ BranchOptionsMenu: () => null }));
vi.mock("../BranchToWorktreeModal", () => ({ BranchToWorktreeModal: () => null }));
vi.mock("../BranchToExistingWorktreeModal", () => ({ BranchToExistingWorktreeModal: () => null }));
vi.mock("../CodeContextPill", () => ({ CodeContextPill: () => null }));

function results() {
  const out: Record<string, { content: string; is_error: boolean }> = {
    "call-shell": { content: "ok", is_error: false },
  };
  TITLES.forEach((title, i) => (out[`call-${i}`] = { content: `Started ${title}.`, is_error: false }));
  return out;
}

function message(): Message {
  const spawn = (i: number) => ({
    id: `block-${i}`, index: i + 1, type: ContentBlockType.TOOL_CALL, toolCallId: `call-${i}`, toolName: "spawn",
    input: JSON.stringify({ title: TITLES[i], preset: "general", prompt: TITLES[i] }), childWorkflowId: `wf-${i}`,
  });
  return {
    id: "msg-1", chatId: "chat-1", seq: BigInt(1), thread: "chat-1", role: MessageRole.ASSISTANT,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: [
      { id: "block-shell", index: 0, type: ContentBlockType.TOOL_CALL, toolCallId: "call-shell", toolName: "shell", input: JSON.stringify({ command: "npm test" }) },
      ...TITLES.map((_, i) => spawn(i)),
    ],
    createdAt: "2024-01-01T00:00:00.000Z", updatedAt: "2024-01-01T00:00:00.000Z", sequenceNumber: BigInt(1),
  } as unknown as Message;
}

afterEach(cleanup);

describe("ChatMessage on mobile: sub-agents", () => {
  it("draws sub-agents started together as one group, after the tool call that ran before them", () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const { container } = render(
      <QueryClientProvider client={queryClient}>
        <SurfaceProvider surface="mobile">
          <ChatMessage message={message()} chatId="chat-1" />
        </SurfaceProvider>
      </QueryClientProvider>,
    );

    const groups = screen.getAllByTestId("sub-agent-group");
    expect(groups).toHaveLength(1);
    const group = groups[0]!;
    expect(within(group).getByText("4 agents · 2 done · 1 running · 1 failed")).toBeInTheDocument();
    expect(group).toHaveAttribute("data-expanded", "true");
    expect(within(group).getAllByTestId("sub-agent-row")).toHaveLength(4);

    // The run keeps its order: the shell call, then the group where the first agent ran.
    const run = container.querySelector(".tool-executions-container")!;
    const children = Array.from(run.children);
    expect(children.indexOf(group)).toBe(children.length - 1);
    expect(children.length).toBe(2);
  });
});
