// Copyright (c) 2025 Reliant Labs

/**
 * An org's own AI accounts (OrgProviderAccountService; control-plane
 * docs/design/org-provider-accounts.md §3, §7) as plain frontend values: the
 * org's API keys, who in the org may use each, and the team workspaces whose
 * members' seats the org pays for.
 *
 * No value here ever holds a key: an org key is write-only. An owner or admin
 * sees each account whole (masked key, usage, grants); a member sees its
 * provider, label and status.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import {
  OrgProviderAccountAccess,
  type ListOrgProviderAccountsResponse,
  type ListOrgProviderWorkspacesResponse,
  type OrgProviderAccount as ProtoOrgAccount,
} from "../gen/reliant/v1/org_provider_account_pb";
import { errorReason } from "../lib/errorReasons";
import { accountFromProto, type ProviderAccount } from "./providerAccount";

/** Who in the org may use an account. */
export type OrgAccountAccess = "members" | "admins" | "selected";

export interface OrgAccountGrant {
  /** The member's sign-in subject (the id reliant keys people by). */
  userId: string;
  grantedByUserId: string;
  grantedAt: string;
}

export interface OrgAccount {
  account: ProviderAccount;
  access: OrgAccountAccess;
  /** Owners and admins only. */
  createdByUserId?: string;
  /** Owners and admins only; they count while access is "selected". */
  grants: OrgAccountGrant[];
}

export interface OrgAccounts {
  accounts: OrgAccount[];
  callerIsAdmin: boolean;
  /** Whether this deployment lets the org add accounts and bind workspaces. */
  addingEnabled: boolean;
}

export interface OrgWorkspace {
  /** "claude" or "codex". */
  provider: string;
  externalOrgId: string;
  displayName: string;
  boundByUserId: string;
  boundAt: string;
}

export interface OrgSeat {
  accountId: string;
  userId: string;
  provider: string;
  email?: string;
  plan?: string;
  attributionSource: string;
  externalOrgId?: string;
}

export interface OrgWorkspaces {
  workspaces: OrgWorkspace[];
  seats: OrgSeat[];
}

const ACCESS_FROM_PROTO: Record<OrgProviderAccountAccess, OrgAccountAccess> = {
  [OrgProviderAccountAccess.UNSPECIFIED]: "members",
  [OrgProviderAccountAccess.MEMBERS]: "members",
  [OrgProviderAccountAccess.ADMINS]: "admins",
  [OrgProviderAccountAccess.SELECTED]: "selected",
};

export const ACCESS_TO_PROTO: Record<OrgAccountAccess, OrgProviderAccountAccess> = {
  members: OrgProviderAccountAccess.MEMBERS,
  admins: OrgProviderAccountAccess.ADMINS,
  selected: OrgProviderAccountAccess.SELECTED,
};

export function orgAccountFromProto(a: ProtoOrgAccount): OrgAccount | null {
  if (!a.account) return null;
  return {
    account: accountFromProto(a.account),
    access: ACCESS_FROM_PROTO[a.access] ?? "members",
    createdByUserId: a.createdByUserId || undefined,
    grants: a.grants.map((g) => ({ userId: g.userId, grantedByUserId: g.grantedByUserId, grantedAt: g.grantedAt })),
  };
}

export function orgAccountsFromProto(res: ListOrgProviderAccountsResponse): OrgAccounts {
  return {
    accounts: res.accounts.map(orgAccountFromProto).filter((a): a is OrgAccount => a !== null),
    callerIsAdmin: res.callerIsAdmin,
    addingEnabled: res.addingEnabled,
  };
}

export function orgWorkspacesFromProto(res: ListOrgProviderWorkspacesResponse): OrgWorkspaces {
  return {
    workspaces: res.workspaces.map((w) => ({
      provider: w.provider,
      externalOrgId: w.externalOrgId,
      displayName: w.displayName,
      boundByUserId: w.boundByUserId,
      boundAt: w.boundAt,
    })),
    seats: res.seats.map((s) => ({
      accountId: s.accountId,
      userId: s.userId,
      provider: s.provider,
      email: s.email || undefined,
      plan: s.plan || undefined,
      attributionSource: s.attributionSource,
      externalOrgId: s.externalOrgId || undefined,
    })),
  };
}

/** The providers an org may hold a key of (an org owns API keys, never a sign-in), in picker order. */
export const ORG_KEY_PROVIDERS: ReadonlyArray<{ id: string; name: string; placeholder: string }> = [
  { id: "anthropic", name: "Anthropic", placeholder: "sk-ant-…" },
  { id: "openai", name: "OpenAI", placeholder: "sk-…" },
  { id: "openrouter", name: "OpenRouter", placeholder: "sk-or-…" },
  { id: "gemini", name: "Google Gemini", placeholder: "AIza…" },
];

/** The access setting in words: "Everyone in Acme" / "Owners and admins" / "Selected people". */
export function accessLabel(access: OrgAccountAccess, orgName: string): string {
  switch (access) {
    case "members":
      return `Everyone in ${orgName}`;
    case "admins":
      return "Owners and admins";
    case "selected":
      return "Selected people";
  }
}

/**
 * Whether `userId` may use the account by its audience: a member sees no
 * grants, so "selected" is "maybe" to them (an admin granted it or not).
 */
export function mayUse(a: OrgAccount, isAdmin: boolean, userId: string | null): "yes" | "maybe" | "no" {
  switch (a.access) {
    case "members":
      return "yes";
    case "admins":
      return isAdmin ? "yes" : "no";
    case "selected":
      if (!isAdmin) return "maybe";
      return userId && a.grants.some((g) => g.userId === userId) ? "yes" : "no";
  }
}

const REASON_COPY: Record<string, string> = {
  org_admin_required: "Only an owner or admin of this organization can change its AI accounts.",
  human_only: "Sign in as yourself to do this: a machine or CLI credential cannot change an organization's AI accounts.",
  org_provider_accounts_disabled: "Adding organization AI accounts isn't enabled on this deployment yet.",
  org_provider_seats_disabled: "Paying for members' team workspace seats isn't enabled on this deployment yet.",
  not_org_member: "You're not a member of this organization any more.",
};

/**
 * Human copy for a failed OrgProviderAccountService call: the reason's words
 * when it names one; a validation refusal's own message (the provider's
 * verdict on a pasted key is written for the person); else `fallback`.
 */
export function orgAccountErrorMessage(err: unknown, fallback: string): string {
  const reason = errorReason(err);
  if (reason && REASON_COPY[reason]) return REASON_COPY[reason];
  if (err instanceof ConnectError) {
    if (err.code === Code.InvalidArgument && err.rawMessage) return sentence(err.rawMessage);
    if (err.code === Code.AlreadyExists) return "That workspace already pays for another organization, or that key is already one of this organization's accounts.";
    if (err.code === Code.NotFound) return "That account is gone. It may have been removed in another window.";
  }
  return fallback;
}

function sentence(s: string): string {
  const t = s.trim();
  if (!t) return t;
  const head = t.charAt(0).toUpperCase() + t.slice(1);
  return /[.!?]$/.test(head) ? head : `${head}.`;
}
