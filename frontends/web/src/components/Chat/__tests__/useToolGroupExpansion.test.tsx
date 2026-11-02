import { act, renderHook } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { toolGroupAutoExpanded, useToolGroupExpansion, type ToolGroupActivity } from "../useToolGroupExpansion";

const idle: ToolGroupActivity = { live: false, failed: false, pendingApproval: false };

describe("tool group expansion", () => {
  it("opens live work and closes finished work", () => {
    expect(toolGroupAutoExpanded({ ...idle, live: true })).toBe(true);
    expect(toolGroupAutoExpanded(idle)).toBe(false);
  });

  it("keeps a failure and an approval in view", () => {
    expect(toolGroupAutoExpanded({ ...idle, failed: true })).toBe(true);
    expect(toolGroupAutoExpanded({ ...idle, pendingApproval: true })).toBe(true);
  });

  it("on a phone opens only for an approval", () => {
    expect(toolGroupAutoExpanded({ ...idle, live: true, collapseAll: true })).toBe(false);
    expect(toolGroupAutoExpanded({ ...idle, pendingApproval: true, collapseAll: true })).toBe(true);
  });

  it("collapses by itself when the last call finishes", () => {
    const { result, rerender } = renderHook((activity: ToolGroupActivity) => useToolGroupExpansion(activity), {
      initialProps: { ...idle, live: true },
    });
    expect(result.current.expanded).toBe(true);
    rerender(idle);
    expect(result.current.expanded).toBe(false);
  });

  it("lets the user's toggle win, until a new approval needs them", () => {
    const { result, rerender } = renderHook((activity: ToolGroupActivity) => useToolGroupExpansion(activity), {
      initialProps: { ...idle, live: true },
    });
    act(() => result.current.toggle());
    expect(result.current.expanded).toBe(false);
    rerender({ ...idle, live: true });
    expect(result.current.expanded).toBe(false);

    rerender({ ...idle, live: true, pendingApproval: true });
    expect(result.current.expanded).toBe(true);
  });
});
