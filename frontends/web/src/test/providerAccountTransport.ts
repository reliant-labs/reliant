/**
 * ProviderAccountService at the transport, for tests that describe accounts
 * as frontend `ProviderAccount` values.
 *
 * Each handler forwards to a plain function with the old wrapper's shape
 * (`listAccounts()`, `updateAccount(id, patch)`, …), so a test keeps stating
 * its fixtures and assertions in frontend terms while the code under test
 * runs the real generated hooks over `setTransport(mockTransport(...))`.
 */

import { create } from "@bufbuild/protobuf";

import {
  ProviderAccountSchema,
  ProviderAccountStatus,
  ProviderAuthKind,
  UsageSource,
  type ProviderAccount as ProtoProviderAccount,
} from "@/gen/reliant/v1/provider_account_pb";
import type { MockHandlers } from "@/lib/test-utils";
import type {
  AccountAuthKind,
  AccountStatus,
  ProviderAccount,
  UsageSourceKind,
} from "@/types/providerAccount";

const STATUS: Record<AccountStatus, ProviderAccountStatus> = {
  available: ProviderAccountStatus.AVAILABLE,
  limited: ProviderAccountStatus.LIMITED,
  paused: ProviderAccountStatus.PAUSED,
  needs_sign_in: ProviderAccountStatus.NEEDS_SIGN_IN,
};

const AUTH_KIND: Record<AccountAuthKind, ProviderAuthKind> = {
  oauth: ProviderAuthKind.OAUTH,
  api_key: ProviderAuthKind.API_KEY,
  device: ProviderAuthKind.DEVICE,
  managed: ProviderAuthKind.MANAGED,
};

const USAGE_SOURCE: Record<UsageSourceKind, UsageSource> = {
  unknown: UsageSource.UNSPECIFIED,
  headers: UsageSource.HEADERS,
  usage_api: UsageSource.USAGE_API,
  limit_error: UsageSource.LIMIT_ERROR,
};

/** The wire form of a frontend account — the inverse of `accountFromProto`. */
export function accountToProto(a: ProviderAccount): ProtoProviderAccount {
  return create(ProviderAccountSchema, {
    ...a,
    authKind: AUTH_KIND[a.authKind],
    status: STATUS[a.status],
    usageSource: USAGE_SOURCE[a.usageSource],
    windows: a.windows.map((w) => ({ ...w, lengthSeconds: BigInt(w.lengthSeconds) })),
  });
}

const maybe = (a: ProviderAccount | null | undefined) => (a ? accountToProto(a) : undefined);

type Fn<A extends unknown[], R> = (...args: A) => R | Promise<R>;

export interface ProviderAccountFakes {
  listAccounts?: Fn<[], ProviderAccount[]>;
  updateAccount?: Fn<[string, { label?: string; paused?: boolean }], ProviderAccount | null>;
  reorderAccounts?: Fn<[string, string[]], ProviderAccount[]>;
  deleteAccount?: Fn<[string], void>;
  addApiKey?: Fn<[string, string, string | undefined], { account: ProviderAccount | null; created: boolean }>;
  refreshUsage?: Fn<[string], { account: ProviderAccount | null; throttled: boolean }>;
  getChatAccount?: Fn<[string], ProviderAccount | null>;
}

/** mockTransport handlers for ProviderAccountService, forwarding to `fakes`. */
export function providerAccountHandlers(fakes: ProviderAccountFakes): MockHandlers {
  const handlers: MockHandlers = {};
  if (fakes.listAccounts) {
    const f = fakes.listAccounts;
    handlers["ProviderAccountService.ListProviderAccounts"] = async () => ({
      accounts: (await f()).map(accountToProto),
    });
  }
  if (fakes.updateAccount) {
    const f = fakes.updateAccount;
    handlers["ProviderAccountService.UpdateProviderAccount"] = async (req) => {
      const r = req as { accountId: string; label?: string; paused?: boolean };
      const patch: { label?: string; paused?: boolean } = {};
      if (r.label !== undefined) patch.label = r.label;
      if (r.paused !== undefined) patch.paused = r.paused;
      return { account: maybe(await f(r.accountId, patch)) };
    };
  }
  if (fakes.reorderAccounts) {
    const f = fakes.reorderAccounts;
    handlers["ProviderAccountService.ReorderProviderAccounts"] = async (req) => {
      const r = req as { provider: string; accountIds: string[] };
      return { accounts: (await f(r.provider, [...r.accountIds])).map(accountToProto) };
    };
  }
  if (fakes.deleteAccount) {
    const f = fakes.deleteAccount;
    handlers["ProviderAccountService.DeleteProviderAccount"] = async (req) => {
      await f((req as { accountId: string }).accountId);
      return {};
    };
  }
  if (fakes.addApiKey) {
    const f = fakes.addApiKey;
    handlers["ProviderAccountService.AddProviderAPIKey"] = async (req) => {
      const r = req as { provider: string; apiKey: string; label?: string };
      const res = await f(r.provider, r.apiKey, r.label);
      return { account: maybe(res.account), created: res.created };
    };
  }
  if (fakes.refreshUsage) {
    const f = fakes.refreshUsage;
    handlers["ProviderAccountService.RefreshProviderAccountUsage"] = async (req) => {
      const res = await f((req as { accountId: string }).accountId);
      return { account: maybe(res.account), throttled: res.throttled };
    };
  }
  if (fakes.getChatAccount) {
    const f = fakes.getChatAccount;
    handlers["ProviderAccountService.GetChatProviderAccount"] = async (req) => ({
      account: maybe(await f((req as { chatId: string }).chatId)),
    });
  }
  return handlers;
}
