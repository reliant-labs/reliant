import { describe, expect, it, vi } from "vitest";
import { watchUserScroll } from "../userScroll";

describe("watchUserScroll", () => {
  it.each([
    ["a finger drag", () => new Event("touchmove")],
    ["a wheel or trackpad", () => new Event("wheel")],
    ["a scrolling key", () => new KeyboardEvent("keydown", { key: "PageDown" })],
  ])("reports %s, once", (_, make) => {
    const onScroll = vi.fn();
    const stop = watchUserScroll(onScroll);
    window.dispatchEvent(make());
    window.dispatchEvent(make());
    expect(onScroll).toHaveBeenCalledTimes(1);
    stop();
  });

  it("ignores scroll events (the tour's own scrollIntoView fires them) and other keys", () => {
    const onScroll = vi.fn();
    const stop = watchUserScroll(onScroll);
    window.dispatchEvent(new Event("scroll"));
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "a" }));
    expect(onScroll).not.toHaveBeenCalled();
    stop();
  });

  it("reports nothing after it is stopped", () => {
    const onScroll = vi.fn();
    watchUserScroll(onScroll)();
    window.dispatchEvent(new Event("touchmove"));
    expect(onScroll).not.toHaveBeenCalled();
  });
});
