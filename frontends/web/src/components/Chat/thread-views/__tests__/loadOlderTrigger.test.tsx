/**
 * Scroll-back paging through the real InterleavedTimeline.
 *
 * Loading older history used to be triggered only from the scroll handler, so
 * every state in which the reader sat inside the load zone WITHOUT a further
 * scroll event stalled: already at scrollTop 0, a page that added little
 * height, a gesture that ended mid-fetch. These tests never dispatch a scroll
 * event after the first render: the timeline has to re-check on its own.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { cleanup, fireEvent, render } from "@testing-library/react";
import { ContentBlockType, MessageRole, StreamingState } from "../../../../types/chat";
import type { Message } from "../../../../types/chat";
import { InterleavedTimeline } from "../InterleavedTimeline";

class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= ResizeObserverStub as unknown as typeof ResizeObserver;

vi.mock("../../../../store/threadActivityStore", () => ({
  useActiveThreads: () => [],
}));

vi.mock("../../ChatMessage", () => ({
  ChatMessage: ({ message }: { message: Message }) => <div data-testid={`msg-${message.id}`} />,
}));

// jsdom has no layout, so the real virtualizer renders nothing. Paging does
// not depend on which rows it picks.
const { virtualizerOptions } = vi.hoisted(() => ({
  virtualizerOptions: {
    current: null as { count: number; getItemKey: (index: number) => string } | null,
  },
}));

vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: (options: { count: number; getItemKey: (index: number) => string }) => {
    virtualizerOptions.current = options;
    return {
    getVirtualItems: () =>
      Array.from({ length: options.count }, (_, index) => ({
        index,
        key: index,
        start: 0,
        end: 0,
        size: 0,
        lane: 0,
      })),
    getTotalSize: () => 0,
    measureElement: () => {},
    containerRef: () => {},
    scrollToIndex: () => {},
    scrollToEnd: () => {},
    };
  },
}));

function message(seq: number): Message {
  return {
    id: `m${seq}`,
    chatId: "chat-1",
    seq: BigInt(seq),
    thread: "chat-1",
    role: seq % 2 === 0 ? MessageRole.USER : MessageRole.ASSISTANT,
    streamingState: StreamingState.COMPLETE,
    contentBlocks: [
      { id: `m${seq}-t`, index: 0, type: ContentBlockType.TEXT, content: `message ${seq}` },
    ],
    createdAt: new Date(Date.UTC(2026, 0, 1, 0, 0, seq)).toISOString(),
    updatedAt: new Date(Date.UTC(2026, 0, 1, 0, 0, seq)).toISOString(),
    sequenceNumber: BigInt(seq),
  } as Message;
}

/** Messages seq `from`..`to`, oldest first. */
function range(from: number, to: number): Message[] {
  return Array.from({ length: to - from + 1 }, (_, i) => message(from + i));
}

type Props = {
  messages: Message[];
  hasOlder: boolean;
  loading: boolean;
  onLoad: () => void;
};

function timeline({ messages, hasOlder, loading, onLoad }: Props) {
  return (
    <InterleavedTimeline
      messages={messages}
      chatId="chat-1"
      isStreaming={false}
      onLoadOlderMessages={onLoad}
      hasOlderMessages={hasOlder}
      isLoadingOlderMessages={loading}
    />
  );
}

/** Let the re-check effect's animation frame run. */
async function frame(): Promise<void> {
  await act(async () => {
    await new Promise((resolve) => requestAnimationFrame(() => resolve(undefined)));
  });
}

function scroller(container: HTMLElement): HTMLElement {
  const el = container.querySelector<HTMLElement>('[data-context="transcript"]');
  expect(el).not.toBeNull();
  return el as HTMLElement;
}

beforeEach(() => {
  // The scroller sits at scrollTop 0 (jsdom's default) throughout.
});
afterEach(cleanup);

