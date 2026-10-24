/**
 * Pending and async questions: QuestionService's two reads, on the generated
 * hooks and keys, kept live by the chat stream.
 *
 * The React Query cache is the single source of truth, and it holds exactly
 * what the generated hooks fetch — `ListPendingQuestionsResponse` /
 * `ListAsyncQuestionsResponse` under `questionServiceKeys` — so a stream event
 * patches the same entry a component reads, and invalidating
 * `questionServiceKeys.all` reaches every one of them. See the forge
 * frontend-state skill, "Real-time server data".
 */

import { create } from "@bufbuild/protobuf";

import {
  ListPendingQuestionsResponseSchema,
  QuestionInfoSchema,
  QuestionService,
  type ListAsyncQuestionsResponse,
  type ListPendingQuestionsResponse,
  type QuestionInfo,
} from "../gen/reliant/v1/question_pb";
import { connectClient } from "../lib/connect";
import { queryClient } from "../lib/query-client";
import type { QuestionUpdate } from "../types/streaming";
import {
  questionServiceKeys,
  useListAsyncQuestions,
  useListPendingQuestions,
} from "./question-service-hooks_gen";

export type { QuestionInfo };

const client = connectClient(QuestionService);

const pendingKey = (chatId: string) => questionServiceKeys.listPendingQuestions({ chatId });
const asyncKey = (chatId: string) => questionServiceKeys.listAsyncQuestions({ chatId });

// --- Query hooks ---
//
// The fetch is only the cold-start seed; freshness comes from the chat stream.
// Question events are durable chat updates, so the stream both patches them
// live and REPLAYS the ones a chat missed when it is reopened (cursor resume)
// or re-snapshotted. A time-based staleTime would add nothing but a
// ListPendingQuestions on every chat open past it, racing the replay that is
// already bringing the same answer.
const streamMaintained = { staleTime: Infinity } as const;

/**
 * Every pending question in a chat, oldest first, each with the thread that
 * asked it (`data.questions`). Pick the one for the thread in view with
 * questionForThread (lib/pendingQuestions); never treat the list as "the
 * chat's question".
 */
export function usePendingQuestions(chatId?: string) {
  return useListPendingQuestions({ chatId: chatId ?? "" }, { enabled: !!chatId, ...streamMaintained });
}

/**
 * Every async question in a chat (ask_user without blocking: true), pending and
 * answered, oldest first (`data.questions`). The timeline renders each on its
 * ask_user tool call (match by toolCallId). Never the composer's: nothing
 * waits on one.
 */
export function useAsyncQuestions(chatId?: string) {
  return useListAsyncQuestions({ chatId: chatId ?? "" }, { enabled: !!chatId, ...streamMaintained });
}

// --- Real-time cache patching ---

/**
 * Patch a chat's pending-question list. Seeded even when the query was never
 * populated, so a streamed question isn't lost before the initial fetch
 * resolves.
 */
function patchPendingQuestionsCache(
  chatId: string,
  updater: (prev: QuestionInfo[]) => QuestionInfo[],
): void {
  queryClient.setQueryData<ListPendingQuestionsResponse>(pendingKey(chatId), (prev) => {
    const questions = updater(prev?.questions ?? []);
    if (prev && questions === prev.questions) return prev;
    return create(ListPendingQuestionsResponseSchema, { questions });
  });
}

/**
 * Add a pending question from a stream event, or merge it over the cached one
 * with the same id. Appended, so the list stays oldest first.
 */
export function upsertPendingQuestionInCache(chatId: string, question: QuestionInfo): void {
  patchPendingQuestionsCache(chatId, (prev) => {
    const idx = prev.findIndex((q) => q.questionId === question.questionId);
    if (idx >= 0) {
      const next = [...prev];
      next[idx] = { ...prev[idx], ...question };
      return next;
    }
    return [...prev, question];
  });
}

/**
 * Drop one resolved question from a chat's pending list. Only that one: other
 * threads' questions are still waiting.
 */
export function removePendingQuestionFromCache(chatId: string, questionId: string): void {
  patchPendingQuestionsCache(chatId, (prev) =>
    prev.some((q) => q.questionId === questionId) ? prev.filter((q) => q.questionId !== questionId) : prev,
  );
}

