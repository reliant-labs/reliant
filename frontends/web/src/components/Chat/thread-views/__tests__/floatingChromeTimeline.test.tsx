/**
 * The transcript under the mobile chat screen's floating chrome (§4.3): the
 * bars' heights become the virtualizer's padding (so the first row starts
 * below the top bar and the last ends above the composer), the pinned strip
 * hangs from the bar, and the scroll edge is reported to the frame as the
 * reader scrolls.
 */

import { afterAll, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render } from "@testing-library/react";
import { ContentBlockType, MessageRole, StreamingState } from "../../../../types/chat";
import type { Message } from "../../../../types/chat";
import { InterleavedTimeline } from "../InterleavedTimeline";
import { ComposeModeProvider } from "../../../../lib/composeMode";
import { FloatingChromeFrame, useFloatingChrome } from "../../floatingChrome";

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

const { virtualizerOptions, scrollToIndex } = vi.hoisted(() => ({
  virtualizerOptions: { current: null as Record<string, unknown> | null },
  scrollToIndex: vi.fn(),
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
      scrollToIndex,
      scrollToEnd: () => {},
    };
  },
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

// u0, a1, u2, a3 — two sections, each headed by a user message. u2 is a
// multi-line prompt that opens with a blank line.
const MESSAGES: Message[] = [
  message(0, MessageRole.USER),
  message(1, MessageRole.ASSISTANT),
  {
    ...message(2, MessageRole.USER),
    contentBlocks: [
      { id: "m2-text", index: 0, type: ContentBlockType.TEXT, content: "\n  Refactor the retry loop\nso that backoff is jittered" },
    ],
  } as Message,
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


const TOP_BAR_PX = 48;
const BAND_PX = 120;
const STRIP_PX = 37;

let paneWidth = 390;
const offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
const clientWidthDescriptor = Object.getOwnPropertyDescriptor(Element.prototype, "clientWidth");
const originalRect = HTMLElement.prototype.getBoundingClientRect;
beforeAll(() => {
  Object.defineProperty(Element.prototype, "clientWidth", { configurable: true, get: () => paneWidth });
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return this.getAttribute("data-testid") === "pinned-user-message-header" ? STRIP_PX : 0;
    },
  });
  // The bars' measured heights; rows get theirs from applyGeometry.
  HTMLElement.prototype.getBoundingClientRect = function (this: HTMLElement) {
    const id = this.getAttribute("data-testid");
    const height = id === "top-bar" ? TOP_BAR_PX : id === "band" ? BAND_PX : 0;
    return { top: 0, bottom: height, height, left: 0, right: 0, width: 0, x: 0, y: 0, toJSON: () => ({}) } as DOMRect;
  };
});
afterAll(() => {
  if (clientWidthDescriptor) Object.defineProperty(Element.prototype, "clientWidth", clientWidthDescriptor);
  if (offsetHeightDescriptor) Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
  HTMLElement.prototype.getBoundingClientRect = originalRect;
});
beforeEach(() => {
  virtualizerOptions.current = null;
  scrollToIndex.mockClear();
  paneWidth = 390;
  vi.stubGlobal("innerHeight", 844);
});

function Bars() {
  const chrome = useFloatingChrome();
  return (
    <>
      <header ref={chrome?.topBarRef} data-testid="top-bar" />
      <div ref={chrome?.bottomBandRef} data-testid="band" />
    </>
  );
}

function renderFloating() {
  return render(
    <ComposeModeProvider keyboardOpen={false}>
      <FloatingChromeFrame>
        <Bars />
        <InterleavedTimeline messages={MESSAGES} chatId="chat-1" isStreaming={false} />
      </FloatingChromeFrame>
    </ComposeModeProvider>,
  );
}

function frame(container: HTMLElement): HTMLElement {
  return container.querySelector<HTMLElement>("[data-floating-chrome]") as HTMLElement;
}

function scroller(container: HTMLElement): HTMLElement {
  return container.querySelector<HTMLElement>('[data-context="transcript"]') as HTMLElement;
}

