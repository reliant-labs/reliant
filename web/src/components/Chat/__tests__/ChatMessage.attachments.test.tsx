import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { ChatMessage } from "../ChatMessage";
import { ContentBlockType, MessageRole, StreamingState } from "../../../types/chat";
import type { Message } from "../../../types/chat";

vi.mock("../MessageAttachments", () => ({
  MessageAttachments: ({ attachments }: { attachments: Array<{ filename: string }> }) => (
    <div data-testid="message-attachments">
      {attachments.map((attachment) => attachment.filename).join(",")}
    </div>
  ),
}));

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
  useProcessedMessages: () => new Map(),
  useToolCallStates: () => new Map(),
  useChat: () => ({ worktreeId: "worktree-1" }),
}));

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: () => ({ id: "project-1" }),
}));

vi.mock("../../../store/chatStore", () => ({
  useChatStore: {
    getState: () => ({
      branchChat: vi.fn(),
    }),
  },
}));

vi.mock("../../../api/client", () => ({
  api: {
    toolCalls: {
      cancel: vi.fn(),
      convertToBackground: vi.fn(),
    },
  },
}));

vi.mock("../../../lib/logger", () => ({
  logger: {
    debug: vi.fn(),
    error: vi.fn(),
    info: vi.fn(),
    warn: vi.fn(),
  },
}));

vi.mock("../../../lib/toast-manager", () => ({
  toast: { error: vi.fn() },
}));

vi.mock("../../../lib/tabSwitchProfiler", () => ({
  tabSwitchProfiler: { isEnabled: () => false },
}));

vi.mock("../BranchOptionsMenu", () => ({
  BranchOptionsMenu: () => null,
}));

vi.mock("../BranchToWorktreeModal", () => ({
  BranchToWorktreeModal: () => null,
}));

vi.mock("../BranchToExistingWorktreeModal", () => ({
  BranchToExistingWorktreeModal: () => null,
}));

vi.mock("../CodeContextPill", () => ({
  CodeContextPill: () => null,
}));

function buildUserMessage(overrides: Partial<Message> = {}): Message {
  return {
    id: "msg-1",
    chatId: "chat-1",
    ordinal: BigInt(1),
    thread: "chat-1",
    role: MessageRole.USER,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: [
      {
        id: "block-1",
        index: 0,
        type: ContentBlockType.TEXT,
        content: "Please inspect this image",
      },
    ],
    attachments: [
      {
        id: "att-1",
        filename: "screenshot.png",
        size: BigInt(123),
        mimeType: "image/png",
        url: "/api/attachments/att-1",
      },
    ],
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    sequenceNumber: BigInt(1),
    ...overrides,
  } as Message;
}

describe("ChatMessage attachments", () => {
  it("renders user-message attachments without requiring the bubble to be expanded", () => {
    render(<ChatMessage message={buildUserMessage()} chatId="chat-1" />);

    expect(screen.getByTestId("message-attachments")).toHaveTextContent("screenshot.png");
  });
});
