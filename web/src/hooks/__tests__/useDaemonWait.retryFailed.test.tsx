import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { QueryClientProvider } from "@tanstack/react-query";

import { createTestQueryClient } from "@/test/renderWithQuery";
import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";

// "Try again" on a machine that FAILED to start used to refetch the same FAILED
// row and nothing else, so a machine whose pod crash-looped on a bad release
// stayed failed no matter how often the user retried. It must call ResumeDaemon,
// which the control plane treats as a retry (rebuild the pod on the current
// image) for a Failed machine.

vi.mock("@/services/controlPlane/capabilities", () => ({
  capabilities: { cloudDaemons: true },
}));

const listDaemons = vi.fn();
vi.mock("@/api/grpc-client", () => ({
  grpcClient: { daemonRegistry: () => ({ listDaemons }) },
}));

const resumeDaemon = vi.fn();
vi.mock("@/services/controlPlane/daemon", () => ({
  resumeDaemon: (id: string) => resumeDaemon(id),
}));

import { useDaemonWait } from "../useDaemonWait";

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={createTestQueryClient()}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  listDaemons.mockReset();
  resumeDaemon.mockReset();
  resumeDaemon.mockResolvedValue(undefined);
});

afterEach(() => cleanup());

describe("useDaemonWait.retryNow on a failed machine", () => {
  it("resumes the failed machine instead of only refetching", async () => {
    listDaemons.mockResolvedValue({
      daemons: [{ daemonId: "d-failed", status: DaemonStatus.FAILED, lastStatusMessage: "" }],
    });
    const onRetry = vi.fn();
    const { result } = renderHook(() => useDaemonWait({ waiting: true, onRetry }), { wrapper });
    await waitFor(() => expect(result.current.state?.tone).toBe("failed"));

    act(() => result.current.retryNow());

    await waitFor(() => expect(resumeDaemon).toHaveBeenCalledWith("d-failed"));
    await waitFor(() => expect(onRetry).toHaveBeenCalled());
  });

  it("does not resume a machine that has not failed", async () => {
    listDaemons.mockResolvedValue({
      daemons: [{ daemonId: "d-ok", status: DaemonStatus.PENDING, lastStatusMessage: "" }],
    });
    const { result } = renderHook(() => useDaemonWait({ waiting: true }), { wrapper });
    await waitFor(() => expect(result.current.daemon?.daemonId).toBe("d-ok"));

    act(() => result.current.retryNow());

    expect(resumeDaemon).not.toHaveBeenCalled();
  });
});
