import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
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

function toolbar(): HTMLElement {
  const el = screen
    .getByRole("button", { name: "Branch from message" })
    .closest(".message-actions");
  if (!(el instanceof HTMLElement)) throw new Error("toolbar not found");
  return el;
}

describe("ChatMessage hover actions", () => {
  it.each([
    ["assistant", MessageRole.ASSISTANT],
    ["user", MessageRole.USER],
  ])("floats the toolbar for an older %s message", (_label, role) => {
    render(<ChatMessage message={buildMessage(role)} chatId="chat-1" />);

    // Floating variant: lifted off the text with an opaque backing, and stuck
    // inside a full-height wrapper so it stays reachable when the top of a long
    // message has scrolled out of view.
    const actions = toolbar();
    expect(actions).toHaveClass("sticky");
    expect(actions.parentElement).toHaveClass("absolute", "inset-y-0");
  });

  it.each([
    ["assistant", MessageRole.ASSISTANT],
    ["user", MessageRole.USER],
  ])("anchors the floating %s toolbar to the content column", (_label, role) => {
    render(<ChatMessage message={buildMessage(role)} chatId="chat-1" />);

    // Right-aligned inside the column rather than hung off the message's own
    // box. Anchoring to the message meant a long one pushed the toolbar past
    // the column edge, where the ancestors that clip overflow (the scroller,
    // the main content pane) cut it off — the toolbar appeared to hide behind
    // whatever panel was squeezing the chat.
    const wrapper = toolbar().parentElement;
    expect(wrapper).toHaveClass("right-2");
    expect(wrapper).not.toHaveClass("left-full");

    // The positioning ancestor must be the full-width column, not the bubble.
    expect(wrapper?.parentElement).toHaveClass("relative", "flex-1", "min-w-0");
  });

  it.each([
    ["assistant", MessageRole.ASSISTANT],
    ["user", MessageRole.USER],
  ])("inlines the toolbar below the latest %s message", (_label, role) => {
    render(
      <ChatMessage message={buildMessage(role)} chatId="chat-1" isLatestMessage />,
    );

    // Inline variant: in the flow under the message, so no backing and nothing
    // to stick to.
    const actions = toolbar();
    expect(actions).not.toHaveClass("sticky");
    expect(actions.parentElement).not.toHaveClass("absolute");
  });

  it.each([
    ["assistant", MessageRole.ASSISTANT],
    ["user", MessageRole.USER],
  ])("keeps the floating %s toolbar box click-through", (_label, role) => {
    render(<ChatMessage message={buildMessage(role)} chatId="chat-1" />);

    // The box spans the timestamp as well as the buttons, so it is far wider
    // than the controls it holds. If the box itself took pointer events, that
    // whole span would swallow clicks aimed at the message underneath — which
    // for an assistant message is a stack of tool rows whose own controls sit
    // flush right, directly beneath this toolbar.
    const actions = toolbar();
    expect(actions).toHaveClass("pointer-events-none");
    expect(actions).not.toHaveClass("group-hover:pointer-events-auto");

    // Only the buttons take the click, and only once the toolbar is actually
    // revealed — at rest it is opacity-0 and must not be a target at all.
    for (const name of ["Copy message", "Branch from message"]) {
      expect(screen.getByRole("button", { name })).toHaveClass(
        "group-hover:pointer-events-auto",
        "group-focus-within:pointer-events-auto",
      );
    }
  });

  it.each([
    ["assistant", MessageRole.ASSISTANT],
    ["user", MessageRole.USER],
  ])("reserves a band above the content for the %s toolbar", (_label, role) => {
    render(<ChatMessage message={buildMessage(role)} chatId="chat-1" />);

    // The separation is vertical. `inset-y-0` measures from the column's padding
    // box, so `top-0` is the top of this band and the message begins below it —
    // the toolbar covers nothing at rest regardless of how wide it is.
    const column = toolbar().parentElement?.parentElement;
    expect(column).toHaveClass("relative", "flex-1", "min-w-0", "pt-7");

    // And it must NOT be bought horizontally. A right-hand gutter narrows every
    // message permanently to hold chrome that is only visible on hover, and it
    // collides with the tool rows' own flush-right controls, which is the bug.
    expect(column?.className).not.toMatch(/\bpr-\d/);
  });

  it("charges no band when no floating toolbar will overlap the message", () => {
    // The latest message uses the inline variant, which sits below the content
    // instead of over it. Reserving the band anyway would push the message down
    // to make room for a toolbar that is not there.
    render(
      <ChatMessage
        message={buildMessage(MessageRole.ASSISTANT)}
        chatId="chat-1"
        isLatestMessage
      />,
    );

    expect(toolbar().parentElement?.parentElement).not.toHaveClass("pt-7");
  });

  it("reserves the band on a compact tool row, which still floats a toolbar", () => {
    // Split-turn tool rows tighten their vertical spacing, but they still get
    // the floating toolbar — and a tool row's own controls (Open, background,
    // cancel, approve/deny) sit flush right, exactly under it. Skipping the
    // band to save space put the toolbar back over those buttons on hover,
    // which is the collision the band exists to prevent.
    render(
      <ChatMessage
        message={buildMessage(MessageRole.ASSISTANT)}
        chatId="chat-1"
        compactToolSpacing
      />,
    );

    expect(toolbar().parentElement?.parentElement).toHaveClass("pt-7");
  });

  it("charges no band on mobile, where the toolbar never reveals", () => {
    // Mobile reaches these actions through a long-press sheet; the hover
    // toolbar is never shown, so the space would be reserved for nothing.
    render(
      <SurfaceProvider surface="mobile">
        <ChatMessage message={buildMessage(MessageRole.ASSISTANT)} chatId="chat-1" />
      </SurfaceProvider>,
    );

    expect(toolbar().parentElement?.parentElement).not.toHaveClass("pt-7");
  });

  it.each([
    ["assistant", MessageRole.ASSISTANT],
    ["user", MessageRole.USER],
  ])("keeps the relative timestamp on the floating %s toolbar", (_label, role) => {
    render(<ChatMessage message={buildMessage(role)} chatId="chat-1" />);

    // The timestamp is the reason the toolbar is wide, but width is no longer
    // what causes the overlap — the band is. It stays visible on hover for every
    // older message; solving the collision by deleting it was the wrong trade.
    const actions = toolbar();
    expect(actions.querySelector("time")).not.toBeNull();
    expect(actions.textContent).toMatch(/ago/);
  });

  it("rests the floating toolbar at the top of the reserved band", () => {
    render(<ChatMessage message={buildMessage(MessageRole.ASSISTANT)} chatId="chat-1" />);

    // No extra offset past the pinned-header allowance: any positive `top` would
    // push the toolbar out of the band and back over the first line of content.
    expect(toolbar().style.top).toBe("var(--chat-pinned-header-h, 0px)");
  });

  it("leaves the inline toolbar fully interactive", () => {
    render(
      <ChatMessage
        message={buildMessage(MessageRole.ASSISTANT)}
        chatId="chat-1"
        isLatestMessage
      />,
    );

    // The inline variant sits in the flow below the message rather than over
    // it, so there is nothing underneath to click through to — and its
    // timestamp keeps the native absolute-time tooltip, which `pointer-events`
    // inheritance would otherwise suppress.
    expect(toolbar()).not.toHaveClass("pointer-events-none");
  });

  it("omits the toolbar entirely from the pinned header breadcrumb", () => {
    render(
      <ChatMessage message={buildMessage(MessageRole.USER)} chatId="chat-1" pinned />,
    );

    expect(
      screen.queryByRole("button", { name: "Branch from message" }),
    ).not.toBeInTheDocument();
  });
});
