/**
 * An older build persisted composer params under `reliant-chat-params`.
 * Nothing reads it now, so loading the store deletes it once.
 */
import { describe, expect, it, vi } from "vitest";

describe("legacy reliant-chat-params key", () => {
  it("is deleted when the store loads", async () => {
    localStorage.setItem("reliant-chat-params", JSON.stringify({ version: 5, state: {} }));
    localStorage.setItem("unrelated", "kept");
    vi.resetModules();

    await import("../chatParamsStore");

    expect(localStorage.getItem("reliant-chat-params")).toBeNull();
    expect(localStorage.getItem("unrelated")).toBe("kept");
  });
});
