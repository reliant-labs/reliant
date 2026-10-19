import { useCallback } from "react";
import { useMutation, useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";

import {
  isProviderAccountsUnavailable,
  providerAccountGrpc,
  type ProviderAccount,
} from "../api/provider-account-grpc";
import { readSetting, upsertStringSetting } from "../lib/settingsPersistence";
import { useAuthStore } from "../store/authStore";

export const providerAccountKeys = {
  all: ["provider-accounts"] as const,
  list: () => [...providerAccountKeys.all, "list"] as const,
  routing: (provider: string) => ["provider-routing", provider] as const,
};

/** What the account list query resolves to: the accounts, or "this server does not serve them". */
export type AccountsResult =
  | { kind: "ready"; accounts: ProviderAccount[] }
  | { kind: "unavailable" };

const REFETCH_MS = 60_000;

/**
 * The caller's provider accounts.
 *
 * "Unimplemented" is DATA here, not an error: a server without the account
 * RPCs (or with forge's scaffold stub answering them) resolves to
 * `{kind: "unavailable"}`, which React Query caches. That is what keeps the
 * probe quiet — no retry loop, no error flash, no polling — and lets Settings
 * fall back to the single-credential view. Any other failure stays an error.
 *
 * While the tab is visible the list refetches every 60 s (usage moves);
 * React Query pauses the interval in a background tab by default.
 */
export function useProviderAccounts() {
  const hasSession = useAuthStore((state) => !!state.session);
  return useQuery<AccountsResult>({
    queryKey: providerAccountKeys.list(),
    queryFn: async () => {
      try {
        return { kind: "ready", accounts: await providerAccountGrpc.listAccounts() };
      } catch (error) {
        if (isProviderAccountsUnavailable(error)) return { kind: "unavailable" };
        throw error;
      }
    },
    enabled: hasSession,
    staleTime: 30_000,
    retry: 1,
    refetchInterval: (query) => (query.state.data?.kind === "unavailable" ? false : REFETCH_MS),
  });
}

export type ProviderAccountsView =
  | { status: "loading" }
  /** Not served by this backend (or failed to load): show today's single-credential view. */
  | { status: "unavailable" }
  | { status: "ready"; accounts: ProviderAccount[] };

/** The three-way gate Settings → Providers renders from. */
export function useProviderAccountsView(): ProviderAccountsView {
  const q = useProviderAccounts();
  if (q.data?.kind === "ready") return { status: "ready", accounts: q.data.accounts };
  if (q.data?.kind === "unavailable" || q.isError) return { status: "unavailable" };
  // Signed out (query disabled) or first fetch in flight.
  return q.isPending && q.fetchStatus !== "idle" ? { status: "loading" } : { status: "unavailable" };
}

function patchAccounts(
  qc: QueryClient,
  patch: (accounts: ProviderAccount[]) => ProviderAccount[],
): void {
  qc.setQueryData<AccountsResult>(providerAccountKeys.list(), (cur) =>
    cur?.kind === "ready" ? { kind: "ready", accounts: patch(cur.accounts) } : cur,
  );
}

function replaceAccount(accounts: ProviderAccount[], next: ProviderAccount): ProviderAccount[] {
  return accounts.map((a) => (a.id === next.id ? next : a));
}

/** The status an account returns to when resumed: still limited if its window has not reset. */
function resumedStatus(account: ProviderAccount, now: number): ProviderAccount["status"] {
  const until = account.limitedUntil ? Date.parse(account.limitedUntil) : NaN;
  return !Number.isNaN(until) && until > now ? "limited" : "available";
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

/**
 * Mutations over the account list. Rename, pause, reorder and remove update
 * the cached list immediately and roll back if the server refuses; each
 * rejects with the server's error so the caller can say what failed.
 */
export function useProviderAccountActions(): ProviderAccountActions {
  const qc = useQueryClient();
  const key = providerAccountKeys.list();

  const snapshot = useCallback(() => qc.getQueryData<AccountsResult>(key), [qc, key]);
  const rollback = useCallback(
    (prev: AccountsResult | undefined) => qc.setQueryData(key, prev),
    [qc, key],
  );

  const renameM = useMutation({
    mutationFn: (v: { id: string; label: string }) =>
      providerAccountGrpc.updateAccount(v.id, { label: v.label }),
    onMutate: async (v) => {
      await qc.cancelQueries({ queryKey: key });
      const prev = snapshot();
      if (v.label.trim()) {
        patchAccounts(qc, (all) =>
          all.map((a) => (a.id === v.id ? { ...a, label: v.label.trim() } : a)),
        );
      }
      return { prev };
    },
    onError: (_e, _v, ctx) => rollback(ctx?.prev),
    onSuccess: (account) => {
      if (account) patchAccounts(qc, (all) => replaceAccount(all, account));
    },
  });

  const pauseM = useMutation({
    mutationFn: (v: { account: ProviderAccount; paused: boolean }) =>
      providerAccountGrpc.updateAccount(v.account.id, { paused: v.paused }),
    onMutate: async (v) => {
      await qc.cancelQueries({ queryKey: key });
      const prev = snapshot();
      patchAccounts(qc, (all) =>
        all.map((a) =>
          a.id === v.account.id
            ? { ...a, status: v.paused ? "paused" : resumedStatus(a, Date.now()) }
            : a,
        ),
      );
      return { prev };
    },
    onError: (_e, _v, ctx) => rollback(ctx?.prev),
    onSuccess: (account) => {
      if (account) patchAccounts(qc, (all) => replaceAccount(all, account));
    },
  });

  const reorderM = useMutation({
    mutationFn: (v: { provider: string; ids: string[] }) =>
      providerAccountGrpc.reorderAccounts(v.provider, v.ids),
    onMutate: async (v) => {
      await qc.cancelQueries({ queryKey: key });
      const prev = snapshot();
      patchAccounts(qc, (all) =>
        all.map((a) => {
          const idx = v.ids.indexOf(a.id);
          return idx >= 0 && a.provider === v.provider ? { ...a, priority: idx } : a;
        }),
      );
      return { prev };
    },
    onError: (_e, _v, ctx) => rollback(ctx?.prev),
    onSuccess: (accounts, v) => {
      if (accounts.length === 0) return;
      patchAccounts(qc, (all) => [...all.filter((a) => a.provider !== v.provider), ...accounts]);
    },
  });

  const removeM = useMutation({
    mutationFn: (account: ProviderAccount) => providerAccountGrpc.deleteAccount(account.id),
    onMutate: async (account) => {
      await qc.cancelQueries({ queryKey: key });
      const prev = snapshot();
      patchAccounts(qc, (all) => all.filter((a) => a.id !== account.id));
      return { prev };
    },
    onError: (_e, _v, ctx) => rollback(ctx?.prev),
    // Deleting re-densifies the provider's priorities server-side.
    onSettled: () => qc.invalidateQueries({ queryKey: key }),
  });

  const refreshM = useMutation({
    mutationFn: (account: ProviderAccount) => providerAccountGrpc.refreshUsage(account.id),
    onSuccess: ({ account }) => {
      if (account) patchAccounts(qc, (all) => replaceAccount(all, account));
    },
  });

  const addKeyM = useMutation({
    mutationFn: (v: { provider: string; apiKey: string; label?: string }) =>
      providerAccountGrpc.addApiKey(v.provider, v.apiKey, v.label),
    onSettled: () => qc.invalidateQueries({ queryKey: key }),
  });

  return {
    rename: async (account, label) => {
      await renameM.mutateAsync({ id: account.id, label });
    },
    setPaused: async (account, paused) => {
      await pauseM.mutateAsync({ account, paused });
    },
    reorder: async (provider, ids) => {
      await reorderM.mutateAsync({ provider, ids });
    },
    remove: async (account) => {
      await removeM.mutateAsync(account);
    },
    refreshUsage: async (account) => (await refreshM.mutateAsync(account)).throttled,
    addApiKey: async (provider, apiKey, label) => {
      const { created } = await addKeyM.mutateAsync({ provider, apiKey, label });
      return { created };
    },
    invalidate: () => qc.invalidateQueries({ queryKey: key }),
  };
}

/**
 * How a provider spreads work across its accounts.
 *  - "balance":  least used account first (the default).
 *  - "priority": the list order — fill the first account until it is limited,
 *                then the next.
 * Neither moves a chat off the account it already runs on, except on a limit.
 */
export type RoutingMode = "balance" | "priority";

export const DEFAULT_ROUTING_MODE: RoutingMode = "balance";

/** The setting the account router reads; one per provider driver id. */
export const routingSettingKey = (provider: string) => `provider.routing.${provider}`;

function parseRoutingMode(value: string | undefined): RoutingMode {
  return value === "priority" ? "priority" : DEFAULT_ROUTING_MODE;
}

export function useRoutingMode(provider: string) {
  const qc = useQueryClient();
  const queryKey = providerAccountKeys.routing(provider);
  const q = useQuery({
    queryKey,
    queryFn: async () => {
      const read = await readSetting(routingSettingKey(provider));
      return parseRoutingMode(read.status === "found" ? read.value : undefined);
    },
    staleTime: 60_000,
  });
  const m = useMutation({
    mutationFn: async (mode: RoutingMode) => {
      await upsertStringSetting(routingSettingKey(provider), mode);
    },
    onMutate: async (mode) => {
      await qc.cancelQueries({ queryKey });
      const prev = qc.getQueryData<RoutingMode>(queryKey);
      qc.setQueryData<RoutingMode>(queryKey, mode);
      return { prev };
    },
    onError: (_e, _mode, ctx) => qc.setQueryData(queryKey, ctx?.prev),
  });
  return {
    mode: q.data ?? DEFAULT_ROUTING_MODE,
    setMode: (mode: RoutingMode) => m.mutateAsync(mode),
  };
}
