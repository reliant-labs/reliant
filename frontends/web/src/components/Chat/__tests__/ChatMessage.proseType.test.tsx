import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ChatMessage } from "../ChatMessage";
import { ContentBlockType, MessageRole, StreamingState } from "../../../types/chat";
import type { Message } from "../../../types/chat";
import { SurfaceProvider } from "../../../lib/surfaceContext";

vi.mock("../MarkdownRenderer", () => ({
  MarkdownRenderer: ({ content, className }: { content: string; className?: string }) => (
    <div data-testid="markdown" className={className}>{content}</div>
  ),
}));

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
  useChatIdOrActive: (chatId?: string | null) => chatId || "chat-1",
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

function renderOn(surface: "mobile" | "desktop", role: MessageRole) {
  return render(
    <SurfaceProvider surface={surface}>
      <ChatMessage message={buildMessage(role)} chatId="chat-1" />
    </SurfaceProvider>,
  );
}

// Reading type (§2.4 of the chat design): `text-copy` (16/26) on desktop for
// the bubble and the answer; on the mobile surface, whose root is 17px, the
// same 16px is `text-ui` at 1.6 leading, so a phone never reads larger.
describe("transcript prose type", () => {
  it("sizes the user bubble's text for the mobile root", () => {
    renderOn("mobile", MessageRole.USER);
    const text = screen.getByText("Hello there.");
    expect(text).toHaveClass("text-ui", "leading-[1.6]");
    expect(text).not.toHaveClass("text-copy");
  });

  it("sizes assistant markdown for the mobile root", () => {
    renderOn("mobile", MessageRole.ASSISTANT);
    expect(screen.getByTestId("markdown")).toHaveClass("text-ui", "[--chat-prose-leading:1.6]");
  });

  it("reads the desktop user bubble at text-copy", () => {
    renderOn("desktop", MessageRole.USER);
    expect(screen.getByText("Hello there.")).toHaveClass("text-copy");
  });

  it("reads desktop markdown at text-copy", () => {
    renderOn("desktop", MessageRole.ASSISTANT);
    expect(screen.getByTestId("markdown")).toHaveClass("text-copy");
  });

  it("draws the user turn as a neutral right-aligned bubble", () => {
    renderOn("desktop", MessageRole.USER);
    const bubble = screen.getByText("Hello there.").closest(".user-message-content");
    expect(bubble).toHaveClass("bg-bubble", "rounded-2xl", "max-w-[85%]");
    expect(bubble?.className).not.toMatch(/\b(border|shadow|bg-primary)/);
    expect(bubble?.parentElement).toHaveClass("flex-col", "items-end");
  });
});
