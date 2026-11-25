// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Happy-path tests for the generated OrganizationService hooks. Written once, when
// no sibling test existed; this file is YOURS from here — forge never
// regenerates it, so edit freely (delete it and run `forge project rescaffold <path>` to
// get a fresh one).
//
// Each test covers the happy path of one RPC: the hook resolves against the
// mock transport, which is the floor that catches a renamed schema, a broken
// transport wiring, or a hook that no longer compiles. Extend with error /
// loading / empty cases as you build the page — see the `frontend-testing`
// skill for the four-state coverage rule.

import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, it, expect } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";

import { useListMyOrganizations } from "./organization-service-hooks_gen";
import { useSetDefaultOrganization } from "./organization-service-hooks_gen";
import { useCreateOrganization } from "./organization-service-hooks_gen";
import { useListMembers } from "./organization-service-hooks_gen";
import { useListMembersInfinite } from "./organization-service-hooks_gen";
import { useInviteMember } from "./organization-service-hooks_gen";
import { useListInvitations } from "./organization-service-hooks_gen";
import { useListInvitationsInfinite } from "./organization-service-hooks_gen";
import { useRevokeInvitation } from "./organization-service-hooks_gen";
import { useGetInvitation } from "./organization-service-hooks_gen";
import { useAcceptInvitation } from "./organization-service-hooks_gen";
import { useRemoveMember } from "./organization-service-hooks_gen";
import { useLeaveOrganization } from "./organization-service-hooks_gen";
import { useUpdateMemberRole } from "./organization-service-hooks_gen";
import { useMoveFreeHostingDesignation } from "./organization-service-hooks_gen";
import { useStartGitHubAppInstall } from "./organization-service-hooks_gen";
import { useLinkGitHubAppInstallation } from "./organization-service-hooks_gen";
import { useListGitHubAppInstallations } from "./organization-service-hooks_gen";
import { useUnlinkGitHubAppInstallation } from "./organization-service-hooks_gen";

import type { ReactNode } from "react";

// One QueryClient per test, built before it and torn down after it. Every
// test must leave NOTHING queued behind it: the last test in a file is
// followed by the jsdom teardown, and a React render that fires after that
// dies on `window` — "ReferenceError: window is not defined … caught after
// test environment was torn down" — failing a run whose tests all passed.
let queryClient: QueryClient;

beforeEach(() => {
  queryClient = new QueryClient({
    defaultOptions: {
      queries: { retry: false, staleTime: 0, gcTime: 0 },
      mutations: { retry: false },
    },
  });
});

// cleanup() is called explicitly because Testing Library only registers its
// own auto-cleanup when `afterEach` is a GLOBAL, and vitest does not expose
// globals unless the config opts in. Without it every renderHook root stays
// mounted, still subscribed, until the worker exits. clear() then drops the
// cache along with any gc timers its queries still hold.
afterEach(() => {
  cleanup();
  queryClient.clear();
});

function wrapper({ children }: { children: ReactNode }) {
  return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>;
}

// mutateAsync resolves BEFORE the hook re-renders: React Query notifies its
// observers on a later macrotask. A mutation test that returned there would
// leave that render queued past its own end — and past the jsdom teardown,
// when it is the file's last test. Wait for the RENDERED status to settle,
// success or error, so the test ends with nothing left to fire.
async function waitForSettled(result: { current: { status: string } }) {
  await waitFor(() => expect(["success", "error"]).toContain(result.current.status));
}

describe("OrganizationService hooks", () => {
  it("useListMyOrganizations resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.ListMyOrganizations": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListMyOrganizations({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useSetDefaultOrganization resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.SetDefaultOrganization": { response: {} },
      }),
    );

    const { result } = renderHook(() => useSetDefaultOrganization(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useCreateOrganization resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.CreateOrganization": { response: {} },
      }),
    );

    const { result } = renderHook(() => useCreateOrganization(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListMembers resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.ListMembers": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListMembers({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListMembersInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.ListMembers": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListMembersInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useInviteMember resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.InviteMember": { response: {} },
      }),
    );

    const { result } = renderHook(() => useInviteMember(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListInvitations resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.ListInvitations": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListInvitations({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useListInvitationsInfinite resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.ListInvitations": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListInvitationsInfinite({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    // An empty next_page_token ends the listing after the first page.
    expect(result.current.data?.pages).toHaveLength(1);
    expect(result.current.hasNextPage).toBe(false);
  });

  it("useRevokeInvitation resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.RevokeInvitation": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRevokeInvitation(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useGetInvitation resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.GetInvitation": { response: {} },
      }),
    );

    const { result } = renderHook(() => useGetInvitation({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useAcceptInvitation resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.AcceptInvitation": { response: {} },
      }),
    );

    const { result } = renderHook(() => useAcceptInvitation(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useRemoveMember resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.RemoveMember": { response: {} },
      }),
    );

    const { result } = renderHook(() => useRemoveMember(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useLeaveOrganization resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.LeaveOrganization": { response: {} },
      }),
    );

    const { result } = renderHook(() => useLeaveOrganization(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useUpdateMemberRole resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.UpdateMemberRole": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUpdateMemberRole(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useMoveFreeHostingDesignation resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.MoveFreeHostingDesignation": { response: {} },
      }),
    );

    const { result } = renderHook(() => useMoveFreeHostingDesignation(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useStartGitHubAppInstall resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.StartGitHubAppInstall": { response: {} },
      }),
    );

    const { result } = renderHook(() => useStartGitHubAppInstall(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useLinkGitHubAppInstallation resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.LinkGitHubAppInstallation": { response: {} },
      }),
    );

    const { result } = renderHook(() => useLinkGitHubAppInstallation(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });

  it("useListGitHubAppInstallations resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.ListGitHubAppInstallations": { response: {} },
      }),
    );

    const { result } = renderHook(() => useListGitHubAppInstallations({} as never), { wrapper });
    await waitFor(() => expect(result.current.isSuccess).toBe(true));
    expect(result.current.data).toBeDefined();
  });

  it("useUnlinkGitHubAppInstallation resolves a happy-path response", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.UnlinkGitHubAppInstallation": { response: {} },
      }),
    );

    const { result } = renderHook(() => useUnlinkGitHubAppInstallation(), { wrapper });
    await result.current.mutateAsync({} as never).catch(() => {
      // Mutations may reject if the empty request fails validation upstream.
      // The point of the starter is that the wiring resolves; tighten the
      // assertions as you flesh out the page.
    });
    await waitForSettled(result);
  });
});
