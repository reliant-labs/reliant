/**
 * A chat's approvals: ApprovalService on the generated hooks and keys, kept
 * live by the chat stream.
 *
 * Approvals are server data and the React Query cache is their single source
 * of truth. The cache holds exactly what the generated useListApprovalsByChat
 * fetches — a `ListApprovalsByChatResponse` under
 * `approvalServiceKeys.listApprovalsByChat({ chatId })` — so a stream event
 * patches the entry every component reads (no refetch round-trip, no shadow
 * copy in Zustand), every write goes through a generated mutation that
 * invalidates `approvalServiceKeys.all`, and a stream "reconcile" nudge
 * invalidates the same scope. See the forge frontend-state skill, "Real-time
 * server data".
 */

import { create } from "@bufbuild/protobuf";

import {
  ApprovalSchema,
  ApprovalService,
  ApprovalStatus,
  ListApprovalsByChatResponseSchema,
  type Approval,
  type ListApprovalsByChatResponse,
} from "../gen/reliant/v1/approval_pb";
import { connectClient } from "../lib/connect";
import { queryClient } from "../lib/query-client";
import {
  approvalServiceKeys,
  useApprove,
  useBatchApprove,
  useBatchDeny,
  useDeny,
  useListApprovalsByChat,
} from "./approval-service-hooks_gen";

export type { Approval };
export { ApprovalStatus };

const client = connectClient(ApprovalService);

const listKey = (chatId: string) => approvalServiceKeys.listApprovalsByChat({ chatId });

/** A chat's cached approvals, oldest first; [] before the first fetch. */
export function approvalsFromCache(chatId: string): Approval[] {
  return queryClient.getQueryData<ListApprovalsByChatResponse>(listKey(chatId))?.approvals ?? EMPTY;
}
const EMPTY: Approval[] = [];

// --- Real-time cache patching ---

/** Some of an approval's fields, as a stream event carries them. */
export type ApprovalFields = Omit<Partial<Approval>, "$typeName">;

/**
 * Patch a chat's approval list. The updater receives the current list (or []
 * if absent) and returns the next one. Defensive: if the query was never
 * populated it is still seeded, so a streamed approval isn't lost before the
 * initial fetch resolves.
 */
export function patchApprovalsCache(chatId: string, updater: (prev: Approval[]) => Approval[]): void {
  queryClient.setQueryData<ListApprovalsByChatResponse>(listKey(chatId), (prev) => {
    const approvals = updater(prev?.approvals ?? []);
    if (prev && approvals === prev.approvals) return prev;
    return create(ListApprovalsByChatResponseSchema, { approvals, total: approvals.length });
  });
}

/**
 * Insert one approval from a stream event, or merge it over the cached one
 * with the same id. Merged rather than replaced, and only with the fields the
 * event actually carries: a resolution event has the new status but not what
 * the approval asked about (its title), and must not erase it.
 */
export function upsertApprovalInCache(chatId: string, fields: ApprovalFields & { id: string }): void {
  const defined = Object.fromEntries(
    Object.entries(fields).filter(([, value]) => value !== undefined),
  ) as ApprovalFields;
  patchApprovalsCache(chatId, (prev) => {
    const idx = prev.findIndex((a) => a.id === fields.id);
    if (idx >= 0) {
      const next = [...prev];
      next[idx] = { ...prev[idx], ...defined };
      return next;
    }
    return [...prev, create(ApprovalSchema, defined)];
  });
}

// --- Query hooks ---
//
// The fetch is only the cold-start seed; freshness comes from the chat stream.
// Approval events are durable chat updates, so the stream both patches them
// live and REPLAYS the ones a chat missed when it is reopened (cursor resume)
// or re-snapshotted — the latest per entity is in every snapshot. A
// time-based staleTime would add nothing but a ListApprovalsByChat on every
// chat open past it, racing the replay that is already bringing the same
// answer.
const streamMaintained = { staleTime: Infinity } as const;

/** Every approval in a chat (`data.approvals`), any status. */
export function useApprovals(chatId?: string) {
  return useListApprovalsByChat({ chatId: chatId ?? "" }, { enabled: !!chatId, ...streamMaintained });
}

/** Only the pending ones. Stable, so React Query reuses the selection. */
function selectPending(data: ListApprovalsByChatResponse): ListApprovalsByChatResponse {
  const approvals = data.approvals.filter((a) => a.status === ApprovalStatus.PENDING);
  return approvals.length === data.approvals.length ? data : { ...data, approvals, total: approvals.length };
}

/**
 * A chat's pending approvals (`data.approvals`). Shares useApprovals' cache
 * entry — pending is a client-side `select` — so one patch updates both.
 */
