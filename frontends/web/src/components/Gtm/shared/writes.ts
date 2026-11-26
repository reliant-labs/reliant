// Copyright (c) 2026 Reliant Labs

/**
 * What every GTM write does when it fails. Writes are optimistic on the
 * record's `revision` (each update sends `expected_revision`): when someone,
 * or an agent, changed the record since the page read it, the server answers
 * Aborted with a GtmRevisionConflict detail. The page then says so, drops
 * what it had and reads the record again, and the person makes their change
 * on the current version.
 *
 * Two refusals say why in words a person can act on, the server's own
 * message after them: PermissionDenied (a change only an agent's limits
 * forbid, or one this caller may not make) and FailedPrecondition (a
 * transition the item's lifecycle does not allow, or an email held while a
 * send is in doubt: it refuses edits until a person resolves it). Any other
 * failure is the app's usual error toast.
 *
 * A successful write needs nothing here: the generated hooks invalidate every
 * GtmService read on success (gtmServiceKeys.all).
 */

import { Code, ConnectError } from "@connectrpc/connect";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import { useCallback } from "react";

import { GtmRevisionConflictSchema } from "@/gen/reliant/v1/gtm_pb";
import { gtmServiceKeys } from "@/hooks/gtm-service-hooks_gen";
import { toast } from "@/lib/toast-manager";

export const REVISION_CONFLICT_MESSAGE = "This changed since you opened it. The latest version is shown now: make your change again.";

/**
 * Whether a write was refused because the record moved past the revision it
 * named: Aborted, which GtmService answers only for that. The
 * GtmRevisionConflict detail (`revisionConflictOf`) says which revision won,
 * but a refusal without it is still one.
 */
export function isRevisionConflict(error: unknown): boolean {
  return error instanceof ConnectError && error.code === Code.Aborted;
}

/** The conflict's detail (the record, the revision sent, the current one), when the server sent it. */
export function revisionConflictOf(error: unknown) {
  if (!isRevisionConflict(error)) return null;
  return (error as ConnectError).findDetails(GtmRevisionConflictSchema)[0] ?? null;
}

/** The words for a refusal a person can act on, or null for any other failure. */
export function refusalMessage(error: unknown): string | null {
  if (!(error instanceof ConnectError)) return null;
  const why = error.rawMessage.trim();
  const detail = why ? ` ${why.charAt(0).toUpperCase()}${why.slice(1)}${/[.!?]$/.test(why) ? "" : "."}` : "";
  if (error.code === Code.FailedPrecondition) return `This change isn't possible right now.${detail}`;
  if (error.code === Code.PermissionDenied) return `You can't make this change.${detail}`;
  return null;
}

/** Report a failed GTM write: a conflict refreshes what is on screen; a refusal says why; anything else is an error toast. */
export function reportGtmWriteError(client: QueryClient, error: unknown): void {
  if (isRevisionConflict(error)) {
    toast.warning(REVISION_CONFLICT_MESSAGE);
    void client.invalidateQueries({ queryKey: gtmServiceKeys.all });
    return;
  }
  const refusal = refusalMessage(error);
  if (refusal) {
    toast.warning(refusal);
    // What refused it (a status moved, a hold) may have changed under the page.
    void client.invalidateQueries({ queryKey: gtmServiceKeys.all });
    return;
  }
  void toast.error(error);
}

/** `onError` for a GTM mutation hook. */
export function useGtmWriteError(): (error: unknown) => void {
  const client = useQueryClient();
  return useCallback((error: unknown) => reportGtmWriteError(client, error), [client]);
}
