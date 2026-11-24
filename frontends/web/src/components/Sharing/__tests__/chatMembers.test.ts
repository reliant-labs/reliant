/**
 * Who may change a chat's members, the 25-member cap, and refusals in words
 * (chat-collaboration.md §3.2).
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";

import { ChatMemberRole } from "@/gen/reliant/v1/sharing_pb";

import {
  DEFAULT_GRANT_ROLE,
  GRANTABLE_ROLES,
  MAX_CHAT_MEMBERS,
  ROLE_TODAY,
  canAdministerMembers,
  grantableRolesFor,
  isAtMemberCap,
  memberErrorMessage,
} from "../chatMembers";

function refusal(reason: string, message = "refused"): ConnectError {
  const err = new ConnectError(message, Code.FailedPrecondition);
  err.metadata.set("x-forge-error-reason", reason);
  return err;
}

describe("chat members", () => {
  it("defaults a grant to View", () => {
    expect(DEFAULT_GRANT_ROLE).toBe(ChatMemberRole.VIEW);
  });

  it("lets the owner and managers administer, up to manage, and no one else", () => {
    expect(canAdministerMembers(ChatMemberRole.UNSPECIFIED)).toBe(true); // the owner
    expect(canAdministerMembers(ChatMemberRole.MANAGE)).toBe(true);
    expect(canAdministerMembers(ChatMemberRole.REPLY)).toBe(false);
    expect(canAdministerMembers(ChatMemberRole.VIEW)).toBe(false);
    expect(grantableRolesFor(ChatMemberRole.MANAGE)).toEqual(GRANTABLE_ROLES);
    expect(grantableRolesFor(ChatMemberRole.REPLY)).toEqual([]);
    // Nothing above manage is ever grantable: the owner is not a level.
    expect(GRANTABLE_ROLES).not.toContain(ChatMemberRole.UNSPECIFIED);
    expect(Math.max(...GRANTABLE_ROLES)).toBe(ChatMemberRole.MANAGE);
  });

  it("caps a chat at 25 members", () => {
    expect(MAX_CHAT_MEMBERS).toBe(25);
    expect(isAtMemberCap(24)).toBe(false);
    expect(isAtMemberCap(25)).toBe(true);
  });

  it("says what reply and manage do before the reply path ships", () => {
    expect(ROLE_TODAY[ChatMemberRole.VIEW]).toBeNull();
    expect(ROLE_TODAY[ChatMemberRole.REPLY]).toMatch(/not available yet/);
    expect(ROLE_TODAY[ChatMemberRole.MANAGE]).toMatch(/add, change or remove people/);
  });

  it("names each refusal, and falls back to the server's words", () => {
    expect(memberErrorMessage(refusal("chat_members_full"), "x")).toMatch(/25 people/);
    expect(memberErrorMessage(refusal("not_org_member"), "x")).toMatch(/not a member/);
    expect(memberErrorMessage(refusal("chat_collaboration_disabled"), "x")).toMatch(/not turned on/);
    expect(memberErrorMessage(refusal("something_new", "the server said no"), "x")).toBe("the server said no");
    expect(memberErrorMessage("??", "fallback")).toBe("fallback");
  });
});
