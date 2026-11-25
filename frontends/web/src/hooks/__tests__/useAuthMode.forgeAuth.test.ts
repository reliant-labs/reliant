/**
 * forge sign-in (AUTH_PROVIDER "forge"): the health probe that names the
 * server's auth mode goes to reliant-api through this page's `/_api/@reliant`
 * (lib/connect reliantApiBaseUrl) and sends no bearer.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";

vi.mock("@/lib/auth/forge-api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/auth/forge-api")>()),
  isForgeAuth: () => true,
}));

import { useAuthMode } from "../useAuthMode";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("useAuthMode with forge sign-in", () => {
  it("probes /_api/@reliant/health on this origin with no Authorization header", async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ auth_mode: "supabase" }), { status: 200 }));
    vi.stubGlobal("fetch", fetchMock);
    const { result } = renderHook(() => useAuthMode());
    await waitFor(() => expect(result.current.loading).toBe(false));

    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit | undefined];
    expect(url).toBe(`${window.location.origin}/_api/@reliant/health`);
    expect(new Headers(init?.headers).has("Authorization")).toBe(false);
  });
});
