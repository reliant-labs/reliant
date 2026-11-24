// Copyright (c) 2025 Reliant Labs

/**
 * Who may do what to a chat's members, and how each level and refusal reads
 * (control-plane docs/design/chat-collaboration.md §2, §3.2). The server
 * decides every change (SharingService.SetChatMember / RemoveChatMember);
 * this only keeps the UI from offering what it would refuse.
 *
 * Reply and manage are granted now but act only once the reply path ships
 * (C3): until then a reply member reads the chat as a viewer does, and a
 * manage member can also add, change and remove people. The copy here says
 * so, and the grant dialog still names what the level will allow, because a
 * grant made today takes effect then without anyone granting again.
 */

import { ConnectError } from "@connectrpc/connect";

import { ChatMemberRole } from "@/gen/reliant/v1/sharing_pb";
import { errorReason } from "@/lib/errorReasons";

/** The most members a chat may have (core.MaxChatMembers). */
export const MAX_CHAT_MEMBERS = 25;

/** The levels a grant can hold, lowest first. View is the default. */
export const GRANTABLE_ROLES = [ChatMemberRole.VIEW, ChatMemberRole.REPLY, ChatMemberRole.MANAGE] as const;
export type GrantableRole = (typeof GRANTABLE_ROLES)[number];

export const DEFAULT_GRANT_ROLE: GrantableRole = ChatMemberRole.VIEW;

export const ROLE_LABEL: Record<GrantableRole, string> = {
  [ChatMemberRole.VIEW]: "View",
  [ChatMemberRole.REPLY]: "Reply",
  [ChatMemberRole.MANAGE]: "Manage",
};

/** What each level will allow once the reply path ships (§2). */
export const ROLE_SUMMARY: Record<GrantableRole, string> = {
  [ChatMemberRole.VIEW]: "Read the conversation and follow it live. Tool results stay hidden.",
  [ChatMemberRole.REPLY]: "View, plus send messages to the agent, answer its questions and approve its tool calls.",
  [ChatMemberRole.MANAGE]: "Reply, plus steer, pause or stop the run, change its settings, and add or remove people.",
};

/**
 * What each level does today, before the reply path ships, or null when it
 * already does everything its summary says.
 */
export const ROLE_TODAY: Record<GrantableRole, string | null> = {
  [ChatMemberRole.VIEW]: null,
  [ChatMemberRole.REPLY]: "Replying is not available yet. Until it is, they can read this chat and nothing more.",
  [ChatMemberRole.MANAGE]:
    "Replying and run controls are not available yet. Until they are, they can read this chat and add, change or remove people.",
};

export function isGrantableRole(role: ChatMemberRole): role is GrantableRole {
  return (GRANTABLE_ROLES as readonly ChatMemberRole[]).includes(role);
}

/**
 * Whether the caller, reading the chat at `callerRole`, may add people and
 * change or remove anyone's grant: the owner (UNSPECIFIED, as
 * ListChatMembers reports them) and members at manage. A manager may grant up
 * to manage, which is every grantable level, and the owner is never a member,
 * so neither can be granted or removed by anyone.
 */
export function canAdministerMembers(callerRole: ChatMemberRole): boolean {
  return callerRole === ChatMemberRole.UNSPECIFIED || callerRole === ChatMemberRole.MANAGE;
}

/** The levels `callerRole` may grant: none, or every grantable level. */
export function grantableRolesFor(callerRole: ChatMemberRole): readonly GrantableRole[] {
  return canAdministerMembers(callerRole) ? GRANTABLE_ROLES : [];
}

/** Whether the chat already has as many members as it may. */
export function isAtMemberCap(memberCount: number): boolean {
  return memberCount >= MAX_CHAT_MEMBERS;
}

/** Why SharingService refused a change of members, in words. */
const MEMBER_REASON_WORDS: Record<string, string> = {
  chat_collaboration_disabled: "Adding people to a chat is not turned on for this deployment yet.",
  needs_manage: "Only the chat's owner and people with Manage can change who has access.",
  owner_not_member: "The chat's owner always has full access; they cannot be added or removed.",
  not_org_member: "That person is not a member of this chat's organization.",
  chat_members_full: `This chat already has ${MAX_CHAT_MEMBERS} people, the most it can have. Remove someone first.`,
  no_org: "This chat belongs to no organization, so there is no one to add.",
  person_required: "Sign in to the Reliant app to change who has access; an access token or agent cannot.",
};

export function memberErrorMessage(err: unknown, fallback: string): string {
  const reason = errorReason(err);
  if (reason && MEMBER_REASON_WORDS[reason]) return MEMBER_REASON_WORDS[reason];
  if (err instanceof ConnectError) return err.rawMessage || fallback;
  if (err instanceof Error) return err.message || fallback;
  return fallback;
}
