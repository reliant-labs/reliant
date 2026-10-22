/**
 * Authenticated settings queries must not fire while signed out.
 *
 * THE BUG THIS PINS: `useProviderStatuses` was a plain `useQuery` with no
 * `enabled` condition, so it fired whenever it was mounted — including on the
 * sign-in screen, where there is no token to send. In prod that produced a
 * stream of 401s against
 * `https://api.reliantapi.com/reliant.v1.SettingsService/GetProviderStatuses`
 * while the user was still moving from email entry to code entry.
 *
 * The assertion that matters is the negative one: a signed-out render issues
 * ZERO authenticated RPCs. It is written against the queryFn rather than the
 * hook's return value because a query can be `isLoading` for benign reasons —
 * only "the network call was made" distinguishes the bug from the fix.
 */
import { describe, expect, it, vi, beforeEach } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

const getProviders = vi.fn(async () => [] as unknown[]);
const getPreferences = vi.fn(async () => ({}) as Record<string, unknown>);

vi.mock("../../api/client", () => ({
  api: {
    settings: {
      getProviders: () => getProviders(),
      getPreferences: () => getPreferences(),
    },
  },
}));

// The auth store, driven by this test. `session` is the signal the gate reads:
// a user object without a committed session still cannot authenticate an RPC.
const authState: { session: unknown } = { session: null };
vi.mock("@/store/authStore", () => {
  const useAuthStore = <T,>(selector: (s: { session: unknown }) => T): T =>
    selector(authState);
  useAuthStore.getState = () => authState;
  return { useAuthStore };
});

import { useProviderStatuses, usePreferences } from "../settings-queries";

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  vi.clearAllMocks();
  authState.session = null;
});

describe("useProviderStatuses", () => {
  it("issues NO GetProviderStatuses call when signed out", async () => {
    const { result } = renderHook(() => useProviderStatuses(), { wrapper });

    // Give the query every chance to fire before asserting it did not.
    await new Promise((r) => setTimeout(r, 20));

    expect(getProviders).not.toHaveBeenCalled();
    // And it reports itself as parked, not as perpetually loading.
    expect(result.current.fetchStatus).toBe("idle");
  });

  it("fires exactly once when a session exists", async () => {
    authState.session = { access_token: "token-abc" };

    renderHook(() => useProviderStatuses(), { wrapper });

    await waitFor(() => expect(getProviders).toHaveBeenCalledTimes(1));
    await new Promise((r) => setTimeout(r, 20));
    expect(getProviders).toHaveBeenCalledTimes(1);
  });
});

describe("usePreferences", () => {
  it("issues NO GetPreferences call when signed out", async () => {
    renderHook(() => usePreferences(), { wrapper });

    await new Promise((r) => setTimeout(r, 20));

    expect(getPreferences).not.toHaveBeenCalled();
  });

  it("still serves its placeholder defaults while signed out", async () => {
    // The gate must not turn a preferences read into `undefined` for the
    // components that destructure it — they render worktree modals off these
    // values and would crash rather than degrade.
    const { result } = renderHook(() => usePreferences(), { wrapper });

    expect(result.current.data).toBeDefined();
    expect(result.current.data?.worktree.archiveMode).toBe("ask_me");
  });

  it("fires when a session exists", async () => {
    authState.session = { access_token: "token-abc" };

    renderHook(() => usePreferences(), { wrapper });

    await waitFor(() => expect(getPreferences).toHaveBeenCalledTimes(1));
  });
});
