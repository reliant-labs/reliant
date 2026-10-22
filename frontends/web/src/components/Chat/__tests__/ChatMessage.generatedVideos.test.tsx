/**
 * Generated videos render as a streaming <video> in the message flow, outside
 * the tool card, and never go through the whole-blob gRPC image path. The
 * image/video split in ChatMessage is by MIME type.
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
  async () => new Blob(["png-bytes"], { type: "image/png" }),
);

vi.mock("../../../api/authProvider", () => ({
  getAuthTokenProvider: () => ({
    getToken: async () => "tok-123",
    hasSession: async () => true,
  }),
}));
vi.mock("../../../api/grpc-client", () => ({
  getGRPCBaseURLPublic: () => "http://api.test",
}));

vi.mock("../../../api/attachment-grpc", () => ({
  attachmentGrpc: {
    getAttachmentAsBlob: (id: string) => getAttachmentAsBlob(id),
  },
}));

// The tool card is rendered for real so the test can prove the image is NOT
// inside it — a mocked-away card could not show that.
vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
  // A getter, not a captured const: vi.mock is hoisted above the GENERATED
  // declaration, so the factory must not read it until render time.
  useToolResultsByCallId: () => resultsWithVideo,
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
  api: { toolCalls: { cancel: vi.fn(), convertToBackground: vi.fn() } },
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
vi.mock("../CodeContextPill", () => ({ CodeContextPill: () => null }));

const GENERATED_VIDEO = {
  id: "att-vid-1",
  filename: "generated-1a2b3c4d.mp4",
  size: BigInt(4096),
  mimeType: "video/mp4",
  url: "/api/attachments/att-vid-1",
};
const GENERATED_IMAGE = {
  id: "att-img-1",
  filename: "frame.png",
  size: BigInt(2048),
  mimeType: "image/png",
  url: "/api/attachments/att-img-1",
};

const resultsWithVideo = {
  "call-1": {
    content: "Generated generated-1a2b3c4d.mp4",
    is_error: false,
    attachments: [GENERATED_VIDEO, GENERATED_IMAGE],
  },
};

function buildAssistantMessage(): Message {
  return {
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
        toolName: "generate_video",
        input: JSON.stringify({ prompt: "a red ball" }),
        matchedResult: { toolCallId: "call-1", content: "ok", isError: false },
      },
    ],
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    sequenceNumber: BigInt(1),
  } as unknown as Message;
}

function renderIt() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <ChatMessage message={buildAssistantMessage()} chatId="chat-1" />
    </QueryClientProvider>,
  );
}

beforeAll(() => {
  Object.defineProperty(URL, "createObjectURL", {
    configurable: true,
    writable: true,
    value: vi.fn(() => "blob:img"),
  });
  Object.defineProperty(URL, "revokeObjectURL", {
    configurable: true,
    writable: true,
    value: vi.fn(),
  });
});

describe("generated videos in the chat transcript", () => {
  it("renders a <video> streaming from the Range content endpoint, not a blob", async () => {
    renderIt();
    const video = await waitFor(() => screen.getByTestId("generated-video"));
    expect(video.tagName).toBe("VIDEO");
    expect(video.getAttribute("src")).toBe(
      "http://api.test/api/attachments/att-vid-1/content?token=tok-123",
    );
    expect(video).toHaveAttribute("controls");
    expect(video.getAttribute("preload")).toBe("metadata");
    // Streamed over HTTP: the clip's bytes never go through the gRPC blob loader.
    expect(getAttachmentAsBlob).not.toHaveBeenCalledWith("att-vid-1");
  });

  it("splits by MIME: the image still takes the image path, the video does not", async () => {
    renderIt();
    await waitFor(() => screen.getByTestId("generated-video"));
    await waitFor(() => screen.getByAltText("frame.png"));
    expect(getAttachmentAsBlob).toHaveBeenCalledWith("att-img-1");
    expect(screen.queryByAltText("generated-1a2b3c4d.mp4")).toBeNull();
  });

  it("renders outside the tool card, after it, with a download link", async () => {
    const { container } = renderIt();
    const video = await waitFor(() => screen.getByTestId("generated-video"));
    expect(video.closest(".tool-content-generic")).toBeNull();
    const block = container.querySelector('[data-testid="message-generated-videos"]');
    const toolRun = container.querySelector(".tool-executions-container");
    expect(block).not.toBeNull();
    expect(toolRun).not.toBeNull();
    expect(
      toolRun!.compareDocumentPosition(block!) & Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
    expect(screen.getByLabelText("Download generated-1a2b3c4d.mp4")).toBeInTheDocument();
  });

  it("shows an error state when the clip fails to load", async () => {
    renderIt();
    const video = await waitFor(() => screen.getByTestId("generated-video"));
    fireEvent.error(video);
    await waitFor(() =>
      expect(screen.getByText(/Could not load video/)).toBeInTheDocument(),
    );
  });
});