/** Put the scroller at `scrollTop` in a `scrollHeight`-tall transcript, and scroll. */
async function scrollTo(container: HTMLElement, { scrollTop, scrollHeight = 3000, clientHeight = 796 }: {
  scrollTop: number;
  scrollHeight?: number;
  clientHeight?: number;
}) {
  const el = scroller(container);
  Object.defineProperty(el, "scrollTop", { configurable: true, value: scrollTop, writable: true });
  Object.defineProperty(el, "scrollHeight", { configurable: true, value: scrollHeight });
  Object.defineProperty(el, "clientHeight", { configurable: true, value: clientHeight });
  await act(async () => {
    fireEvent.scroll(el);
    await new Promise((resolve) => requestAnimationFrame(() => resolve(undefined)));
  });
}

describe("transcript under floating chrome", () => {
  it("reserves the bars' heights in the virtual space", () => {
    renderFloating();
    const options = virtualizerOptions.current as Record<string, number>;
    // 32px is the scroll-back header band the first row always sits below.
    expect(options.paddingStart).toBe(TOP_BAR_PX + 32);
    expect(options.scrollPaddingEnd).toBe(BAND_PX);
    // The footer (0 here: nothing measured) plus the band.
    expect(options.paddingEnd).toBe(BAND_PX);
    // A jump aligns below the bar and the (estimated) pinned header.
    expect(options.scrollPaddingStart).toBeGreaterThan(TOP_BAR_PX);
  });

  it("keeps native scrolling clear of the bars with scroll-padding", () => {
    const { container } = renderFloating();
    expect(scroller(container).style.scrollPaddingTop).toBe("var(--chat-top-inset, 0px)");
    expect(scroller(container).style.scrollPaddingBottom).toBe("var(--chat-bottom-inset, 0px)");
  });

  it("reports the scroll edge as the reader scrolls", async () => {
    const { container } = renderFloating();

    // Opened at the end: nothing under the composer; nothing under the bar yet
    // either, at the very top of a short scroll.
    await scrollTo(container, { scrollTop: 0, scrollHeight: 796 });
    expect(frame(container)).not.toHaveAttribute("data-scroll-edge-top");
    expect(frame(container)).not.toHaveAttribute("data-scroll-edge-bottom");

    // At the end of a long transcript: content under the top bar only.
    await scrollTo(container, { scrollTop: 3000 - 796 });
    expect(frame(container)).toHaveAttribute("data-scroll-edge-top");
    expect(frame(container)).not.toHaveAttribute("data-scroll-edge-bottom");

    // Scrolled up mid-way: the conversation continues under the composer.
    await scrollTo(container, { scrollTop: 1200 });
    expect(frame(container)).toHaveAttribute("data-scroll-edge-bottom");
  });

  it("hangs the pinned strip from the bar and lets it carry the top edge", async () => {
    const { container } = renderFloating();
    // m2 has slid under the strip line (the bar plus the strip).
    applyGeometry(container, {
      0: { top: -3000, height: 100 },
      1: { top: -2900, height: 2480 },
      2: { top: -420, height: 440 },
      3: { top: 20 + TOP_BAR_PX, height: 3000 },
    });
    await scrollTo(container, { scrollTop: 1200 });

    const strip = container.querySelector<HTMLElement>('[data-testid="pinned-user-message-header"]');
    expect(strip).toHaveAttribute("data-form", "strip");
    expect(strip?.style.top).toBe(`${TOP_BAR_PX}px`);
    // The bars' glass, and its own hairline: one edge under bar and strip.
    expect(strip).toHaveClass("bg-background/85", "backdrop-blur-xl", "border-b", "border-border/60");
    expect(frame(container)).not.toHaveAttribute("data-scroll-edge-top");
    // 36px to see (plus the hairline), a real 44px box to tap: the button
    // overflows the strip downward rather than relying on a pseudo-element.
    expect(strip).toHaveClass("h-[37px]");
    const jump = strip?.querySelector("button");
    expect(jump).toHaveClass("h-[44px]");
    expect(jump?.className).not.toMatch(/after:/);
  });
});
