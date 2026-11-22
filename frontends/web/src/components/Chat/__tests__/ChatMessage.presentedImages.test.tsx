/**
 * present_image: the agent shows the user an image file from the machine.
 * Its result's attachments render in the message flow like a generated
 * image's, outside the tool card, with the call's caption under them.
 */
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ChatMessage } from "../ChatMessage";
import {
  ContentBlockType,
  MessageRole,
  StreamingState,
} from "../../../types/chat";
import type { Message } from "../../../types/chat";

const getAttachmentAsBlob = vi.fn(
  async (_id: string) => new Blob(["png-bytes"], { type: "image/png" }),
);

vi.mock("../../../hooks/attachment-queries", () => ({
  fetchAttachmentBlob: (id: string) => getAttachmentAsBlob(id),
}));

// The tool card is rendered for real so the test can prove the image is NOT
// inside it — a mocked-away card could not show that.
vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
  useChatIdOrActive: (chatId?: string | null) => chatId || "chat-1",
  // A getter, not a captured const: vi.mock is hoisted above the GENERATED
  // declaration, so the factory must not read it until render time.
  useToolResultsByCallId: () => resultsWithImage,
  useToolCallStates: () => new Map(),
  useChat: () => ({ worktreeId: "worktree-1" }),
  useChatMessages: () => [],
  useStreamingMessages: () => [],
}));

vi.mock("../../../store/projectStore", () => ({
  useProjectStore: () => ({ id: "project-1" }),
}));

vi.mock("../../../store/chatStore", () => ({
  useChatStore: { getState: () => ({ branchChat: vi.fn() }) },
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
vi.mock("../BranchToWorktreeModal", () => ({
  BranchToWorktreeModal: () => null,
}));
vi.mock("../BranchToExistingWorktreeModal", () => ({
  BranchToExistingWorktreeModal: () => null,
}));
// The action is "open this path in the file viewer"; what the opener does
// with a path is lib/__tests__/openPathInFileViewer.test.ts's.
const openPathInFileViewer = vi.fn((_path: string, _worktreeId?: string) => true);
vi.mock("../../../lib/fileOpener", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../lib/fileOpener")>()),
  openPathInFileViewer: (path: string, worktreeId?: string) => openPathInFileViewer(path, worktreeId),
}));

vi.mock("../CodeContextPill", () => ({ CodeContextPill: () => null }));
// The real tool card reads the chat's workflow executions (for spawn links).
vi.mock("../../../hooks/useWorkflowExecutions", () => ({
  useWorkflowExecutions: () => ({ allWorkflows: [] }),
}));

const PRESENTED = {
  id: "att-shown-1",
  filename: "chart.png",
  size: BigInt(4096),
  mimeType: "image/png",
  url: "/api/attachments/att-shown-1",
  sourcePath: "/tmp/chart.png",
};

const resultsWithImage = {
  "call-1": {
    content: "Showed 1 image to the user in the chat",
    is_error: false,
    attachments: [PRESENTED],
  },
};

function renderPresented() {
  const message = {
    id: "msg-1",
    chatId: "chat-1",
    seq: BigInt(1),
    thread: "chat-1",
    role: MessageRole.ASSISTANT,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: [
      {
        id: "block-1",
        index: 0,
        type: ContentBlockType.TOOL_CALL,
        toolCallId: "call-1",
        toolName: "present_image",
        input: JSON.stringify({ paths: ["/tmp/chart.png"], caption: "p95 latency, last 24h" }),
      },
    ],
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    sequenceNumber: BigInt(1),
  } as unknown as Message;
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <ChatMessage message={message} chatId="chat-1" />
    </QueryClientProvider>,
  );
}

// jsdom implements neither createObjectURL nor revokeObjectURL, and the loader
// turns attachment bytes into a blob URL — without these the img never gets a
// src and every assertion below would fail for the wrong reason.
const originalCreateObjectURL = URL.createObjectURL;
const originalRevokeObjectURL = URL.revokeObjectURL;

beforeAll(() => {
  Object.defineProperty(URL, "createObjectURL", {
    configurable: true,
    writable: true,
    value: vi.fn(() => "blob:generated-image"),
  });
  Object.defineProperty(URL, "revokeObjectURL", {
    configurable: true,
    writable: true,
    value: vi.fn(),
  });
});

afterAll(() => {
  Object.defineProperty(URL, "createObjectURL", {
    configurable: true,
    writable: true,
    value: originalCreateObjectURL,
  });
  Object.defineProperty(URL, "revokeObjectURL", {
    configurable: true,
    writable: true,
    value: originalRevokeObjectURL,
  });
});

describe("presented images in the chat transcript", () => {
  it("renders the presented image outside the tool card, once, with its caption", async () => {
    renderPresented();

    const image = await waitFor(() => screen.getByAltText<HTMLImageElement>("chart.png"));
    expect(image.closest(".tool-executions-container")).toBeNull();
    expect(image.closest(".message-content")).not.toBeNull();
    expect(screen.getAllByAltText("chart.png")).toHaveLength(1);

    const figure = image.closest("figure");
    expect(figure).not.toBeNull();
    expect(figure!.querySelector("figcaption")).toHaveTextContent("p95 latency, last 24h");
    expect(getAttachmentAsBlob).toHaveBeenCalledWith("att-shown-1");
  });

  it("opens the image's source file in the file viewer, against the chat's worktree", async () => {
    openPathInFileViewer.mockClear();
    renderPresented();
    await waitFor(() => screen.getByAltText("chart.png"));

    const action = screen.getByRole("button", { name: /open in file viewer/i });
    expect(action.closest(".tool-executions-container")).toBeNull();
    fireEvent.click(action);
    expect(openPathInFileViewer).toHaveBeenCalledWith("/tmp/chart.png", "worktree-1");
  });

  it("offers the same action in the enlarged preview, closing it first", async () => {
    openPathInFileViewer.mockClear();
    renderPresented();
    const image = await waitFor(() => screen.getByAltText("chart.png"));

    fireEvent.click(image.closest("button")!);
    const dialog = await waitFor(() => screen.getByRole("dialog"));
    const action = Array.from(dialog.querySelectorAll("button")).find((b) =>
      /open in file viewer/i.test(b.textContent ?? ""),
    );
    expect(action).toBeDefined();
    fireEvent.click(action!);

    expect(openPathInFileViewer).toHaveBeenCalledWith("/tmp/chart.png", "worktree-1");
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});
