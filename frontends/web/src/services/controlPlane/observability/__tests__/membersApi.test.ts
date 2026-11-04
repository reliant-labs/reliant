// Copyright (c) 2025 Reliant Labs

import { beforeEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { IssuesService } from "@/gen/controlplane/services/issues/v1/issues_pb";
import { OrganizationService } from "@/gen/controlplane/services/organization/v1/organization_pb";
import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";
import { getCurrentIssueUser, listOrgMembers } from "../membersApi";

const clients = vi.hoisted(() => ({
  issues: { listAssignableUsers: vi.fn() },
  organizations: { listMyOrganizations: vi.fn() },
  user: { getCurrentUser: vi.fn() },
}));
vi.mock("../../client", () => ({ getControlPlaneClient: (service: { typeName: string }) => {
  if (service.typeName === IssuesService.typeName) return clients.issues;
  if (service.typeName === OrganizationService.typeName) return clients.organizations;
  if (service.typeName === UserService.typeName) return clients.user;
  throw new Error(`Unexpected service ${service.typeName}`);
} }));

beforeEach(() => {
  vi.resetAllMocks();
  clients.issues.listAssignableUsers.mockResolvedValue({ users: [{ id: "b-user", name: "B member" }], nextPageToken: "" });
  clients.organizations.listMyOrganizations.mockResolvedValue({ defaultOrgId: "a", organizations: [{ id: "a", role: "admin" }, { id: "b", role: "member" }] });
});

describe("explicit issue organization roster composition", () => {
  it("reads all assignable pages for B, deduplicates people and selects B's caller role, not default A", async () => {
    clients.issues.listAssignableUsers.mockResolvedValueOnce({ users: [{ id: "b1", name: "B first" }], nextPageToken: "next-b" })
      .mockResolvedValueOnce({ users: [{ id: "b1", name: "B first" }, { id: "b2", name: "" }], nextPageToken: "" });
    expect(await listOrgMembers("b")).toEqual({ users: [{ id: "b1", name: "B first" }, { id: "b2", name: "b2" }], callerRole: "member" });
    expect(clients.issues.listAssignableUsers.mock.calls).toEqual([[{ orgId: "b", pageSize: 100, pageToken: "" }], [{ orgId: "b", pageSize: 100, pageToken: "next-b" }]]);
    expect(clients.organizations.listMyOrganizations).toHaveBeenCalledWith({});
  });

  it.each(["admin", "owner"])("retains the caller's %s role in the explicitly requested org", async (role) => {
    clients.organizations.listMyOrganizations.mockResolvedValue({ defaultOrgId: "a", organizations: [{ id: "a", role: "member" }, { id: "b", role }] });
    expect((await listOrgMembers("b")).callerRole).toBe(role);
  });

  it("keeps the roster but no admin role when membership lookup is refused (including machine credentials)", async () => {
    clients.organizations.listMyOrganizations.mockRejectedValue(new ConnectError("person required", Code.PermissionDenied));
    expect(await listOrgMembers("b")).toEqual({ users: [{ id: "b-user", name: "B member" }], callerRole: null });
  });

  it("never borrows a role from another org if the requested membership is missing", async () => {
    clients.organizations.listMyOrganizations.mockResolvedValue({ defaultOrgId: "a", organizations: [{ id: "a", role: "owner" }] });
    expect((await listOrgMembers("b")).callerRole).toBeNull();
  });

  it("rejects a denied later roster page instead of silently returning a truncated roster", async () => {
    clients.issues.listAssignableUsers.mockResolvedValueOnce({ users: [{ id: "b1", name: "first" }], nextPageToken: "next" })
      .mockRejectedValueOnce(new ConnectError("revoked", Code.PermissionDenied));
    await expect(listOrgMembers("b")).rejects.toMatchObject({ code: Code.PermissionDenied });
  });

  it("requires an explicit org without making any default-org request", async () => {
    await expect(listOrgMembers("")).rejects.toThrow("explicit issue organization");
    expect(clients.issues.listAssignableUsers).not.toHaveBeenCalled();
    expect(clients.organizations.listMyOrganizations).not.toHaveBeenCalled();
  });

  it("names a non-person reader as null rather than inventing a human role", async () => {
    clients.user.getCurrentUser.mockResolvedValue({});
    expect(await getCurrentIssueUser()).toBeNull();
  });
});
