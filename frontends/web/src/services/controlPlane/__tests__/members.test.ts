// @vitest-environment node
/**
 * The org-membership client (members.ts), through the real generated client
 * over a mock transport.
 *
 * Pinned: every org-acting rpc names its org on the wire (it is required, and
 * a destructive call must never fall back to the default org), the wire
 * shapes map into the page's model, the seat count reads "N of M seats", and
 * each refusal reason the page can meet gets human words rather than the
 * server's prose.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";

vi.mock("@/services/controlPlane/config", () => ({
  CONTROL_PLANE_API_URL: "https://admin.example.test",
  hasControlPlane: true,
}));

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import {
  acceptInvitation,
  createOrganization,
  getInvitation,
  inviteMember,
  isTerminalInviteRefusal,
  leaveOrganization,
  listInvitations,
  listMembers,
  listMembershipActivity,
  memberErrorMessage,
  removeMember,
  revokeInvitation,
  seatSummary,
  seatsFull,
  updateMemberRole,
} from "../members";

const ORG = "org_acme";
const JOINED = new Date("2026-10-01T00:00:00Z");

const refusal = (reason: string, code = Code.FailedPrecondition) =>
  new ConnectError("server prose that is not copy", code, { "x-forge-error-reason": reason });

afterEach(() => setTransport(null));

describe("org-acting rpcs name their org", () => {
  it.each([
    ["ListMembers", () => listMembers(ORG), { seats: { members: 1, limit: 3 } }],
    ["InviteMember", () => inviteMember(ORG, " bob@example.com ", "admin"), {}],
    ["ListInvitations", () => listInvitations(ORG), {}],
    ["RevokeInvitation", () => revokeInvitation(ORG, "inv_1"), {}],
    ["RemoveMember", () => removeMember(ORG, "u_bob"), {}],
    ["LeaveOrganization", () => leaveOrganization(ORG), {}],
    ["UpdateMemberRole", () => updateMemberRole(ORG, "u_bob", "admin"), {}],
  ] as const)("%s carries org_id", async (rpc, call, response) => {
    const seen = vi.fn(() => response);
    setTransport(mockTransport({ [`OrganizationService.${rpc}`]: seen }));
    await call();
    expect(seen).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG }));
  });

  it("ListMembershipActivity carries org_id", async () => {
    const seen = vi.fn(() => ({ events: [] }));
    setTransport(mockTransport({ "ActivityService.ListMembershipActivity": seen }));
    await listMembershipActivity(ORG);
    expect(seen).toHaveBeenCalledWith(expect.objectContaining({ orgId: ORG }));
  });

  it("refuses to send without an org rather than let the interceptor pick one", async () => {
    const seen = vi.fn(() => ({}));
    setTransport(mockTransport({ "OrganizationService.RemoveMember": seen }));
    await expect(removeMember("", "u_bob")).rejects.toThrow(/organization/);
    expect(seen).not.toHaveBeenCalled();
  });
});

describe("wire mapping", () => {
  it("maps the roster and its seats", async () => {
    setTransport(
      mockTransport({
        "OrganizationService.ListMembers": {
          response: {
            members: [{ userId: "u_ada", email: "ada@example.com", name: "Ada", role: "owner", isFounder: true, joinedAt: timestampFromDate(JOINED) }],
            nextPageToken: "p2",
            seats: { members: 1, pendingInvitations: 2, limit: 3 },
          },
        },
      }),
    );
    const page = await listMembers(ORG);
    expect(page.members).toEqual([
      { userId: "u_ada", email: "ada@example.com", name: "Ada", avatarUrl: "", role: "owner", joinedAt: JOINED, isFounder: true },
    ]);
    expect(page.nextPageToken).toBe("p2");
    expect(page.seats).toEqual({ members: 1, pendingInvitations: 2, limit: 3 });
  });

  it("sends the trimmed address and role, and returns the copy-link fallback", async () => {
    const seen = vi.fn(() => ({ emailSent: false, acceptUrl: "https://app.test/accept-invite#rinv_x", resent: true, invitation: { id: "inv_1", email: "bob@example.com", role: "admin", status: "pending" } }));
    setTransport(mockTransport({ "OrganizationService.InviteMember": seen }));
    const res = await inviteMember(ORG, "  bob@example.com ", "admin");
    expect(seen).toHaveBeenCalledWith(expect.objectContaining({ email: "bob@example.com", role: "admin" }));
    expect(res).toMatchObject({ emailSent: false, acceptUrl: "https://app.test/accept-invite#rinv_x", resent: true, invitation: { id: "inv_1", status: "pending" } });
  });

  it("previews and accepts an invitation by its token, in the body", async () => {
    const preview = vi.fn(() => ({ orgName: "Acme", role: "member", invitedByName: "Ada", inviteeEmailHint: "b***@example.com", status: "pending", emailMatches: true }));
    const accept = vi.fn(() => ({ organization: { id: ORG, name: "Acme", slug: "acme", role: "member" } }));
    setTransport(mockTransport({ "OrganizationService.GetInvitation": preview, "OrganizationService.AcceptInvitation": accept }));

    expect(await getInvitation("rinv_token")).toMatchObject({ orgName: "Acme", emailMatches: true, alreadyMember: false });
    expect(await acceptInvitation("rinv_token")).toMatchObject({ id: ORG, name: "Acme", role: "member" });
    expect(preview).toHaveBeenCalledWith(expect.objectContaining({ token: "rinv_token" }));
    expect(accept).toHaveBeenCalledWith(expect.objectContaining({ token: "rinv_token" }));
  });

  it("creates an org from a trimmed name", async () => {
    const seen = vi.fn(() => ({ organization: { id: "org_new", name: "New Co", slug: "new-co-x1", role: "owner", isOwner: true } }));
    setTransport(mockTransport({ "OrganizationService.CreateOrganization": seen }));
    expect(await createOrganization("  New Co ")).toMatchObject({ id: "org_new", isOwner: true });
    expect(seen).toHaveBeenCalledWith(expect.objectContaining({ name: "New Co" }));
  });

  it("a refused call stays an error", async () => {
    setTransport(mockTransport({ "OrganizationService.RemoveMember": { error: refusal("last_owner") } }));
    await expect(removeMember(ORG, "u_ada")).rejects.toBeInstanceOf(ConnectError);
  });
});

describe("seat count", () => {
  it("counts pending invitations, since each holds a seat", () => {
    expect(seatSummary({ members: 2, pendingInvitations: 0, limit: 5 })).toBe("2 of 5 seats");
    expect(seatSummary({ members: 2, pendingInvitations: 1, limit: 3 })).toBe("3 of 3 seats (1 pending)");
    expect(seatsFull({ members: 2, pendingInvitations: 1, limit: 3 })).toBe(true);
  });

  it("says unlimited for -1", () => {
    expect(seatSummary({ members: 12, pendingInvitations: 4, limit: -1 })).toBe("12 members · unlimited seats");
    expect(seatsFull({ members: 999, pendingInvitations: 0, limit: -1 })).toBe(false);
  });
});

describe("refusal copy", () => {
  it.each([
    ["last_owner", /at least one owner/],
    ["seat_limit_reached", /Every seat/],
    ["invite_email_mismatch", /different email address/],
    ["grant_ceiling", /permissions you hold yourself/],
    ["already_member", /already a member/],
    ["invitation_expired", /expired/],
    ["invitation_revoked", /withdrawn/],
  ])("%s has human words", (reason, words) => {
    const msg = memberErrorMessage(refusal(reason), "invite", "fallback");
    expect(msg).toMatch(words);
    expect(msg).not.toContain("server prose");
  });

  it("speaks to the invitee when they are the one accepting", () => {
    expect(memberErrorMessage(refusal("seat_limit_reached", Code.ResourceExhausted), "accept", "x")).toMatch(/no free seats/);
    expect(memberErrorMessage(refusal("already_member"), "accept", "x")).toMatch(/You're already a member/);
  });

  it("falls back for an error with no reason, never showing the server's prose", () => {
    expect(memberErrorMessage(new ConnectError("boom", Code.Internal), "invite", "Could not send.")).toBe("Could not send.");
    expect(memberErrorMessage(refusal("some_future_reason"), "invite", "Could not send.")).toBe("Could not send.");
  });

  it("knows which accept refusals are final", () => {
    expect(isTerminalInviteRefusal(refusal("invitation_expired"))).toBe(true);
    expect(isTerminalInviteRefusal(refusal("already_member"))).toBe(true);
    expect(isTerminalInviteRefusal(refusal("seat_limit_reached"))).toBe(false);
    expect(isTerminalInviteRefusal(refusal("invite_email_mismatch", Code.PermissionDenied))).toBe(false);
  });
});
