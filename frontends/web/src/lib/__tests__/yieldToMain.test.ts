// Copyright (c) 2025 Reliant Labs

import { afterEach, describe, expect, it, vi } from "vitest";

import { yieldToMain } from "../yieldToMain";

describe("yieldToMain", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.useRealTimers();
  });

  it("uses scheduler.yield where the browser has it", async () => {
    const yieldFn = vi.fn(() => Promise.resolve());
    vi.stubGlobal("scheduler", { yield: yieldFn });
    await yieldToMain();
    expect(yieldFn).toHaveBeenCalledTimes(1);
  });

  it("resolves after the current task elsewhere, even under fake timers", async () => {
    vi.useFakeTimers();
    const order: string[] = [];
    const done = yieldToMain().then(() => order.push("yielded"));
    await Promise.resolve();
    order.push("same task");
    await done;
    expect(order).toEqual(["same task", "yielded"]);
  });
});
