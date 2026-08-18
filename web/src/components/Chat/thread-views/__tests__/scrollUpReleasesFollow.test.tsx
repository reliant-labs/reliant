/**
 * Scrolling UP with the wheel must release follow mode.
 *
 * While streaming, followState deliberately refuses to read Virtuoso's
 * `isScrolling` as user intent — Virtuoso's own SIZE_INCREASED correction
 * fires the same callback and cannot be announced, so blaming the user for it
 * made follow mode flap once per streamed chunk. The ONLY signal that can
 * release follow mode while streaming is therefore a raw input event, which
 * InterleavedTimeline's `wheel` listener supplies.
 *
 * That makes the listener load-bearing rather than incidental: if it does not
 * receive the event, a user scrolling up during a stream cannot escape follow
 * mode at all, and every subsequent streamed delta yanks the viewport back to
 * the bottom. That yank IS the reported jitter.
 *
 * The listener is attached to the timeline shell, which is also the element
 * Virtuoso's scroller lives inside — so a wheel over the transcript must reach
 * it whether it is dispatched at the shell or at the scrolled child. These
 * tests pin both, because a `wheel` handler on a non-scrolling ancestor is
 * exactly the shape of bug that passes a shell-level test and fails in the app.
 */

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { fireEvent, render } from "@testing-library/react";
import { act } from "react";
import { VirtuosoMockContext } from "react-virtuoso";
import { ContentBlockType, MessageRole, StreamingState } from "../../../../types/chat";
import type { Message } from "../../../../types/chat";
import { InterleavedTimeline } from "../InterleavedTimeline";

// The timeline pulls the live thread-activity store and the settings sync
// layer. Neither participates in scroll follow, and both reach for transports
// that do not exist under jsdom.
vi.mock("../../../../store/threadActivityStore", () => ({
  useActiveThreads: () => [],
}));

// ChatMessage renders the entire message stack (markdown, tool cards, Monaco).
// A fixed-height marker is enough: these tests assert on follow-mode state,
// not on message rendering.
vi.mock("../../ChatMessage", () => ({
  ChatMessage: ({ message }: { message: Message }) => (
    <div data-testid={`msg-${message.id}`} style={{ height: 100 }}>
      {message.id}
    </div>
  ),
}));

function assistantMessage(index: number): Message {
  return {
    id: `m${index}`,
    chatId: "chat-1",
    seq: BigInt(index),
    thread: "chat-1",
    role: index % 2 === 0 ? MessageRole.ASSISTANT : MessageRole.USER,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: [
      {
        id: `m${index}-text`,
        index: 0,
        type: ContentBlockType.TEXT,
        content: `message ${index}`,
      },
    ],
    createdAt: new Date(Date.UTC(2026, 0, 1, 0, 0, index)).toISOString(),
    updatedAt: new Date(Date.UTC(2026, 0, 1, 0, 0, index)).toISOString(),
    sequenceNumber: BigInt(index),
  } as Message;
}

const MESSAGES = Array.from({ length: 30 }, (_, i) => assistantMessage(i + 1));

/**
 * followOutput is the single function that decides whether new content pulls
 * the viewport down. Capturing Virtuoso's prop is how these tests read follow
 * mode without reaching into the component's internals.
 */
let capturedFollowOutput: (() => unknown) | null = null;

vi.mock("react-virtuoso", async () => {
  const React = await import("react");
  return {
    VirtuosoMockContext: React.createContext(undefined),
    Virtuoso: (props: Record<string, unknown>) => {
      capturedFollowOutput = props.followOutput as () => unknown;
      const itemContent = props.itemContent as (i: number, item: unknown) => React.ReactNode;
      const data = props.data as unknown[];
      const firstItemIndex = props.firstItemIndex as number;
      // Virtuoso renders into a child that owns the scrolling. Reproduce that
      // structure — the wheel listener's whole job is to hear events that land
      // on this inner element, not on the shell.
      return (
        <div data-testid="virtuoso-scroller">
          {data.slice(0, 5).map((item, i) => (
            <div key={i}>{itemContent(firstItemIndex + i, item)}</div>
          ))}
        </div>
      );
    },
  };
});

function renderTimeline(isStreaming: boolean) {
  return render(
    <VirtuosoMockContext.Provider value={{ viewportHeight: 600, itemHeight: 100 }}>
      <InterleavedTimeline
        messages={MESSAGES}
        chatId="chat-1"
        isStreaming={isStreaming}
      />
    </VirtuosoMockContext.Provider>,
  );
}

describe("timeline scroll-up releases follow mode", () => {
  beforeEach(() => {
    capturedFollowOutput = null;
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("follows output while the user sits at the bottom during a stream", () => {
    renderTimeline(true);
    expect(capturedFollowOutput).not.toBeNull();
    // At the bottom and no upward intent — new content should pull down.
    expect(capturedFollowOutput!()).not.toBe(false);
  });

  it("releases follow when a wheel-up lands on the SHELL during a stream", () => {
    const { container } = renderTimeline(true);
    const shell = container.querySelector<HTMLElement>('[data-context="transcript"]');
    expect(shell).not.toBeNull();

    act(() => {
      // A decisive scroll up: well past WHEEL_UP_THRESHOLD (12px).
      fireEvent.wheel(shell!, { deltaY: -120 });
    });

    expect(capturedFollowOutput!()).toBe(false);
  });

  it("releases follow when a wheel-up lands on Virtuoso's SCROLLER during a stream", () => {
    // This is the real gesture. The user's pointer is over the transcript, so
    // the wheel event's target is the element that actually scrolls — the
    // child Virtuoso owns — not the shell the listener is bound to. The event
    // must still reach the listener by bubbling.
    const { getByTestId } = renderTimeline(true);
    const scroller = getByTestId("virtuoso-scroller");

    act(() => {
      fireEvent.wheel(scroller, { deltaY: -120 });
    });

    expect(capturedFollowOutput!()).toBe(false);
  });

  it("keeps following when the wheel gesture is downward", () => {
    const { getByTestId } = renderTimeline(true);
    const scroller = getByTestId("virtuoso-scroller");

    act(() => {
      fireEvent.wheel(scroller, { deltaY: 120 });
    });

    expect(capturedFollowOutput!()).not.toBe(false);
  });

});
