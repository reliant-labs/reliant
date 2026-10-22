// Copyright (c) 2025 Reliant Labs

import { useQuery } from "@tanstack/react-query";

import {
  isProviderAccountsUnavailable,
  providerAccountGrpc,
  type ProviderAccount,
} from "../api/provider-account-grpc";
import {
  usageLimitFallbackOf,
  type UsageLimitFallback,
} from "../lib/usageLimitFallback";
import { useAuthStore } from "../store/authStore";
import { useModels } from "../store/globalDataStore";
import { useChatSelection } from "./chat-queries";
import { providerAccountKeys, useProviderAccounts } from "./provider-account-queries";

const REFETCH_MS = 60_000;

export interface ChatAccountUsage {
  /** The account the chat's root thread is using now; undefined before its first model call. */
  active?: ProviderAccount;
  /** Every account the user has, for finding the next one. */
  accounts: ProviderAccount[];
}

/**
 * The account a chat is running on, joined with the user's account list.
 *
 * `GetChatProviderAccount` names the account; `ListProviderAccounts` (shared
 * with Settings → Providers, so a refresh there shows here) supplies the
 * siblings and, when present, the fresher reading. Neither query runs for a
 * user with no accounts, nor against a server that does not serve them.
 */
export function useChatAccountUsage(chatId: string | undefined): ChatAccountUsage {
  const hasSession = useAuthStore((state) => !!state.session);
  const list = useProviderAccounts();
  const accounts = list.data?.kind === "ready" ? list.data.accounts : NO_ACCOUNTS;

  const chatAccount = useQuery<ProviderAccount | null>({
    queryKey: [...providerAccountKeys.all, "chat", chatId] as const,
    queryFn: async () => {
      try {
        return await providerAccountGrpc.getChatAccount(chatId!);
      } catch (error) {
        if (isProviderAccountsUnavailable(error)) return null;
        throw error;
      }
    },
    enabled: hasSession && !!chatId && accounts.length > 0,
    staleTime: 15_000,
    refetchInterval: REFETCH_MS,
    retry: 1,
  });

  const named = chatAccount.data ?? undefined;
  const active = named ? (accounts.find((a) => a.id === named.id) ?? named) : undefined;
  return { active, accounts };
}

const NO_ACCOUNTS: ProviderAccount[] = [];

/**
 * Which provider the chat's model runs on, and what could stand in for it
 * while that provider is limited. Read from the chat's stored selection (what
 * the composer already loaded) and the user's model list.
 */
export function useChatModelFallback(chatId: string | undefined): UsageLimitFallback {
  const selection = useChatSelection(chatId).data;
  const { models, tiers } = useModels();
  return usageLimitFallbackOf({
    params: selection?.params,
    policyFallbackModel: selection?.providerPolicy?.fallbackModel || undefined,
    models,
    tiers,
  });
}
