import { afterEach, describe, expect, it, vi } from "vitest";

describe("installUpdateDepthStack", () => {
  const original = console.error;

  afterEach(() => {
    console.error = original;
    vi.resetModules();
  });

  it("appends the stack of the state update to React's bare update-depth warning", async () => {
    const seen: unknown[][] = [];
    console.error = (...args: unknown[]) => {
      seen.push(args);
    };
    const { installUpdateDepthStack } = await import("../updateDepthStack");
    installUpdateDepthStack();

    // React logs this from inside the setter call; the app frame that called
    // the setter is what has to survive into the log.
    function setConnectionStateInAnEffect() {
      console.error(
        "Maximum update depth exceeded. This can happen when a component calls setState inside useEffect, but useEffect either doesn't have a dependency array, or one of the dependencies changes on every render.",
      );
    }
    setConnectionStateInAnEffect();

    expect(seen).toHaveLength(1);
    expect(String(seen[0]![0])).toMatch(/^Maximum update depth exceeded/);
    expect(String(seen[0]![1])).toContain("setConnectionStateInAnEffect");
  });

  it("passes every other error through untouched", async () => {
    const seen: unknown[][] = [];
    console.error = (...args: unknown[]) => {
      seen.push(args);
    };
    const { installUpdateDepthStack } = await import("../updateDepthStack");
    installUpdateDepthStack();

    const err = new Error("boom");
    console.error("[ProjectStore] Failed to load projects", err);

    expect(seen).toEqual([["[ProjectStore] Failed to load projects", err]]);
  });
});
