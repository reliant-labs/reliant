import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useLongPress } from "../useLongPress";

function touchEvent(x: number, y: number, target: Element = document.createElement("div")): Partial<React.TouchEvent> {
  return {
    target,
    touches: [{ clientX: x, clientY: y }] as unknown as React.TouchList,
    preventDefault: vi.fn(),
  };
}

describe("useLongPress", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("fires after holding for 500ms without moving", () => {
    const onLongPress = vi.fn();
    const { result } = renderHook(() => useLongPress(onLongPress));

    act(() => {
      result.current.onTouchStart(touchEvent(10, 10) as React.TouchEvent);
    });
    expect(onLongPress).not.toHaveBeenCalled();

    act(() => {
      vi.advanceTimersByTime(500);
    });
    expect(onLongPress).toHaveBeenCalledTimes(1);
  });

  it("does not fire on a short tap (release before threshold)", () => {
    const onLongPress = vi.fn();
    const { result } = renderHook(() => useLongPress(onLongPress));

    act(() => {
      result.current.onTouchStart(touchEvent(10, 10) as React.TouchEvent);
    });
    act(() => {
      vi.advanceTimersByTime(200);
      result.current.onTouchEnd(touchEvent(10, 10) as React.TouchEvent);
    });
    act(() => {
      vi.advanceTimersByTime(500);
    });
    expect(onLongPress).not.toHaveBeenCalled();
  });

  it("cancels when the touch moves past the threshold (scroll)", () => {
    const onLongPress = vi.fn();
    const { result } = renderHook(() => useLongPress(onLongPress));

    act(() => {
      result.current.onTouchStart(touchEvent(10, 10) as React.TouchEvent);
    });
    act(() => {
      vi.advanceTimersByTime(100);
      result.current.onTouchMove(touchEvent(10, 40) as React.TouchEvent);
    });
    act(() => {
      vi.advanceTimersByTime(500);
    });
    expect(onLongPress).not.toHaveBeenCalled();
  });

  it("tolerates small jitter under the move threshold", () => {
    const onLongPress = vi.fn();
    const { result } = renderHook(() => useLongPress(onLongPress));

    act(() => {
      result.current.onTouchStart(touchEvent(10, 10) as React.TouchEvent);
    });
    act(() => {
      vi.advanceTimersByTime(100);
      result.current.onTouchMove(touchEvent(13, 12) as React.TouchEvent);
    });
    act(() => {
      vi.advanceTimersByTime(500);
    });
    expect(onLongPress).toHaveBeenCalledTimes(1);
  });

  it("cancels on touchcancel", () => {
    const onLongPress = vi.fn();
    const { result } = renderHook(() => useLongPress(onLongPress));

    act(() => {
      result.current.onTouchStart(touchEvent(10, 10) as React.TouchEvent);
    });
    act(() => {
      vi.advanceTimersByTime(100);
      result.current.onTouchCancel(touchEvent(10, 10) as React.TouchEvent);
    });
    act(() => {
      vi.advanceTimersByTime(500);
    });
    expect(onLongPress).not.toHaveBeenCalled();
  });

  it("prevents the default touchend action once fired", () => {
    const onLongPress = vi.fn();
    const { result } = renderHook(() => useLongPress(onLongPress));

    act(() => {
      result.current.onTouchStart(touchEvent(10, 10) as React.TouchEvent);
      vi.advanceTimersByTime(500);
    });
    const endEvent = touchEvent(10, 10) as React.TouchEvent;
    act(() => {
      result.current.onTouchEnd(endEvent);
    });
    expect(endEvent.preventDefault).toHaveBeenCalled();
  });

  it.each(["before the press", "during the press"])("preserves native selection created %s", (when) => {
    const removeAllRanges = vi.fn();
    const onLongPress = vi.fn();
    let selected = when === "before the press" ? "selected text" : "";
    vi.spyOn(window, "getSelection").mockReturnValue({
      toString: () => selected,
      removeAllRanges,
    } as unknown as Selection);
    const { result } = renderHook(() => useLongPress(onLongPress));
    act(() => {
      result.current.onTouchStart(touchEvent(10, 10) as React.TouchEvent);
      selected = "selected text";
      vi.advanceTimersByTime(500);
    });
    expect(onLongPress).not.toHaveBeenCalled();
    expect(removeAllRanges).not.toHaveBeenCalled();
  });

  it.each(["a", "button", "input", "textarea", "pre", "code", "[data-message-interactive]", '[role="button"]'])("ignores a press inside %s", (selector) => {
    const target = document.createElement(selector.startsWith("[") ? "div" : selector);
    if (selector === "[data-message-interactive]") target.setAttribute("data-message-interactive", "");
    if (selector === '[role="button"]') target.setAttribute("role", "button");
    const child = document.createElement("span");
    target.append(child);
    const onLongPress = vi.fn();
    const { result } = renderHook(() => useLongPress(onLongPress));
    act(() => {
      result.current.onTouchStart(touchEvent(10, 10, child) as React.TouchEvent);
      vi.advanceTimersByTime(500);
    });
    expect(onLongPress).not.toHaveBeenCalled();
  });

  it("cancels when a scroll event occurs without touch movement", () => {
    const onLongPress = vi.fn();
    const { result } = renderHook(() => useLongPress(onLongPress));
    act(() => {
      result.current.onTouchStart(touchEvent(10, 10) as React.TouchEvent);
      document.dispatchEvent(new Event("scroll"));
      vi.advanceTimersByTime(500);
    });
    expect(onLongPress).not.toHaveBeenCalled();
  });

  it("does not touch the selection when the press never fires", () => {
    const removeAllRanges = vi.fn();
    const spy = vi
      .spyOn(window, "getSelection")
      .mockReturnValue({ toString: () => "", removeAllRanges } as unknown as Selection);
    try {
      const { result } = renderHook(() => useLongPress(vi.fn()));
      act(() => {
        result.current.onTouchStart(touchEvent(10, 10) as React.TouchEvent);
        vi.advanceTimersByTime(200);
        result.current.onTouchEnd(touchEvent(10, 10) as React.TouchEvent);
        vi.advanceTimersByTime(500);
      });
      expect(removeAllRanges).not.toHaveBeenCalled();
    } finally {
      spy.mockRestore();
    }
  });

  it("preserves the native context menu until a long press opens actions", () => {
    const onLongPress = vi.fn();
    const { result } = renderHook(() => useLongPress(onLongPress));
    const preventDefault = vi.fn();
    const event = { preventDefault } as unknown as React.SyntheticEvent;
    result.current.onContextMenu(event);
    expect(preventDefault).not.toHaveBeenCalled();
    act(() => {
      result.current.onTouchStart(touchEvent(10, 10) as React.TouchEvent);
      vi.advanceTimersByTime(500);
    });
    expect(onLongPress).toHaveBeenCalledOnce();
    result.current.onContextMenu(event);
    expect(preventDefault).toHaveBeenCalledOnce();
  });
});