describe("scroll-back paging trigger", () => {
  it("loads on arrival when already at the top, with no scroll event", async () => {
    const onLoad = vi.fn();
    render(timeline({ messages: range(100, 110), hasOlder: true, loading: false, onLoad }));
    await frame();
    expect(onLoad).toHaveBeenCalledTimes(1);
  });

  it("loads again after a short page leaves the reader inside the zone", async () => {
    const onLoad = vi.fn();
    const first = range(100, 110);
    const view = render(timeline({ messages: first, hasOlder: true, loading: false, onLoad }));
    await frame();
    expect(onLoad).toHaveBeenCalledTimes(1);

    // The fetch starts, then lands a page that adds rows but not enough
    // height to move scrollTop out of the zone. No scroll event follows.
    view.rerender(timeline({ messages: first, hasOlder: true, loading: true, onLoad }));
    await frame();
    view.rerender(
      timeline({ messages: [...range(95, 99), ...first], hasOlder: true, loading: false, onLoad }),
    );
    await frame();

    expect(onLoad).toHaveBeenCalledTimes(2);
  });

  it("picks up a load that was swallowed while one was already in flight", async () => {
    const onLoad = vi.fn();
    const msgs = range(100, 110);
    // The reader arrives (e.g. a gesture ends) while a fetch is running: the
    // check is skipped. When that fetch lands, nobody scrolls.
    const view = render(timeline({ messages: msgs, hasOlder: true, loading: true, onLoad }));
    await frame();
    expect(onLoad).not.toHaveBeenCalled();

    view.rerender(
      timeline({ messages: [...range(90, 99), ...msgs], hasOlder: true, loading: false, onLoad }),
    );
    await frame();
    expect(onLoad).toHaveBeenCalledTimes(1);
  });

  it("loads once history is known to exist, even though hasMore arrived late", async () => {
    const onLoad = vi.fn();
    const msgs = range(100, 110);
    const view = render(timeline({ messages: msgs, hasOlder: false, loading: false, onLoad }));
    await frame();
    expect(onLoad).not.toHaveBeenCalled();

    view.rerender(timeline({ messages: msgs, hasOlder: true, loading: false, onLoad }));
    await frame();
    expect(onLoad).toHaveBeenCalledTimes(1);
  });

  it("does not spin on a failed fetch, and retries on the reader's next input", async () => {
    const onLoad = vi.fn();
    const msgs = range(100, 110);
    const view = render(timeline({ messages: msgs, hasOlder: true, loading: false, onLoad }));
    await frame();
    expect(onLoad).toHaveBeenCalledTimes(1);

    // The fetch fails: loading flips on and back off, the list is unchanged.
    view.rerender(timeline({ messages: msgs, hasOlder: true, loading: true, onLoad }));
    await frame();
    view.rerender(timeline({ messages: msgs, hasOlder: true, loading: false, onLoad }));
    await frame();
    expect(onLoad).toHaveBeenCalledTimes(1);

    // Pulling up at the very top is the reader asking again.
    await act(async () => {
      fireEvent.wheel(scroller(view.container), { deltaY: -40 });
    });
    expect(onLoad).toHaveBeenCalledTimes(2);
  });

  it("loads on upward wheel input at scrollTop 0, which fires no scroll event", async () => {
    const onLoad = vi.fn();
    const msgs = range(100, 110);
    // Hold the arrival check off by starting mid-fetch, then finish it with
    // nothing changed, so only the wheel can be what triggers the call.
    const view = render(timeline({ messages: msgs, hasOlder: true, loading: true, onLoad }));
    await frame();
    view.rerender(timeline({ messages: msgs, hasOlder: true, loading: false, onLoad }));
    await frame();
    expect(onLoad).not.toHaveBeenCalled();

    await act(async () => {
      fireEvent.wheel(scroller(view.container), { deltaY: -40 });
    });
    expect(onLoad).toHaveBeenCalledTimes(1);

    onLoad.mockClear();
    await act(async () => {
      fireEvent.wheel(scroller(view.container), { deltaY: 40 });
    });
    expect(onLoad).not.toHaveBeenCalled();
  });

  it("does not load when the reader is far from the top", async () => {
    const onLoad = vi.fn();
    const view = render(
      timeline({ messages: range(100, 110), hasOlder: true, loading: true, onLoad }),
    );
    const el = scroller(view.container);
    Object.defineProperty(el, "scrollTop", { configurable: true, value: 5000 });
    view.rerender(timeline({ messages: range(100, 110), hasOlder: true, loading: false, onLoad }));
    await frame();
    await act(async () => {
      fireEvent.scroll(el);
      await new Promise((resolve) => requestAnimationFrame(() => resolve(undefined)));
    });
    expect(onLoad).not.toHaveBeenCalled();
  });
});

