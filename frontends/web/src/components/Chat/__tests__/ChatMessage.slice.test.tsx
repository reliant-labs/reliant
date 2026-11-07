/**
 * `slice` cuts an assistant message into the rows of the simplified thread:
 * the answer, the folded work, and what the fold must keep visible.
 */

import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ChatMessage } from "../ChatMessage";
import { ApprovalSchema, ApprovalStatus } from "../../../gen/reliant/v1/approval_pb";
import { AttachmentSchema } from "../../../gen/reliant/v1/chat_pb";
import type { Approval } from "../../../gen/reliant/v1/approval_pb";
import { ContentBlockType, MessageRole, StreamingState } from "../../../types/chat";
import type { Message } from "../../../types/chat";
import type { MessageSlice } from "../thread-views/simplifiedTurns";

vi.mock("../ToolExecution", () => ({
  ToolExecution: ({
    toolCall,
    approval,
  }: {
    toolCall: { name: string };
    approval?: { status: number };
  }) => (
    <div data-testid="tool-card" data-approval={approval?.status}>
      tool:{toolCall.name}
    </div>
  ),
}));
vi.mock("../ToolExecutionGroup", () => ({
  ToolExecutionGroup: () => <div data-testid="tool-group">group</div>,
}));
vi.mock("../ToolExecutionCollapsibleGroup", () => ({
  ToolExecutionCollapsibleGroup: () => <div data-testid="tool-collapsible-group">collapsible</div>,
}));
vi.mock("../MarkdownRenderer", () => ({
  MarkdownRenderer: ({ content }: { content: string }) => <div data-testid="text-run">{content}</div>,
}));
vi.mock("../AskUserCard", () => ({
  AskUserCard: () => <div data-testid="ask-user-card">ask</div>,
}));
vi.mock("../MessageGeneratedImages", () => ({
  MessageGeneratedImages: () => <div data-testid="generated-images">images</div>,
}));
vi.mock("../../Apps/DeployedAppCard", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../Apps/DeployedAppCard")>()),
  DeployedAppCard: ({ deploy }: { deploy: { env: string } }) => (
    <div data-testid="deployed-app-card" data-env={deploy.env}>
      deployed:{deploy.env}
    </div>
  ),
}));
vi.mock("../MessageGeneratedVideos", () => ({
  MessageGeneratedVideos: () => <div data-testid="generated-videos">videos</div>,
  isVideoMimeType: (mime: string) => mime.startsWith("video/"),
}));

vi.mock("../../../store/chatStoreHooks", () => ({
  useActiveChatId: () => "chat-1",
  useToolResultsByCallId: () => ({
    "call-deploy": { content: JSON.stringify({ exit_code: 0, output: "released" }), is_error: false },
    "call-img": {
      content: "Image generated.",
      is_error: false,
      attachments: [
        { id: "att-1", filename: "a.png", size: BigInt(1), mimeType: "image/png", url: "/a" },
      ],
    },
  }),
  useToolCallStates: () => new Map(),
  useChat: () => ({ worktreeId: "worktree-1" }),
  useChatMessages: () => [],
  useStreamingMessages: () => [],
}));
vi.mock("../../../store/projectStore", () => ({ useProjectStore: () => ({ id: "project-1" }) }));
vi.mock("../../../store/chatStore", () => ({
  useChatStore: { getState: () => ({ branchChat: vi.fn() }) },
}));
vi.mock("../../../api/client", () => ({ api: {} }));
vi.mock("../../../lib/logger", () => ({
  logger: { debug: vi.fn(), error: vi.fn(), info: vi.fn(), warn: vi.fn() },
}));
vi.mock("../../../lib/toast-manager", () => ({ toast: { error: vi.fn() } }));
vi.mock("../../../lib/tabSwitchProfiler", () => ({ tabSwitchProfiler: { isEnabled: () => false } }));
vi.mock("../BranchOptionsMenu", () => ({ BranchOptionsMenu: () => null }));
vi.mock("../BranchToWorktreeModal", () => ({ BranchToWorktreeModal: () => null }));
vi.mock("../BranchToExistingWorktreeModal", () => ({ BranchToExistingWorktreeModal: () => null }));
vi.mock("../CodeContextPill", () => ({ CodeContextPill: () => null }));

const writeText = vi.fn();

beforeEach(() => {
  writeText.mockReset();
  writeText.mockResolvedValue(undefined);
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
});

type Block = Record<string, unknown>;
const text = (i: number, content: string): Block => ({
  id: `b${i}`,
  index: i,
  type: ContentBlockType.TEXT,
  content,
});
const tool = (i: number, name: string, callId: string): Block => ({
  id: `b${i}`,
  index: i,
  type: ContentBlockType.TOOL_CALL,
  toolName: name,
  toolCallId: callId,
  input: "{}",
});

