import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { renderHook, act } from "@testing-library/react";
import { useLongPress } from "../useLongPress";

function touchEvent(x: number, y: number): Partial<React.TouchEvent> {
  return {
    touches: [{ clientX: x, clientY: y }] as unknown as React.TouchList,
    preventDefault: vi.fn(),
  };
}

describe("useLongPress", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
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

  it("clears any native selection right before firing", () => {
    const removeAllRanges = vi.fn();
    const onLongPress = vi.fn(() => {
      expect(removeAllRanges).toHaveBeenCalledTimes(1);
    });
    const spy = vi
      .spyOn(window, "getSelection")
      .mockReturnValue({ removeAllRanges } as unknown as Selection);
    try {
      const { result } = renderHook(() => useLongPress(onLongPress));
      act(() => {
        result.current.onTouchStart(touchEvent(10, 10) as React.TouchEvent);
        vi.advanceTimersByTime(500);
      });
      expect(onLongPress).toHaveBeenCalledTimes(1);
    } finally {
      spy.mockRestore();
    }
  });

  it("does not touch the selection when the press never fires", () => {
    const removeAllRanges = vi.fn();
    const spy = vi
      .spyOn(window, "getSelection")
      .mockReturnValue({ removeAllRanges } as unknown as Selection);
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

  it("returns an onContextMenu handler that prevents the default menu", () => {
    const { result } = renderHook(() => useLongPress(vi.fn()));
    const preventDefault = vi.fn();
    result.current.onContextMenu({ preventDefault } as unknown as React.SyntheticEvent);
    expect(preventDefault).toHaveBeenCalled();
  });
});
