import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useVisualViewport } from "../useVisualViewport";

function install() {
  const viewport = Object.assign(new EventTarget(), { height: 844, offsetTop: 0, scale: 1 });
  vi.stubGlobal("visualViewport", viewport);
  vi.stubGlobal("innerHeight", 844);
  return viewport;
}
afterEach(() => vi.unstubAllGlobals());

describe("useVisualViewport lifecycle", () => {
  it("follows open, pan and dismissal without losing the layout fallback", () => {
    const vv = install();
    const { result } = renderHook(() => useVisualViewport());
    act(() => { vv.height = 508; vv.dispatchEvent(new Event("resize")); });
    expect(result.current?.keyboardOpen).toBe(true);
    act(() => { vv.offsetTop = 120; vv.dispatchEvent(new Event("scroll")); });
    expect(result.current?.offsetTop).toBe(120);
    act(() => { vv.height = 844; vv.offsetTop = 0; vv.dispatchEvent(new Event("resize")); });
    expect(result.current).toEqual({ height: 844, offsetTop: 0, keyboardOpen: false });
  });

  it("reconciles a rotation whose layout resize arrives after the visual resize", () => {
    const vv = install();
    const { result } = renderHook(() => useVisualViewport());
    act(() => { vv.height = 390; vv.dispatchEvent(new Event("resize")); });
    act(() => { vi.stubGlobal("innerHeight", 390); window.dispatchEvent(new Event("resize")); });
    expect(result.current?.keyboardOpen).toBe(false);
  });

  it("opts out, refreshes on re-enable, and removes every listener", () => {
    const vv = install();
    const remove = vi.spyOn(vv, "removeEventListener");
    const removeWindow = vi.spyOn(window, "removeEventListener");
    const { result, rerender, unmount } = renderHook(({ enabled }) => useVisualViewport(enabled), { initialProps: { enabled: true } });
    rerender({ enabled: false });
    expect(result.current).toBeNull();
    vv.height = 400;
    rerender({ enabled: true });
    expect(result.current?.height).toBe(400);
    unmount();
    expect(remove).toHaveBeenCalledWith("resize", expect.any(Function));
    expect(remove).toHaveBeenCalledWith("scroll", expect.any(Function));
    expect(removeWindow).toHaveBeenCalledWith("resize", expect.any(Function));
    removeWindow.mockRestore();
  });

  it("falls back without the visual viewport API", () => {
    vi.stubGlobal("visualViewport", undefined);
    expect(renderHook(() => useVisualViewport()).result.current).toBeNull();
  });
});
