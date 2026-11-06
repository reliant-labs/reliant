// Approvals, one implementation for every surface that shows them: the card in
// a chat's timeline, the permissions panel, and a row of the inbox.
//
// An approval is server data. The React Query cache holds a chat's list under
// the key forge's generated ApprovalService hooks use, so a stream event patches
// the entry every reader sees (no refetch, no shadow copy), every write flips
// the status at once, rolls back on failure and reads the list again once the
// server has recorded it.

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import type { QueryClient } from "@tanstack/react-query";
import {
  ApprovalSchema,
  ApprovalService,
  ApprovalStatus,
  ApprovalType,
  ListApprovalsByChatResponseSchema,
  type Approval,
  type ListApprovalsByChatResponse,
} from "#gen/reliant/v1/approval_pb.js";
import type { ReliantClient } from "../client/client.js";
import type { ToolApprovalUpdate } from "../client/events.js";
import { approvalKeys } from "./keys.js";

export { ApprovalStatus, ApprovalType, type Approval };

/** Some of an approval's fields, as a stream event carries them. */
export type ApprovalFields = Omit<Partial<Approval>, "$typeName">;

const EMPTY: Approval[] = [];
const listKey = (chatId: string) => approvalKeys.listByChat({ chatId });

/**
 * The status a chat feed approval update names. Only "pending" is pending: a
 * status this client does not know (a newer server's) is some resolution, and
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
export function approvalsFromCache(queryClient: QueryClient, chatId: string): Approval[] {
  return queryClient.getQueryData<ListApprovalsByChatResponse>(listKey(chatId))?.approvals ?? EMPTY;
}

/**
 * Patch a chat's approval list. The updater receives the current list (or [])
 * and returns the next one. Seeded even when the query was never populated, so
 * a streamed approval is not lost before the first fetch resolves.
 */
export function patchApprovals(
  queryClient: QueryClient,
  chatId: string,
  updater: (prev: Approval[]) => Approval[],
): void {
  queryClient.setQueryData<ListApprovalsByChatResponse>(listKey(chatId), (prev) => {
    const approvals = updater(prev?.approvals ?? []);
    if (prev && approvals === prev.approvals) return prev;
    return create(ListApprovalsByChatResponseSchema, { approvals, total: approvals.length });
  });
}

/**
 * Insert one approval from a stream event, or merge it over the cached one with
 * the same id. Merged rather than replaced, and only with the fields the event
 * actually carries: a resolution event has the new status but not what the
 * approval asked about (its title), and must not erase it.
 */
export function upsertApproval(
  queryClient: QueryClient,
  chatId: string,
  fields: ApprovalFields & { id: string },
): void {
  const defined = Object.fromEntries(
    Object.entries(fields).filter(([, value]) => value !== undefined),
  ) as ApprovalFields;
  patchApprovals(queryClient, chatId, (prev) => {
    const idx = prev.findIndex((a) => a.id === fields.id);
    if (idx >= 0) {
      const next = [...prev];
      next[idx] = { ...prev[idx]!, ...defined };
      return next;
    }
    return [...prev, create(ApprovalSchema, defined)];
  });
}

/**
 * What a streamed approval carries besides its status: only the fields the
 * update actually has, so a status-only update (approved, denied) merged over
 * the cached approval does not blank its title.
 */
function approvalFieldsOf(update: ToolApprovalUpdate): ApprovalFields {
  const fields: ApprovalFields = {};
  if (update.title) fields.title = update.title;
  // ApprovalCreate sends the name; Approve/Deny echo the row's enum value.
  const kind: unknown = update.approval_type;
  if (kind === "workflow_step" || kind === ApprovalType.WORKFLOW_STEP) fields.approvalType = ApprovalType.WORKFLOW_STEP;
  if (kind === "tool_call" || kind === ApprovalType.TOOL_CALL) fields.approvalType = ApprovalType.TOOL_CALL;
  if (update.batch_id) fields.batchId = update.batch_id;
  return fields;
}

/** Fold one approval event of a chat's stream into the chat's cached list. */
export function applyApprovalUpdate(queryClient: QueryClient, chatId: string, update: ToolApprovalUpdate): void {
  upsertApproval(queryClient, chatId, {
    id: update.id,
    chatId: update.chat_id,
    entityId: update.content_block_id || update.entity_id,
    status: approvalStatusFromFeed(update.status),
    denialReason: update.denial_reason,
    createdAt: update.created_at,
    resolvedAt: update.resolved_at ?? update.responded_at,
    actionTaken: update.action_taken, // Which action was clicked
    ...approvalFieldsOf(update),
  });
}

// ── Failures ────────────────────────────────────────────────────────────────

const REASON_HEADER = "x-forge-error-reason";

/** The stable reason code the server stamps on an error: the runtime chain's, else the header's. */
function reasonOf(err: unknown): string | null {
  if (!(err instanceof ConnectError)) return null;
  const chained = (err as ConnectError & { reason?: unknown }).reason;
  if (typeof chained === "string" && chained) return chained;
  return err.metadata.get(REASON_HEADER) || null;
}

