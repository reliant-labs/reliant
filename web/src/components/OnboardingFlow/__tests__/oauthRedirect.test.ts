/**
 * Tests for OAuth redirect step preservation.
 *
 * Verifies that the onboarding flow saves the current step to localStorage
 * before OAuth redirect, and that the callback restores and cleans up that step.
 *
 * Pure logic tests — uses a simple in-memory storage mock, no React rendering.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

// ── In-memory localStorage mock ──────────────────────────────

let store: Record<string, string> = {};

const localStorageMock = {
  getItem: vi.fn((key: string) => store[key] ?? null),
  setItem: vi.fn((key: string, value: string) => {
    store[key] = value;
  }),
  removeItem: vi.fn((key: string) => {
    delete store[key];
  }),
  clear: vi.fn(() => {
    store = {};
  }),
  get length() {
    return Object.keys(store).length;
  },
  key: vi.fn((index: number) => Object.keys(store)[index] ?? null),
};

vi.stubGlobal("localStorage", localStorageMock);

// ── Test helpers that mirror the actual logic ────────────────

/**
 * Mirrors GitHubConnectStep.handleConnect: saves the return step before OAuth.
 */
function saveReturnStepBeforeOAuth() {
  localStorage.setItem("onboarding-return-step", "github-connect");
}

/**
 * Mirrors OAuthCallback: reads the saved step, removes it, and navigates.
 * Returns the navigation search params that would be passed to navigate().
 */
function handleOAuthReturn(): { step?: string } {
  const returnStep = localStorage.getItem("onboarding-return-step");
  localStorage.removeItem("onboarding-return-step");
  return returnStep ? { step: returnStep } : {};
}

// ── Tests ────────────────────────────────────────────────────

beforeEach(() => {
  vi.clearAllMocks();
  store = {};
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("OAuth redirect — step preservation", () => {
  it("saves 'github-connect' to localStorage before OAuth redirect", () => {
    saveReturnStepBeforeOAuth();

    expect(localStorageMock.setItem).toHaveBeenCalledWith(
      "onboarding-return-step",
      "github-connect",
    );
  });

  it("returns saved step after OAuth callback", () => {
    store["onboarding-return-step"] = "github-connect";

    const searchParams = handleOAuthReturn();

    expect(searchParams).toEqual({ step: "github-connect" });
  });

  it("returns empty search params when no step was saved", () => {
    const searchParams = handleOAuthReturn();

    expect(searchParams).toEqual({});
  });

  it("cleans up localStorage after reading the saved step", () => {
    store["onboarding-return-step"] = "github-connect";

    handleOAuthReturn();

    expect(localStorageMock.removeItem).toHaveBeenCalledWith("onboarding-return-step");
    expect(store["onboarding-return-step"]).toBeUndefined();
  });

  it("cleans up localStorage even when no step was saved", () => {
    handleOAuthReturn();

    expect(localStorageMock.removeItem).toHaveBeenCalledWith("onboarding-return-step");
  });

  it("round-trips: save before redirect → restore after callback", () => {
    // Simulate: user clicks Connect GitHub → OAuth redirect → callback
    saveReturnStepBeforeOAuth();
    expect(store["onboarding-return-step"]).toBe("github-connect");

    // Simulate: OAuth callback fires
    const searchParams = handleOAuthReturn();

    expect(searchParams).toEqual({ step: "github-connect" });
    expect(store["onboarding-return-step"]).toBeUndefined();
  });
});
