/**
 * The pinned user-message header, driven through the real component.
 *
 * The pin is a breadcrumb: it names the most recent user message that has
 * slid entirely under the header. These tests drive row GEOMETRY rather than
 * indices, because every defect this header has had came from deciding on
 * something other than measured edges:
 *
 * (a) WRONG HANDOFF LEVEL. The pin was once a function of the first rendered
 *     row index, so it dropped the moment the heading became that row —
 *     including when it was 95% scrolled off the top.
 *
 * (b) JITTER. That first rendered row index is not monotonic (overscan), so
 *     the header toggled on and off between frames with no input.
 *
 * (c) EARLY SWAP. The swap fired when the incoming user message's TOP edge
 *     reached the header. A user row opens with an empty toolbar band, so at
 *     that moment the whole bubble was still on screen just below the header,
 *     which already showed it — the same message twice.
 *
 * (d) STUCK AT THE TOP. The first row starts 32px down (the scroll-back
 *     loader's space), inside the ~48px header band, so a rule keyed on the
 *     row's top edge could never release it: scrolled all the way up, the
 *     header sat over the very message it named.
 */

import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
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

const { virtualizerOptions } = vi.hoisted(() => ({
  virtualizerOptions: { current: null as Record<string, unknown> | null },
}));

// jsdom has no layout, so the real virtualizer would measure every row as
// 0px and render none of them. Render every row instead: these tests drive
// the pin from stubbed row GEOMETRY, which is what the pin actually reads, so
// which rows the virtualizer would have chosen is beside the point.
vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: (options: { count: number }) => {
    virtualizerOptions.current = options as unknown as Record<string, unknown>;
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

/** The pinned header's rendered height — one line of text plus its padding. */
const HEADER_PX = 48;

// jsdom lays nothing out, so every offsetHeight is 0. Give the header its real
// height: the crossing line is the header's bottom edge, and at 0px the
// stuck-at-the-top defect cannot be reproduced at all.
const offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
beforeAll(() => {
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return this.getAttribute("data-testid") === "pinned-user-message-header" ? HEADER_PX : 0;
    },
  });
});
afterAll(() => {
  if (offsetHeightDescriptor) {
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
  }
});

beforeEach(() => {
  virtualizerOptions.current = null;
});

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
  // Defect (a). u2 is 95% above the viewport top and its section (a3) fills
  // the screen, so it is precisely what the header should be showing.
  it("pins the heading user message once it has slid under the header", async () => {
    const { container } = renderTimeline();

    applyGeometry(container, {
      0: { top: -3000, height: 100 },
      1: { top: -2900, height: 2480 },
      2: { top: -420, height: 440 },
      3: { top: 20, height: 3000 },
    });
    await scroll(container);

    expect(pinnedMessageId(container)).toBe("m2");
  });

  // Defect (c). The header changes hands when the incoming user message has
  // gone ENTIRELY under it — never while it is still on screen below it.
  it("swaps only once the incoming user message is fully under the header", async () => {
    const { container } = renderTimeline();

    // u2 is still below the header: the reader is in u0's section.
    applyGeometry(container, {
      0: { top: -3000, height: 100 },
      1: { top: -2900, height: 2960 },
      2: { top: 60, height: 440 },
      3: { top: 500, height: 3000 },
    });
    await scroll(container);
    expect(pinnedMessageId(container)).toBe("m0");

    // u2's top edge has crossed into the header but most of it is still on
    // screen. Swapping here would print u2 twice, one above the other.
    applyGeometry(container, {
      0: { top: -3120, height: 100 },
      1: { top: -3020, height: 2960 },
      2: { top: -60, height: 440 },
      3: { top: 380, height: 3000 },
    });
    await scroll(container);
    expect(pinnedMessageId(container)).toBe("m0");

    // Now u2's bottom edge is behind the header: it has gone, so it takes over.
    applyGeometry(container, {
      0: { top: -3460, height: 100 },
      1: { top: -3360, height: 2960 },
      2: { top: -400, height: 440 },
      3: { top: 40, height: 3000 },
    });
    await scroll(container);
    expect(pinnedMessageId(container)).toBe("m2");
  });

  // Defect (b). With the geometry unchanged, repeated scroll events must not
  // move the header at all.
  it("does not flicker across repeated scrolls when geometry is unchanged", async () => {
    const { container } = renderTimeline();

    const stable: Geometry = {
      0: { top: -3000, height: 100 },
      1: { top: -2900, height: 2480 },
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
      0: { top: 32, height: 66 },
      1: { top: 98, height: 400 },
      2: { top: 498, height: 66 },
      3: { top: 564, height: 400 },
    });
    await scroll(container);

    expect(pinnedMessageId(container)).toBeNull();
  });

  // Defect (d). Pinned first, then scrolled all the way back up: the header
  // must give way to the first message rather than cover it.
  it("clears the header when scrolled back to the top of the transcript", async () => {
    const { container } = renderTimeline();

    applyGeometry(container, {
      0: { top: -200, height: 66 },
      1: { top: -134, height: 3000 },
      2: { top: 2866, height: 66 },
      3: { top: 2932, height: 400 },
    });
    await scroll(container);
    expect(pinnedMessageId(container)).toBe("m0");

    applyGeometry(container, {
      0: { top: 32, height: 66 },
      1: { top: 98, height: 3000 },
      2: { top: 3098, height: 66 },
      3: { top: 3164, height: 400 },
    });
    await scroll(container);
    expect(pinnedMessageId(container)).toBeNull();
  });

  // "Jump to" aligns the message to the top of the viewport. Without clearance
  // it lands underneath the header, half hidden by the very thing that was
  // clicked to reveal it.
  it("reserves the header's height when jumping to a message", async () => {
    const { container } = renderTimeline();

    applyGeometry(container, {
      0: { top: -3000, height: 100 },
      1: { top: -2900, height: 2480 },
      2: { top: -420, height: 440 },
      3: { top: 20, height: 3000 },
    });
    await scroll(container);
    expect(pinnedMessageId(container)).toBe("m2");

    expect(virtualizerOptions.current?.scrollPaddingStart).toBe(HEADER_PX);
  });
});
