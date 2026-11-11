/**
 * Settings › Members, over the real generated clients on a mock transport.
 *
 * Pinned: an admin sees the roster with roles, the seat count, pending
 * invitations with resend and revoke, and can change a role or remove someone,
 * every call naming the org being looked at; a plain member sees the roster
 * and nothing that would only be refused; leaving switches the app to the
 * default org the server returns; a refusal shows its human words; admins get
 * the membership history.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

vi.mock("@/services/controlPlane/config", () => ({ CONTROL_PLANE_API_URL: "https://cp.test", hasControlPlane: true }));
const toastSuccess = vi.fn();
vi.mock("@/lib/toast-manager", () => ({ toast: { success: (m: string) => toastSuccess(m), error: vi.fn() } }));

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { renderWithQuery } from "@/test/renderWithQuery";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import { useAuthStore } from "@/store/authStore";

import { MembersPage } from "../MembersPage";

const ORG = "org-a";
const refusal = (reason: string, code = Code.FailedPrecondition) =>
  new ConnectError("server prose", code, { "x-forge-error-reason": reason });

function myOrgs(role: string) {
  return {
    organizations: [
      { id: ORG, name: "Acme", slug: "acme", role, isOwner: role === "owner", isDefault: true },
      { id: "org-b", name: "Side", slug: "side", role: "owner", isOwner: true, isDefault: false },
    ],
    defaultOrgId: ORG,
  };
}

const ROSTER = {
  members: [
    { userId: "u_ada", email: "ada@example.com", name: "Ada", role: "owner", isFounder: true, joinedAt: timestampFromDate(new Date("2026-01-01T00:00:00Z")) },
    { userId: "u_bob", email: "bob@example.com", name: "", role: "member" },
  ],
  seats: { members: 2, pendingInvitations: 1, limit: 3 },
};

const INVITES = {
  invitations: [
    { id: "inv_carol", email: "carol@example.com", role: "admin", status: "pending", invitedByEmail: "ada@example.com", expiresAt: timestampFromDate(new Date(Date.now() + 5 * 86_400_000)) },
  ],
};

type Handlers = Parameters<typeof mockTransport>[0];

function setup(role = "owner", overrides: Handlers = {}) {
  const calls = {
    updateRole: vi.fn((_req: unknown) => ({ member: { userId: "u_bob", role: "admin" }, scopes: ["deploy:read"] })),
    remove: vi.fn((_req: unknown) => ({ revokedTokenCount: 2 })),
    revoke: vi.fn((_req: unknown) => ({})),
    invite: vi.fn((_req: unknown) => ({ emailSent: true, resent: true, acceptUrl: "https://app.test/accept-invite#rinv_x" })),
    invitations: vi.fn((_req: unknown) => INVITES),
    leave: vi.fn((_req: unknown) => ({ defaultOrgId: "org-b" })),
    activity: vi.fn((_req: unknown) => ({
      events: [
        { id: "e1", verb: "invite", actor: { id: "u_ada", name: "Ada" }, target: { kind: "member", id: "inv_carol", label: "carol@example.com" }, detail: { role: "admin" }, via: "console", at: timestampFromDate(new Date()) },
        { id: "e2", verb: "set_role", actor: { id: "u_ada", name: "Ada" }, target: { kind: "member", id: "u_bob" }, detail: { role: "member" }, via: "console", at: timestampFromDate(new Date()) },
      ],
    })),
  };
  setTransport(
    mockTransport({
      "OrganizationService.ListMyOrganizations": { response: myOrgs(role) },
      "OrganizationService.ListMembers": { response: ROSTER },
      "OrganizationService.ListInvitations": calls.invitations,
      "OrganizationService.UpdateMemberRole": calls.updateRole,
      "OrganizationService.RemoveMember": calls.remove,
      "OrganizationService.RevokeInvitation": calls.revoke,
      "OrganizationService.InviteMember": calls.invite,
      "OrganizationService.LeaveOrganization": calls.leave,
      "ActivityService.ListMembershipActivity": calls.activity,
      "AccessTokenService.ListOrgMemberGrants": { response: { members: [], grantable: [] } },
      ...overrides,
    }),
  );
  const view = renderWithQuery(<MembersPage />);
  return { ...view, calls };
}

beforeEach(() => {
  localStorage.clear();
  toastSuccess.mockReset();
  setTransport(null);
  useActiveOrgStore.setState({ userId: "u_ada", chosenId: ORG, effectiveId: ORG });
  useAuthStore.setState({ user: { id: "u_ada", email: "ada@example.com" } as never });
});

describe("MembersPage (admin)", () => {
  it("shows the roster with roles, the founder, you, and the seats in use", async () => {
    setup("owner");
    const ada = await screen.findByTestId("member-u_ada");
    expect(ada).toHaveTextContent("Ada");
    expect(ada).toHaveTextContent("(you)");
    expect(ada).toHaveTextContent("Founder");
    expect(screen.getByTestId("member-u_bob")).toHaveTextContent("bob@example.com");
    expect(screen.getByRole("combobox", { name: "Role for bob@example.com" })).toHaveValue("member");
    expect(screen.getByTestId("members-seats")).toHaveTextContent("3 of 3 seats (1 pending)");
    expect(screen.getByTestId("members-seats")).toHaveTextContent("every seat is taken");
  });

  it("never offers to remove yourself: that is Leave", async () => {
    setup("owner");
    await screen.findByTestId("member-u_bob");
    expect(screen.queryByRole("button", { name: "Remove Ada" })).toBeNull();
    expect(screen.getByRole("button", { name: "Remove bob@example.com" })).toBeInTheDocument();
  });

  it("changes a role in the org being looked at", async () => {
    const { calls } = setup("owner");
    await userEvent.selectOptions(await screen.findByRole("combobox", { name: "Role for bob@example.com" }), "admin");
    await waitFor(() => expect(calls.updateRole).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG, userId: "u_bob", role: "admin" })));
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(expect.stringMatching(/now an admin.*reset/)));
  });

  it("removes a member after confirming, and says how many tokens went with them", async () => {
    const { calls } = setup("owner");
    await userEvent.click(await screen.findByRole("button", { name: "Remove bob@example.com" }));
    expect(screen.getByText(/Access tokens they made for this organization/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Remove" }));
    await waitFor(() => expect(calls.remove).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG, userId: "u_bob" })));
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(expect.stringMatching(/2 access tokens/)));
  });

  it("shows the refusal's words when a change is refused", async () => {
    setup("owner", { "OrganizationService.UpdateMemberRole": { error: refusal("last_owner") } });
    await userEvent.selectOptions(await screen.findByRole("combobox", { name: "Role for bob@example.com" }), "admin");
    expect(await screen.findByTestId("members-error")).toHaveTextContent(/at least one owner/);
  });

  it("an admin cannot touch an owner or make one", async () => {
    setup("admin");
    await screen.findByTestId("member-u_bob");
    // Ada is an owner: shown, not editable.
    expect(screen.queryByRole("combobox", { name: "Role for Ada" })).toBeNull();
    const bob = screen.getByRole("combobox", { name: "Role for bob@example.com" });
    expect(within(bob).queryByRole("option", { name: "Owner" })).toBeNull();
    // ...and cannot resend or revoke an owner invitation (Carol's is admin: allowed).
    expect(await screen.findByRole("button", { name: "Revoke the invitation to carol@example.com" })).toBeInTheDocument();
  });

  it("lists pending invitations with resend and revoke", async () => {
    const { calls } = setup("owner");
    const row = await screen.findByTestId("invitation-inv_carol");
    expect(row).toHaveTextContent("carol@example.com");
    expect(row).toHaveTextContent("Admin");
    expect(row).toHaveTextContent("invited by ada@example.com");

    await userEvent.click(screen.getByRole("button", { name: "Resend the invitation to carol@example.com" }));
    await waitFor(() => expect(calls.invite).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG, email: "carol@example.com", role: "admin" })));
    await waitFor(() => expect(toastSuccess).toHaveBeenCalledWith(expect.stringMatching(/again/)));

    await userEvent.click(screen.getByRole("button", { name: "Revoke the invitation to carol@example.com" }));
    await userEvent.click(screen.getByRole("button", { name: "Revoke" }));
    await waitFor(() => expect(calls.revoke).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG, invitationId: "inv_carol" })));
  });

  it("a resend with mail off hands over the link to share", async () => {
    setup("owner", {
      "OrganizationService.InviteMember": { response: { emailSent: false, resent: true, acceptUrl: "https://app.test/accept-invite#rinv_copy", invitation: { email: "carol@example.com" } } },
    });
    await userEvent.click(await screen.findByRole("button", { name: "Resend the invitation to carol@example.com" }));
    expect(await screen.findByTestId("invite-link")).toHaveTextContent("https://app.test/accept-invite#rinv_copy");
  });

  it("shows membership history in words, naming people from the roster", async () => {
    setup("owner");
    await userEvent.click(await screen.findByRole("tab", { name: "Activity" }));
    expect(await screen.findByText("Ada invited carol@example.com as an admin")).toBeInTheDocument();
    expect(screen.getByText("Ada made bob@example.com a member")).toBeInTheDocument();
  });

  it("keeps the per-permission editor as its own tab", async () => {
    setup("owner");
    await userEvent.click(await screen.findByRole("tab", { name: "Permissions" }));
    expect(await screen.findByText(/one permission at a time/)).toBeInTheDocument();
  });
});

describe("MembersPage (member)", () => {
  it("shows the roster and nothing that would only be refused", async () => {
    const { calls } = setup("member");
    await screen.findByTestId("member-u_bob");
    expect(screen.queryByTestId("members-invite")).toBeNull();
    expect(screen.queryByRole("combobox")).toBeNull();
    expect(screen.queryByRole("button", { name: /^Remove / })).toBeNull();
    expect(screen.queryByTestId("members-invitations")).toBeNull();
    expect(screen.queryByRole("tab", { name: "Activity" })).toBeNull();
    expect(calls.invitations).not.toHaveBeenCalled();
    expect(screen.getByTestId("member-u_ada")).toHaveTextContent("Owner");
  });
});

describe("leaving", () => {
  it("leaves and switches the app to the default org the server returns", async () => {
    const { calls } = setup("member");
    await userEvent.click(await screen.findByTestId("members-leave"));
    await userEvent.click(screen.getByRole("button", { name: "Leave" }));
    await waitFor(() => expect(calls.leave).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG })));
    await waitFor(() => expect(useActiveOrgStore.getState().chosenId).toBe("org-b"));
  });

  it("the last owner is told what to do instead", async () => {
    setup("owner", { "OrganizationService.LeaveOrganization": { error: refusal("last_owner") } });
    await userEvent.click(await screen.findByTestId("members-leave"));
    await userEvent.click(screen.getByRole("button", { name: "Leave" }));
    expect(await screen.findByTestId("members-leave-error")).toHaveTextContent(/Make someone else an owner first/);
    expect(useActiveOrgStore.getState().chosenId).toBe(ORG);
  });
});
