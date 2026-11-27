// Copyright (c) 2025 Reliant Labs

import type { ReactNode } from "react";
import { act, renderHook } from "@testing-library/react";
import { QueryClient } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";

import { DeployAttemptState } from "@/gen/controlplane/controlplane/v1/deploy_pb";
import { mockTransport } from "@/lib/test-utils";
import { createTransportScope, TransportScopeProvider } from "@/lib/transportScope";

import { useDeployRuns } from "./release-queries";

vi.mock("@/lib/orgScope", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/orgScope")>()),
  useOrgScope: () => ({ ready: true, orgId: "org-polling", error: null, retry: () => {} }),
}));

afterEach(() => {
  vi.useRealTimers();
});

it("keeps requesting deploy runs every ten seconds while an attempt remains running", async () => {
  vi.useFakeTimers();
  vi.setSystemTime(new Date("2026-01-01T00:00:00Z"));
  const list = vi.fn(() => ({
    attempts: [{ id: "deploy-running", environmentName: "prod", state: DeployAttemptState.RUNNING }],
  }));
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: Infinity } } });
  const scope = createTransportScope({
    id: "deploy-polling-test",
    queryClient: client,
    transport: mockTransport({ "DeployService.ListDeployAttempts": list }),
  });
  const wrapper = ({ children }: { children: ReactNode }) => <TransportScopeProvider scope={scope}>{children}</TransportScopeProvider>;
  const { result, unmount } = renderHook(() => useDeployRuns("shop", "prod"), { wrapper });
  try {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(list).toHaveBeenCalledTimes(1);

    for (let requestCount = 2; requestCount <= 4; requestCount++) {
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_000);
      });
      expect(list).toHaveBeenCalledTimes(requestCount);
      expect(result.current.data?.[0]?.state).toBe("running");
    }
  } finally {
    unmount();
    client.clear();
  }
});
