/**
 * The caller's provider accounts: ProviderAccountService on the generated
 * hooks and keys.
 *
 * The cache holds exactly what the generated hooks fetch — a
 * `ListProviderAccountsResponse` under
 * `providerAccountServiceKeys.listProviderAccounts({})`, a
 * `GetChatProviderAccountResponse` per chat — and every write is a generated
 * mutation, which invalidates `providerAccountServiceKeys.all` on success. So
 * the Settings list and a chat's account chip read the same entries, and no
 * write leaves either stale. Components get plain `ProviderAccount` values
 * (types/providerAccount.ts), converted on read.
 */

import { useMemo } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";

import {
  ProviderAccountService,
  ProviderAccountStatus,
  type ListProviderAccountsResponse,
  type ProviderAccount as ProtoProviderAccount,
} from "../gen/reliant/v1/provider_account_pb";
import { connectClient } from "../lib/connect";
import { queryClient } from "../lib/query-client";
import { useAuthStore } from "../store/authStore";
import { invalidateProviderStatuses } from "./settings-queries";
import {
  accountFromProto,
  isProviderAccountsUnavailable,
  type ProviderAccount,
} from "../types/providerAccount";
import {
  providerAccountServiceKeys,
  useAddProviderAPIKey,
  useDeleteProviderAccount,
  useListProviderAccounts,
  useRefreshProviderAccountUsage,
  useReorderProviderAccounts,
  useUpdateProviderAccount,
} from "./provider-account-service-hooks_gen";

export type { ProviderAccount };

const client = connectClient(ProviderAccountService);

const listKey = () => providerAccountServiceKeys.listProviderAccounts({});

/** What the account list resolves to: the accounts, or "this server does not serve them". */
export type AccountsResult =
  | { kind: "ready"; accounts: ProviderAccount[] }
  | { kind: "unavailable" };

const REFETCH_MS = 60_000;

/** True when a query failed only because the server does not serve the account RPCs. */
const unavailable = (query: { state: { error: unknown } }) => isProviderAccountsUnavailable(query.state.error);

/**
 * The generated ListProviderAccounts read, tuned so that "Unimplemented" is a
 * settled answer rather than an error to chase: a server without the account
 * RPCs (or with forge's scaffold stub answering them) fails once and stays
 * failed — no retry, no refetch on mount, focus or interval — which keeps the
 * probe quiet and lets Settings fall back to the single-credential view. Any
 * other failure retries once and recovers on the next focus or interval.
 *
 * While the tab is visible the list refetches every 60 s (usage moves);
 * React Query pauses the interval in a background tab by default.
 */
function useAccountListQuery() {
  const hasSession = useAuthStore((state) => !!state.session);
  return useListProviderAccounts(
    {},
    {
      enabled: hasSession,
      staleTime: 30_000,
      retry: (failures, error) => !isProviderAccountsUnavailable(error) && failures < 1,
      // An errored query with no data would otherwise refetch on every mount.
      retryOnMount: false,
      refetchOnWindowFocus: (query) => !unavailable(query),
      refetchInterval: (query) => (unavailable(query) ? false : REFETCH_MS),
    },
  );
}

function accountsOf(data: ListProviderAccountsResponse | undefined): ProviderAccount[] | undefined {
  return data?.accounts.map(accountFromProto);
}

/**
 * The caller's provider accounts: `{kind: "ready", accounts}` once listed,
 * `{kind: "unavailable"}` when this server does not serve them, undefined
 * while signed out, loading, or after any other failure.
 */
export function useProviderAccounts(): AccountsResult | undefined {
  const q = useAccountListQuery();
  const accounts = useMemo(() => accountsOf(q.data), [q.data]);
  if (accounts) return { kind: "ready", accounts };
  if (isProviderAccountsUnavailable(q.error)) return { kind: "unavailable" };
  return undefined;
}

export type ProviderAccountsView =
  | { status: "loading" }
  /** Not served by this backend (or failed to load): show today's single-credential view. */
  | { status: "unavailable" }
  | { status: "ready"; accounts: ProviderAccount[] };

/** The three-way gate Settings → Providers renders from. */
export function useProviderAccountsView(): ProviderAccountsView {
  const q = useAccountListQuery();
  const accounts = useMemo(() => accountsOf(q.data), [q.data]);
  if (accounts) return { status: "ready", accounts };
  if (q.isError) return { status: "unavailable" };
  // Signed out (query disabled) or first fetch in flight.
  return q.isPending && q.fetchStatus !== "idle" ? { status: "loading" } : { status: "unavailable" };
}

function patchAccounts(
  qc: QueryClient,
  patch: (accounts: ProtoProviderAccount[]) => ProtoProviderAccount[],
): void {
  qc.setQueryData<ListProviderAccountsResponse>(listKey(), (cur) =>
    cur ? { ...cur, accounts: patch(cur.accounts) } : cur,
  );
}

function replaceAccount(accounts: ProtoProviderAccount[], next: ProtoProviderAccount): ProtoProviderAccount[] {
  return accounts.map((a) => (a.id === next.id ? next : a));
}

/** The status an account returns to when resumed: still limited if its window has not reset. */
function resumedStatus(account: ProtoProviderAccount, now: number): ProviderAccountStatus {
  const until = account.limitedUntil ? Date.parse(account.limitedUntil) : NaN;
  return !Number.isNaN(until) && until > now ? ProviderAccountStatus.LIMITED : ProviderAccountStatus.AVAILABLE;
}

