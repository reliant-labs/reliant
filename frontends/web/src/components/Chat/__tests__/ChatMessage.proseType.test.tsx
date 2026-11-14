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

// §7.1: transcript prose reads at the platform body size on the mobile surface
// (16px at the default Appearance step, rem so the step still scales it) and
// keeps desktop's dense text-sm.
describe("transcript prose type", () => {
  it("sizes the user bubble's text up on the mobile surface", () => {
    renderOn("mobile", MessageRole.USER);
    const text = screen.getByText("Hello there.").parentElement as HTMLElement;
    expect(text).toHaveClass("text-[0.9412rem]", "leading-[1.55]");
    expect(text).not.toHaveClass("text-sm");
  });

  it("sizes assistant markdown up on the mobile surface", () => {
    renderOn("mobile", MessageRole.ASSISTANT);
    expect(screen.getByTestId("markdown")).toHaveClass("text-[0.9412rem]", "[--chat-prose-leading:1.55]");
  });

  it("keeps desktop prose at text-sm", () => {
    renderOn("desktop", MessageRole.USER);
    expect(screen.getByText("Hello there.").parentElement).toHaveClass("text-sm", "leading-relaxed");
  });

  it("keeps desktop markdown at text-sm", () => {
    renderOn("desktop", MessageRole.ASSISTANT);
    expect(screen.getByTestId("markdown")).toHaveClass("text-sm");
  });
});