describe("prepend anchoring", () => {
  // The virtualizer holds the reader still across a prepend by re-finding the
  // row under the viewport by KEY. A key that changes when rows are added
  // above it (a position-based one) loses the anchor and the view jumps.
  it("keeps every row's key when an older page is prepended", () => {
    const withWorkflow = (m: Message, workflowId: string) => ({ ...m, workflowId }) as Message;
    // The handoff row carries no id of its own: w1 -> w2 happens at m104.
    const newer = range(100, 108).map((m, i) => withWorkflow(m, i < 4 ? "w1" : "w2"));
    const older = range(90, 99).map((m) => withWorkflow(m, "w1"));
    const noop = vi.fn();

    const view = render(timeline({ messages: newer, hasOlder: true, loading: false, onLoad: noop }));
    const before = virtualizerOptions.current!;
    const keysBefore = Array.from({ length: before.count }, (_, i) => before.getItemKey(i));
    expect(keysBefore.some((k) => k.startsWith("handoff-"))).toBe(true);

    view.rerender(
      timeline({ messages: [...older, ...newer], hasOlder: true, loading: false, onLoad: noop }),
    );
    const after = virtualizerOptions.current!;
    const keysAfter = new Set(Array.from({ length: after.count }, (_, i) => after.getItemKey(i)));

    for (const key of keysBefore) expect(keysAfter.has(key)).toBe(true);
  });
});

describe("scroll-back affordances", () => {
  it("shows the loading indicator in the viewport, outside the scroll content", () => {
    const view = render(
      timeline({ messages: range(100, 110), hasOlder: true, loading: true, onLoad: vi.fn() }),
    );
    const indicator = view.getByTestId("older-messages-loading");
    expect(indicator.textContent).toContain("Loading earlier messages");
    // Scroll content scrolls away with the reader; the overlay must not.
    expect(scroller(view.container).contains(indicator)).toBe(false);
  });

  it("sits above the pinned user-message header", () => {
    const view = render(
      timeline({ messages: range(100, 110), hasOlder: true, loading: true, onLoad: vi.fn() }),
    );
    const indicator = view.getByTestId("older-messages-loading").parentElement as HTMLElement;
    // The pinned header is z-50.
    expect(indicator.className).toContain("z-[60]");
  });

  it("hides the indicator when nothing is loading", () => {
    const view = render(
      timeline({ messages: range(100, 110), hasOlder: true, loading: false, onLoad: vi.fn() }),
    );
    expect(view.queryByTestId("older-messages-loading")).toBeNull();
  });

  it("offers a Load earlier messages button when history exists and nothing is loading", async () => {
    const onLoad = vi.fn();
    const msgs = range(100, 110);
    // Start mid-fetch so the arrival check does not consume the call.
    const view = render(timeline({ messages: msgs, hasOlder: true, loading: true, onLoad }));
    expect(view.queryByText("Load earlier messages")).toBeNull();
    view.rerender(timeline({ messages: msgs, hasOlder: true, loading: false, onLoad }));
    await frame();
    onLoad.mockClear();

    fireEvent.click(view.getByText("Load earlier messages"));
    expect(onLoad).toHaveBeenCalledTimes(1);
  });

  it("marks the start of the conversation once there is no more history", () => {
    const view = render(
      timeline({ messages: range(1, 10), hasOlder: false, loading: false, onLoad: vi.fn() }),
    );
    expect(view.getByTestId("start-of-conversation")).toBeTruthy();
    expect(view.queryByText("Load earlier messages")).toBeNull();
  });

  it("shows neither marker nor button while loading", () => {
    const view = render(
      timeline({ messages: range(1, 10), hasOlder: false, loading: true, onLoad: vi.fn() }),
    );
    expect(view.queryByTestId("start-of-conversation")).toBeNull();
  });
});