/**
 * The approval was not pending when the server got the request: someone decided
 * it elsewhere, its wait expired, or the run waiting on it ended. Retrying
 * cannot help, so it is not an error to show raw.
 */
export function isApprovalNotActionable(err: unknown): boolean {
  return reasonOf(err) === "approval_not_pending" || (err instanceof ConnectError && err.code === Code.NotFound);
}

/** The server's sentence for a not-actionable approval ("This request expired: ..."). */
export function notActionableMessage(err: unknown): string {
  const text = err instanceof ConnectError ? err.rawMessage : "";
  return text || "This request was already resolved.";
}

// ── Writes ──────────────────────────────────────────────────────────────────

export interface ApprovalActionsOptions {
  client: ReliantClient;
  queryClient: QueryClient;
  /**
   * The chat whose list the writes patch optimistically. Without it (an inbox
   * row, which holds approvals of many chats) a write only reads the list again
   * once the server has recorded it.
   */
  chatId?: string;
  /**
   * Called when a write lost to the approval's own end (decided elsewhere,
   * expired, its run over), with the server's sentence. Supplying it also takes
   * the approvals out of the pending list instead of rolling them back, since
   * rolling back would put a dead Approve button back, and reads the list again.
   */
  onNotActionable?: (message: string, error: unknown) => void;
}

export interface ApprovalActions {
  approve(requestId: string, options?: { actionTaken?: string }): Promise<void>;
  deny(requestId: string, options?: { denialReason?: string; actionTaken?: string }): Promise<void>;
  approveMany(requestIds: readonly string[], options?: { actionTaken?: string }): Promise<void>;
  denyMany(requestIds: readonly string[], options?: { denialReason?: string; actionTaken?: string }): Promise<void>;
}

/**
 * The four writes. Each flips the approvals' status at once (when the chat is
 * known), and on failure puts the list back and rejects with the error, after
 * handling a not-actionable one as `onNotActionable` asks.
 */
export function createApprovalActions(options: ApprovalActionsOptions): ApprovalActions {
  const { client, queryClient, chatId, onNotActionable } = options;
  const service = () => client.service(ApprovalService);

  /** Optimistically resolve `ids` in the chat's list; returns the rollback. */
  const resolveLocally = (
    ids: readonly string[],
    status: ApprovalStatus,
    extra: Partial<Approval> = {},
  ): (() => void) => {
    if (!chatId) return () => {};
    const key = listKey(chatId);
    const previous = queryClient.getQueryData<ListApprovalsByChatResponse>(key);
    const marked = new Set(ids);
    patchApprovals(queryClient, chatId, (prev) =>
      prev.map((a) => (marked.has(a.id) ? { ...a, status, resolvedAt: new Date().toISOString(), ...extra } : a)),
    );
    return () => queryClient.setQueryData(key, previous);
  };

  const dropNotActionable = (ids: readonly string[], err: unknown): void => {
    const message = notActionableMessage(err);
    if (chatId) {
      const gone = new Set(ids);
      patchApprovals(queryClient, chatId, (prev) =>
        prev.map((a) =>
          gone.has(a.id)
            ? { ...a, status: ApprovalStatus.CANCELLED, denialReason: message, resolvedAt: new Date().toISOString() }
            : a,
        ),
      );
    }
    onNotActionable?.(message, err);
    void queryClient.invalidateQueries({ queryKey: approvalKeys.all });
  };

  const run = async (
    ids: readonly string[],
    status: ApprovalStatus,
    extra: Partial<Approval>,
    call: () => Promise<unknown>,
  ): Promise<void> => {
    const rollback = resolveLocally(ids, status, extra);
    try {
      await call();
    } catch (err) {
      rollback();
      if (onNotActionable && isApprovalNotActionable(err)) dropNotActionable(ids, err);
      throw err;
    }
    void queryClient.invalidateQueries({ queryKey: approvalKeys.all });
  };

  return {
    approve: (requestId, o) =>
      run([requestId], ApprovalStatus.APPROVED, { actionTaken: o?.actionTaken }, () =>
        service().approve({ requestId, actionTaken: o?.actionTaken }),
      ),
    deny: (requestId, o) =>
      run([requestId], ApprovalStatus.DENIED, { denialReason: o?.denialReason, actionTaken: o?.actionTaken }, () =>
        service().deny({ requestId, denialReason: o?.denialReason, actionTaken: o?.actionTaken }),
      ),
    approveMany: (requestIds, o) =>
      run(requestIds, ApprovalStatus.APPROVED, { actionTaken: o?.actionTaken }, () =>
        service().batchApprove({ requestIds: [...requestIds], actionTaken: o?.actionTaken }),
      ),
    denyMany: (requestIds, o) =>
      run(
        requestIds,
        ApprovalStatus.DENIED,
        { denialReason: o?.denialReason, actionTaken: o?.actionTaken },
        () => service().batchDeny({ requestIds: [...requestIds], denialReason: o?.denialReason, actionTaken: o?.actionTaken }),
      ),
  };
}
