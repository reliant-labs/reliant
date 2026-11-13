// Copyright (c) 2025 Reliant Labs

/**
 * The org-membership rpcs of `controlplane.v1.OrganizationService` (and the
 * membership audit read on ActivityService), mapped into the web's model.
 * Contract: control-plane docs/design/orgs-and-members.md, phase 1.
 *
 *   CreateOrganization   any verified person; the caller becomes its owner
 *   ListMembers          any member: the roster and the seat count
 *   InviteMember         admin/owner; re-inviting a pending address RESENDS it
 *   ListInvitations      admin/owner (other people's addresses)
 *   RevokeInvitation     admin/owner
 *   GetInvitation        the accept page's preview, from the link's token
 *   AcceptInvitation     the invitee, whose verified email must match
 *   RemoveMember         admin/owner; never yourself (that is leave)
 *   LeaveOrganization    any member except the last owner
 *   UpdateMemberRole     admin/owner; only an owner touches owners
 *
 * Every org-acting request names its org EXPLICITLY. The field is required
 * on the wire, and a destructive call must never act in whatever org the
 * transport's interceptor would fill in after a switch that raced it.
 *
 * Membership administration is gated on the caller's ROLE, never on a grant:
 * what this module's callers disable is a courtesy, the server decides.
 *
 * The invitation token (`rinv_…`) is a bearer secret until it is accepted.
 * It travels in a request body only, and nothing here logs it.
 */

import { timestampDate, type Timestamp } from "@bufbuild/protobuf/wkt";

import { ActivityService } from "@/gen/controlplane/services/activity/v1/activity_pb";
import {
  OrganizationService,
  type MyOrganization,
  type OrgInvitation as WireInvitation,
  type OrgMember as WireMember,
} from "@/gen/controlplane/services/organization/v1/organization_pb";
import { errorReason, hasErrorReason } from "@/lib/errorReasons";

import { getControlPlaneClient } from "./client";
import { mapActivityEvent, type ActivityList } from "./observability/activityApi";
import type { MyOrg } from "./orgs";

/** The three roles. Roles are presets of grants; admin and owner hold them all. */
export type OrgRole = "owner" | "admin" | "member";
export const ORG_ROLES: readonly OrgRole[] = ["member", "admin", "owner"];

/**
 * Query keys. The root is in activeOrgStore's ORG_SCOPED_QUERY_ROOTS, so an
 * org switch drops every one of them.
 */
export const memberKeys = {
  all: ["orgs-members"] as const,
  members: (orgId: string) => ["orgs-members", orgId, "members"] as const,
  invitations: (orgId: string) => ["orgs-members", orgId, "invitations"] as const,
  activity: (orgId: string) => ["orgs-members", orgId, "activity"] as const,
};

export interface OrgMember {
  userId: string;
  email: string;
  /** "" when the person set none. */
  name: string;
  avatarUrl: string;
  role: string;
  joinedAt: Date | null;
  /** The org's founding, accountable owner. It confers no authority. */
  isFounder: boolean;
  /**
   * Their sign-in subject: the id this app keys a person's chats and projects
   * by (userId is control-plane's own). "" from a control plane that
   * predates it.
   */
  externalId: string;
}

export interface SeatUsage {
  members: number;
  /** Pending, unexpired invitations: they hold a seat until answered. */
  pendingInvitations: number;
  /** -1 = unlimited. */
  limit: number;
}

export interface MemberPage {
  members: OrgMember[];
  nextPageToken: string;
  seats: SeatUsage;
}

export interface OrgInvitation {
  id: string;
  email: string;
  role: string;
  invitedByUserId: string;
  invitedByEmail: string;
  /** "pending", "accepted", "revoked" or "expired". */
  status: string;
  createdAt: Date | null;
  expiresAt: Date | null;
  lastSentAt: Date | null;
}

export interface InviteResult {
  invitation: OrgInvitation | null;
  /** False when mail is off on this deployment or the send failed: acceptUrl is then the delivery. */
  emailSent: boolean;
  /** The invitee's link. Returned only here, never listed again. */
  acceptUrl: string;
  /** This re-sent an existing pending invitation. */
  resent: boolean;
}

export interface InvitationPreview {
  orgName: string;
  role: string;
  invitedByName: string;
  /** The invited address, masked: "a***@example.com". */
  inviteeEmailHint: string;
  status: string;
  expiresAt: Date | null;
  /** The caller's verified email is the invited one. */
  emailMatches: boolean;
  alreadyMember: boolean;
}

const dateOf = (ts: Timestamp | undefined): Date | null => (ts ? timestampDate(ts) : null);

