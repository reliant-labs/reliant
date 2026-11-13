import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ readSetting: vi.fn(), upsertStringSetting: vi.fn() }));
vi.mock("@/lib/settingsPersistence", () => ({
  readSetting: (k: string) => mocks.readSetting(k),
  upsertStringSetting: (k: string, v: string) => mocks.upsertStringSetting(k, v),
}));

import { PENDING_PROVIDER_KEY, clearPendingProvider, readPendingProvider, setPendingProvider } from "../pendingProvider";

beforeEach(() => {
  vi.clearAllMocks();
  mocks.upsertStringSetting.mockResolvedValue(undefined);
});

describe("pendingProvider", () => {
  it("is stored per user in settings, not in the browser", async () => {
    await setPendingProvider("codex");
    expect(mocks.upsertStringSetting).toHaveBeenCalledWith(PENDING_PROVIDER_KEY, "codex");
  });

  it("reads back a known provider", async () => {
    mocks.readSetting.mockResolvedValue({ status: "found", value: "claude" });
    expect(await readPendingProvider()).toBe("claude");
  });

  it("treats cleared, missing, unreadable and unknown values as nothing pending", async () => {
    for (const read of [
      { status: "found", value: "" },
      { status: "found", value: "gemini" },
      { status: "missing" },
      { status: "error" },
    ]) {
      mocks.readSetting.mockResolvedValue(read);
      expect(await readPendingProvider()).toBeNull();
    }
  });

  it("clearing writes an empty value", async () => {
    await clearPendingProvider();
    expect(mocks.upsertStringSetting).toHaveBeenCalledWith(PENDING_PROVIDER_KEY, "");
  });
});
