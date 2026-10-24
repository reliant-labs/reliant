/**
 * useVersionInfo's server query lives under the generated SystemService key
 * scope, so invalidating `systemServiceKeys.all` refetches it.
 *
 * SystemService has no rpc that changes what Version answers and no stream
 * event, so nothing in the app invalidates it today; this pins that the
 * moment something does (or forge emits `systemServiceKeys.version`), the
 * service scope reaches the cached answer instead of leaving it stale.
 */

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import type { ReactNode } from "react";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { systemServiceKeys } from "../system-service-hooks_gen";
import { useVersionInfo } from "../useVersionInfo";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: false, staleTime: Infinity, gcTime: 0 } },
});

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

afterEach(() => {
  cleanup();
  queryClient.clear();
  setTransport(null);
});

describe("useVersionInfo keys", () => {
  it("refetches the server version when systemServiceKeys.all is invalidated", async () => {
    let version = "1.0.0";
    let calls = 0;
    setTransport(
      mockTransport({
        "SystemService.Version": () => {
          calls++;
          return { version, commit: "" };
        },
      }),
    );

    const { result } = renderHook(() => useVersionInfo(), { wrapper });
    await waitFor(() => expect(result.current.server).toEqual({ state: "known", version: "v1.0.0" }));
    expect(calls).toBe(1);

    version = "1.0.1";
    await queryClient.invalidateQueries({ queryKey: systemServiceKeys.all });

    await waitFor(() => expect(result.current.server).toEqual({ state: "known", version: "v1.0.1" }));
    expect(calls).toBe(2);
  });
});