function mapMember(m: WireMember): OrgMember {
  return {
    userId: m.userId,
    email: m.email,
    name: m.name,
    avatarUrl: m.avatarUrl,
    role: m.role,
    joinedAt: dateOf(m.joinedAt),
    isFounder: m.isFounder,
    externalId: m.externalId,
  };
}

function mapInvitation(i: WireInvitation): OrgInvitation {
  return {
    id: i.id,
    email: i.email,
    role: i.role,
    invitedByUserId: i.invitedByUserId,
    invitedByEmail: i.invitedByEmail,
    status: i.status,
    createdAt: dateOf(i.createdAt),
    expiresAt: dateOf(i.expiresAt),
    lastSentAt: dateOf(i.lastSentAt),
  };
}

function mapMyOrg(o: MyOrganization | undefined): MyOrg {
  return {
    id: o?.id ?? "",
    name: o?.name ?? "",
    slug: o?.slug ?? "",
    role: o?.role ?? "",
    isOwner: o?.isOwner ?? false,
    isDefault: o?.isDefault ?? false,
  };
}

const client = () => getControlPlaneClient(OrganizationService);

function requireOrg(orgId: string): void {
  if (!orgId) throw new Error("Choose an organization first.");
}

/** CreateOrganization: an org owned by the caller. The slug is derived from the name. */
export async function createOrganization(name: string): Promise<MyOrg> {
  const res = await client().createOrganization({ name: name.trim() });
  return mapMyOrg(res.organization);
}

/** ListMembers: one page of the roster, oldest member first, and the seat count. */
export async function listMembers(orgId: string, pageToken = "", pageSize = 200): Promise<MemberPage> {
  requireOrg(orgId);
  const res = await client().listMembers({ orgId, pageToken, pageSize });
  return {
    members: res.members.map(mapMember),
    nextPageToken: res.nextPageToken,
    seats: {
      members: res.seats?.members ?? res.members.length,
      pendingInvitations: res.seats?.pendingInvitations ?? 0,
      limit: res.seats?.limit ?? -1,
    },
  };
}

/** InviteMember. Inviting an address with a pending invitation re-sends it (`resent`). */
export async function inviteMember(orgId: string, email: string, role: OrgRole): Promise<InviteResult> {
  requireOrg(orgId);
  const res = await client().inviteMember({ orgId, email: email.trim(), role });
  return {
    invitation: res.invitation ? mapInvitation(res.invitation) : null,
    emailSent: res.emailSent,
    acceptUrl: res.acceptUrl,
    resent: res.resent,
  };
}

/** ListInvitations: the pending ones, newest first. Admin/owner. */
export async function listInvitations(orgId: string): Promise<OrgInvitation[]> {
  requireOrg(orgId);
  const res = await client().listInvitations({ orgId, pageSize: 200 });
  return res.invitations.map(mapInvitation);
}

export async function revokeInvitation(orgId: string, invitationId: string): Promise<void> {
  requireOrg(orgId);
  await client().revokeInvitation({ orgId, invitationId });
}

/** GetInvitation: what the accept page shows before anyone presses accept. */
export async function getInvitation(token: string): Promise<InvitationPreview> {
  const res = await client().getInvitation({ token });
  return {
    orgName: res.orgName,
    role: res.role,
    invitedByName: res.invitedByName,
    inviteeEmailHint: res.inviteeEmailHint,
    status: res.status,
    expiresAt: dateOf(res.expiresAt),
    emailMatches: res.emailMatches,
    alreadyMember: res.alreadyMember,
  };
}

/** AcceptInvitation: joins the caller; answers the org they joined. */
export async function acceptInvitation(token: string): Promise<MyOrg> {
  const res = await client().acceptInvitation({ token });
  return mapMyOrg(res.organization);
}

/** RemoveMember: answers how many access tokens the removal revoked. */
export async function removeMember(orgId: string, userId: string): Promise<number> {
  requireOrg(orgId);
  const res = await client().removeMember({ orgId, userId });
  return res.revokedTokenCount;
}

/** LeaveOrganization: answers the caller's default org afterwards ("" for none). */
export async function leaveOrganization(orgId: string): Promise<{ defaultOrgId: string; revokedTokenCount: number }> {
  requireOrg(orgId);
  const res = await client().leaveOrganization({ orgId });
  return { defaultOrgId: res.defaultOrgId, revokedTokenCount: res.revokedTokenCount };
}

/** UpdateMemberRole: a real change resets the member's permissions to the role's preset. */
export async function updateMemberRole(orgId: string, userId: string, role: OrgRole): Promise<{ member: OrgMember | null; scopes: string[] }> {
  requireOrg(orgId);
  const res = await client().updateMemberRole({ orgId, userId, role });
  return { member: res.member ? mapMember(res.member) : null, scopes: [...res.scopes] };
}

