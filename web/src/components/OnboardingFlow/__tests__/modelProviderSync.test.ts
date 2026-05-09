/**
 * Tests for Reliant provider sync on model step completion.
 *
 * Verifies that syncReliantProvider is called when the user selects
 * "reliant_credits" and is NOT called for other providers. Also verifies
 * that sync failures don't block onboarding completion.
 *
 * Follows the handleCloud.test.ts pattern: extract logic into a testable
 * function that mirrors ModelStep.finishOnboarding's sync behavior.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ModelProvider } from "../types";

// ── Mock the api.settings module ─────────────────────────────

const mockSyncReliantProvider = vi.fn<() => Promise<{ synced: boolean }>>();

// Mock the api client used in ModelStep
vi.mock("@/api/client", () => ({
  api: {
    settings: {
      syncReliantProvider: () => mockSyncReliantProvider(),
    },
  },
}));

// ── Extract the sync logic from ModelStep.finishOnboarding ───

/**
 * Mirrors the provider sync logic in ModelStep.finishOnboarding:
 * Only calls syncReliantProvider when modelProvider === "reliant_credits".
 * Failures are caught and logged but don't throw.
 */
async function syncProviderIfNeeded(
  modelProvider: ModelProvider,
  hasControlPlane: boolean,
): Promise<{ syncCalled: boolean; syncError?: string }> {
  if (!hasControlPlane) {
    return { syncCalled: false };
  }

  if (modelProvider === "reliant_credits") {
    try {
      await mockSyncReliantProvider();
      return { syncCalled: true };
    } catch (err) {
      // Mirror the .then(success, error) pattern — failures are logged, not thrown
      return {
        syncCalled: true,
        syncError: err instanceof Error ? err.message : String(err),
      };
    }
  }

  return { syncCalled: false };
}

// ── Tests ────────────────────────────────────────────────────

beforeEach(() => {
  vi.clearAllMocks();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("ModelStep — Reliant provider sync", () => {
  it("calls syncReliantProvider when modelProvider is 'reliant_credits'", async () => {
    mockSyncReliantProvider.mockResolvedValue({ synced: true });

    const result = await syncProviderIfNeeded("reliant_credits", true);

    expect(mockSyncReliantProvider).toHaveBeenCalledTimes(1);
    expect(result.syncCalled).toBe(true);
    expect(result.syncError).toBeUndefined();
  });

  it("does NOT call syncReliantProvider when modelProvider is 'anthropic'", async () => {
    const result = await syncProviderIfNeeded("anthropic", true);

    expect(mockSyncReliantProvider).not.toHaveBeenCalled();
    expect(result.syncCalled).toBe(false);
  });

  it("does NOT call syncReliantProvider when modelProvider is 'openai'", async () => {
    const result = await syncProviderIfNeeded("openai", true);

    expect(mockSyncReliantProvider).not.toHaveBeenCalled();
    expect(result.syncCalled).toBe(false);
  });

  it("does NOT call syncReliantProvider when modelProvider is 'openrouter'", async () => {
    const result = await syncProviderIfNeeded("openrouter", true);

    expect(mockSyncReliantProvider).not.toHaveBeenCalled();
    expect(result.syncCalled).toBe(false);
  });

  it("does NOT call syncReliantProvider when hasControlPlane is false", async () => {
    const result = await syncProviderIfNeeded("reliant_credits", false);

    expect(mockSyncReliantProvider).not.toHaveBeenCalled();
    expect(result.syncCalled).toBe(false);
  });

  it("does not throw when syncReliantProvider fails", async () => {
    mockSyncReliantProvider.mockRejectedValue(new Error("network timeout"));

    const result = await syncProviderIfNeeded("reliant_credits", true);

    expect(mockSyncReliantProvider).toHaveBeenCalledTimes(1);
    expect(result.syncCalled).toBe(true);
    expect(result.syncError).toBe("network timeout");
  });

  it("does not throw when syncReliantProvider fails with non-Error", async () => {
    mockSyncReliantProvider.mockRejectedValue("unexpected failure");

    const result = await syncProviderIfNeeded("reliant_credits", true);

    expect(result.syncCalled).toBe(true);
    expect(result.syncError).toBe("unexpected failure");
  });
});
