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
import { Code, ConnectError } from "@connectrpc/connect";
import { toast } from "sonner";

import {
  ApprovalSchema,
  ApprovalService,
  ApprovalStatus,
  ListApprovalsByChatResponseSchema,
  type Approval,
  type ListApprovalsByChatResponse,
} from "../gen/reliant/v1/approval_pb";
import { planApprovalBatch, type ApprovalBatchPlan } from "../lib/approvalCard";
import { connectClient } from "../lib/connect";
import { hasErrorReason } from "../lib/errorReasons";
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

/**
 * The status a chat feed approval update names. Only "pending" is pending: a
 * status this web does not know (a newer server's) is some resolution, and
 * reading it as pending would show an Approve button that can only fail.
 */
export function approvalStatusFromFeed(status: string | undefined): ApprovalStatus {
  switch (status) {
    case "pending":
      return ApprovalStatus.PENDING;
    case "approved":
      return ApprovalStatus.APPROVED;
    case "denied":
      return ApprovalStatus.DENIED;
    case "expired":
    case "timeout":
      return ApprovalStatus.EXPIRED;
    default:
      return ApprovalStatus.CANCELLED;
  }
}

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
 * The approval was not pending when the server got the request: someone
 * decided it elsewhere, its wait expired, or the run waiting on it ended.
 * Retrying cannot help, so it is not an error to show raw.
 */
export function isApprovalNotActionable(err: unknown): boolean {
  return hasErrorReason(err, "approval_not_pending") || (err instanceof ConnectError && err.code === Code.NotFound);
}

/** The server's sentence for a not-actionable approval ("This request expired: …"). */
export function notActionableMessage(err: unknown): string {
  const text = err instanceof ConnectError ? err.rawMessage : "";
  return text || "This request was already resolved.";
}

/**
 * A write lost to the approval's own end: take the approvals out of the
 * pending list (rolling back would put the dead Approve button back), tell
 * the person what happened, and re-read the chat's approvals from the server.
 */
function dropNotActionable(chatId: string | undefined, ids: readonly string[], err: unknown): void {
  const message = notActionableMessage(err);
  if (chatId) {
    const gone = new Set(ids);
    patchApprovalsCache(chatId, (prev) =>
      prev.map((a) =>
        gone.has(a.id) ? { ...a, status: ApprovalStatus.CANCELLED, denialReason: message, resolvedAt: new Date().toISOString() } : a,
      ),
    );
  }
  toast.info(message);
  void queryClient.invalidateQueries({ queryKey: approvalServiceKeys.all });
}

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
    onError: (err: unknown, req: Req, rollback: Rollback | undefined) => {
      rollback?.();
      if (isApprovalNotActionable(err)) dropNotActionable(chatId, resolve(req)[0], err);
    },
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
 * The chat's pending approvals as "Approve all" treats them, from the cache
 * (no fetch): what one Approve all decides, the activation cards whose grant
 * summaries its confirmation must show, and what stays out (lib/approvalCard).
 */
export function pendingApprovalBatch(chatId: string): ApprovalBatchPlan<Approval> {
  return planApprovalBatch(approvalsFromCache(chatId).filter((a) => a.status === ApprovalStatus.PENDING));
}

/**
 * Imperatively approve the approvals a confirmation showed — for
 * non-component contexts like the global keyboard shortcut. It approves
 * exactly `requestIds` (pendingApprovalBatch's `together`, as shown), never
 * whatever is pending by the time the person confirms: an approval raised
 * after the confirmation opened was not read. Same contract as
 * useBatchApproveInChat: optimistic, rolled back on error, and the service's
 * reads invalidated once the server has recorded it.
 */
export async function approveAllPendingApprovals(chatId: string, requestIds: readonly string[]): Promise<void> {
  const shown = new Set(requestIds);
  const pendingIds = approvalsFromCache(chatId)
    .filter((a) => a.status === ApprovalStatus.PENDING && shown.has(a.id))
    .map((a) => a.id);
  if (pendingIds.length === 0) return;

  const rollback = optimisticallyResolve(chatId, pendingIds, ApprovalStatus.APPROVED);
  try {
    await client.batchApprove({ requestIds: pendingIds });
  } catch (err) {
    rollback();
    if (isApprovalNotActionable(err)) {
      dropNotActionable(chatId, pendingIds, err);
      return;
    }
    throw err;
  }
  void queryClient.invalidateQueries({ queryKey: approvalServiceKeys.all });
}
