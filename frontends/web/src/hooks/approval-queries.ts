/**
 * A chat's approvals, and every way to decide them: the app's face of
 * @reliantlabs/sdk/react's useApprovals.
 *
 * Approvals are server data and the React Query cache is their single source of
 * truth. The cache holds exactly what ApprovalService.ListApprovalsByChat
 * returns — a `ListApprovalsByChatResponse` under
 * `approvalServiceKeys.listApprovalsByChat({ chatId })` — so a stream event
 * patches the entry every component reads (no refetch round-trip, no shadow
 * copy in Zustand), every write flips the status at once, rolls back on
 * failure and reads the list again once the server has recorded it. The SDK
 * owns all of that (reads, cache patches, the four writes, the "decided
 * elsewhere" handling); this module binds it to the app's QueryClient and
 * keeps the shapes the app's components were written against. The chat's
 * approval cards, the permissions panel, the keyboard shortcut and the inbox
 * rows all decide through it, so there is one implementation of approving.
 *
 * The chat stream is the app's (globalUpdatesStore owns it), so these hooks
 * read and write the cache but open no stream of their own.
 */

import { useCallback, useMemo } from "react";
import { toast } from "sonner";
import {
  ApprovalStatus,
  applyApprovalUpdate as applyApprovalUpdateTo,
  approvalStatusFromFeed,
  approvalsFromCache as approvalsFromQueryClient,
  createApprovalActions,
  isApprovalNotActionable,
  notActionableMessage,
  patchApprovals,
  upsertApproval,
  useApprovalActions,
  useApprovals as useSdkApprovals,
  type Approval,
  type ApprovalActions,
  type ApprovalFields,
} from "@reliantlabs/sdk/react";

import type { ToolApprovalUpdate } from "../types/streaming";
import { planApprovalBatch, type ApprovalBatchPlan } from "../lib/approvalCard";
import { queryClient } from "../lib/query-client";
import { reliantClient } from "../lib/reliantClient";

export type { Approval, ApprovalFields };
export { ApprovalStatus, approvalStatusFromFeed, isApprovalNotActionable, notActionableMessage };

/** A chat's cached approvals, oldest first; [] before the first fetch. */
export function approvalsFromCache(chatId: string): Approval[] {
  return approvalsFromQueryClient(queryClient, chatId);
}

/**
 * Patch a chat's approval list. The updater receives the current list (or []
 * if absent) and returns the next one. Defensive: if the query was never
 * populated it is still seeded, so a streamed approval isn't lost before the
 * initial fetch resolves.
 */
export function patchApprovalsCache(chatId: string, updater: (prev: Approval[]) => Approval[]): void {
  patchApprovals(queryClient, chatId, updater);
}

/** Fold one approval event of a chat's stream into the chat's cached list. */
export function applyApprovalUpdate(chatId: string, update: ToolApprovalUpdate): void {
  applyApprovalUpdateTo(queryClient, chatId, update);
}

/**
 * Insert one approval from a stream event, or merge it over the cached one
 * with the same id: only with the fields the event actually carries, so a
 * resolution event does not erase the title.
 */
export function upsertApprovalInCache(chatId: string, fields: ApprovalFields & { id: string }): void {
  upsertApproval(queryClient, chatId, fields);
}

// --- Query hooks ---
//
// The fetch is only the cold-start seed; freshness comes from the chat stream,
// which both patches approvals live and REPLAYS the ones a reopened chat missed
// (cursor resume) or re-snapshotted. A time-based staleTime would add nothing
// but a ListApprovalsByChat on every chat open, racing that replay.

/** Every approval in a chat (`data.approvals`), any status. */
export function useApprovals(chatId?: string) {
  return useSdkApprovals(chatId, { live: false }).query;
}

/**
 * A chat's pending approvals (`data.approvals`). Shares useApprovals' cache
 * entry — pending is a client-side filter — so one patch updates both.
 */
export function usePendingApprovals(chatId?: string) {
  const { pending, query } = useSdkApprovals(chatId, { live: false });
  const data = useMemo(
    () => (query.data ? (pending === query.data.approvals ? query.data : { ...query.data, approvals: pending, total: pending.length }) : undefined),
    [query.data, pending],
  );
  return useMemo(() => ({ ...query, data }), [query, data]);
}

