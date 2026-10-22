import { useQuery, useMutation } from "@tanstack/react-query";
import { api } from "../api/client";
import type { ToolApprovalRequest } from "../api/approval-grpc";
import { ApprovalStatus } from "../api/approval-grpc";
import { questionGrpc, type QuestionInfo } from "../api/question-grpc";
import { queryClient } from "../lib/query-client";
import type { QuestionUpdate } from "../types/streaming";

// --- Query key factories ---

export const approvalKeys = {
  all: ["approvals"] as const,
  list: (chatId: string) => [...approvalKeys.all, "list", chatId] as const,
  pending: (chatId: string) =>
    [...approvalKeys.all, "pending", chatId] as const,
};

export const questionKeys = {
  all: ["questions"] as const,
  /** Every pending question in a chat, across all its threads, oldest first. */
  pendingList: (chatId: string) =>
    [...questionKeys.all, "pending-list", chatId] as const,
  /** Every async question in a chat (asked without waiting), oldest first. */
  asyncList: (chatId: string) =>
    [...questionKeys.all, "async-list", chatId] as const,
};

// --- Real-time cache patching ---
//
// Approvals and pending questions are server data: the React Query cache is
// their single source of truth. When the chat stream pushes an approval/question
// event, patch the cache directly with setQueryData — no refetch round-trip, and
// no shadow copy in Zustand. useApprovals/usePendingApprovals share the
// approvalKeys.list(chatId) cache (pending is a client-side `select`), so a
// single list patch updates every subscriber. See the forge frontend-state skill,
// "Real-time server data".

/**
 * Patch a chat's approval list cache. The updater receives the current list
 * (or [] if absent) and returns the next one. Defensive: if the query was
 * never populated, we still seed it so a streamed approval isn't lost before
 * the initial fetch resolves.
 */
export function patchApprovalsCache(
  chatId: string,
  updater: (prev: ToolApprovalRequest[]) => ToolApprovalRequest[],
): void {
  queryClient.setQueryData<ToolApprovalRequest[]>(
    approvalKeys.list(chatId),
    (prev) => updater(prev ?? []),
  );
}

/**
 * Insert a single approval into the cache, or merge it over the cached one
 * with the same id, from a stream event. Merged rather than replaced: a
 * resolution event carries the new status but not what the approval asked
 * about (its title, a tool approval's params), and must not erase them.
 */
export function upsertApprovalInCache(
  chatId: string,
  approval: ToolApprovalRequest,
): void {
  const defined = Object.fromEntries(
    Object.entries(approval).filter(([, value]) => value !== undefined),
  ) as Partial<ToolApprovalRequest>;
  patchApprovalsCache(chatId, (prev) => {
    const idx = prev.findIndex((a) => a.id === approval.id);
    if (idx >= 0) {
      const next = [...prev];
      next[idx] = { ...prev[idx], ...defined };
      return next;
    }
    return [...prev, approval];
  });
}

/**
 * Patch a chat's pending-question list cache. The updater receives the current
 * list (or [] if absent) and returns the next one. Seeded even when the query
 * was never populated, so a streamed question isn't lost before the initial
 * fetch resolves.
 */
function patchPendingQuestionsCache(
  chatId: string,
  updater: (prev: QuestionInfo[]) => QuestionInfo[],
): void {
  queryClient.setQueryData<QuestionInfo[]>(
    questionKeys.pendingList(chatId),
    (prev) => updater(prev ?? []),
  );
}

/**
 * Add a pending question from a stream event, or replace the cached one with
 * the same id. Appended, so the list stays oldest first.
 */
