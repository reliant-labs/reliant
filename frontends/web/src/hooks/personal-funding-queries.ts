// Copyright (c) 2025 Reliant Labs

/**
 * A chat's question "continue on your own account?" (control-plane
 * docs/design/org-provider-accounts.md §10): before a chat in an org moves from
 * the org's AI accounts to the person's own, it parks and asks once, for every
 * thread of the chat. The server decides when to ask and holds the run; this
 * reads the question and answers it.
 */

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { create } from "zustand";

import {
  PersonalFundingQuestion_Reason,
  PersonalFundingQuestion_State,
  ResolvePersonalFundingChoiceRequest_Choice,
  type PersonalFundingQuestion as ProtoQuestion,
} from "../gen/reliant/v1/provider_account_pb";
import { hasControlPlane } from "../services/controlPlane/config";
import { useAuthStore } from "../store/authStore";
import { rememberOrgFundingLocally } from "./org-funding-queries";
import {
  providerAccountServiceKeys,
  providerAccountServiceMutations,
  useGetPersonalFundingQuestion,
} from "./provider-account-service-hooks_gen";

export type FundingReason = "limited" | "unusable" | "unserved";

export interface FundingQuestion {
  chatId: string;
  orgId: string;
  reason: FundingReason;
  /** Asked and not answered: the chat waits. */
  open: boolean;
  /** The model the chat's turn resolved to (a registry id). */
  model: string;
  /** The person's own account that would serve: its driver, and its label. */
  personalProvider: string;
  personalAccountLabel: string;
  /** When the org's accounts reset (reason "limited"), epoch ms. */
  orgResetAt?: number;
  askedAt: string;
}

const REASON: Record<PersonalFundingQuestion_Reason, FundingReason> = {
  [PersonalFundingQuestion_Reason.UNSPECIFIED]: "unusable",
  [PersonalFundingQuestion_Reason.LIMITED]: "limited",
  [PersonalFundingQuestion_Reason.UNUSABLE]: "unusable",
  [PersonalFundingQuestion_Reason.UNSERVED]: "unserved",
};

export function fundingQuestionFromProto(q: ProtoQuestion): FundingQuestion {
  const reset = q.orgResetAt ? Date.parse(q.orgResetAt) : NaN;
  return {
    chatId: q.chatId,
    orgId: q.orgId,
    reason: REASON[q.reason] ?? "unusable",
    open: q.state === PersonalFundingQuestion_State.ASKED,
    model: q.model,
    personalProvider: q.personalProvider,
    personalAccountLabel: q.personalAccountLabel,
    orgResetAt: Number.isFinite(reset) ? reset : undefined,
    askedAt: q.askedAt,
  };
}

const select = (res: { question?: ProtoQuestion }) => (res.question ? fundingQuestionFromProto(res.question) : null);

/**
 * The chat's question, or null when it has none. Read when the chat opens, and
 * every 15 s while `waiting` (the run is held: the question may arrive).
 */
export function usePersonalFundingQuestion(chatId: string | undefined, waiting: boolean) {
  const hasSession = useAuthStore((s) => !!s.session);
  return useGetPersonalFundingQuestion(
    { chatId: chatId ?? "" },
    {
      enabled: hasControlPlane && hasSession && !!chatId,
      select,
      staleTime: 10_000,
      refetchInterval: waiting ? 15_000 : false,
      retry: false,
    },
  );
}

export type FundingChoice = "use_personal" | "keep_waiting";

/** Answer the chat's question; `remember` also sets the person's preference for the org. */
export function useResolvePersonalFunding(chatId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async ({ choice, remember }: { choice: FundingChoice; remember: boolean; orgId: string }) =>
      providerAccountServiceMutations(qc).resolvePersonalFundingChoice({
        chatId,
        choice:
          choice === "use_personal"
            ? ResolvePersonalFundingChoiceRequest_Choice.USE_PERSONAL
            : ResolvePersonalFundingChoiceRequest_Choice.KEEP_WAITING,
        remember,
      }),
    onSuccess: (_res, { choice, remember, orgId }) => {
      if (remember && orgId) rememberOrgFundingLocally(qc, orgId, choice === "use_personal" ? "use_personal" : "wait");
      void qc.invalidateQueries({ queryKey: providerAccountServiceKeys.getPersonalFundingQuestion({ chatId }) });
    },
  });
}

/**
 * Which chat's question the popup shows, and which questions the person closed
 * without answering (by chat and when it was asked), so a closed popup does not
 * reopen by itself until the chat asks again. "Review" on the waiting card
 * reopens it.
 */
interface PromptState {
  openFor: string | null;
  closed: Record<string, string>;
  open: (chatId: string) => void;
  close: (chatId: string, askedAt: string) => void;
}

export const usePersonalFundingPrompt = create<PromptState>((set) => ({
  openFor: null,
  closed: {},
  open: (chatId) => set({ openFor: chatId }),
  close: (chatId, askedAt) => set((s) => ({ openFor: s.openFor === chatId ? null : s.openFor, closed: { ...s.closed, [chatId]: askedAt } })),
}));
