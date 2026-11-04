/**
 * useTimelineVirtualizer with floating chrome insets: the composer band's
 * height is reserved after the footer (so "the end" is the last row resting
 * above the composer), growth there follows a reader at the end, and a change
 * in the top bar's height moves the reader with the rows instead of letting
 * them jump — or silently dropping them off the end.
 */

import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useTimelineVirtualizer } from "../useTimelineVirtualizer";

const { virtualizer, lastOptions } = vi.hoisted(() => ({
  virtualizer: {
    scrollToEnd: vi.fn(),
    scrollBy: vi.fn(),
    containerRef: () => {},
  },
  lastOptions: { current: null as null | Record<string, unknown> },
}));

vi.mock("@tanstack/react-virtual", () => ({
  useVirtualizer: (options: Record<string, unknown>) => {
    lastOptions.current = options;
    return virtualizer;
  },
}));

/** A scroller `distance` px short of the end of a 3000px transcript. */
function scrollerAt(distance: number): HTMLDivElement {
  const el = document.createElement("div");
  Object.defineProperty(el, "scrollHeight", { configurable: true, value: 3000 });
  Object.defineProperty(el, "clientHeight", { configurable: true, value: 800 });
  Object.defineProperty(el, "scrollTop", { configurable: true, writable: true, value: 2200 - distance });
  return el;
}

function setup(initial: { paddingStart: number; endInset: number }, scroller = scrollerAt(0)) {
  const hook = renderHook((props: { paddingStart: number; endInset: number }) =>
    useTimelineVirtualizer({
      count: 10,
      getItemKey: (i) => String(i),
      paddingStart: props.paddingStart,
      scrollPaddingStart: props.paddingStart,
      endInset: props.endInset,
    }),
  { initialProps: initial });
  act(() => hook.result.current.scrollerRef(scroller));
  virtualizer.scrollToEnd.mockClear();
  virtualizer.scrollBy.mockClear();
  return { ...hook, scroller };
}

/** What the virtualizer's onChange does on a scroll: re-read "at the end". */
function scrolled(scroller: HTMLDivElement) {
  const onChange = lastOptions.current?.onChange as (instance: { scrollElement: HTMLElement }) => void;
  act(() => onChange({ scrollElement: scroller }));
}

beforeEach(() => {
  lastOptions.current = null;
});

describe("useTimelineVirtualizer insets", () => {
  it("reserves the composer band after the footer and as the end's scroll padding", () => {
    setup({ paddingStart: 80, endInset: 120 });
    expect(lastOptions.current?.paddingEnd).toBe(120);
    expect(lastOptions.current?.scrollPaddingEnd).toBe(120);
  });

  it("follows the end when the composer band grows while at the end", () => {
    const { rerender, scroller } = setup({ paddingStart: 80, endInset: 120 });
    // The band grew a line: the DOM's end moved 24px below the reader.
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 3024 });
    rerender({ paddingStart: 80, endInset: 144 });
    expect(virtualizer.scrollToEnd).toHaveBeenCalledTimes(1);
  });

  it("leaves a scrolled-up reader alone when the composer band grows", () => {
    const scroller = scrollerAt(600);
    const { rerender } = setup({ paddingStart: 80, endInset: 120 }, scroller);
    scrolled(scroller);
    Object.defineProperty(scroller, "scrollHeight", { configurable: true, value: 3024 });
    rerender({ paddingStart: 80, endInset: 144 });
    expect(virtualizer.scrollToEnd).not.toHaveBeenCalled();
  });

  it("keeps a reader at the end following when the top bar changes height", () => {
    const { rerender } = setup({ paddingStart: 80, endInset: 120 });
    rerender({ paddingStart: 96, endInset: 120 });
    expect(virtualizer.scrollToEnd).toHaveBeenCalledTimes(1);
    expect(virtualizer.scrollBy).not.toHaveBeenCalled();
  });

  it("holds a scrolled-up reader on the same text when the top bar changes height", () => {
    const scroller = scrollerAt(600);
    const { rerender, result } = setup({ paddingStart: 80, endInset: 120 }, scroller);
    scrolled(scroller);
    expect(result.current.atBottom).toBe(false);

    rerender({ paddingStart: 96, endInset: 120 });
    expect(virtualizer.scrollBy).toHaveBeenCalledWith(16, expect.anything());
    rerender({ paddingStart: 80, endInset: 120 });
    expect(virtualizer.scrollBy).toHaveBeenLastCalledWith(-16, expect.anything());
    expect(virtualizer.scrollToEnd).not.toHaveBeenCalled();
  });
});
