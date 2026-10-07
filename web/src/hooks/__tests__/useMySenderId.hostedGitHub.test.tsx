// Copyright (c) 2025 Reliant Labs

/**
 * "Only from: Me" on GitHub for a HOSTED account, end to end from the wire.
 *
 * A hosted account has no saved GitHub connection — its token is delegated by
 * control-plane — so "Me" comes from GetGitCredentialResponse.account_id. This
 * drives the real chain (cloud getCredential → useGitHubCredential →
 * useGitHubSender) from a generated control-plane message, mocking only the
 * transport, so it fails if any hop drops the id.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import type { ReactNode } from "react";

const getGitCredential = vi.fn();

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "http://127.0.0.1:8090",
  hasControlPlane: true,
}));
vi.mock("@/services/controlPlane/client", () => ({
  getControlPlaneClient: () => ({ getGitCredential }),
}));
// No saved GitHub connection: the hosted case.
vi.mock("@/hooks/connection-queries", () => ({
  useConnections: () => ({ isLoading: false, data: [] }),
}));

import { GetGitCredentialResponseSchema } from "@/gen/controlplane/services/git_credential/v1/git_credential_pb";
import { useGitHubSender } from "../useMySenderId";

function wrapper({ children }: { children: ReactNode }) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return <QueryClientProvider client={client}>{children}</QueryClientProvider>;
}

beforeEach(() => {
  getGitCredential.mockReset();
});

describe("useGitHubSender for a hosted GitHub account", () => {
  it("offers 'Me' as the numeric user id control-plane reports, shown by login", async () => {
    getGitCredential.mockResolvedValue(
      create(GetGitCredentialResponseSchema, {
        provider: "github",
        hasToken: true,
        accountLogin: "OctoCat",
        accountId: "583231",
      }),
    );

    const { result } = renderHook(() => useGitHubSender(), { wrapper });

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current).toEqual({ loading: false, id: "583231", displayName: "OctoCat" });
  });

  it("keeps the clear message when control-plane could not resolve the id", async () => {
    getGitCredential.mockResolvedValue(
      create(GetGitCredentialResponseSchema, { provider: "github", hasToken: true, accountLogin: "OctoCat" }),
    );

    const { result } = renderHook(() => useGitHubSender(), { wrapper });

    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.id).toBeUndefined();
    expect(result.current.missing).toBe(
      `Your GitHub sign-in doesn't report your GitHub user id yet, so "Me" isn't available. Add your login (OctoCat) instead.`,
    );
  });
});
