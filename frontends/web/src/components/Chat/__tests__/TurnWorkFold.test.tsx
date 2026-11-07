import { useContext } from "react";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TurnWorkFold } from "../TurnWorkFold";
import { ContentBlockType, MessageRole, StreamingState } from "../../../types/chat";
import { ToolCallStatus, type Message } from "../../../gen/reliant/v1/chat_pb";
import type { TurnWork } from "../thread-views/simplifiedTurns";

vi.mock("../ChatMessage", async () => {
  const { DeployCardShownOutside } = await import("../../Apps/DeployedAppCard");
  return {
    ChatMessage: ({ message, slice }: { message: Message; slice?: { role: string; segmentIndexes: number[] } }) => (
      <div
        data-testid={`row-${slice?.role}`}
        data-message={message.id}
        data-deploy-card-outside={String(useContext(DeployCardShownOutside))}
      >
        {slice?.role}:{message.id}:{slice?.segmentIndexes.join(",")}
      </div>
    ),
  };
});

vi.mock("../AgentMessageCard", () => ({
  isAgentMessage: (message: Message) => message.id.startsWith("agent-"),
  AgentMessageCard: ({ message }: { message: Message }) => <div data-testid="agent-event">{message.id}</div>,
}));
vi.mock("../SystemNotificationMessage", () => ({
  SystemNotificationMessage: ({ message }: { message: Message }) => <div data-testid="system-event">{message.id}</div>,
}));

vi.mock("../../../store/chatStoreHooks", () => ({
  useToolResultsByCallId: () => ({}),
  useToolCallStates: () => new Map(),
}));

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

describe("TurnWorkFold", () => {
  beforeEach(() => vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "Date"] }));
  afterEach(() => vi.useRealTimers());

  it("is collapsed by default, with the done headline and surfaced rows", () => {
    render(<TurnWorkFold work={makeWork()} chatId="chat-1" />);
    const button = screen.getByRole("button");
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(screen.getByText("Activity")).toBeTruthy();
    expect(screen.getByRole("timer").textContent).toBe("1:12");
    expect(button.textContent).not.toContain("read 1 file");
    expect(screen.queryAllByTestId("row-work")).toHaveLength(0);
    expect(screen.getAllByTestId("row-work-surfaced")).toHaveLength(2);
  });

  it("toggles the work rows open and closed; surfaced rows stay", () => {
    render(<TurnWorkFold work={makeWork()} chatId="chat-1" />);
    const button = screen.getByRole("button");
    fireEvent.click(button);
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(button.getAttribute("aria-controls")).toBe(screen.getByTestId("turn-work-body").id);
    expect(screen.getByText("read 1 file · ran 1 command")).toBeTruthy();
    expect(screen.getAllByTestId("row-work")).toHaveLength(2);
    expect(screen.getAllByTestId("row-work-surfaced")).toHaveLength(2);
    fireEvent.click(button);
    expect(screen.queryAllByTestId("row-work")).toHaveLength(0);
    expect(screen.getAllByTestId("row-work-surfaced")).toHaveLength(2);
  });

  it("leaves a deploy's card to the surfaced rows, so an open fold never shows it twice", () => {
    render(<TurnWorkFold work={makeWork()} chatId="chat-1" />);
    fireEvent.click(screen.getByRole("button"));
    for (const row of screen.getAllByTestId("row-work")) expect(row.dataset.deployCardOutside).toBe("true");
    for (const row of screen.getAllByTestId("row-work-surfaced")) expect(row.dataset.deployCardOutside).toBe("false");
  });

  it("keeps failures explained and inspectable in the disclosure", () => {
    const work = makeWork();
    work.parts[0].message.contentBlocks[0].toolCallStatus = ToolCallStatus.FAILED;
    render(<TurnWorkFold work={work} />);
    expect(screen.getByRole("button").textContent).toContain("1 failed step");
    fireEvent.click(screen.getByRole("button"));
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
    fireEvent.click(screen.getByRole("button"));
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
    const button = screen.getByRole("button");
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