export function usePendingApprovals(chatId?: string) {
  return useListApprovalsByChat(
    { chatId: chatId ?? "" },
    { enabled: !!chatId, ...streamMaintained, select: selectPending },
  );
}

// --- Writes ---
//
// Approve/deny optimistically flip the approval's status in the chat's list
// so the buttons respond at once, roll back on error, and — being the
// generated mutations — invalidate approvalServiceKeys.all on success, so the
// list converges on what the server recorded.

/**
 * Optimistically set the status (and stamp resolvedAt / action / reason) on
 * the given approval ids in a chat's cache. Returns a rollback closure that
 * restores the pre-mutation cache.
 */
function optimisticallyResolve(
  chatId: string,
  requestIds: readonly string[],
  status: ApprovalStatus,
  extra: Partial<Approval> = {},
): () => void {
  const key = listKey(chatId);
  const previous = queryClient.getQueryData<ListApprovalsByChatResponse>(key);
  const ids = new Set(requestIds);
  patchApprovalsCache(chatId, (prev) =>
    prev.map((a) => (ids.has(a.id) ? { ...a, status, resolvedAt: new Date().toISOString(), ...extra } : a)),
  );
  return () => queryClient.setQueryData(key, previous);
}

type Rollback = () => void;

/**
 * The optimistic onMutate/onError pair for one chat. onMutate's return value
 * reaches onError as its third argument; the generated hook infers its type
 * (TanStack's TContext) from onMutate, so the rollback is typed end to end.
 */
function optimistic<Req>(chatId: string | undefined, resolve: (req: Req) => [readonly string[], ApprovalStatus, Partial<Approval>]) {
  return {
    onMutate: (req: Req): Rollback => {
      // No chat, no cache entry to patch: nothing to roll back.
      if (!chatId) return () => {};
      const [ids, status, extra] = resolve(req);
      return optimisticallyResolve(chatId, ids, status, extra);
    },
    onError: (_err: unknown, _req: Req, rollback: Rollback | undefined) => rollback?.(),
  };
}

/** Approve one approval in a chat: `mutate({ requestId, actionTaken? })`. */
export function useApproveToolRequest(chatId?: string) {
  return useApprove(
    optimistic(chatId, (req: { requestId?: string; actionTaken?: string }) => [
      [req.requestId ?? ""],
      ApprovalStatus.APPROVED,
      { actionTaken: req.actionTaken },
    ]),
  );
}

/** Deny one approval in a chat: `mutate({ requestId, denialReason?, actionTaken? })`. */
export function useDenyToolRequest(chatId?: string) {
  return useDeny(
    optimistic(chatId, (req: { requestId?: string; denialReason?: string; actionTaken?: string }) => [
      [req.requestId ?? ""],
      ApprovalStatus.DENIED,
      { denialReason: req.denialReason, actionTaken: req.actionTaken },
    ]),
  );
}

/** Approve several of a chat's approvals: `mutate({ requestIds, actionTaken? })`. */
export function useBatchApproveInChat(chatId?: string) {
  return useBatchApprove(
    optimistic(chatId, (req: { requestIds?: string[]; actionTaken?: string }) => [
      req.requestIds ?? [],
      ApprovalStatus.APPROVED,
      { actionTaken: req.actionTaken },
    ]),
  );
}

/** Deny several of a chat's approvals: `mutate({ requestIds, denialReason?, actionTaken? })`. */
export function useBatchDenyInChat(chatId?: string) {
  return useBatchDeny(
    optimistic(chatId, (req: { requestIds?: string[]; denialReason?: string; actionTaken?: string }) => [
      req.requestIds ?? [],
      ApprovalStatus.DENIED,
      { denialReason: req.denialReason, actionTaken: req.actionTaken },
    ]),
  );
}

/**
 * Imperatively approve every pending approval for a chat — for non-component
 * contexts like the global keyboard shortcut. Same contract as
 * useBatchApproveInChat: optimistic, rolled back on error, and the service's
 * reads invalidated once the server has recorded it.
 */
export async function approveAllPendingApprovals(chatId: string): Promise<void> {
  const pendingIds = approvalsFromCache(chatId)
    .filter((a) => a.status === ApprovalStatus.PENDING)
    .map((a) => a.id);
  if (pendingIds.length === 0) return;

  const rollback = optimisticallyResolve(chatId, pendingIds, ApprovalStatus.APPROVED);
  try {
    await client.batchApprove({ requestIds: pendingIds });
  } catch (err) {
    rollback();
    throw err;
  }
  void queryClient.invalidateQueries({ queryKey: approvalServiceKeys.all });
}
