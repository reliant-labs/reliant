// Questions an agent asked (ask_user), as server data in the React Query cache
// under the keys forge's generated QuestionService hooks use. A blocking
// question is a gate on its thread; an async one (asked without waiting) is
// only ever shown beside its tool call and never takes over the composer.

import { create } from "@bufbuild/protobuf";
import type { QueryClient } from "@tanstack/react-query";
import {
  ListPendingQuestionsResponseSchema,
  QuestionInfoSchema,
  QuestionService,
  type ListAsyncQuestionsResponse,
  type ListPendingQuestionsResponse,
  type QuestionInfo,
} from "#gen/reliant/v1/question_pb.js";
import type { ReliantClient } from "../client/client.js";
import type { QuestionUpdate } from "../client/events.js";
import { questionKeys } from "./keys.js";

export type { QuestionInfo };

const pendingKey = (chatId: string) => questionKeys.listPending({ chatId });
const asyncKey = (chatId: string) => questionKeys.listAsync({ chatId });

function patchPending(queryClient: QueryClient, chatId: string, updater: (prev: QuestionInfo[]) => QuestionInfo[]): void {
  queryClient.setQueryData<ListPendingQuestionsResponse>(pendingKey(chatId), (prev) => {
    const questions = updater(prev?.questions ?? []);
    if (prev && questions === prev.questions) return prev;
    return create(ListPendingQuestionsResponseSchema, { questions });
  });
}

/**
 * Patch the async list only once it has been fetched: a list never seeded is
 * filled by its first fetch, and a partial seed from one event would hide the
 * rest of the chat's questions.
 */
function patchAsync(queryClient: QueryClient, chatId: string, updater: (prev: QuestionInfo[]) => QuestionInfo[]): void {
  const key = asyncKey(chatId);
  const prev = queryClient.getQueryData<ListAsyncQuestionsResponse>(key);
  if (!prev) return;
  const questions = updater(prev.questions);
  if (questions === prev.questions) return;
  queryClient.setQueryData<ListAsyncQuestionsResponse>(key, { ...prev, questions });
}

function upsertPending(queryClient: QueryClient, chatId: string, question: QuestionInfo): void {
  patchPending(queryClient, chatId, (prev) => {
    const idx = prev.findIndex((q) => q.questionId === question.questionId);
    if (idx < 0) return [...prev, question];
    const next = [...prev];
    next[idx] = { ...prev[idx]!, ...question };
    return next;
  });
}

function removePending(queryClient: QueryClient, chatId: string, questionId: string): void {
  patchPending(queryClient, chatId, (prev) =>
    prev.some((q) => q.questionId === questionId) ? prev.filter((q) => q.questionId !== questionId) : prev,
  );
}

function upsertAsync(queryClient: QueryClient, chatId: string, question: QuestionInfo): void {
  patchAsync(queryClient, chatId, (prev) => {
    const idx = prev.findIndex((q) => q.questionId === question.questionId);
    if (idx < 0) return [...prev, question];
    const next = [...prev];
    next[idx] = { ...prev[idx]!, ...question, createdAt: prev[idx]!.createdAt || question.createdAt };
    return next;
  });
}

/** Mark a cached async question resolved: answered, or withdrawn by its agent. It stays listed; its card shows the answer or the reason. */
function markAsyncResolved(
  queryClient: QueryClient,
  chatId: string,
  questionId: string,
  responseData?: string,
  withdrawnReason?: string,
): void {
  patchAsync(queryClient, chatId, (prev) =>
    prev.some((q) => q.questionId === questionId)
      ? prev.map((q) =>
          q.questionId === questionId
            ? {
                ...q,
                status: "resolved",
                responseData: responseData ?? q.responseData,
                withdrawnReason: withdrawnReason ?? q.withdrawnReason,
              }
            : q,
        )
      : prev,
  );
}

/** The question a stream event describes, as the RPC would have returned it. */
function questionOf(update: QuestionUpdate): QuestionInfo {
  return create(QuestionInfoSchema, {
    questionId: update.question_id,
    chatId: update.chat_id,
    workflowId: update.workflow_id,
    threadId: update.thread_id ?? "",
    stepId: update.step_id,
    status: update.status,
    metadata: update.metadata,
    async: !!update.async,
    toolCallId: update.tool_call_id ?? "",
  });
}

/**
 * Fold one question event of a chat's stream into the chat's cached lists. A
 * blocking question goes to the pending list; an async one only to the async
 * list. "pending" adds or merges; "resolved" removes that question alone, since
 * other threads' questions are still waiting.
 */
export function applyQuestionUpdate(queryClient: QueryClient, chatId: string, update: QuestionUpdate): void {
  if (update.async) {
    if (update.status === "pending") upsertAsync(queryClient, chatId, questionOf(update));
    else if (update.status === "resolved") markAsyncResolved(queryClient, chatId, update.question_id, undefined, update.withdrawn_reason);
    return;
  }
  if (update.status === "pending") upsertPending(queryClient, chatId, questionOf(update));
  else if (update.status === "resolved") removePending(queryClient, chatId, update.question_id);
}

/**
 * Resolve a question with any action. The cache is patched at once (the question
 * leaves the pending list; an async one stays listed, answered), then every
 * QuestionService read is invalidated so what the server now says replaces
 * anything the patch got wrong.
 */
export async function resolveQuestion(
  client: ReliantClient,
  queryClient: QueryClient,
  chatId: string,
  questionId: string,
  action: string,
  responseData?: string,
): Promise<void> {
  await client.service(QuestionService).resolveQuestion({ questionId, action, responseData });
  removePending(queryClient, chatId, questionId);
  markAsyncResolved(queryClient, chatId, questionId, responseData);
  void queryClient.invalidateQueries({ queryKey: questionKeys.all });
}

/** What a person's answer to an ask_user form looks like on the wire. */
export interface QuestionAnswers {
  answers: Array<{ question: string; selected: string[]; freetext?: string }>;
}
