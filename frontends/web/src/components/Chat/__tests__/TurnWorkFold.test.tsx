import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TurnWorkFold } from "../TurnWorkFold";
import { ContentBlockType, MessageRole, StreamingState } from "../../../types/chat";
import { ToolCallStatus, type Message } from "../../../gen/reliant/v1/chat_pb";
import type { TurnWork } from "../thread-views/simplifiedTurns";
import { SurfaceProvider } from "../../../lib/surfaceContext";

vi.mock("../ChatMessage", () => ({
  ChatMessage: ({ message, slice }: { message: Message; slice?: { role: string; segmentIndexes: number[]; callIds?: string[] } }) => (
    <div data-testid={`row-${slice?.role}`} data-message={message.id} data-calls={slice?.callIds?.join(",")}>
      {slice?.role}:{message.id}:{slice?.segmentIndexes.join(",")}
    </div>
  ),
}));

vi.mock("../AgentMessageCard", () => ({
  isAgentMessage: (message: Message) => message.id.startsWith("agent-"),
  AgentMessageCard: ({ message }: { message: Message }) => <div data-testid="agent-event">{message.id}</div>,
}));
vi.mock("../SystemNotificationMessage", () => ({
  SystemNotificationMessage: ({ message }: { message: Message }) => <div data-testid="system-event">{message.id}</div>,
}));

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
  useChatIdOrActive: (chatId?: string | null) => chatId || "chat-1",
  useChat: () => ({ worktreeId: "worktree-1" }),
  useToolResultsByCallId: () => ({}),
  useToolCallStates: () => new Map(),
}));

const { branchChat } = vi.hoisted(() => ({ branchChat: vi.fn() }));
vi.mock("../../../store/chatStore", () => ({ useChatStore: { getState: () => ({ branchChat }) } }));
vi.mock("../../../store/projectStore", () => ({ useProjectStore: () => ({ id: "project-1" }) }));
vi.mock("../BranchOptionsMenu", () => ({
  BranchOptionsMenu: ({ onBranchChat }: { onBranchChat: () => void }) => (
    <button type="button" onClick={onBranchChat}>Branch in place</button>
  ),
}));
vi.mock("../BranchToWorktreeModal", () => ({ BranchToWorktreeModal: () => null }));
vi.mock("../BranchToExistingWorktreeModal", () => ({ BranchToExistingWorktreeModal: () => null }));

vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({ allWorkflows: [] }),
}));

function message(id: string, createdAt: string, blocks: Message["contentBlocks"]): Message {
  return {
    id,
    chatId: "chat-1",
    thread: "chat-1",
    role: MessageRole.ASSISTANT,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: blocks,
    createdAt,
    updatedAt: createdAt,
  } as unknown as Message;
}

function toolBlock(id: string, name: string, input: object, completedAt?: string) {
  return {
    id: `b-${id}`,
    index: 0,
    type: ContentBlockType.TOOL_CALL,
    toolName: name,
    toolCallId: id,
    input: JSON.stringify(input),
    completedAt,
  };
}

function makeWork(): TurnWork {
  const m1 = message("m1", "2024-01-01T00:00:00.000Z", [
    toolBlock("c1", "view", { path: "a.ts" }, "2024-01-01T00:00:10.000Z"),
  ] as Message["contentBlocks"]);
  const m2 = message("m2", "2024-01-01T00:00:20.000Z", [
    toolBlock("c2", "shell", { command: "ls" }, "2024-01-01T00:01:12.000Z"),
  ] as Message["contentBlocks"]);
  return {
    key: "work-m1",
    thread: "chat-1",
    parts: [
      { message: m1, segmentIndexes: [0] },
      { message: m2, segmentIndexes: [0] },
    ],
  };
}

/** The fold's disclosure: the header's first button. */
const toggle = () => screen.getAllByRole("button")[0];

