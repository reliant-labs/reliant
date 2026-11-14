import { act, fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { ChatMessage } from "../ChatMessage";
import { ContentBlockType, MessageRole, StreamingState } from "../../../types/chat";
import type { Message } from "../../../types/chat";
import { SurfaceProvider } from "../../../lib/surfaceContext";

vi.mock("../MarkdownRenderer", () => ({
  MarkdownRenderer: ({ content }: { content: string }) => <div>{content}</div>,
}));

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
  useToolResultsByCallId: () => ({}),
  useToolCallStates: () => new Map(),
  useChat: () => ({ worktreeId: "worktree-1" }),
  useChatMessages: () => [],
  useStreamingMessages: () => [],
}));

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: () => ({ id: "project-1" }),
}));

const branchChat = vi.fn();
vi.mock("../../../store/chatStore", () => ({
  useChatStore: { getState: () => ({ branchChat }) },
}));

vi.mock("../../../api/client", () => ({
  api: {},
}));

vi.mock("../../../lib/logger", () => ({
  logger: { debug: vi.fn(), error: vi.fn(), info: vi.fn(), warn: vi.fn() },
}));

vi.mock("../../../lib/toast-manager", () => ({ toast: { error: vi.fn() } }));

vi.mock("../../../lib/tabSwitchProfiler", () => ({
  tabSwitchProfiler: { isEnabled: () => false },
}));

vi.mock("../BranchOptionsMenu", () => ({ BranchOptionsMenu: () => null }));
vi.mock("../BranchToWorktreeModal", () => ({ BranchToWorktreeModal: () => null }));
vi.mock("../BranchToExistingWorktreeModal", () => ({
  BranchToExistingWorktreeModal: () => null,
}));
vi.mock("../CodeContextPill", () => ({ CodeContextPill: () => null }));

function buildMessage(role: MessageRole): Message {
  return {
    id: "msg-1",
    chatId: "chat-1",
    seq: BigInt(1),
    thread: "chat-1",
    role,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: [
      { id: "b0", index: 0, type: ContentBlockType.TEXT, content: "Hello there." },
    ],
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    sequenceNumber: BigInt(1),
  } as Message;
}

function touch(x: number, y: number) {
  return { touches: [{ clientX: x, clientY: y }] };
}

function renderMobile(role: MessageRole) {
  return render(
    <SurfaceProvider surface="mobile">
      <ChatMessage message={buildMessage(role)} chatId="chat-1" />
    </SurfaceProvider>,
  );
}

function messageSurface(): HTMLElement {
  const el = document.querySelector(".user-message-content, .message-content");
  if (!(el instanceof HTMLElement)) throw new Error("message surface not found");
  return el;
}

describe("ChatMessage long-press actions (mobile)", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("opens the actions sheet from a discoverable button", () => {
    renderMobile(MessageRole.USER);
    fireEvent.click(screen.getByRole("button", { name: "Message actions" }));

    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByText("Copy message")).toBeInTheDocument();
    expect(screen.queryByText("Branch in place")).not.toBeInTheDocument();
    expect(screen.queryByText("Branch to existing workspace")).not.toBeInTheDocument();
  });

  it("does not open the sheet on a short tap", () => {
    renderMobile(MessageRole.USER);
    const surface = messageSurface();

    fireEvent.touchStart(surface, touch(10, 10));
    vi.advanceTimersByTime(200);
    fireEvent.touchEnd(surface, touch(10, 10));
    vi.advanceTimersByTime(500);

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("cancels when the touch moves past the threshold (scroll)", () => {
    renderMobile(MessageRole.ASSISTANT);
    const surface = messageSurface();

    fireEvent.touchStart(surface, touch(10, 10));
    vi.advanceTimersByTime(100);
    fireEvent.touchMove(surface, touch(10, 60));
    vi.advanceTimersByTime(500);

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("does not attach long-press handlers on desktop", () => {
    render(<ChatMessage message={buildMessage(MessageRole.USER)} chatId="chat-1" />);
    const surface = messageSurface();

    fireEvent.touchStart(surface, touch(10, 10));
    vi.advanceTimersByTime(500);

    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("does not expose branching before mobile routing supports it", () => {
    branchChat.mockClear();
    renderMobile(MessageRole.USER);
    fireEvent.click(screen.getByRole("button", { name: "Message actions" }));
    expect(screen.queryByText("Branch in place")).not.toBeInTheDocument();
    expect(branchChat).not.toHaveBeenCalled();
  });

  it.each([
    ["user", MessageRole.USER],
    ["assistant", MessageRole.ASSISTANT],
  ] as const)("keeps %s prose selectable on mobile", (_n, role) => {
    renderMobile(role);
    const surface = messageSurface();
    expect(surface.className).toContain("select-text");
    expect(surface.className).not.toContain("touch-callout");
  });

  it("preserves the native context menu on prose", () => {
    renderMobile(MessageRole.USER);
    const surface = messageSurface();
    const notPrevented = fireEvent.contextMenu(surface);
    expect(notPrevented).toBe(true);
  });

  it("never clears selection or opens actions while holding prose", () => {
    const removeAllRanges = vi.fn();
    const spy = vi
      .spyOn(window, "getSelection")
      .mockReturnValue({ removeAllRanges } as unknown as Selection);
    try {
      renderMobile(MessageRole.USER);
      fireEvent.touchStart(messageSurface(), touch(10, 10));
      act(() => {
        vi.advanceTimersByTime(500);
      });
      expect(removeAllRanges).not.toHaveBeenCalled();
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    } finally {
      spy.mockRestore();
    }
  });

  it("keeps the desktop surface selectable", () => {
    render(<ChatMessage message={buildMessage(MessageRole.USER)} chatId="chat-1" />);
    const surface = messageSurface();
    expect(surface.className).not.toContain("select-none");
    expect(surface.className).not.toContain("touch-callout");
    expect(fireEvent.contextMenu(surface)).toBe(true);
  });
});
