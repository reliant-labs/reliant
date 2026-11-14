/**
 * ListMyOrganizations is an authenticated RPC: a signed-out visitor (the
 * /auth page, the forge landing) must never fire it.
 */
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { createTestQueryClient } from "@/test/renderWithQuery";
import { useAuthStore } from "@/store/authStore";

const listMyOrganizations = vi.fn();
vi.mock("@/services/controlPlane/orgs", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/services/controlPlane/orgs")>()),
  listMyOrganizations: () => listMyOrganizations(),
}));
vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { useActiveOrg, useMyOrgs } from "../useOrgs";

function wrapper() {
  const client = createTestQueryClient();
  return ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children);
}

describe("useMyOrgs", () => {
  beforeEach(() => {
    listMyOrganizations.mockReset().mockResolvedValue({ orgs: [], defaultOrgId: "" });
    useAuthStore.setState({ user: null, session: null });
  });

  it("is disabled and sends no request while signed out", async () => {
    const { result } = renderHook(() => useMyOrgs(), { wrapper: wrapper() });
    await new Promise((r) => setTimeout(r, 20));
    expect(result.current.fetchStatus).toBe("idle");
    expect(listMyOrganizations).not.toHaveBeenCalled();
  });

  it("useActiveOrg stays idle and unloading while signed out", async () => {
    const { result } = renderHook(() => useActiveOrg(), { wrapper: wrapper() });
    await new Promise((r) => setTimeout(r, 20));
    expect(result.current.isLoading).toBe(false);
    expect(listMyOrganizations).not.toHaveBeenCalled();
  });

  it("fetches once a user signs in", async () => {
    const { result } = renderHook(() => useMyOrgs(), { wrapper: wrapper() });
    expect(listMyOrganizations).not.toHaveBeenCalled();
    act(() => useAuthStore.setState({ user: { id: "alice" } as never }));
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(listMyOrganizations).toHaveBeenCalledTimes(1);
  });
});
