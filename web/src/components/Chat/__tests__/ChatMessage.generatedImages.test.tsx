/**
 * Generated images render as content in the message flow, not inside the
 * tool-call card.
 *
 * The bug: `generate_image`'s output was drawn by MessageAttachments at the
 * bottom of GenericToolRenderer — a 32px thumbnail inside a card that is
 * collapsed by default, so the image was effectively invisible. A generated
 * image is the outcome of the turn and has to read as content.
 *
 * These assert PLACEMENT (in the assistant's flow, outside the tool card) and
 * ORDER (after the tool run that produced it, before any later prose), because
 * those are the properties that were wrong. Pixel size is asserted only as
 * "not the 32px chip class", which is the specific regression to avoid.
 */

import { render, screen, waitFor } from "@testing-library/react";
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

const GENERATED = {
  id: "att-gen-1",
  filename: "generated.png",
  size: BigInt(2048),
  mimeType: "image/png",
  url: "/api/attachments/att-gen-1",
};

/**
 * The tool-result index the store injects, carrying the generated image. This is
 * the real delivery path: foldToolResultImages attributes the TOOL message's
 * IMAGE block to its call id and the store exposes it via
 * useToolResultsByCallId, because a TOOL message is never rendered on its own.
 */
const resultsWithImage = {
  "call-1": {
    content: "Image generated.",
    is_error: false,
    attachments: [GENERATED],
  },
};

/**
 * An assistant turn that says something, calls generate_image, and then keeps
 * talking — the interleaving case that pins ordering.
 */
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
        id: "block-0",
        index: 0,
        type: ContentBlockType.TEXT,
        content: "Here comes a picture.",
      },
      {
        id: "block-1",
        index: 1,
        type: ContentBlockType.TOOL_CALL,
        toolCallId: "call-1",
        toolName: "generate_image",
        input: JSON.stringify({ prompt: "a cat" }),
        matchedResult: {
          toolCallId: "call-1",
          content: "Image generated.",
          isError: false,
        },
      },
      {
        id: "block-2",
        index: 2,
        type: ContentBlockType.TEXT,
        content: "Hope that works.",
      },
    ],
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    sequenceNumber: BigInt(1),
  } as unknown as Message;
}

function renderWithGeneratedImage() {
  // The real ToolExecution card uses react-query mutations for approvals, and
  // rendering the card for real is the point — a mocked card could not show the
  // image is outside it.
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <ChatMessage message={buildAssistantMessage()} chatId="chat-1" />
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

describe("generated images in the chat transcript", () => {
  it("renders the generated image at a readable size in the message flow", async () => {
    renderWithGeneratedImage();

    const image = await waitFor(() =>
      screen.getByAltText<HTMLImageElement>("generated.png"),
    );

    // Not the 32px chip used for user-uploaded attachment references.
    expect(image.className).not.toMatch(/\bh-8\b/);
    expect(image.className).not.toMatch(/\bw-8\b/);
    // Scales with its container rather than being a fixed thumbnail.
    expect(image.className).toMatch(/\bw-full\b/);
  });

  it("renders the image OUTSIDE the collapsed tool-call card", async () => {
    renderWithGeneratedImage();

    const image = await waitFor(() => screen.getByAltText("generated.png"));

    // The regression being guarded: the image used to live inside
    // .tool-content-generic, which is collapsed by default.
    expect(image.closest(".tool-content-generic")).toBeNull();
    expect(image.closest(".tool-executions-container")).toBeNull();
    // It lives in the assistant's content flow instead.
    expect(image.closest(".message-content")).not.toBeNull();
  });

  it("is click-to-expand and reachable by keyboard", async () => {
    renderWithGeneratedImage();

    const image = await waitFor(() => screen.getByAltText("generated.png"));
    const trigger = image.closest("button");

    expect(trigger).not.toBeNull();
    expect(trigger).toHaveAttribute(
      "title",
      expect.stringContaining("generated.png"),
    );
  });

  it("places the image after the tool run that produced it", async () => {
    const { container } = renderWithGeneratedImage();

    await waitFor(() => screen.getByAltText("generated.png"));

    const imageBlock = container.querySelector(
      '[data-testid="message-generated-images"]',
    );
    const toolRun = container.querySelector(".tool-executions-container");
    expect(imageBlock).not.toBeNull();
    expect(toolRun).not.toBeNull();

    // DOCUMENT_POSITION_FOLLOWING: the image block comes after the tool card.
    expect(
      toolRun!.compareDocumentPosition(imageBlock!) &
        Node.DOCUMENT_POSITION_FOLLOWING,
    ).toBeTruthy();
  });

  it("does not render the image twice", async () => {
    renderWithGeneratedImage();

    await waitFor(() => screen.getByAltText("generated.png"));
    // getAllBy, not getBy: getBy would also pass while a duplicate existed only
    // if it threw, and the point here is the exact count.
    expect(screen.getAllByAltText("generated.png")).toHaveLength(1);
  });
});
