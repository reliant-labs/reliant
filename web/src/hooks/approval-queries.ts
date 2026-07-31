import { useQuery, useMutation } from "@tanstack/react-query";
import { api } from "../api/client";
import type { ToolApprovalRequest } from "../api/approval-grpc";
import { ApprovalStatus } from "../api/approval-grpc";
import { questionGrpc, type QuestionInfo } from "../api/question-grpc";
import { queryClient } from "../lib/query-client";

// --- Query key factories ---

export const approvalKeys = {
  all: ["approvals"] as const,
  list: (chatId: string) => [...approvalKeys.all, "list", chatId] as const,
  pending: (chatId: string) =>
    [...approvalKeys.all, "pending", chatId] as const,
};

export const questionKeys = {
  all: ["questions"] as const,
  pending: (chatId: string) =>
    [...questionKeys.all, "pending", chatId] as const,
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
 * Insert or replace a single approval in the cache (by id), from a stream event.
 */
export function upsertApprovalInCache(
  chatId: string,
  approval: ToolApprovalRequest,
): void {
  patchApprovalsCache(chatId, (prev) => {
    const idx = prev.findIndex((a) => a.id === approval.id);
    if (idx >= 0) {
      const next = [...prev];
      next[idx] = approval;
      return next;
    }
    return [...prev, approval];
  });
}

/**
 * Patch the pending-question cache for a chat from a stream event.
 */
export function patchPendingQuestionCache(
  chatId: string,
  question: QuestionInfo | null,
): void {
  queryClient.setQueryData(questionKeys.pending(chatId), question);
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

export function useApprovals(chatId?: string) {
  return useQuery<ToolApprovalRequest[]>({
    queryKey: approvalKeys.list(chatId!),
    queryFn: () => api.approvals.listByChat(chatId!),
    enabled: !!chatId,
  });
}

export function usePendingApprovals(chatId?: string) {
  return useQuery<ToolApprovalRequest[], Error, ToolApprovalRequest[]>({
    queryKey: approvalKeys.list(chatId!),
    queryFn: () => api.approvals.listByChat(chatId!),
    enabled: !!chatId,
    select: (data) =>
      data.filter((a) => a.status === ApprovalStatus.PENDING),
  });
}

export function usePendingQuestion(chatId?: string) {
  return useQuery<QuestionInfo | null>({
    queryKey: questionKeys.pending(chatId!),
    queryFn: () => questionGrpc.getPendingQuestion(chatId!),
    enabled: !!chatId,
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
