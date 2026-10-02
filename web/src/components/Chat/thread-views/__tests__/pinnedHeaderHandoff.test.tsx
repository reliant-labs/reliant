/**
 * The pinned user-message header, driven through the real component.
 *
 * Both reported defects are reproduced here, and both are the same root cause
 * seen twice: the pin used to be a pure function of the virtualized list's
 * first rendered row index (react-virtuoso's `rangeChanged.startIndex`), and a
 * row is not the unit the handoff happens in.
 *
 * (a) WRONG HANDOFF LEVEL. The old rule was
 *       layerUserIdx < firstVisible ? layerUserIdx : null
 *     so the moment the heading user message became the first rendered row it
 *     stopped being pinned — including when it was scrolled 95% off the top,
 *     which is exactly when the header is most needed. The header vanished
 *     instead of handing off.
 *
 * (b) JITTER. `startIndex` is neither monotonic nor a measure of the visual
 *     top: it is the first RENDERED row, inflated by overscan/increaseViewportBy,
 *     and scroll recordings from a real session showed it stepping
 *     115 -> 111 -> 112 -> 105 -> 115 -> 104 between consecutive animation
 *     frames with no user input. Feeding that straight into a visible overlay
 *     toggles the header on and off across frames — the flicker.
 *
 * The fix resolves the pin from measured row geometry instead, so these tests
 * drive geometry rather than indices.
 */

import { describe, expect, it, vi } from "vitest";
import { fireEvent, render } from "@testing-library/react";
import { act } from "react";
import { ContentBlockType, MessageRole, StreamingState } from "../../../../types/chat";
import type { Message } from "../../../../types/chat";
import { InterleavedTimeline } from "../InterleavedTimeline";

// jsdom has no ResizeObserver, and the timeline constructs one to measure the
// pinned header.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver ??= ResizeObserverStub as unknown as typeof ResizeObserver;

vi.mock("../../../../store/threadActivityStore", () => ({
  useActiveThreads: () => [],
}));

// A marker, not a message renderer: these tests assert which message is in the
// header, so the only thing that matters is the id and the `pinned` flag.
vi.mock("../../ChatMessage", () => ({
  ChatMessage: ({ message, pinned }: { message: Message; pinned?: boolean }) => (
    <div data-testid={`msg-${message.id}`} data-pinned={pinned ? "true" : "false"}>
      {message.id}
    </div>
  ),
}));

// jsdom has no layout, so the real virtualizer would measure every row as
// 0px and render none of them. Render every row instead: these tests drive
// the pin from stubbed row GEOMETRY, which is what the pin actually reads, so
// which rows the virtualizer would have chosen is beside the point.
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: (options: { count: number }) => ({
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
  }),
}));

