// Copy message and Export chat over a transcript a stub read sent: every
// elided input and result is read whole, in one GetContentBlockBodies batch
// with `full`, so neither the clipboard nor the file ever holds a skeleton or
// a stub.

import { fireEvent, render, renderHook, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { ContentBlockType, MessageRole, StreamingState } from "../../../types/chat";
import type { Message } from "../../../types/chat";
import { queryClient } from "../../../lib/query-client";
import { setTransport } from "../../../lib/connect";
import { mockTransport } from "../../../lib/test-utils";
import type { ToolResultsByCallId } from "../../../lib/messageProcessor";

const mocks = vi.hoisted(() => ({
  results: {} as ToolResultsByCallId,
  messages: [] as Message[],
  toastError: vi.fn(),
}));

vi.mock("../MarkdownRenderer", () => ({
  MarkdownRenderer: ({ content }: { content: string }) => <div>{content}</div>,
}));
vi.mock("../ToolExecution", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../ToolExecution")>()),
  ToolExecution: () => null,
}));
vi.mock("../../../hooks/useWorkflowExecutions", () => ({ useWorkflowExecutions: () => ({ allWorkflows: [] }) }));
vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
  useChatIdOrActive: (chatId?: string | null) => chatId || "chat-1",
  useToolResultsByCallId: () => mocks.results,
  useToolCallStates: () => new Map(),
  useChat: () => ({ worktreeId: "worktree-1" }),
  useChatMessages: () => [],
  useStreamingMessages: () => [],
}));
vi.mock("../../../store/projectStore", () => ({ useProjectStore: () => ({ id: "project-1" }) }));
vi.mock("../../../store/chatStore", () => ({ useChatStore: { getState: () => ({ branchChat: vi.fn() }) } }));
vi.mock("../../../lib/logger", () => ({ logger: { debug: vi.fn(), error: vi.fn(), info: vi.fn(), warn: vi.fn() } }));
vi.mock("../../../lib/toast-manager", () => ({
  toast: { error: mocks.toastError, notify: vi.fn(), success: vi.fn(), info: vi.fn() },
}));
vi.mock("../../../lib/tabSwitchProfiler", () => ({ tabSwitchProfiler: { isEnabled: () => false } }));
vi.mock("../BranchOptionsMenu", () => ({ BranchOptionsMenu: () => null }));
vi.mock("../BranchToWorktreeModal", () => ({ BranchToWorktreeModal: () => null }));
vi.mock("../BranchToExistingWorktreeModal", () => ({ BranchToExistingWorktreeModal: () => null }));
vi.mock("../CodeContextPill", () => ({ CodeContextPill: () => null }));
// Export's own collaborators.
vi.mock("../../../hooks/chat-queries", () => ({
  useChat: () => ({ data: { id: "chat-1", title: "Totals", worktreeId: "wt-1" } }),
  useRenameChat: () => ({ mutateAsync: vi.fn() }),
  useArchiveChat: () => ({ mutateAsync: vi.fn() }),
  useUnarchiveChat: () => ({ mutate: vi.fn() }),
}));
vi.mock("../../../hooks/message-queries", () => ({ useMessages: () => ({ data: { messages: mocks.messages } }) }));
vi.mock("../../../hooks/worktree-queries", () => ({
  resolveWorktree: vi.fn(async () => null),
  resolveMainWorktree: vi.fn(async () => undefined),
}));

const { ChatMessage } = await import("../ChatMessage");
const { useChatActions } = await import("../useChatActions");

const SKELETON = JSON.stringify({ file_path: "src/totals.ts", content: "SKELETON…" });
const WHOLE_INPUT = JSON.stringify({ file_path: "src/totals.ts", content: "WHOLE-INPUT" });

function assistant(callBlockId = "call-c1"): Message {
  return {
    id: "msg-1",
    chatId: "chat-1",
    seq: 1n,
    thread: "chat-1",
    role: MessageRole.ASSISTANT,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: [
      { id: "t0", index: 0, type: ContentBlockType.TEXT, content: "Wrote the totals." },
      {
        id: callBlockId,
        index: 1,
        type: ContentBlockType.TOOL_CALL,
        toolName: "write",
        toolCallId: "c1",
        input: SKELETON,
        bodyElided: true,
        bodySize: 4000n,
      },
    ],
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
  } as Message;
}