export interface ProviderAccountActions {
  rename: (account: ProviderAccount, label: string) => Promise<void>;
  setPaused: (account: ProviderAccount, paused: boolean) => Promise<void>;
  /** `orderedIds` is the provider's complete account order after the move. */
  reorder: (provider: string, orderedIds: string[]) => Promise<void>;
  remove: (account: ProviderAccount) => Promise<void>;
  /** Resolves true when the server throttled the refresh and sent stored state. */
  refreshUsage: (account: ProviderAccount) => Promise<boolean>;
  addApiKey: (
    provider: string,
    apiKey: string,
    label?: string,
  ) => Promise<{ created: boolean }>;
  /** Refetch the list (after an OAuth sign-in added or re-authed an account). */
  invalidate: () => Promise<void>;
}

/** The list as it was before an optimistic patch, for the rollback. */
interface Snapshot {
  prev: ListProviderAccountsResponse | undefined;
}

/**
 * Mutations over the account list. Rename, pause, reorder and remove update
 * the cached list immediately and roll back if the server refuses; each
 * rejects with the server's error so the caller can say what failed. Every
 * one is a generated mutation, so success also invalidates
 * providerAccountServiceKeys.all and the list converges on what the server
 * recorded.
 */
export function useProviderAccountActions(): ProviderAccountActions {
  const qc = useQueryClient();

  const optimistic = async (patch: (accounts: ProtoProviderAccount[]) => ProtoProviderAccount[]): Promise<Snapshot> => {
    await qc.cancelQueries({ queryKey: listKey() });
    const prev = qc.getQueryData<ListProviderAccountsResponse>(listKey());
    patchAccounts(qc, patch);
    return { prev };
  };
  // onMutate's Snapshot reaches onError typed: the generated hooks infer
  // TanStack's TContext from onMutate.
  const rollback = (_e: unknown, _v: unknown, ctx: Snapshot | undefined) => qc.setQueryData(listKey(), ctx?.prev);
  const applyAccount = (res: { account?: ProtoProviderAccount }) => {
    const account = res.account;
    if (account) patchAccounts(qc, (all) => replaceAccount(all, account));
  };

  const renameM = useUpdateProviderAccount({
    onMutate: (v) => {
      const label = v.label?.trim();
      return optimistic((all) => (label ? all.map((a) => (a.id === v.accountId ? { ...a, label } : a)) : all));
    },
    onError: rollback,
    onSuccess: applyAccount,
  });

  const pauseM = useUpdateProviderAccount({
    onMutate: (v) =>
      optimistic((all) =>
        all.map((a) =>
          a.id === v.accountId
            ? { ...a, status: v.paused ? ProviderAccountStatus.PAUSED : resumedStatus(a, Date.now()) }
            : a,
        ),
      ),
    onError: rollback,
    onSuccess: applyAccount,
  });

  const reorderM = useReorderProviderAccounts({
    onMutate: (v) =>
      optimistic((all) =>
        all.map((a) => {
          const idx = (v.accountIds ?? []).indexOf(a.id);
          return idx >= 0 && a.provider === v.provider ? { ...a, priority: idx } : a;
        }),
      ),
    onError: rollback,
    onSuccess: (res, v) => {
      if (res.accounts.length === 0) return;
      patchAccounts(qc, (all) => [...all.filter((a) => a.provider !== v.provider), ...res.accounts]);
    },
  });

  // Deleting re-densifies the provider's priorities server-side; the
  // generated invalidation refetches them.
  const removeM = useDeleteProviderAccount({
    onMutate: (v) => optimistic((all) => all.filter((a) => a.id !== v.accountId)),
    onError: rollback,
  });

  const refreshM = useRefreshProviderAccountUsage({ onSuccess: applyAccount });

  const addKeyM = useAddProviderAPIKey();

  return {
    rename: async (account, label) => {
      await renameM.mutateAsync({ accountId: account.id, label });
    },
    setPaused: async (account, paused) => {
      await pauseM.mutateAsync({ accountId: account.id, paused });
    },
    reorder: async (provider, accountIds) => {
      await reorderM.mutateAsync({ provider, accountIds });
    },
    remove: async (account) => {
      await removeM.mutateAsync({ accountId: account.id });
    },
    refreshUsage: async (account) => (await refreshM.mutateAsync({ accountId: account.id })).throttled,
    addApiKey: async (provider, apiKey, label) => {
      const { created } = await addKeyM.mutateAsync({ provider, apiKey, label });
      return { created };
    },
    invalidate: () => qc.invalidateQueries({ queryKey: providerAccountServiceKeys.all }),
  };
}

// --- Writes outside a component ---
//
// Settings' single-credential view (api.settings.updateProvider /
// deleteProvider) adds and removes accounts imperatively. They invalidate the
// same scope the generated mutations do, so the account list never shows what
// they just changed as stale — and the provider statuses that view renders,
// which GetProviderStatuses derives from the accounts.

/** Add an API key as a new account of the provider. The same key twice is one account. */
export async function addProviderApiKey(provider: string, apiKey: string): Promise<{ created: boolean }> {
  try {
    const { created } = await client.addProviderAPIKey({ provider, apiKey });
    return { created };
  } finally {
    void queryClient.invalidateQueries({ queryKey: providerAccountServiceKeys.all });
    invalidateProviderStatuses();
  }
}

/** Remove every account of the provider (it becomes disconnected). */
export async function disconnectProvider(provider: string): Promise<void> {
  try {
    const { accounts } = await client.listProviderAccounts({});
    for (const account of accounts) {
      if (account.provider === provider) {
        await client.deleteProviderAccount({ accountId: account.id });
      }
    }
  } finally {
    void queryClient.invalidateQueries({ queryKey: providerAccountServiceKeys.all });
    invalidateProviderStatuses();
  }
}
