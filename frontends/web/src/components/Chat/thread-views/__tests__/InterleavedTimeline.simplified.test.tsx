/**
 * The simplified thread through the real InterleavedTimeline, ChatMessage and
 * TurnWorkFold: each agent turn becomes one fold row plus its answer, and the
 * fold's contents come and go with its header.
 *
 * Only the leaves are stubbed (virtualizer, markdown, tool cards, stores);
 * the timeline pass, the slicing and the fold are real.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { create } from "@bufbuild/protobuf";
import { ContentBlockType, DisplayStyle, MessageRole, StreamingState } from "../../../../types/chat";
import type { Message } from "../../../../types/chat";
import { ApprovalSchema, ApprovalStatus } from "../../../../gen/reliant/v1/approval_pb";
import type { Approval } from "../../../../gen/reliant/v1/approval_pb";
import { InterleavedTimeline } from "../InterleavedTimeline";
import { SurfaceProvider } from "../../../../lib/surfaceContext";
import { setChatThreadMode } from "../useChatThreadMode";

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= ResizeObserverStub as unknown as typeof ResizeObserver;

const settings = vi.hoisted(() => ({ store: new Map<string, string>() }));
vi.mock("../../../../services/settingsSync", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../../../services/settingsSync")>();
  return {
    ...actual,
    settingsSync: new Proxy(actual.settingsSync, {
      get(target, prop, receiver) {
        if (prop === "getSetting") return (key: string, fallback = "") => settings.store.get(key) || fallback;
        if (prop === "setSettingIfChanged") {
          return async (key: string, value: string) => {
            settings.store.set(key, value);
            return true;
          };
        }
        const value = Reflect.get(target, prop, receiver);
        return typeof value === "function" ? value.bind(target) : value;
      },
    }),
  };
});

vi.mock("../../../../store/threadActivityStore", () => ({
  useActiveThreads: () => [],
}));

vi.mock("../../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({
    data: null,
    allWorkflows: [],
    isLoading: false,
    error: null,
    refetch: async () => {},
  }),
}));

vi.mock("../../ToolExecution", () => ({
  workflowLifecycleToDisplayStatus: () => undefined,
  ToolExecution: ({
    toolCall,
    approval,
  }: {
    toolCall: { name: string };
    approval?: { status: number };
  }) => (
    <div data-testid="tool-card">
      <span>tool:{toolCall.name}</span>
      {approval?.status === 1 && <button type="button">Approve tool execution</button>}
    </div>
  ),
}));
vi.mock("../../ToolExecutionGroup", () => ({
  ToolExecutionGroup: () => <div data-testid="tool-group">group</div>,
}));
vi.mock("../../ToolExecutionCollapsibleGroup", () => ({
  ToolExecutionCollapsibleGroup: () => <div data-testid="tool-collapsible-group">collapsible</div>,
}));
vi.mock("../../MarkdownRenderer", () => ({
  MarkdownRenderer: ({ content }: { content: string }) => <div data-testid="text-run">{content}</div>,
}));
vi.mock("../../AskUserCard", () => ({
  AskUserCard: () => <div data-testid="ask-user-card">ask</div>,
}));
vi.mock("../../MessageGeneratedImages", () => ({
  MessageGeneratedImages: () => null,
}));
vi.mock("../../MessageGeneratedVideos", () => ({
  MessageGeneratedVideos: () => null,
  isVideoMimeType: (mime: string) => mime.startsWith("video/"),
}));
vi.mock("../../BranchOptionsMenu", () => ({ BranchOptionsMenu: () => null }));
vi.mock("../../BranchToWorktreeModal", () => ({ BranchToWorktreeModal: () => null }));
vi.mock("../../BranchToExistingWorktreeModal", () => ({ BranchToExistingWorktreeModal: () => null }));
vi.mock("../../CodeContextPill", () => ({ CodeContextPill: () => null }));

vi.mock("../../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
  useToolResultsByCallId: () => ({}),
  useToolCallStates: () => new Map(),
  useChat: () => ({ worktreeId: "worktree-1" }),
  useChatMessages: () => [],
  useStreamingMessages: () => [],
}));
vi.mock("../../../../store/projectStore", () => ({ useProjectStore: () => ({ id: "project-1" }) }));
vi.mock("../../../../store/chatStore", () => ({
  useChatStore: { getState: () => ({ branchChat: vi.fn() }) },
}));
vi.mock("../../../../api/client", () => ({ api: {} }));
vi.mock("../../../../lib/logger", () => ({
  logger: { debug: vi.fn(), error: vi.fn(), info: vi.fn(), warn: vi.fn() },
}));
vi.mock("../../../../lib/toast-manager", () => ({ toast: { error: vi.fn() } }));
vi.mock("../../../../lib/tabSwitchProfiler", () => ({ tabSwitchProfiler: { isEnabled: () => false } }));

const { scrollToIndex } = vi.hoisted(() => ({ scrollToIndex: vi.fn() }));

// jsdom has no layout: render every row instead of the visible window.
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: (options: { count: number }) => ({
    getVirtualItems: () =>
      Array.from({ length: options.count }, (_, index) => ({
        index,
        key: index,
        start: 0,
        end: 0,
        size: 0,
        lane: 0,
      })),
    getTotalSize: () => 0,
    measureElement: () => {},
    containerRef: () => {},
    scrollToIndex,
    scrollToEnd: () => {},
  }),
}));

type Block = Record<string, unknown>;
const text = (i: number, content: string): Block => ({
  id: `b${i}`,
  index: i,
  type: ContentBlockType.TEXT,
  content,
});
const tool = (i: number, name: string, callId: string): Block => ({
  id: `b${i}`,
  index: i,
  type: ContentBlockType.TOOL_CALL,
  toolName: name,
  toolCallId: callId,
  input: "{}",
});

function message(
  seq: number,
  role: MessageRole,
  contentBlocks: Block[],
  streamingState = StreamingState.COMPLETE,
): Message {
  const at = new Date(Date.UTC(2026, 0, 1, 0, 0, seq * 10)).toISOString();
  return {
    id: `m${seq}`,
    chatId: "chat-1",
    seq: BigInt(seq),
    thread: "chat-1",
    role,
    streamingState,
    contentBlocks: contentBlocks.map((block, i) => ({ ...block, id: `m${seq}-${block.id as string}`, index: i })),
    createdAt: at,
    updatedAt: at,
    sequenceNumber: BigInt(seq),
  } as unknown as Message;
}

const user = (seq: number, content: string) => message(seq, MessageRole.USER, [text(0, content)]);
const assistant = (seq: number, blocks: Block[], state = StreamingState.COMPLETE) =>
  message(seq, MessageRole.ASSISTANT, blocks, state);

// user, then one agent turn: narration+tool, tool, final answer.
const finishedTurn = (): Message[] => [
  user(1, "Fix the flaky test"),
  assistant(2, [text(0, "Let me look at the test."), tool(1, "read_file", "call-read")]),
  assistant(3, [tool(0, "bash", "call-bash")]),
  assistant(4, [text(0, "Fixed: the race is gone.")]),
];

const pendingApproval = (blockId: string): Approval =>
  create(ApprovalSchema, {
    id: "approval-1",
    chatId: "chat-1",
    entityId: blockId,
    title: "Run it?",
    status: ApprovalStatus.PENDING,
    createdAt: new Date().toISOString(),
  });

function renderTimeline(
  messages: Message[],
  props: { isStreaming?: boolean; approvals?: Approval[] } = {},
) {
  const element = (next: Message[], p: typeof props) => (
    <SurfaceProvider surface="mobile">
      <InterleavedTimeline messages={next} chatId="chat-1" isStreaming={p.isStreaming ?? false} approvals={p.approvals} />
    </SurfaceProvider>
  );
  const result = render(element(messages, props));
  return { ...result, rerenderWith: (next: Message[], p: typeof props = props) => result.rerender(element(next, p)) };
}

const fold = () => screen.getByTestId("turn-work-fold");
const foldButton = () => within(fold()).getAllByRole("button")[0];
const visible = (content: string) => screen.queryByText(content) !== null;

beforeEach(() => {
  settings.store.clear();
  scrollToIndex.mockClear();
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: false,
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  })) as unknown as typeof window.matchMedia;
});

afterEach(() => cleanup());

describe("InterleavedTimeline simplified thread", () => {
  it("detailed mode renders every message and no fold", () => {
    settings.store.set("appearance.chatThreadMode", "detailed");
    renderTimeline(finishedTurn());

    expect(screen.queryByTestId("turn-work-fold")).toBeNull();
    expect(visible("Let me look at the test.")).toBe(true);
    expect(visible("Fixed: the race is gone.")).toBe(true);
    expect(screen.getAllByTestId("tool-card")).toHaveLength(2);
  });

  it("folds a finished turn into one collapsed header followed by the answer", () => {
    settings.store.set("appearance.chatThreadMode", "simplified");
    renderTimeline(finishedTurn());

    expect(visible("Fix the flaky test")).toBe(true);
    expect(screen.getAllByTestId("turn-work-fold")).toHaveLength(1);
    expect(foldButton()).toHaveAttribute("aria-expanded", "false");
    expect(foldButton()).toHaveTextContent(/^Activity/);
    expect(visible("Fixed: the race is gone.")).toBe(true);
    expect(visible("Let me look at the test.")).toBe(false);
    expect(screen.queryAllByTestId("tool-card")).toHaveLength(0);

    // The header sits between the question and the answer.
    const follows = (a: Node, b: Node) => Boolean(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING);
    expect(follows(screen.getByText("Fix the flaky test"), fold())).toBe(true);
    expect(follows(fold(), screen.getByText("Fixed: the race is gone."))).toBe(true);
  });

  it("reveals the interim narration and tool calls when the fold is expanded", () => {
    settings.store.set("appearance.chatThreadMode", "simplified");
    renderTimeline(finishedTurn());

    fireEvent.click(foldButton());

    expect(foldButton()).toHaveAttribute("aria-expanded", "true");
    expect(visible("Let me look at the test.")).toBe(true);
    expect(screen.getAllByTestId("tool-card").length).toBeGreaterThan(0);
    // The answer is not duplicated into the fold.
    expect(screen.getAllByText("Fixed: the race is gone.")).toHaveLength(1);

    fireEvent.click(foldButton());
    expect(visible("Let me look at the test.")).toBe(false);
  });

  it("keeps a pending-approval call and an ask_user card visible while collapsed", () => {
    settings.store.set("appearance.chatThreadMode", "simplified");
    const messages = [
      user(1, "Deploy it"),
      assistant(2, [text(0, "Checking first."), tool(1, "read_file", "call-read")]),
      assistant(3, [tool(0, "bash", "call-bash")]),
      assistant(4, [tool(0, "ask_user", "call-ask"), text(1, "Need your call.")]),
    ];
    renderTimeline(messages, { approvals: [pendingApproval("m3-b0")] });

    expect(foldButton()).toHaveAttribute("aria-expanded", "false");
    expect(visible("Checking first.")).toBe(false);
    expect(screen.getByRole("button", { name: /approve tool execution/i })).toBeInTheDocument();
    expect(screen.getByTestId("ask-user-card")).toBeInTheDocument();
    // Nothing else of the work leaks out: only the approval's own card.
    expect(screen.getAllByTestId("tool-card")).toHaveLength(1);
  });

  it("shows the pending approval exactly once when the fold is expanded", () => {
    settings.store.set("appearance.chatThreadMode", "simplified");
    const messages = [
      user(1, "Deploy it"),
      assistant(2, [text(0, "Checking first."), tool(1, "read_file", "call-read")]),
      assistant(3, [tool(0, "bash", "call-bash")]),
      assistant(4, [text(0, "Waiting on you.")]),
    ];
    renderTimeline(messages, { approvals: [pendingApproval("m3-b0")] });

    expect(screen.getAllByRole("button", { name: /approve tool execution/i })).toHaveLength(1);
    fireEvent.click(foldButton());

    expect(visible("Checking first.")).toBe(true);
    expect(screen.getAllByRole("button", { name: /approve tool execution/i })).toHaveLength(1);
  });

  it("labels the fold of a streaming turn with its current action", () => {
    settings.store.set("appearance.chatThreadMode", "simplified");
    const messages = [
      user(1, "Run the suite"),
      assistant(2, [text(0, "Starting."), tool(1, "bash", "call-bash")]),
      assistant(3, [tool(0, "bash", "call-bash-2")], StreamingState.STREAMING),
    ];
    renderTimeline(messages, { isStreaming: true });

    expect(fold()).toHaveAttribute("data-live", "true");
    expect(foldButton()).toHaveTextContent(/^bash/);
    expect(foldButton()).toHaveAttribute("aria-expanded", "false");
    expect(visible("Starting.")).toBe(false);
  });

  it("keeps successful spawn notices inspectable inside one activity and unchanged in detailed mode", async () => {
    settings.store.set("appearance.chatThreadMode", "simplified");
    const completion = {
      ...assistant(3, [text(0, 'spawn "research" completed')]),
      role: MessageRole.SYSTEM,
      displayStyle: DisplayStyle.INFO,
    };
    renderTimeline([
      user(1, "Investigate"),
      assistant(2, [tool(0, "read_file", "read")]),
      completion,
      assistant(4, [tool(0, "bash", "check")]),
      assistant(5, [text(0, "Verified.")]),
    ]);
    expect(screen.getAllByTestId("turn-work-fold")).toHaveLength(1);
    expect(visible('spawn "research" completed')).toBe(false);
    fireEvent.click(foldButton());
    expect(visible('spawn "research" completed')).toBe(true);
    expect(visible("Verified.")).toBe(true);
    await act(async () => setChatThreadMode("detailed"));
    expect(screen.queryByTestId("turn-work-fold")).toBeNull();
    expect(visible('spawn "research" completed')).toBe(true);
  });

  it("switches modes through the setting without remount errors", async () => {
    settings.store.set("appearance.chatThreadMode", "detailed");
    const errors = vi.spyOn(console, "error").mockImplementation(() => {});
    renderTimeline(finishedTurn());
    expect(screen.queryByTestId("turn-work-fold")).toBeNull();

    await act(async () => setChatThreadMode("simplified"));
    expect(screen.getAllByTestId("turn-work-fold")).toHaveLength(1);
    expect(visible("Let me look at the test.")).toBe(false);

    await act(async () => setChatThreadMode("detailed"));
    expect(screen.queryByTestId("turn-work-fold")).toBeNull();
    expect(visible("Let me look at the test.")).toBe(true);

    await act(async () => setChatThreadMode("simplified"));
    expect(screen.getAllByTestId("turn-work-fold")).toHaveLength(1);
    expect(errors).not.toHaveBeenCalled();
    errors.mockRestore();
  });

  it("scroll-to-message for a message that lives only in the fold lands on the fold", () => {
    settings.store.set("appearance.chatThreadMode", "simplified");
    renderTimeline(finishedTurn());

    // m3 is a tool-only message: it has no row of its own, only a place in the fold.
    expect(() =>
      act(() => {
        window.dispatchEvent(new CustomEvent("scroll-to-message", { detail: { messageId: "m3" } }));
      }),
    ).not.toThrow();

    expect(scrollToIndex).toHaveBeenCalledTimes(1);
    const foldRow = fold().closest("[data-timeline-index]");
    expect(scrollToIndex).toHaveBeenCalledWith(Number(foldRow?.getAttribute("data-timeline-index")), expect.anything());
  });
});