function toolMessage(): Message {
  return {
    id: "msg-2",
    chatId: "chat-1",
    seq: 2n,
    thread: "chat-1",
    role: MessageRole.TOOL,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: [
      {
        id: "res-c1",
        index: 0,
        type: ContentBlockType.TOOL_RESULT,
        toolName: "write",
        toolCallId: "c1",
        content: "STUB-RESULT",
        truncated: true,
        originalSize: 9000n,
        bodyElided: true,
        bodySize: 9000n,
      },
    ],
    createdAt: "2024-01-01T00:00:01.000Z",
    updatedAt: "2024-01-01T00:00:01.000Z",
  } as Message;
}

const getContentBlockBodies = vi.fn();

beforeEach(() => {
  queryClient.clear();
  mocks.toastError.mockReset();
  getContentBlockBodies.mockReset();
  getContentBlockBodies.mockImplementation(async (request: { blockIds: string[] }) => ({
    bodies: request.blockIds.map((blockId) =>
      blockId.startsWith("call-") ? { blockId, input: WHOLE_INPUT } : { blockId, content: "WHOLE-RESULT" },
    ),
    notFound: [],
    deferred: [],
  }));
  setTransport(mockTransport({ "ChatService.GetContentBlockBodies": (req: unknown) => getContentBlockBodies(req) }));
  mocks.results = {
    c1: { content: "STUB-RESULT", truncated: true, original_size: 9000, elided: true, block_id: "res-c1" },
  };
  mocks.messages = [assistant(), toolMessage()];
});
afterEach(() => {
  setTransport(null);
  queryClient.clear();
});

describe("Copy message over elided bodies", () => {
  it("copies the whole result, read in one full batch, never the stub", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });

    render(<ChatMessage message={assistant()} chatId="chat-1" />);
    fireEvent.click(screen.getByRole("button", { name: "Copy message" }));

    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    const copied = writeText.mock.calls[0][0] as string;
    expect(copied).toContain("WHOLE-RESULT");
    expect(copied).not.toContain("STUB-RESULT");
    expect(copied).not.toContain("SKELETON");
    expect(getContentBlockBodies).toHaveBeenCalledTimes(1);
    expect(getContentBlockBodies.mock.calls[0][0]).toMatchObject({ blockIds: ["call-c1", "res-c1"], full: true });
  });

  it("copies nothing, and says so, when a body cannot be read", async () => {
    // Ids of their own: the client remembers what the server did not find.
    mocks.results = { c1: { ...mocks.results.c1, block_id: "res-gone" } };
    getContentBlockBodies.mockImplementation(async (request: { blockIds: string[] }) => ({
      bodies: [],
      notFound: request.blockIds,
      deferred: [],
    }));
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });

    render(<ChatMessage message={assistant("call-gone")} chatId="chat-1" />);
    fireEvent.click(screen.getByRole("button", { name: "Copy message" }));

    await waitFor(() => expect(mocks.toastError).toHaveBeenCalled());
    expect(writeText).not.toHaveBeenCalled();
  });
});

describe("Export chat over elided bodies", () => {
  it("writes the whole inputs and results, read in one full batch", async () => {
    let exported: Blob | undefined;
    const createObjectURL = vi.fn((blob: Blob) => {
      exported = blob;
      return "blob:export";
    });
    Object.defineProperty(URL, "createObjectURL", { value: createObjectURL, configurable: true });
    Object.defineProperty(URL, "revokeObjectURL", { value: vi.fn(), configurable: true });
    // jsdom cannot follow the download link.
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => {});

    const { result } = renderHook(() => useChatActions("chat-1"));
    await result.current.exportChat();

    expect(mocks.toastError).not.toHaveBeenCalled();
    expect(exported).toBeDefined();
    const markdown = await new Promise<string>((resolve) => {
      const reader = new FileReader();
      reader.onload = () => resolve(reader.result as string);
      reader.readAsText(exported!);
    });
    expect(markdown).toContain("WHOLE-INPUT");
    expect(markdown).toContain("WHOLE-RESULT");
    expect(markdown).not.toContain("SKELETON");
    expect(markdown).not.toContain("STUB-RESULT");
    expect(getContentBlockBodies).toHaveBeenCalledTimes(1);
    expect(getContentBlockBodies.mock.calls[0][0]).toMatchObject({ blockIds: ["call-c1", "res-c1"], full: true });
  });
});
