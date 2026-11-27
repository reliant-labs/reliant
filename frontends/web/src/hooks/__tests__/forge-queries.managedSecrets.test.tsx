// Copyright (c) 2025 Reliant Labs

/**
 * useManagedSecrets at the transport boundary: a hosted env's lookup reaches
 * SecretStoreService with its environment id, and a non-hosted env resolves to
 * its availability WITHOUT a single RPC.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";

const listSecretsRpc = vi.fn();

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://127.0.0.1:8090",
}));

import { forgeCloudKeys, managedSecretsQueryOptions, useManagedSecrets, useSetManagedSecret } from "../forge-queries";
import { secretStoreServiceKeys } from "../controlplane/secret-store-service-hooks_gen";
import { managedStoreTarget } from "@/services/forge/secretStore";

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

/** A client whose default WOULD retry, so the read's own `retry: false` is what is tested. */
function retryingWrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: 3, retryDelay: 0 } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  listSecretsRpc.mockReset().mockResolvedValue({ secrets: [{ name: "DATABASE_URL", currentVersion: 1 }] });
  setTransport(mockTransport({ "SecretStoreService.ListSecrets": (req: unknown) => listSecretsRpc(req) }));
});
afterEach(() => setTransport(null));

describe("useManagedSecrets", () => {
  it("calls SecretStoreService with the hosted env's environment_id", async () => {
    const target = managedStoreTarget({
      destination: "hosted",
      endpoint: "http://127.0.0.1:8090",
      environment_id: "denv_42",
    });
    const { result } = renderHook(() => useManagedSecrets("cloud", target), { wrapper });
    await waitFor(() => expect(result.current.data?.availability).toBe("available"));
    expect(listSecretsRpc).toHaveBeenCalledTimes(1);
    expect(listSecretsRpc).toHaveBeenCalledWith(expect.objectContaining({ environmentId: "denv_42" }));
    expect(result.current.data?.secrets.map((s) => s.name)).toEqual(["DATABASE_URL"]);
  });

  it("loads a cloud-only environment without a local project and separates environment cache entries", async () => {
    const target = managedStoreTarget({ destination: "hosted", endpoint: "http://127.0.0.1:8090", environment_id: "denv_cloud" });
    const { result } = renderHook(() => useManagedSecrets("prod", target), { wrapper });
    await waitFor(() => expect(result.current.data?.availability).toBe("available"));
    expect(listSecretsRpc).toHaveBeenCalledWith(expect.objectContaining({ environmentId: "denv_cloud" }));
    const other = managedStoreTarget({ destination: "hosted", endpoint: "http://127.0.0.1:8090", environment_id: "denv_other_org" });
    expect(managedSecretsQueryOptions("prod", target).queryKey).not.toEqual(managedSecretsQueryOptions("prod", other).queryKey);
  });

  it("makes NO call for a non-hosted env, and reports not-hosted", async () => {
    const target = managedStoreTarget({ destination: "cluster", kube_context: "gke" } as never);
    const { result } = renderHook(() => useManagedSecrets("prod", target), { wrapper });
    await waitFor(() => expect(result.current.data?.availability).toBe("not-hosted"));
    expect(listSecretsRpc).not.toHaveBeenCalled();
    expect(result.current.data?.secrets).toEqual([]);
  });

  it("makes NO call for a hosted env forge reported without an id", async () => {
    const target = managedStoreTarget({ destination: "hosted", endpoint: "http://127.0.0.1:8090" });
    const { result } = renderHook(() => useManagedSecrets("cloud", target), { wrapper });
    await waitFor(() => expect(result.current.data?.availability).toBe("not-ensured"));
    expect(listSecretsRpc).not.toHaveBeenCalled();
  });

  it.each([
    [Code.Unavailable, "not-configured"],
    [Code.Unimplemented, "not-configured"],
    [Code.Internal, "unreachable"],
  ] as const)("resolves a %s failure into availability %s, with no retry and no error thrown", async (code, availability) => {
    listSecretsRpc.mockRejectedValue(new ConnectError("no store", code));
    const target = managedStoreTarget({ destination: "hosted", endpoint: "http://127.0.0.1:8090", environment_id: "denv_42" });
    const { result } = renderHook(() => useManagedSecrets("cloud", target), { wrapper: retryingWrapper });
    await waitFor(() => expect(result.current.data?.availability).toBe(availability));
    expect(result.current.data?.secrets).toEqual([]);
    expect(result.current.data?.protected).toBe(false);
    expect(listSecretsRpc).toHaveBeenCalledTimes(1);
  });

  it("does nothing at all until the topology has decided the target", () => {
    const { result } = renderHook(() => useManagedSecrets("cloud", null), { wrapper });
    expect(result.current.fetchStatus).toBe("idle");
    expect(listSecretsRpc).not.toHaveBeenCalled();
  });
});

describe("useSetManagedSecret", () => {
  it("sends the value as secret_value keyed on the environment, and refreshes only that environment's reads", async () => {
    const setSecretRpc = vi.fn().mockResolvedValue({ version: 4 });
    setTransport(mockTransport({ "SecretStoreService.SetSecret": (req: unknown) => setSecretRpc(req) }));
    const client = new QueryClient();
    const keys = {
      list: secretStoreServiceKeys.listSecrets({ environmentId: "denv_1" }),
      history: secretStoreServiceKeys.getSecretVersions({ environmentId: "denv_1", name: "K" }),
      otherList: secretStoreServiceKeys.listSecrets({ environmentId: "denv_2" }),
      otherHistory: secretStoreServiceKeys.getSecretVersions({ environmentId: "denv_2", name: "K" }),
      live: forgeCloudKeys.liveView("org", "shop"),
    };
    for (const key of Object.values(keys)) client.setQueryData(key, {});
    const { result } = renderHook(() => useSetManagedSecret("denv_1"), {
      wrapper: ({ children }: { children: ReactNode }) => <QueryClientProvider client={client}>{children}</QueryClientProvider>,
    });

    await expect(result.current.mutateAsync({ name: "K", value: "v", cas: 0 })).resolves.toEqual({ version: 4, createdAt: undefined });
    expect(setSecretRpc).toHaveBeenCalledWith(
      expect.objectContaining({ environmentId: "denv_1", name: "K", secretValue: "v", cas: 0 })
    );
    const stale = (key: readonly unknown[]) => client.getQueryState(key)?.isInvalidated;
    expect([stale(keys.list), stale(keys.history), stale(keys.live)]).toEqual([true, true, true]);
    expect([stale(keys.otherList), stale(keys.otherHistory)]).toEqual([false, false]);
  });

  it("refuses a write with no environment id as the mutation's own error, sending nothing", async () => {
    const setSecretRpc = vi.fn();
    setTransport(mockTransport({ "SecretStoreService.SetSecret": (req: unknown) => setSecretRpc(req) }));
    const { result } = renderHook(() => useSetManagedSecret(null), { wrapper });
    await expect(result.current.mutateAsync({ name: "K", value: "v" })).rejects.toThrow("no managed store");
    expect(setSecretRpc).not.toHaveBeenCalled();
  });
});