// --- Writes ---
//
// Approve/deny optimistically flip the approval's status in the chat's list so
// the buttons respond at once, roll back on error, and invalidate the whole
// ApprovalService scope on success so the list converges on what the server
// recorded. A write that lost to the approval's own end takes the approval out
// of the pending list instead of rolling back, and says why.

/** The shape of a mutation the app's components were written against. */
interface Decision<Req> {
  mutate: (req: Req) => void;
  mutateAsync: (req: Req) => Promise<void>;
  isPending: boolean;
}

const tellNotActionable = (message: string): void => void toast.info(message);

function decision<Req>(run: (req: Req) => Promise<void>, isPending: boolean): Decision<Req> {
  return {
    // A write that failed was already rolled back, and the transport reported it.
    mutate: (req) => void run(req).catch(() => undefined),
    mutateAsync: run,
    isPending,
  };
}

function useChatDecisions(chatId: string | undefined) {
  const { approve, deny, approveMany, denyMany, busy } = useApprovalActions(chatId, { onNotActionable: tellNotActionable });
  return { approve, deny, approveMany, denyMany, busy };
}

/** Approve one approval in a chat: `mutate({ requestId, actionTaken? })`. */
export function useApproveToolRequest(chatId?: string) {
  const { approve, busy } = useChatDecisions(chatId);
  const run = useCallback((req: { requestId?: string; actionTaken?: string }) => approve(req.requestId ?? "", { actionTaken: req.actionTaken }), [approve]);
  return decision(run, busy);
}

/** Deny one approval in a chat: `mutate({ requestId, denialReason?, actionTaken? })`. */
export function useDenyToolRequest(chatId?: string) {
  const { deny, busy } = useChatDecisions(chatId);
  const run = useCallback(
    (req: { requestId?: string; denialReason?: string; actionTaken?: string }) =>
      deny(req.requestId ?? "", { denialReason: req.denialReason, actionTaken: req.actionTaken }),
    [deny],
  );
  return decision(run, busy);
}

/** Approve several of a chat's approvals: `mutate({ requestIds, actionTaken? })`. */
export function useBatchApproveInChat(chatId?: string) {
  const { approveMany, busy } = useChatDecisions(chatId);
  const run = useCallback((req: { requestIds?: string[]; actionTaken?: string }) => approveMany(req.requestIds ?? [], { actionTaken: req.actionTaken }), [approveMany]);
  return decision(run, busy);
}

/** Deny several of a chat's approvals: `mutate({ requestIds, denialReason?, actionTaken? })`. */
export function useBatchDenyInChat(chatId?: string) {
  const { denyMany, busy } = useChatDecisions(chatId);
  const run = useCallback(
    (req: { requestIds?: string[]; denialReason?: string; actionTaken?: string }) =>
      denyMany(req.requestIds ?? [], { denialReason: req.denialReason, actionTaken: req.actionTaken }),
    [denyMany],
  );
  return decision(run, busy);
}

/**
 * Decide approvals that belong to no open chat: an inbox row holds approvals of
 * every chat, so there is no list to flip, and a decision that lost to the
 * approval's own end rejects for the row to show "Already handled".
 */
export function useInboxApprovalActions(): ApprovalActions & { busy: boolean } {
  return useApprovalActions(undefined);
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
 * reads invalidated once the server has recorded it. An approval that ended
 * on its own is dropped with a notice, not thrown.
 */
export async function approveAllPendingApprovals(chatId: string, requestIds: readonly string[]): Promise<void> {
  const shown = new Set(requestIds);
  const pendingIds = approvalsFromCache(chatId)
    .filter((a) => a.status === ApprovalStatus.PENDING && shown.has(a.id))
    .map((a) => a.id);
  if (pendingIds.length === 0) return;

  const { approveMany } = createApprovalActions({
    client: reliantClient,
    queryClient,
    chatId,
    onNotActionable: tellNotActionable,
  });
  try {
    await approveMany(pendingIds);
  } catch (err) {
    if (isApprovalNotActionable(err)) return;
    throw err;
  }
}