function assistant(contentBlocks: Block[], extra: Partial<Message> = {}): Message {
  return {
    id: "msg-1",
    chatId: "chat-1",
    seq: BigInt(1),
    thread: "chat-1",
    role: MessageRole.ASSISTANT,
    streamingState: StreamingState.COMPLETE,
    contentBlocks,
    createdAt: "2024-01-01T00:00:00.000Z",
    updatedAt: "2024-01-01T00:00:00.000Z",
    sequenceNumber: BigInt(1),
    ...extra,
  } as unknown as Message;
}

// segments: 0 text, 1 tools(bash), 2 text, 3 tools(ask_user), 4 text
const turn = () =>
  assistant([
    text(0, "Looking into it."),
    tool(1, "bash", "call-bash"),
    text(2, "Found the cause."),
    tool(3, "ask_user", "call-ask"),
    text(4, "All done."),
  ]);

const slice = (role: MessageSlice["role"], segmentIndexes: number[]): MessageSlice => ({
  role,
  segmentIndexes,
});

const pendingApproval = (blockId: string): Approval =>
  create(ApprovalSchema, {
    id: "approval-1",
    chatId: "chat-1",
    entityId: blockId,
    title: "Run it?",
    status: ApprovalStatus.PENDING,
    createdAt: new Date().toISOString(),
  });

const texts = () => screen.queryAllByTestId("text-run").map((el) => el.textContent);

