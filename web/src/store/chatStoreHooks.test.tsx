/**
 * Test for chatStoreHooks to verify no infinite loops
 */

import { renderHook } from "@testing-library/react";
import { useChatStoreActions } from "./chatStoreHooks";
import { describe, it, expect } from "vitest";

describe("chatStoreHooks", () => {
  it("useChatStoreActions should return stable reference", () => {
    const { result, rerender } = renderHook(() => useChatStoreActions());

    const firstResult = result.current;
    rerender();
    const secondResult = result.current;

    // Should be the SAME object reference (memoized)
    expect(firstResult).toBe(secondResult);
    console.log("✅ useChatStoreActions returns stable reference");
  });

  it("useChatStoreActions should not cause re-renders when used", () => {
    let renderCount = 0;
    const { result: _result, rerender } = renderHook(() => {
      renderCount++;
      const actions = useChatStoreActions();
      return actions;
    });

    expect(renderCount).toBe(1);

    // Rerender multiple times
    rerender();
    rerender();
    rerender();

    expect(renderCount).toBe(4); // 1 initial + 3 manual rerenders
    console.log(
      `✅ useChatStoreActions rendered ${renderCount} times (expected 4)`
    );
  });
});
