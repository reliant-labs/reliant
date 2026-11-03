/**
 * The pinned prompt at compact width: a one-line strip that is itself the jump
 * target, hidden while composing and in compact height. See
 * dev-docs/design/mobile-chat-redesign.md §7.2. The pin RESOLUTION (which
 * message, when) is pinnedHeaderHandoff.test.tsx's; this file covers the form.
 */

import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { act } from "react";
import { ContentBlockType, MessageRole, StreamingState } from "../../../../types/chat";
import type { Message } from "../../../../types/chat";
import { InterleavedTimeline } from "../InterleavedTimeline";
import { ComposeModeProvider } from "../../../../lib/composeMode";

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

/** The strip's rendered height: 36px plus its hairline. */
const HEADER_PX = 37;

// jsdom lays nothing out, so every offsetHeight is 0. Give the header its real
// height: the crossing line is the header's bottom edge, and at 0px the
// stuck-at-the-top defect cannot be reproduced at all.
const offsetHeightDescriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "offsetHeight");
const clientWidthDescriptor = Object.getOwnPropertyDescriptor(Element.prototype, "clientWidth");
beforeAll(() => {
  // A phone-width pane: the compact width class that gets the strip.
  Object.defineProperty(Element.prototype, "clientWidth", {
    configurable: true,
    get: () => paneWidth,
  });
  Object.defineProperty(HTMLElement.prototype, "offsetHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return this.getAttribute("data-testid") === "pinned-user-message-header" ? HEADER_PX : 0;
    },
  });
});
afterAll(() => {
  if (clientWidthDescriptor) Object.defineProperty(Element.prototype, "clientWidth", clientWidthDescriptor);
  if (offsetHeightDescriptor) {
    Object.defineProperty(HTMLElement.prototype, "offsetHeight", offsetHeightDescriptor);
  }
});

let paneWidth = 390;

beforeEach(() => {
  virtualizerOptions.current = null;
  scrollToIndex.mockClear();
  paneWidth = 390;
  vi.stubGlobal("innerHeight", 844);
});
afterEach(() => vi.unstubAllGlobals());

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

function renderTimeline({ composeMode = false }: { composeMode?: boolean } = {}) {
  return render(
    <ComposeModeProvider keyboardOpen={composeMode}>
      <InterleavedTimeline messages={MESSAGES} chatId="chat-1" isStreaming={false} />
    </ComposeModeProvider>,
  );
}

/** m2 has slid under the header and its section fills the screen. */
const M2_PINNED: Geometry = {
  0: { top: -3000, height: 100 },
  1: { top: -2900, height: 2480 },
  2: { top: -420, height: 440 },
  3: { top: 20, height: 3000 },
};

function header(container: HTMLElement): HTMLElement | null {
  return container.querySelector<HTMLElement>('[data-testid="pinned-user-message-header"]');
}

describe("pinned prompt strip (compact width)", () => {
  it("renders the prompt's first line as a single jump strip", async () => {
    const { container } = renderTimeline();
    applyGeometry(container, M2_PINNED);
    await scroll(container);

    const strip = header(container);
    expect(strip).toHaveAttribute("data-form", "strip");
    const jump = screen.getByRole("button", { name: "Jump to your last message" });
    expect(strip).toContainElement(jump);
    expect(jump).toHaveTextContent("Refactor the retry loop");
    expect(jump).not.toHaveTextContent("jittered");
    // No bubble: the full message renderer is the medium-width overlay's.
    expect(pinnedMessageId(container)).toBeNull();
    // Opaque and flat, the top bar's treatment: no shadow.
    expect(strip).toHaveClass("bg-background");
    expect(strip?.className).not.toMatch(/shadow/);
  });

  it("jumps to the pinned message, clearing the strip's height", async () => {
    const { container } = renderTimeline();
    applyGeometry(container, M2_PINNED);
    await scroll(container);

    // Sticky toolbars (ChatMessage) and the jump's clearance both follow the
    // strip's real height, not the overlay's.
    expect((container.firstElementChild as HTMLElement).style.getPropertyValue("--chat-pinned-header-h")).toBe(`${HEADER_PX}px`);
    expect(virtualizerOptions.current?.scrollPaddingStart).toBe(HEADER_PX);

    fireEvent.click(screen.getByRole("button", { name: "Jump to your last message" }));
    expect(scrollToIndex).toHaveBeenCalledWith(2, expect.objectContaining({ align: "start" }));
  });

  it("is hidden entirely in compose mode", async () => {
    const { container } = renderTimeline({ composeMode: true });
    applyGeometry(container, M2_PINNED);
    await scroll(container);

    expect(header(container)).toBeNull();
    expect((container.firstElementChild as HTMLElement).style.getPropertyValue("--chat-pinned-header-h")).toBe("");
  });

  it("is hidden in compact height (a phone in landscape)", async () => {
    vi.stubGlobal("innerHeight", 390);
    paneWidth = 844;
    const { container } = renderTimeline();
    applyGeometry(container, M2_PINNED);
    await scroll(container);

    expect(header(container)).toBeNull();
  });

  it("keeps the desktop overlay at medium width and up", async () => {
    paneWidth = 900;
    const { container } = renderTimeline();
    applyGeometry(container, M2_PINNED);
    await scroll(container);

    expect(header(container)).not.toHaveAttribute("data-form");
    expect(pinnedMessageId(container)).toBe("m2");
    expect(screen.getByRole("button", { name: "Jump to message" })).toBeInTheDocument();
  });
});