describe("ChatMessage slice", () => {
  it("without a slice renders the whole message", () => {
    render(<ChatMessage message={turn()} chatId="chat-1" />);

    expect(texts()).toEqual(["Looking into it.", "Found the cause.", "All done."]);
    expect(screen.getAllByTestId("tool-card")).toHaveLength(1);
    expect(screen.getByTestId("ask-user-card")).toBeInTheDocument();
    expect(screen.getByTestId("message-msg-1")).toBeInTheDocument();
  });

  it("answer renders only the listed text, with the message chrome and no tool rows or cards", () => {
    render(<ChatMessage message={turn()} chatId="chat-1" slice={slice("answer", [4])} />);

    expect(texts()).toEqual(["All done."]);
    expect(screen.queryByTestId("tool-card")).toBeNull();
    expect(screen.queryByTestId("ask-user-card")).toBeNull();
    expect(screen.getByTestId("message-msg-1")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /copy message/i })).toBeInTheDocument();
  });

  it("answer copies the answer text only", async () => {
    render(<ChatMessage message={turn()} chatId="chat-1" slice={slice("answer", [4])} />);

    fireEvent.click(screen.getByRole("button", { name: /copy message/i }));

    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(1));
    expect(writeText).toHaveBeenCalledWith("All done.");
  });

  it("answer keeps attachments", () => {
    const message = assistant([text(0, "Here."), tool(1, "bash", "call-bash"), text(2, "File attached.")], {
      attachments: [
        create(AttachmentSchema, { id: "att-9", filename: "report.pdf", size: BigInt(10), mimeType: "application/pdf", url: "/r" }),
      ],
    });
    render(<ChatMessage message={message} chatId="chat-1" slice={slice("answer", [2])} />);

    expect(screen.getByText("report.pdf")).toBeInTheDocument();
  });

  it("an answer with no segments still renders the message's attachments, and nothing else", () => {
    const message = assistant([text(0, "Working."), tool(1, "bash", "call-bash")], {
      attachments: [
        create(AttachmentSchema, { id: "att-9", filename: "report.pdf", size: BigInt(10), mimeType: "application/pdf", url: "/r" }),
      ],
    });
    render(<ChatMessage message={message} chatId="chat-1" slice={slice("answer", [])} />);

    expect(screen.getByText("report.pdf")).toBeInTheDocument();
    expect(texts()).toEqual([]);
    expect(screen.queryByTestId("tool-card")).toBeNull();
  });

  it("an answer with no segments and no attachments renders nothing", () => {
    const message = assistant([text(0, "Working."), tool(1, "bash", "call-bash")]);
    const { container } = render(<ChatMessage message={message} chatId="chat-1" slice={slice("answer", [])} />);

    expect(container).toBeEmptyDOMElement();
  });

  it("work renders narration and tool rows in order, without chrome, cards or media", () => {
    render(<ChatMessage message={turn()} chatId="chat-1" slice={slice("work", [0, 1, 2, 3])} />);

    const order = Array.from(
      document.querySelectorAll('[data-testid="text-run"], [data-testid="tool-card"]'),
    ).map((el) => el.textContent);
    expect(order).toEqual(["Looking into it.", "tool:bash", "Found the cause."]);
    expect(screen.queryByTestId("ask-user-card")).toBeNull();
    expect(screen.queryByRole("button", { name: /copy message/i })).toBeNull();
    expect(document.querySelector("time")).toBeNull();
    expect(screen.queryByTestId("message-msg-1")).toBeNull();
  });

  it("work does not render generated images or videos", () => {
    const message = assistant([text(0, "Drawing."), tool(1, "generate_image", "call-img")]);
    render(<ChatMessage message={message} chatId="chat-1" slice={slice("work", [0, 1])} />);

    expect(screen.getByTestId("tool-card")).toBeInTheDocument();
    expect(screen.queryByTestId("generated-images")).toBeNull();
  });

  it("work-surfaced renders only user cards, not narration or tool rows", () => {
    render(<ChatMessage message={turn()} chatId="chat-1" slice={slice("work-surfaced", [0, 1, 2, 3])} />);

    expect(screen.getByTestId("ask-user-card")).toBeInTheDocument();
    expect(texts()).toEqual([]);
    expect(screen.queryByTestId("tool-card")).toBeNull();
    expect(screen.queryByRole("button", { name: /copy message/i })).toBeNull();
  });

  it("work-surfaced surfaces a tool call awaiting approval", () => {
    const message = assistant([text(0, "Running it."), tool(1, "bash", "call-bash")]);
    render(
      <ChatMessage
        message={message}
        chatId="chat-1"
        approvals={[pendingApproval("b1")]}
        slice={slice("work-surfaced", [0, 1])}
      />,
    );

    const card = screen.getByTestId("tool-card");
    expect(card).toHaveTextContent("tool:bash");
    expect(card).toHaveAttribute("data-approval", String(ApprovalStatus.PENDING));
    expect(texts()).toEqual([]);
  });

  it("work-surfaced hides a tool call whose approval is settled", () => {
    const approved = create(ApprovalSchema, {
      id: "approval-1",
      chatId: "chat-1",
      entityId: "b1",
      status: ApprovalStatus.APPROVED,
      createdAt: new Date().toISOString(),
    });
    const message = assistant([tool(0, "bash", "call-bash")]);
    const { container } = render(
      <ChatMessage message={message} chatId="chat-1" approvals={[approved]} slice={slice("work-surfaced", [0])} />,
    );

    expect(container).toBeEmptyDOMElement();
  });

  it("work-surfaced surfaces generated images", () => {
    const message = assistant([text(0, "Drawing."), tool(1, "generate_image", "call-img")]);
    render(<ChatMessage message={message} chatId="chat-1" slice={slice("work-surfaced", [0, 1])} />);

    expect(screen.getByTestId("generated-images")).toBeInTheDocument();
    expect(screen.queryByTestId("tool-card")).toBeNull();
  });

  it("work-surfaced surfaces what a deploy put live", () => {
    const deploy = { ...tool(1, "shell", "call-deploy"), input: JSON.stringify({ command: "reliant forge env deploy prod" }) };
    const message = assistant([text(0, "Shipping it."), deploy]);
    render(<ChatMessage message={message} chatId="chat-1" slice={slice("work-surfaced", [0, 1])} />);

    expect(screen.getByTestId("deployed-app-card")).toHaveAttribute("data-env", "prod");
    expect(screen.queryByTestId("tool-card")).toBeNull();
    expect(texts()).toEqual([]);
  });

  it("work-surfaced renders nothing when there is nothing to surface", () => {
    const message = assistant([text(0, "Reading."), tool(1, "bash", "call-bash")]);
    const { container } = render(
      <ChatMessage message={message} chatId="chat-1" slice={slice("work-surfaced", [0, 1])} />,
    );

    expect(container).toBeEmptyDOMElement();
  });

  it("only renders the segments the slice lists", () => {
    render(<ChatMessage message={turn()} chatId="chat-1" slice={slice("work", [2])} />);

    expect(texts()).toEqual(["Found the cause."]);
    expect(screen.queryByTestId("tool-card")).toBeNull();
  });

  it("re-renders when the slice's indexes change even if the array identity is new", () => {
    const message = turn();
    const { rerender } = render(<ChatMessage message={message} chatId="chat-1" slice={slice("answer", [4])} />);
    expect(texts()).toEqual(["All done."]);

    rerender(<ChatMessage message={message} chatId="chat-1" slice={slice("answer", [4])} />);
    expect(texts()).toEqual(["All done."]);

    rerender(<ChatMessage message={message} chatId="chat-1" slice={slice("answer", [2, 4])} />);
    expect(texts()).toEqual(["Found the cause.", "All done."]);
  });
});