function message(index: number, role: MessageRole): Message {
  return {
    id: `m${index}`,
    chatId: "chat-1",
    seq: BigInt(index),
    thread: "chat-1",
    role,
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

// u0, a1, u2, a3 — two sections, each headed by a user message.
const MESSAGES: Message[] = [
  message(0, MessageRole.USER),
  message(1, MessageRole.ASSISTANT),
  message(2, MessageRole.USER),
  message(3, MessageRole.ASSISTANT),
];

/** Row geometry in the scroller's client space: 0 is the viewport top. */
type Geometry = Record<number, { top: number; height: number }>;

function applyGeometry(container: HTMLElement, geometry: Geometry): void {
  const rows = container.querySelectorAll<HTMLElement>("[data-timeline-index]");
  // The resolver finds rows by this attribute and quietly pins nothing when it
  // is missing, so assert it rather than letting its absence surface as a
  // confusing geometry failure three tests down.
  expect(rows.length).toBeGreaterThan(0);
  rows.forEach((el) => {
    const index = Number(el.getAttribute("data-timeline-index"));
    const g = geometry[index];
    if (!g) return;
    el.getBoundingClientRect = () =>
      ({
        top: g.top,
        bottom: g.top + g.height,
        height: g.height,
        left: 0,
        right: 0,
        width: 0,
        x: 0,
        y: g.top,
        toJSON: () => ({}),
      }) as DOMRect;
  });
}

/** The id of the message currently in the pinned header, or null for no header. */
function pinnedMessageId(container: HTMLElement): string | null {
  const el = container.querySelector<HTMLElement>('[data-pinned="true"]');
  if (!el) return null;
  return el.getAttribute("data-testid")?.replace(/^msg-/, "") ?? null;
}

/**
 * What the real list does when the user scrolls: a `scroll` event on the
 * transcript's scroller. The timeline re-resolves the pin on the next
 * animation frame, so flush that frame before asserting.
 */
async function scroll(container: HTMLElement): Promise<void> {
  const scroller = container.querySelector<HTMLElement>('[data-context="transcript"]');
  expect(scroller).not.toBeNull();
  await act(async () => {
    fireEvent.scroll(scroller as HTMLElement);
    await new Promise((resolve) => requestAnimationFrame(() => resolve(undefined)));
  });
}

function renderTimeline() {
  return render(
    <InterleavedTimeline messages={MESSAGES} chatId="chat-1" isStreaming={false} />,
  );
}

describe("pinned header handoff", () => {
  // Defect (a). The heading user message is the row at the top of the viewport
  // and is 95% scrolled past it — its own section fills the screen, so it is
  // precisely what the header should be showing. The old index rule computed
  // `layerUserIdx < firstVisible` as `2 < 2` and pinned nothing.
  it("pins the heading user message while it is scrolled off the top", async () => {
    const { container } = renderTimeline();

    // u2 is 95% above the viewport top; its section (a3) fills the screen.
    applyGeometry(container, {
      0: { top: -3000, height: 100 },
      1: { top: -2900, height: 2400 },
      2: { top: -420, height: 440 },
      3: { top: 20, height: 3000 },
    });
    await scroll(container);

    expect(pinnedMessageId(container)).toBe("m2");
  });

  // The other side of the same rule: while the heading is still visibly in
  // flow below the viewport top, the section ABOVE it is what the reader is
  // looking at, so the previous heading stays pinned. Handoff happens when the
  // incoming heading's top edge crosses the top, not when its row index does.
  it("keeps the previous heading pinned until the next one crosses the top", async () => {
    const { container } = renderTimeline();

    // u2's top edge is still 60px BELOW the viewport top — not yet its turn.
    applyGeometry(container, {
      0: { top: -3000, height: 100 },
      1: { top: -2900, height: 2960 },
      2: { top: 60, height: 440 },
      3: { top: 500, height: 3000 },
    });
    await scroll(container);
    expect(pinnedMessageId(container)).toBe("m0");

    // Now it has crossed. The header hands off — same rows, different
    // geometry, which is the whole point.
    applyGeometry(container, {
      0: { top: -3120, height: 100 },
      1: { top: -3020, height: 2960 },
      2: { top: -60, height: 440 },
      3: { top: 380, height: 3000 },
    });
    await scroll(container);
    expect(pinnedMessageId(container)).toBe("m2");
  });

  // Defect (b). The pin used to follow the first RENDERED row, which jumps
  // around between frames as overscan shifts. With the geometry unchanged,
  // repeated scroll events must not move the header at all.
  it("does not flicker across repeated scrolls when geometry is unchanged", async () => {
    const { container } = renderTimeline();

    const stable: Geometry = {
      0: { top: -3000, height: 100 },
      1: { top: -2900, height: 2400 },
      2: { top: -420, height: 440 },
      3: { top: 20, height: 3000 },
    };

    const seen: (string | null)[] = [];
    for (let i = 0; i < 7; i++) {
      applyGeometry(container, stable);
      await scroll(container);
      seen.push(pinnedMessageId(container));
    }

    expect(seen).toEqual(["m2", "m2", "m2", "m2", "m2", "m2", "m2"]);
  });

  // The pin is a breadcrumb for content that has scrolled away. At the top of
  // the transcript nothing has, so there is no header.
  it("shows no header at the top of the transcript", async () => {
    const { container } = renderTimeline();

    applyGeometry(container, {
      0: { top: 10, height: 100 },
      1: { top: 110, height: 400 },
      2: { top: 510, height: 100 },
      3: { top: 610, height: 400 },
    });
    await scroll(container);

    expect(pinnedMessageId(container)).toBeNull();
  });
});
