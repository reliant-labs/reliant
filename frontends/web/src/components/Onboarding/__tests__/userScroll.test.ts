import { describe, expect, it, vi } from "vitest";
import { holdUserScroll, watchUserScroll } from "../userScroll";

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

describe("holdUserScroll", () => {
  const cancelable = (type: string) => new Event(type, { bubbles: true, cancelable: true });

  it("cancels drags and wheels outside the allowed box, and lets them through inside it", () => {
    const box = document.createElement("div");
    document.body.appendChild(box);
    const release = holdUserScroll(() => box);
    for (const type of ["touchmove", "wheel"]) {
      const outside = cancelable(type);
      document.body.dispatchEvent(outside);
      expect(outside.defaultPrevented).toBe(true);
      const inside = cancelable(type);
      box.dispatchEvent(inside);
      expect(inside.defaultPrevented).toBe(false);
    }
    release();
    const after = cancelable("touchmove");
    document.body.dispatchEvent(after);
    expect(after.defaultPrevented).toBe(false);
    box.remove();
  });

  it("holds scrolling keys only when nothing is focused", () => {
    const input = document.createElement("input");
    document.body.appendChild(input);
    const release = holdUserScroll(() => null);
    const onPage = new KeyboardEvent("keydown", { key: " ", bubbles: true, cancelable: true });
    document.body.dispatchEvent(onPage);
    expect(onPage.defaultPrevented).toBe(true);
    const typing = new KeyboardEvent("keydown", { key: " ", bubbles: true, cancelable: true });
    input.dispatchEvent(typing);
    expect(typing.defaultPrevented).toBe(false);
    release();
    input.remove();
  });
});
