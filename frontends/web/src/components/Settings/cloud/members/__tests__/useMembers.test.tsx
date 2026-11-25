/**
 * The roster and invitation reads on OrganizationService's generated hooks:
 * mapped into the page's model, refreshed per org, and dropped (not shown in
 * the next org) on an org switch, while the person's own org list survives it.
 */
import { renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { QueryClientProvider, type QueryClient } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));

import { organizationServiceKeys } from "@/hooks/controlplane/organization-service-hooks_gen";
import { setTransport } from "@/lib/connect";
import { resetAfterLeavingOrg } from "@/lib/orgScope";
import { mockTransport } from "@/lib/test-utils";
import { ORG_MEMBER_READS } from "@/services/controlPlane/members";
import { orgKeys } from "@/services/controlPlane/orgs";
import { createTestQueryClient } from "@/test/renderWithQuery";

import { useInvitations, useMembers, useRefreshMembers } from "../useMembers";

const listMembers = vi.fn((req: { orgId: string }) => ({
  members: [{ userId: `u-${req.orgId}`, email: "ada@acme.dev", name: "Ada", role: "owner" }],
  seats: { members: 1, pendingInvitations: 1, limit: 3 },
}));
const listInvitations = vi.fn((_req: { orgId: string }) => ({ invitations: [{ id: "i1", email: "sam@acme.dev", status: "pending" }] }));

let client: QueryClient;
const wrapper = ({ children }: { children: ReactNode }) => createElement(QueryClientProvider, { client }, children);

beforeEach(() => {
  client = createTestQueryClient();
  listMembers.mockClear();
  listInvitations.mockClear();
  setTransport(
    mockTransport({
      "OrganizationService.ListMembers": (req) => listMembers(req as { orgId: string }),
      "OrganizationService.ListInvitations": (req) => listInvitations(req as { orgId: string }),
    }),
  );
});
afterEach(() => setTransport(null));

describe("useMembers / useInvitations", () => {
  it("read the org they name and map into the page's model", async () => {
    const members = renderHook(() => useMembers("org-a"), { wrapper });
    const invitations = renderHook(() => useInvitations("org-a"), { wrapper });
    await waitFor(() => expect(members.result.current.isSuccess && invitations.result.current.isSuccess).toBe(true));
    expect(listMembers).toHaveBeenCalledWith(expect.objectContaining({ orgId: "org-a", pageSize: 200 }));
    expect(members.result.current.data?.pages[0]).toMatchObject({
      members: [{ userId: "u-org-a", name: "Ada", joinedAt: null }],
      seats: { members: 1, pendingInvitations: 1, limit: 3 },
    });
    expect(invitations.result.current.data).toEqual([expect.objectContaining({ id: "i1", status: "pending", createdAt: null })]);
  });

  it("send nothing with no org, and invitations nothing when not enabled", async () => {
    renderHook(() => useMembers(""), { wrapper });
    renderHook(() => useInvitations("org-a", false), { wrapper });
    await new Promise((r) => setTimeout(r, 20));
    expect(listMembers).not.toHaveBeenCalled();
    expect(listInvitations).not.toHaveBeenCalled();
  });

  it("a refresh re-reads that org's roster and invitations, not another org's", async () => {
    renderHook(() => useMembers("org-a"), { wrapper });
    renderHook(() => useInvitations("org-a"), { wrapper });
    renderHook(() => useMembers("org-b"), { wrapper });
    await waitFor(() => expect(listMembers).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(listInvitations).toHaveBeenCalledTimes(1));
    const refresh = renderHook(() => useRefreshMembers("org-a"), { wrapper });
    await refresh.result.current();
    expect(listMembers.mock.calls.map(([req]) => req.orgId)).toEqual(["org-a", "org-b", "org-a"]);
    expect(listInvitations).toHaveBeenCalledTimes(2);
  });

  it("an org switch drops the roster reads by their keys, and keeps the person's org list", () => {
    client.setQueryData(organizationServiceKeys.listMembers({ orgId: "org-a", pageSize: 1 }), { members: [] } as never);
    client.setQueryData(organizationServiceKeys.listInvitations({ orgId: "org-a", pageSize: 200 }), { invitations: [] } as never);
    client.setQueryData(orgKeys.mine, { organizations: [], defaultOrgId: "" } as never);
    for (const queryKey of ORG_MEMBER_READS) void resetAfterLeavingOrg(client, queryKey, "org-a");
    expect(client.getQueryData(organizationServiceKeys.listMembers({ orgId: "org-a", pageSize: 1 }))).toBeUndefined();
    expect(client.getQueryData(organizationServiceKeys.listInvitations({ orgId: "org-a", pageSize: 200 }))).toBeUndefined();
    expect(client.getQueryData(orgKeys.mine)).toBeDefined();
  });
});