/** ListMembershipActivity: who created, invited, joined, left, was removed or changed role. Admin/owner. */
export async function listMembershipActivity(orgId: string, pageToken = ""): Promise<ActivityList> {
  requireOrg(orgId);
  const res = await getControlPlaneClient(ActivityService).listMembershipActivity({ orgId, pageToken, pageSize: 50 });
  return { events: res.events.map(mapActivityEvent), nextPageToken: res.nextPageToken };
}

/**
 * Display names by the id this app keys a person's records by: a chat's
 * `userId` is their sign-in subject, which the roster carries as
 * `externalId` (its `userId` is control-plane's own id, which matches
 * nothing here). The name they set, else their email. A control plane that
 * predates `externalId` names nobody, and the callers say "the person who
 * shared it" instead.
 */
export function memberNamesBySubject(
  members: Iterable<Pick<OrgMember, "externalId" | "name" | "email">>,
): Map<string, string> {
  const names = new Map<string, string>();
  for (const m of members) {
    if (m.externalId) names.set(m.externalId, m.name || m.email);
  }
  return names;
}

/** Admin and owner administer membership; a member only reads the roster. */
export function canAdministerMembers(role: string | undefined): boolean {
  return role === "owner" || role === "admin";
}

/** The role, in words. */
export function roleName(role: string): string {
  return role ? role.charAt(0).toUpperCase() + role.slice(1) : "Member";
}

/** "3 of 5 seats" — members plus pending invitations, since an invitation holds a seat until answered. */
export function seatSummary(seats: SeatUsage): string {
  if (seats.limit < 0) return `${seats.members} ${seats.members === 1 ? "member" : "members"} · unlimited seats`;
  const used = seats.members + seats.pendingInvitations;
  const pending = seats.pendingInvitations > 0 ? ` (${seats.pendingInvitations} pending)` : "";
  return `${used} of ${seats.limit} seats${pending}`;
}

/** No seat left for another invitation. */
export function seatsFull(seats: SeatUsage): boolean {
  return seats.limit >= 0 && seats.members + seats.pendingInvitations >= seats.limit;
}

/** Where the refusal happened, for reasons whose words depend on who is reading. */
export type MemberErrorContext = "invite" | "accept" | "remove" | "leave" | "role" | "create" | "revoke" | "load";

const REASON_COPY: Record<string, string> = {
  last_owner: "An organization needs at least one owner. Make someone else an owner first.",
  seat_limit_reached:
    "Every seat in this organization is taken. Remove a member or revoke a pending invitation to free one.",
  invite_email_mismatch:
    "This invitation was sent to a different email address. Sign in with the address it was sent to.",
  grant_ceiling: "You can only give someone a role whose permissions you hold yourself.",
  already_member: "That person is already a member of this organization.",
  invitation_expired: "This invitation has expired. Ask whoever invited you to send a new one.",
  invitation_revoked: "This invitation was withdrawn. Ask whoever invited you for a new one.",
  invitation_not_pending: "This invitation has already been used.",
  admin_required: "Only an admin or owner of this organization can do that.",
  owner_required: "Only an owner can change another owner, or make someone an owner.",
  email_unverified: "Verify your email address first, then try again.",
  human_only: "Sign in as yourself to do this: a machine token cannot manage members.",
  org_unclaimed: "This organization belongs to a site nobody has claimed yet, so it cannot have members.",
  invite_resend_too_soon: "An invitation went to that address a moment ago. Wait a minute before sending it again.",
  cannot_remove_self: "To remove yourself, leave the organization instead.",
  owned_org_limit: "You own as many organizations as one person can.",
  pending_invite_limit: "This organization has too many invitations waiting. Revoke some before sending more.",
  invite_rate_limited: "You've sent a lot of invitations in the last hour. Try again later.",
  slug_taken: "That name is taken. Try another.",
  not_org_member: "You're not a member of this organization any more.",
};

/** Words that depend on whether the reader is the admin or the invitee. */
const ACCEPT_COPY: Record<string, string> = {
  seat_limit_reached: "This organization has no free seats right now. Ask whoever invited you to free one, then try again.",
  already_member: "You're already a member of this organization.",
};

/**
 * Human copy for a failed membership call: the reason's words when the server
 * named one, else `fallback`. Never the server's prose, which is not copy.
 */
export function memberErrorMessage(err: unknown, context: MemberErrorContext, fallback: string): string {
  const reason = errorReason(err);
  if (!reason) return fallback;
  if (context === "accept" && ACCEPT_COPY[reason]) return ACCEPT_COPY[reason];
  return REASON_COPY[reason] ?? fallback;
}

/** An invitation that can never be accepted: the accept page stops offering it. */
export function isTerminalInviteRefusal(err: unknown): boolean {
  return hasErrorReason(err, ["invitation_expired", "invitation_revoked", "invitation_not_pending", "already_member"]);
}