/**
 * Patch a chat's async-question list, but only once it has been fetched: a
 * list that was never seeded is filled by its first fetch, and a partial seed
 * from one event would hide the rest of the chat's questions.
 */
function patchAsyncQuestionsCache(chatId: string, updater: (prev: QuestionInfo[]) => QuestionInfo[]): void {
  const key = asyncKey(chatId);
  const prev = queryClient.getQueryData<ListAsyncQuestionsResponse>(key);
  if (!prev) return;
  const questions = updater(prev.questions);
  if (questions === prev.questions) return;
  queryClient.setQueryData<ListAsyncQuestionsResponse>(key, { ...prev, questions });
}

/** Add an async question from a stream event, or merge into the cached one. */
export function upsertAsyncQuestionInCache(chatId: string, question: QuestionInfo): void {
  patchAsyncQuestionsCache(chatId, (prev) => {
    const idx = prev.findIndex((q) => q.questionId === question.questionId);
    if (idx >= 0) {
      const next = [...prev];
      next[idx] = { ...prev[idx], ...question, createdAt: prev[idx].createdAt || question.createdAt };
      return next;
    }
    return [...prev, question];
  });
}

/**
 * Mark a cached async question resolved — answered, or withdrawn by its agent
 * (it stays listed: its card shows the answer or the reason).
 */
function markAsyncQuestionResolvedInCache(
  chatId: string,
  questionId: string,
  responseData?: string,
  withdrawnReason?: string,
): void {
  patchAsyncQuestionsCache(chatId, (prev) =>
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
function questionFromUpdate(qu: QuestionUpdate): QuestionInfo {
  return create(QuestionInfoSchema, {
    questionId: qu.question_id,
    chatId: qu.chat_id,
    workflowId: qu.workflow_id,
    threadId: qu.thread_id ?? "",
    stepId: qu.step_id,
    status: qu.status,
    metadata: qu.metadata,
    async: !!qu.async,
    toolCallId: qu.tool_call_id ?? "",
  });
}

/**
 * Apply one question event from the chat stream. A blocking question goes to
 * the pending list (the composer and the background-work pill read it); an
 * async one goes only to the async list (the timeline's cards read it) — it
 * has no thread parked on it, so it must never take over the composer.
 */
export function applyQuestionUpdate(chatId: string, qu: QuestionUpdate): void {
  if (qu.async) {
    if (qu.status === "pending") {
      upsertAsyncQuestionInCache(chatId, questionFromUpdate(qu));
    } else if (qu.status === "resolved") {
      markAsyncQuestionResolvedInCache(chatId, qu.question_id, undefined, qu.withdrawn_reason);
    }
    return;
  }
  if (qu.status === "pending") {
    upsertPendingQuestionInCache(chatId, questionFromUpdate(qu));
  } else if (qu.status === "resolved") {
    removePendingQuestionFromCache(chatId, qu.question_id);
  }
}

// --- Writes ---

/**
 * Answer a pending ask_user question by id. Store-free, so the chat composer
 * (through chatStore.resolveQuestion), the timeline's async cards and the
 * Inbox, which holds questions from many chats, share one path.
 */
export async function answerQuestion(
  chatId: string,
  questionId: string,
  answers: { answers: Array<{ question: string; selected: string[]; freetext?: string }> },
): Promise<void> {
  await resolvePendingQuestion(chatId, questionId, "reply", JSON.stringify(answers));
}

/**
 * Resolve a question with any action. The cache is patched at once — the
 * question leaves the pending list, and an async one stays in its list,
 * answered, for its timeline card — and then every QuestionService read is
 * invalidated, the same contract as the generated useResolveQuestion, so
 * what the server now says replaces anything the patch got wrong.
 */
export async function resolvePendingQuestion(
  chatId: string,
  questionId: string,
  action: string,
  responseData?: string,
): Promise<void> {
  await client.resolveQuestion({ questionId, action, responseData });
  removePendingQuestionFromCache(chatId, questionId);
  markAsyncQuestionResolvedInCache(chatId, questionId, responseData);
  void queryClient.invalidateQueries({ queryKey: questionServiceKeys.all });
}
