/**
 * Loading the workspace-state store removes the keys of the stores it
 * replaced.
 *
 * persist rehydrates synchronously while `create()` runs, so the cleanup runs
 * during the module's own evaluation. It read DEPRECATED_STORAGE_KEYS, which
 * was declared further down the module and so was still in its temporal dead
 * zone: every load threw, logged "[WorkspaceState] Post-rehydrate step failed
 * (non-fatal): ReferenceError: Cannot access 'DEPRECATED_STORAGE_KEYS' before
 * initialization", and removed nothing.
 */

import { afterEach, describe, expect, it, vi } from "vitest";

const loggerWarn = vi.hoisted(() => vi.fn());
vi.mock("../../lib/logger", () => ({
  logger: { info: vi.fn(), debug: vi.fn(), warn: loggerWarn, error: vi.fn() },
}));

afterEach(() => {
  localStorage.clear();
  vi.resetModules();
});

describe("workspaceStateStore: deprecated storage cleanup", () => {
  it("removes the replaced stores' keys when it loads, without a post-rehydrate failure", async () => {
    localStorage.setItem("tab-store", "{}");
    localStorage.setItem("viewer-storage", "{}");
    localStorage.setItem("unrelated", "kept");

    vi.resetModules();
    await import("../workspaceStateStore");

    expect(localStorage.getItem("tab-store")).toBeNull();
    expect(localStorage.getItem("viewer-storage")).toBeNull();
    expect(localStorage.getItem("unrelated")).toBe("kept");
    expect(loggerWarn).not.toHaveBeenCalledWith(
      expect.stringContaining("Post-rehydrate step failed"),
      expect.anything(),
    );
  });
});
