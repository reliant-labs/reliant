// Copyright (c) 2025 Reliant Labs

/**
 * useManagedSecrets at the transport boundary: a hosted env's lookup reaches
 * SecretStoreService with its environment id, and a non-hosted env resolves to
 * its availability WITHOUT a single RPC.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

const listSecretsRpc = vi.fn();

vi.mock("@/services/controlPlane/client", () => ({
  getControlPlaneClient: () => ({ listSecrets: listSecretsRpc }),
}));
vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://127.0.0.1:8090",
}));

import { useManagedSecrets } from "../forge-queries";
import { managedStoreTarget } from "@/services/forge/secretStore";

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  listSecretsRpc.mockReset().mockResolvedValue({ secrets: [{ name: "DATABASE_URL", currentVersion: 1 }] });
});

describe("useManagedSecrets", () => {
  it("calls SecretStoreService with the hosted env's environment_id", async () => {
    const target = managedStoreTarget({
      destination: "hosted",
      endpoint: "http://127.0.0.1:8090",
      environment_id: "denv_42",
    });
    const { result } = renderHook(() => useManagedSecrets("proj", "cloud", target), { wrapper });
    await waitFor(() => expect(result.current.data?.availability).toBe("available"));
    expect(listSecretsRpc).toHaveBeenCalledTimes(1);
    expect(listSecretsRpc).toHaveBeenCalledWith({ environmentId: "denv_42" });
    expect(result.current.data?.secrets.map((s) => s.name)).toEqual(["DATABASE_URL"]);
  });

  it("makes NO call for a non-hosted env, and reports not-hosted", async () => {
    const target = managedStoreTarget({ destination: "cluster", kube_context: "gke" } as never);
    const { result } = renderHook(() => useManagedSecrets("proj", "prod", target), { wrapper });
    await waitFor(() => expect(result.current.data?.availability).toBe("not-hosted"));
    expect(listSecretsRpc).not.toHaveBeenCalled();
    expect(result.current.data?.secrets).toEqual([]);
  });

  it("makes NO call for a hosted env forge reported without an id", async () => {
    const target = managedStoreTarget({ destination: "hosted", endpoint: "http://127.0.0.1:8090" });
    const { result } = renderHook(() => useManagedSecrets("proj", "cloud", target), { wrapper });
    await waitFor(() => expect(result.current.data?.availability).toBe("not-ensured"));
    expect(listSecretsRpc).not.toHaveBeenCalled();
  });

  it("does nothing at all until the topology has decided the target", () => {
    const { result } = renderHook(() => useManagedSecrets("proj", "cloud", null), { wrapper });
    expect(result.current.fetchStatus).toBe("idle");
    expect(listSecretsRpc).not.toHaveBeenCalled();
  });
});
