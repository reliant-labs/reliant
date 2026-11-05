"use client";
// useQuestions: the questions agents asked in a chat, and answering them.

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo } from "react";
import {
  QuestionService,
  type ListAsyncQuestionsResponse,
  type ListPendingQuestionsResponse,
  type QuestionInfo,
} from "#gen/reliant/v1/question_pb.js";
import type { QuestionUpdate } from "../client/events.js";
import { useReliantClient } from "./context.js";
import { questionKeys } from "./keys.js";
import { applyQuestionUpdate, resolveQuestion, type QuestionAnswers } from "./questions.js";
import { useChatSession } from "./sessions.js";

export interface UseQuestionsOptions {
  /** Keep the lists current from the chat's stream. Default true. */
  live?: boolean;
  enabled?: boolean;
}

export interface UseQuestionsResult {
  /**
   * Every question a thread is blocked on, oldest first, each with the thread
   * that asked it. Pick the one for the thread in view; never treat the list as
   * "the chat's question".
   */
  pending: QuestionInfo[];
  /** Questions asked without waiting, pending and answered, oldest first. Nothing waits on these. */
  async: QuestionInfo[];
  /** Answer a question from its ask_user form. */
  answer(questionId: string, answers: QuestionAnswers): Promise<void>;
  /** Resolve a question with any action. */
  resolve(questionId: string, action: string, responseData?: string): Promise<void>;
}

const EMPTY: QuestionInfo[] = [];

export function useQuestions(chatId: string | undefined, options: UseQuestionsOptions = {}): UseQuestionsResult {
  const client = useReliantClient();
  const queryClient = useQueryClient();
  const { live = true, enabled = true } = options;

  // The fetch only seeds a cold chat; the stream keeps the lists current and
  // replays what a reopened chat missed.
  const pendingQuery = useQuery<ListPendingQuestionsResponse>({
    queryKey: questionKeys.listPending({ chatId: chatId ?? "" }),
    queryFn: ({ signal }) => client.service(QuestionService).listPendingQuestions({ chatId }, { signal }),
    enabled: !!chatId && enabled,
    staleTime: Infinity,
  });
  const asyncQuery = useQuery<ListAsyncQuestionsResponse>({
    queryKey: questionKeys.listAsync({ chatId: chatId ?? "" }),
    queryFn: ({ signal }) => client.service(QuestionService).listAsyncQuestions({ chatId: chatId ?? "" }, { signal }),
    enabled: !!chatId && enabled,
    staleTime: Infinity,
  });

  const session = useChatSession(chatId, undefined, live && enabled);
  useEffect(() => {
    if (!session || !chatId) return;
    let seenPending = new Map<string, QuestionUpdate>();
    let seenAsync = new Set<QuestionUpdate>();
    const feed = () => {
      const { pendingQuestions, asyncQuestions } = session.getState();
      for (const q of pendingQuestions) {
        if (seenPending.get(q.question_id) !== q) applyQuestionUpdate(queryClient, chatId, q);
      }
      // A question that left the reducer's pending list was resolved.
      const now = new Set(pendingQuestions.map((q) => q.question_id));
      for (const [id, q] of seenPending) {
        if (!now.has(id)) applyQuestionUpdate(queryClient, chatId, { ...q, status: "resolved" });
      }
      seenPending = new Map(pendingQuestions.map((q) => [q.question_id, q]));
      for (const q of asyncQuestions) if (!seenAsync.has(q)) applyQuestionUpdate(queryClient, chatId, q);
      seenAsync = new Set(asyncQuestions);
    };
    feed();
    return session.subscribe(feed);
  }, [session, chatId, queryClient]);

  const resolve = useCallback(
    (questionId: string, action: string, responseData?: string) => {
      if (!chatId) return Promise.reject(new Error("@reliantlabs/sdk/react: no chat id"));
      return resolveQuestion(client, queryClient, chatId, questionId, action, responseData);
    },
    [client, queryClient, chatId],
  );
  const answer = useCallback(
    (questionId: string, answers: QuestionAnswers) => resolve(questionId, "reply", JSON.stringify(answers)),
    [resolve],
  );

  const pending = pendingQuery.data?.questions ?? EMPTY;
  const asked = asyncQuery.data?.questions ?? EMPTY;
  return useMemo(() => ({ pending, async: asked, answer, resolve }), [pending, asked, answer, resolve]);
}