export function upsertPendingQuestionInCache(
  chatId: string,
  question: QuestionInfo,
): void {
  patchPendingQuestionsCache(chatId, (prev) => {
    const idx = prev.findIndex((q) => q.question_id === question.question_id);
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
export function removePendingQuestionFromCache(
  chatId: string,
  questionId: string,
): void {
  patchPendingQuestionsCache(chatId, (prev) =>
    prev.some((q) => q.question_id === questionId)
      ? prev.filter((q) => q.question_id !== questionId)
      : prev,
  );
}

/**
 * Patch a chat's async-question list, but only once it has been fetched: a
 * list that was never seeded is filled by its first fetch, and a partial
 * seed from one event would hide the rest of the chat's questions.
 */
function patchAsyncQuestionsCache(
  chatId: string,
  updater: (prev: QuestionInfo[]) => QuestionInfo[],
): void {
  const key = questionKeys.asyncList(chatId);
  const prev = queryClient.getQueryData<QuestionInfo[]>(key);
  if (!prev) return;
  queryClient.setQueryData<QuestionInfo[]>(key, updater(prev));
}

/** Add an async question from a stream event, or merge into the cached one. */
export function upsertAsyncQuestionInCache(chatId: string, question: QuestionInfo): void {
  patchAsyncQuestionsCache(chatId, (prev) => {
    const idx = prev.findIndex((q) => q.question_id === question.question_id);
    if (idx >= 0) {
      const next = [...prev];
      next[idx] = { ...prev[idx], ...question, created_at: prev[idx].created_at || question.created_at };
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
    prev.some((q) => q.question_id === questionId)
      ? prev.map((q) =>
          q.question_id === questionId
            ? {
                ...q,
                status: "resolved",
                response_data: responseData ?? q.response_data,
                withdrawn_reason: withdrawnReason ?? q.withdrawn_reason,
              }
            : q,
        )
      : prev,
  );
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
      upsertAsyncQuestionInCache(chatId, {
        question_id: qu.question_id,
        chat_id: qu.chat_id,
        workflow_id: qu.workflow_id,
        thread_id: qu.thread_id ?? "",
        step_id: qu.step_id,
        status: qu.status,
        created_at: "",
        metadata: qu.metadata,
        async: true,
        tool_call_id: qu.tool_call_id,
      });
    } else if (qu.status === "resolved") {
      markAsyncQuestionResolvedInCache(chatId, qu.question_id, undefined, qu.withdrawn_reason);
    }
    return;
  }
  if (qu.status === "pending") {
    upsertPendingQuestionInCache(chatId, {
      question_id: qu.question_id,
      chat_id: qu.chat_id,
      workflow_id: qu.workflow_id,
      thread_id: qu.thread_id ?? "",
      step_id: qu.step_id,
      status: qu.status,
      created_at: "",
      metadata: qu.metadata,
    });
  } else if (qu.status === "resolved") {
    removePendingQuestionFromCache(chatId, qu.question_id);
  }
}

/**
 * Answer a pending ask_user question by id, then drop it from that chat's
 * pending list. Store-free, so the chat composer (through
 * chatStore.resolveQuestion) and the Inbox, which holds questions from many
 * chats, share one path.
 */
export async function answerQuestion(
  chatId: string,
  questionId: string,
  answers: { answers: Array<{ question: string; selected: string[]; freetext?: string }> },
): Promise<void> {
  await resolvePendingQuestion(chatId, questionId, "reply", JSON.stringify(answers));
}

/** Resolve a question with any action, and drop it from the chat's pending list. */
export async function resolvePendingQuestion(
  chatId: string,
  questionId: string,
  action: string,
  responseData?: string,
): Promise<void> {
  await questionGrpc.resolveQuestion(questionId, action, responseData);
  // Optimistically drop it; the stream's "resolved" event does the same. An
  // async question stays in its list, answered, for its timeline card.
  removePendingQuestionFromCache(chatId, questionId);
  markAsyncQuestionResolvedInCache(chatId, questionId, responseData);
}

/**
 * Imperatively approve every pending approval for a chat — for non-component
 * contexts like the global keyboard shortcut. Reads pending straight from the
 * cache, optimistically resolves, then calls the batch API (rolling back on
 * error). Components should prefer the useBatchApprove hook.
 */
export async function approveAllPendingApprovals(chatId: string): Promise<void> {
  const list =
    queryClient.getQueryData<ToolApprovalRequest[]>(approvalKeys.list(chatId)) ??
    [];
  const pendingIds = list
    .filter((a) => a.status === ApprovalStatus.PENDING)
    .map((a) => a.id);
  if (pendingIds.length === 0) return;

  const rollback = optimisticallyResolve(
    chatId,
    pendingIds,
    ApprovalStatus.APPROVED,
  );
  try {
    await api.approvals.batchApprove(pendingIds);
  } catch (err) {
    rollback();
    throw err;
  }
}

// --- Query hooks ---
//
// The fetch is only the cold-start seed; freshness comes from the chat stream.
// Approval and question events are durable chat updates, so the stream both
// patches them live and REPLAYS the ones a chat missed when it is reopened
// (cursor resume) or re-snapshotted — the latest per entity is in every
// snapshot. A time-based staleTime added nothing on top of that except a
// ListApprovalsByChat + ListPendingQuestions pair on every chat open past 30s,
// racing the replay that was already bringing the same answer.
const streamMaintained = { staleTime: Infinity } as const;

export function useApprovals(chatId?: string) {
  return useQuery<ToolApprovalRequest[]>({
    queryKey: approvalKeys.list(chatId!),
    queryFn: () => api.approvals.listByChat(chatId!),
    enabled: !!chatId,
    ...streamMaintained,
  });
}

export function usePendingApprovals(chatId?: string) {
  return useQuery<ToolApprovalRequest[], Error, ToolApprovalRequest[]>({
    queryKey: approvalKeys.list(chatId!),
    queryFn: () => api.approvals.listByChat(chatId!),
    enabled: !!chatId,
    ...streamMaintained,
    select: (data) =>
      data.filter((a) => a.status === ApprovalStatus.PENDING),
  });
}

/**
 * Every pending question in a chat, oldest first, each with the thread that
 * asked it. Pick the one for the thread in view with questionForThread
 * (lib/pendingQuestions); never treat the list as "the chat's question".
 */
export function usePendingQuestions(chatId?: string) {
  return useQuery<QuestionInfo[]>({
    queryKey: questionKeys.pendingList(chatId!),
    queryFn: () => questionGrpc.listPendingQuestions(chatId!),
    enabled: !!chatId,
    ...streamMaintained,
  });
}

/**
 * Every async question in a chat (ask_user async: true), pending and
 * answered, oldest first. The timeline renders each on its ask_user tool call
 * (match by tool_call_id). Never the composer's: nothing waits on one.
 */
export function useAsyncQuestions(chatId?: string) {
  return useQuery<QuestionInfo[]>({
    queryKey: questionKeys.asyncList(chatId!),
    queryFn: () => questionGrpc.listAsyncQuestions(chatId!),
    enabled: !!chatId,
    ...streamMaintained,
  });
}

// --- Mutation hooks ---
//
// Approve/deny mutations optimistically flip the approval's status in the cache
// so the buttons respond instantly (matching the old Zustand optimistic path),
// then roll back on error. No success-path invalidate: the server confirms the
// same transition the stream already broadcasts, so a refetch would only add a
// round-trip. Callers pass chatId so we patch the right chat's list.

/**
 * Optimistically set the status (and stamp responded_at / action / reason) on
 * the given approval ids in a chat's cache. Returns a rollback closure that
 * restores the pre-mutation cache.
 */
function optimisticallyResolve(
  chatId: string,
  requestIds: string[],
  status: ApprovalStatus,
  extra: Partial<ToolApprovalRequest> = {},
): () => void {
  const key = approvalKeys.list(chatId);
  const previous = queryClient.getQueryData<ToolApprovalRequest[]>(key);
  const ids = new Set(requestIds);
  patchApprovalsCache(chatId, (prev) =>
    prev.map((a) =>
      ids.has(a.id)
        ? { ...a, status, responded_at: new Date().toISOString(), ...extra }
        : a,
    ),
  );
  return () => queryClient.setQueryData(key, previous);
}

export function useApproveToolRequest() {
  return useMutation({
    mutationFn: ({
      requestId,
      actionTaken,
    }: {
      chatId: string;
      requestId: string;
      actionTaken?: string;
    }) => api.approvals.approve(requestId, actionTaken),
    onMutate: ({ chatId, requestId, actionTaken }) => ({
      rollback: optimisticallyResolve(chatId, [requestId], ApprovalStatus.APPROVED, {
        action_taken: actionTaken,
      }),
    }),
    onError: (_err, _vars, context) => context?.rollback(),
  });
}

export function useDenyToolRequest() {
  return useMutation({
    mutationFn: ({
      requestId,
      denialReason,
      actionTaken,
    }: {
      chatId: string;
      requestId: string;
      denialReason?: string;
      actionTaken?: string;
    }) => api.approvals.deny(requestId, denialReason, actionTaken),
    onMutate: ({ chatId, requestId, denialReason, actionTaken }) => ({
      rollback: optimisticallyResolve(chatId, [requestId], ApprovalStatus.DENIED, {
        denial_reason: denialReason,
        action_taken: actionTaken,
      }),
    }),
    onError: (_err, _vars, context) => context?.rollback(),
  });
}

export function useBatchApprove() {
  return useMutation({
    mutationFn: ({
      requestIds,
      actionTaken,
    }: {
      chatId: string;
      requestIds: string[];
      actionTaken?: string;
    }) => api.approvals.batchApprove(requestIds, actionTaken),
    onMutate: ({ chatId, requestIds, actionTaken }) => ({
      rollback: optimisticallyResolve(chatId, requestIds, ApprovalStatus.APPROVED, {
        action_taken: actionTaken,
      }),
    }),
    onError: (_err, _vars, context) => context?.rollback(),
  });
}

export function useBatchDeny() {
  return useMutation({
    mutationFn: ({
      requestIds,
      denialReason,
      actionTaken,
    }: {
      chatId: string;
      requestIds: string[];
      denialReason?: string;
      actionTaken?: string;
    }) => api.approvals.batchDeny(requestIds, denialReason, actionTaken),
    onMutate: ({ chatId, requestIds, denialReason, actionTaken }) => ({
      rollback: optimisticallyResolve(chatId, requestIds, ApprovalStatus.DENIED, {
        denial_reason: denialReason,
        action_taken: actionTaken,
      }),
    }),
    onError: (_err, _vars, context) => context?.rollback(),
  });
}
