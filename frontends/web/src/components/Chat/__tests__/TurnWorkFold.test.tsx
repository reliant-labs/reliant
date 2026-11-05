import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { TurnWorkFold } from "../TurnWorkFold";
import { ContentBlockType, MessageRole, StreamingState } from "../../../types/chat";
import type { Message } from "../../../gen/reliant/v1/chat_pb";
import type { TurnWork } from "../thread-views/simplifiedTurns";

vi.mock("../ChatMessage", () => ({
  ChatMessage: ({ message, slice }: { message: Message; slice?: { role: string; segmentIndexes: number[] } }) => (
    <div data-testid={`row-${slice?.role}`} data-message={message.id}>
      {slice?.role}:{message.id}:{slice?.segmentIndexes.join(",")}
    </div>
  ),
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
    expect(button.textContent).toContain("Worked for 1m 12s · read 1 file · ran 1 command");
    expect(screen.queryAllByTestId("row-work")).toHaveLength(0);
    expect(screen.getAllByTestId("row-work-surfaced")).toHaveLength(2);
  });

  it("toggles the work rows open and closed; surfaced rows stay", () => {
    render(<TurnWorkFold work={makeWork()} chatId="chat-1" />);
    const button = screen.getByRole("button");
    fireEvent.click(button);
    expect(button.getAttribute("aria-expanded")).toBe("true");
    expect(button.getAttribute("aria-controls")).toBe(screen.getByTestId("turn-work-body").id);
    expect(screen.getAllByTestId("row-work")).toHaveLength(2);
    expect(screen.getAllByTestId("row-work-surfaced")).toHaveLength(2);
    fireEvent.click(button);
    expect(screen.queryAllByTestId("row-work")).toHaveLength(0);
    expect(screen.getAllByTestId("row-work-surfaced")).toHaveLength(2);
  });

  it("shows live text while streaming, collapsed, and ticks only then", () => {
    vi.setSystemTime(new Date("2024-01-01T00:00:30.000Z"));
    const { rerender } = render(<TurnWorkFold work={makeWork()} chatId="chat-1" isStreaming />);
    const button = screen.getByRole("button");
    expect(button.getAttribute("aria-expanded")).toBe("false");
    expect(button.textContent).toContain("Working…");
    expect(screen.getByTestId("turn-work-fold").dataset.live).toBe("true");

    rerender(<TurnWorkFold work={makeWork()} chatId="chat-1" />);
    expect(screen.getByRole("button").textContent).toContain("Worked for");
    act(() => {
      vi.advanceTimersByTime(5000);
    });
    expect(screen.getByRole("button").textContent).toContain("Worked for 1m 12s");
  });
});
