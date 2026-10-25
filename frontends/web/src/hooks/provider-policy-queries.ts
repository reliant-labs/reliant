// Copyright (c) 2025 Reliant Labs

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { readSetting, upsertStringSetting } from "../lib/settingsPersistence";
import { chatKeys, setChatSelection, useChatSelection } from "./chat-queries";
import {
  POLICY_DEFAULTS_KEY,
  choiceFromDefault,
  choiceFromPolicy,
  parsePolicyDefaults,
  policyFromChoice,
  withDefault,
  type OnLimitChoice,
  type PolicyDefaults,
} from "../lib/providerPolicy";
import * as chatActions from "./chat-actions";

const defaultsKey = ["provider-policy-defaults"] as const;

/** The Settings default on-limit behaviour, by provider. Nothing set = every provider waits. */
export function usePolicyDefaults() {
  const qc = useQueryClient();
  const q = useQuery({
    queryKey: defaultsKey,
    queryFn: async () => {
      const read = await readSetting(POLICY_DEFAULTS_KEY);
      return parsePolicyDefaults(read.status === "found" ? read.value : undefined);
    },
    staleTime: 60_000,
  });
  const m = useMutation({
    mutationFn: async (next: PolicyDefaults) => {
      await upsertStringSetting(POLICY_DEFAULTS_KEY, JSON.stringify(next));
    },
    onMutate: async (next) => {
      await qc.cancelQueries({ queryKey: defaultsKey });
      const prev = qc.getQueryData<PolicyDefaults>(defaultsKey);
      qc.setQueryData<PolicyDefaults>(defaultsKey, next);
      return { prev };
    },
    onError: (_e, _next, ctx) => qc.setQueryData(defaultsKey, ctx?.prev),
  });
  const defaults = q.data ?? {};
  return {
    defaults,
    /** What `driver` does on a limit by default. */
    choiceFor: (driver: string): OnLimitChoice => choiceFromDefault(defaults, driver),
    setChoice: (driver: string, choice: OnLimitChoice) => m.mutateAsync(withDefault(defaults, driver, choice)),
  };
}

/** A chat's own on-limit choice (stored in its selection) and a setter that writes it through the RPC. */
export function useChatOnLimit(chatId: string | undefined) {
  const selection = useChatSelection(chatId);
  const qc = useQueryClient();
  const m = useMutation({
    mutationFn: async (choice: OnLimitChoice) => {
      if (!chatId) return undefined;
      const version = selection.data?.version;
      return chatActions.updateChatProviderPolicy(chatId, policyFromChoice(choice), version);
    },
    onSuccess: (next) => {
      if (chatId && next) setChatSelection(chatId, next);
    },
    onError: () => {
      // A stale version was refused (or the write failed): re-read, so the row shows what the server holds.
      if (chatId) void qc.invalidateQueries({ queryKey: chatKeys.selection(chatId) });
    },
  });
  return {
    choice: choiceFromPolicy(selection.data?.providerPolicy),
    setChoice: (choice: OnLimitChoice) => m.mutateAsync(choice),
    saving: m.isPending,
    error: m.error,
  };
}
