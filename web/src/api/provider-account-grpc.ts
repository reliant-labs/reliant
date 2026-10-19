// Copyright (c) 2025 Reliant Labs

/**
 * Thin client over `ProviderAccountService`: the LLM accounts a user has
 * connected (several per provider), their derived status, and their usage.
 *
 * Proto messages become plain frontend types here, so components never touch
 * `$typeName`, bigint or proto enums.
 */

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError, createClient } from "@connectrpc/connect";

import { getTransport } from "./grpc-client";
import {
  AddProviderAPIKeyRequestSchema,
  DeleteProviderAccountRequestSchema,
  GetChatProviderAccountRequestSchema,
  ListProviderAccountsRequestSchema,
  ProviderAccountService,
  ProviderAccountStatus,
  ProviderAuthKind,
  RefreshProviderAccountUsageRequestSchema,
  ReorderProviderAccountsRequestSchema,
  UpdateProviderAccountRequestSchema,
  UsageSource,
  type ProviderAccount as ProtoProviderAccount,
  type UsageWindow as ProtoUsageWindow,
} from "../gen/reliant/v1/provider_account_pb";

export type AccountStatus = "available" | "limited" | "paused" | "needs_sign_in";
export type AccountAuthKind = "oauth" | "api_key" | "device" | "managed";
export type UsageSourceKind = "headers" | "usage_api" | "limit_error" | "unknown";

export interface UsageWindow {
  /** "5h", "7d", "7d_opus", "primary", … */
  name: string;
  /** "5-hour", "Weekly", "Weekly · Opus". */
  label: string;
  /** 0..1; undefined when the provider did not report it. */
  usedFraction?: number;
  /** RFC 3339; undefined when the reset time is unknown. */
  resetsAt?: string;
  lengthSeconds: number;
}

export interface ProviderAccount {
  id: string;
  /** Driver id: "claude", "codex", "anthropic", … */
  provider: string;
  providerDisplayName: string;
  authKind: AccountAuthKind;
  /** The user's label, else the detected email, else a masked key. */
  label: string;
  email?: string;
  organizationName?: string;
  plan?: string;
  planDisplay?: string;
  maskedKey?: string;
  priority: number;
  status: AccountStatus;
  limitedUntil?: string;
  limitedWindow?: string;
  needsSignInReason?: string;
  windows: UsageWindow[];
  usageObservedAt?: string;
  usageSource: UsageSourceKind;
  usageError?: string;
  createdAt: string;
}

const STATUS_FROM_PROTO: Record<ProviderAccountStatus, AccountStatus> = {
  [ProviderAccountStatus.UNSPECIFIED]: "available",
  [ProviderAccountStatus.AVAILABLE]: "available",
  [ProviderAccountStatus.LIMITED]: "limited",
  [ProviderAccountStatus.PAUSED]: "paused",
  [ProviderAccountStatus.NEEDS_SIGN_IN]: "needs_sign_in",
};

const AUTH_KIND_FROM_PROTO: Record<ProviderAuthKind, AccountAuthKind> = {
  [ProviderAuthKind.UNSPECIFIED]: "api_key",
  [ProviderAuthKind.OAUTH]: "oauth",
  [ProviderAuthKind.API_KEY]: "api_key",
  [ProviderAuthKind.DEVICE]: "device",
  [ProviderAuthKind.MANAGED]: "managed",
};

const USAGE_SOURCE_FROM_PROTO: Record<UsageSource, UsageSourceKind> = {
  [UsageSource.UNSPECIFIED]: "unknown",
  [UsageSource.HEADERS]: "headers",
  [UsageSource.USAGE_API]: "usage_api",
  [UsageSource.LIMIT_ERROR]: "limit_error",
};

function windowFromProto(w: ProtoUsageWindow): UsageWindow {
  return {
    name: w.name,
    label: w.label,
    usedFraction: w.usedFraction,
    resetsAt: w.resetsAt || undefined,
    lengthSeconds: Number(w.lengthSeconds),
  };
}

export function accountFromProto(a: ProtoProviderAccount): ProviderAccount {
  return {
    id: a.id,
    provider: a.provider,
    providerDisplayName: a.providerDisplayName,
    authKind: AUTH_KIND_FROM_PROTO[a.authKind] ?? "api_key",
    label: a.label,
    email: a.email || undefined,
    organizationName: a.organizationName || undefined,
    plan: a.plan || undefined,
    planDisplay: a.planDisplay || undefined,
    maskedKey: a.maskedKey || undefined,
    priority: a.priority,
    status: STATUS_FROM_PROTO[a.status] ?? "available",
    limitedUntil: a.limitedUntil || undefined,
    limitedWindow: a.limitedWindow || undefined,
    needsSignInReason: a.needsSignInReason || undefined,
    windows: a.windows.map(windowFromProto),
    usageObservedAt: a.usageObservedAt || undefined,
    usageSource: USAGE_SOURCE_FROM_PROTO[a.usageSource] ?? "unknown",
    usageError: a.usageError || undefined,
    createdAt: a.createdAt,
  };
}

const client = () => createClient(ProviderAccountService, getTransport());

/**
 * True when the server does not serve the account RPCs — a build that predates
 * them (a bare HTTP 404 or a handler answering Unimplemented, which is what
 * forge's scaffold stub does). Settings → Providers falls back to the
 * single-credential view on this.
 */
export function isProviderAccountsUnavailable(error: unknown): boolean {
  return error instanceof ConnectError && error.code === Code.Unimplemented;
}

export const providerAccountGrpc = {
  async listAccounts(): Promise<ProviderAccount[]> {
    const res = await client().listProviderAccounts(create(ListProviderAccountsRequestSchema, {}));
    return res.accounts.map(accountFromProto);
  },

  async updateAccount(
    accountId: string,
    patch: { label?: string; paused?: boolean },
  ): Promise<ProviderAccount | null> {
    const res = await client().updateProviderAccount(
      create(UpdateProviderAccountRequestSchema, { accountId, ...patch }),
    );
    return res.account ? accountFromProto(res.account) : null;
  },

  async reorderAccounts(provider: string, accountIds: string[]): Promise<ProviderAccount[]> {
    const res = await client().reorderProviderAccounts(
      create(ReorderProviderAccountsRequestSchema, { provider, accountIds }),
    );
    return res.accounts.map(accountFromProto);
  },

  async deleteAccount(accountId: string): Promise<void> {
    await client().deleteProviderAccount(create(DeleteProviderAccountRequestSchema, { accountId }));
  },

  async addApiKey(
    provider: string,
    apiKey: string,
    label?: string,
  ): Promise<{ account: ProviderAccount | null; created: boolean }> {
    const res = await client().addProviderAPIKey(
      create(AddProviderAPIKeyRequestSchema, { provider, apiKey, label }),
    );
    return { account: res.account ? accountFromProto(res.account) : null, created: res.created };
  },

  async refreshUsage(
    accountId: string,
  ): Promise<{ account: ProviderAccount | null; throttled: boolean }> {
    const res = await client().refreshProviderAccountUsage(
      create(RefreshProviderAccountUsageRequestSchema, { accountId }),
    );
    return { account: res.account ? accountFromProto(res.account) : null, throttled: res.throttled };
  },

  async getChatAccount(chatId: string): Promise<ProviderAccount | null> {
    const res = await client().getChatProviderAccount(
      create(GetChatProviderAccountRequestSchema, { chatId }),
    );
    return res.account ? accountFromProto(res.account) : null;
  },
};