describe("TurnWorkFold", () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] });
    branchChat.mockReset();
  });
  afterEach(() => vi.useRealTimers());

  it("is collapsed by default, with the done headline and its counts, and nothing of the run outside it", () => {
    render(<TurnWorkFold work={makeWork()} chatId="chat-1" />);
    const button = toggle();
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(screen.getByText("Activity")).toBeTruthy();
    expect(button.textContent).toContain("Activity · 1 file read · 1 command");
    expect(button.getAttribute("aria-label")).toBe("Activity, 1 file read · 1 command");
    expect(screen.getByRole("timer").textContent).toBe("1:12");
    expect(screen.getByTestId("turn-work-fold").querySelectorAll("[data-testid^='row-']")).toHaveLength(0);
  });

  it("carries a message row's chrome: the run's end time and a branch from its last message", () => {
    vi.setSystemTime(new Date("2024-01-01T00:06:00.000Z"));
    render(<TurnWorkFold work={makeWork()} chatId="chat-1" />);
    const time = screen.getByTestId("turn-work-fold").querySelector("time");
    expect(time?.getAttribute("dateTime")).toBe("2024-01-01T00:01:12.000Z");
    expect(time?.textContent).toBe("4 minutes ago");
    expect(time?.getAttribute("title")).toBe(new Date("2024-01-01T00:01:12.000Z").toLocaleString());

    fireEvent.click(screen.getByRole("button", { name: "Branch from message" }));
    fireEvent.click(screen.getByRole("button", { name: "Branch in place" }));
    expect(branchChat).toHaveBeenCalledWith("chat-1", "m2");
  });

  it("leaves the chrome off the phone, and the time off a live run", () => {
    const { unmount } = render(
      <SurfaceProvider surface="mobile">
        <TurnWorkFold work={makeWork()} chatId="chat-1" />
      </SurfaceProvider>,
    );
    expect(screen.getByTestId("turn-work-fold").querySelector("time")).toBeNull();
    expect(screen.queryByRole("button", { name: "Branch from message" })).toBeNull();
    unmount();

    render(<TurnWorkFold work={makeWork()} chatId="chat-1" isStreaming />);
    expect(screen.getByTestId("turn-work-fold").querySelector("time")).toBeNull();
  });

  it("counts folded agent mail in the header", () => {
    const work = makeWork();
    work.events = [
      { type: "message", message: message("agent-1", "2024-01-01T00:00:15.000Z", []), afterPart: 1 },
      { type: "message", message: message("agent-2", "2024-01-01T00:00:16.000Z", []), afterPart: 1 },
    ];
    render(<TurnWorkFold work={work} chatId="chat-1" />);
    expect(toggle().textContent).toContain("1 command · 2 agent messages");
  });

  it("times a run of mail alone from its first message to its last, with nothing to branch from", () => {
    const work: TurnWork = {
      key: "work-agent-1",
      thread: "chat-1",
      parts: [],
      events: [
        { type: "message", message: message("agent-1", "2024-01-01T00:00:05.000Z", []), afterPart: 0 },
        { type: "message", message: message("agent-2", "2024-01-01T00:00:50.000Z", []), afterPart: 0 },
      ],
    };
    render(<TurnWorkFold work={work} chatId="chat-1" />);
    expect(toggle().textContent).toContain("Activity · 2 agent messages");
    expect(screen.getByRole("timer").textContent).toBe("0:45");
    expect(screen.queryByRole("button", { name: "Branch from message" })).toBeNull();
  });

  it("toggles the work rows open and closed", () => {
    render(<TurnWorkFold work={makeWork()} chatId="chat-1" />);
    const button = toggle();
    fireEvent.click(button);
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(button.getAttribute("aria-controls")).toBe(screen.getByTestId("turn-work-body").id);
    expect(screen.getAllByTestId("row-work")).toHaveLength(2);
    fireEvent.click(button);
    expect(screen.queryAllByTestId("row-work")).toHaveLength(0);
  });

  it("renders and counts only the calls a part holds when a surfaced call cut its segment", () => {
    const m1 = message("m1", "2024-01-01T00:00:00.000Z", [
      toolBlock("c1", "view", { path: "a.ts" }, "2024-01-01T00:00:10.000Z"),
      { ...toolBlock("ask", "ask_user", {}), id: "b-ask", index: 1 },
    ] as Message["contentBlocks"]);
    render(
      <TurnWorkFold
        work={{ key: "work-m1-0", thread: "chat-1", parts: [{ message: m1, segmentIndexes: [0], callIds: ["c1"] }] }}
        chatId="chat-1"
      />,
    );
    expect(toggle().textContent).toContain("Activity · 1 file read");
    expect(toggle().textContent).not.toContain("other tool");
    fireEvent.click(toggle());
    expect(screen.getByTestId("row-work").dataset.calls).toBe("c1");
  });

  it("keeps failures explained and inspectable in the disclosure", () => {
    const work = makeWork();
    work.parts[0].message.contentBlocks[0].toolCallStatus = ToolCallStatus.FAILED;
    render(<TurnWorkFold work={work} />);
    expect(toggle().textContent).toContain("1 failed step");
    expect(toggle().getAttribute("aria-label")).toBe("Activity, 1 file read · 1 command, 1 failed step");
    fireEvent.click(toggle());
    expect(screen.getByText("1 step failed. Expand the steps below to inspect their errors.")).toBeTruthy();
    expect(screen.getAllByTestId("row-work")).toHaveLength(2);
  });

  it("interleaves folded events only when expanded and freezes at the last event", () => {
    const work = makeWork();
    work.events = [
      { type: "message", message: message("agent-update", "2024-01-01T00:00:15.000Z", []), afterPart: 1 },
      { type: "message", message: message("system-done", "2024-01-01T01:02:03.000Z", []), afterPart: 2 },
    ];
    render(<TurnWorkFold work={work} />);
    expect(screen.queryByTestId("agent-event")).toBeNull();
    expect(screen.queryByTestId("system-event")).toBeNull();
    expect(screen.getByRole("timer").textContent).toBe("1:02:03");
    fireEvent.click(toggle());
    const rows = screen.getByTestId("turn-work-body").querySelectorAll("[data-testid]");
    expect(Array.from(rows, (row) => row.getAttribute("data-testid"))).toEqual([
      "row-work", "agent-event", "row-work", "system-event",
    ]);
    act(() => vi.advanceTimersByTime(60_000));
    expect(screen.getByRole("timer").textContent).toBe("1:02:03");
  });

  it("shows live text while streaming, collapsed, and ticks only then", () => {
    vi.setSystemTime(new Date("2024-01-01T00:00:30.000Z"));
    const { rerender } = render(<TurnWorkFold work={makeWork()} chatId="chat-1" isStreaming />);
    const button = toggle();
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(button.textContent).not.toContain("Working…");
    expect(button.querySelector(".turn-work-shimmer")).toBeNull();
    expect(screen.getByTestId("turn-work-fold").dataset.live).toBe("true");
    const timer = screen.getByRole("timer");
    expect(timer.textContent).toBe("0:30");
    expect(timer.getAttribute("aria-live")).toBe("off");
    expect(timer.classList.contains("tabular-nums")).toBe(true);
    act(() => vi.advanceTimersByTime(1000));
    expect(timer.textContent).toBe("0:31");
    act(() => vi.advanceTimersByTime(3000));
    expect(timer.textContent).toBe("0:34");

    rerender(<TurnWorkFold work={makeWork()} chatId="chat-1" />);
    expect(screen.getByText("Activity")).toBeTruthy();
    expect(timer.textContent).toBe("1:12");
    act(() => vi.advanceTimersByTime(5000));
    expect(timer.textContent).toBe("1:12");
    expect(vi.getTimerCount()).toBe(0);
  });
});
