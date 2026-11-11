// Copyright (c) 2026 Reliant Labs

/**
 * The marked worktree item's actions. A live worktree that has been quiet for
 * a day, with its work landed (or clean and idle), is MARKED: its machine puts
 * a tombstone in it (.reliant/RECLAIM_PENDING, and a line in the workspace's
 * reliant.md) and it is reclaimed a day later unless something happens in it
 * first. Nothing has to be done to keep a worktree that is used: any use, or
 * deleting the tombstone, cancels the mark.
 *
 * "Keep" cancels the mark now. "Reclaim now" skips the wait, and only the
 * wait: a worktree used since it was marked, whose tombstone is gone or that
 * has new work is kept and its mark cancelled. Each acts only on the mark the
 * person saw (mark_id).
 */

import { ConnectError } from "@connectrpc/connect";
import { toast } from "sonner";

import type { InboxWorktreeMarked } from "@/gen/reliant/v1/inbox_pb";
import { useKeepMarkedWorktree, useReclaimMarkedWorktreeNow } from "@/hooks/inbox-queries";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import Button from "@/components/forge-ui/button";

const REASON_LABEL: Record<string, string> = {
  landed: "its work has landed",
  "recoverable-idle": "it is clean, pushed and unused",
};

/** The item's one-line detail: why it qualified and when it goes. */
export function markedDetail(item: InboxWorktreeMarked, now: number = Date.now()): string {
  const parts: string[] = [];
  const why = REASON_LABEL[item.reason];
  if (why) parts.push(`Quiet for a day and ${why}`);
  if (item.approved) parts.push("reclaim approved: removed at the next check unless it is used");
  else if (item.reclaimAt) parts.push(`reclaimed ${formatRelativeTime(item.reclaimAt, now)} unless it is used`);
  if (item.path) parts.push(item.path);
  return parts.join(" · ");
}

/** The tooltip: the absolute times. */
export function markedTitle(item: InboxWorktreeMarked): string {
  const parts = [`Marked ${formatAbsoluteTime(item.markedAt)}`];
  if (item.reclaimAt) parts.push(`reclaim ${formatAbsoluteTime(item.reclaimAt)}`);
  return parts.join(" · ");
}

function message(error: unknown): string {
  return error instanceof ConnectError ? error.rawMessage : String(error);
}

export function MarkedWorktreeAction({ item }: { item: InboxWorktreeMarked }) {
  const keep = useKeepMarkedWorktree();
  const reclaim = useReclaimMarkedWorktreeNow();
  const busy = keep.isPending || reclaim.isPending;
  const req = { worktreeId: item.worktreeId, markId: item.markId };

  return (
    <div className="flex shrink-0 items-center gap-2">
      <Button
        size="sm"
        variant="outline"
        disabled={busy}
        onClick={() =>
          keep.mutate(req, {
            onSuccess: () => toast.success(`Keeping ${item.name}`, { description: "Its reclaim mark was cancelled." }),
            onError: (error) => toast.error(`Could not keep ${item.name}`, { description: message(error) }),
          })
        }
      >
        Keep
      </Button>
      <Button
        size="sm"
        variant="outline"
        disabled={busy}
        title={
          item.approved
            ? "Approved: it is retried at every check. Try again now if its machine was unreachable."
            : "Skip the wait. A worktree used since it was marked is kept."
        }
        onClick={() =>
          reclaim.mutate(req, {
            onSuccess: () =>
              toast.success(`Reclaiming ${item.name}`, {
                description: "It is removed once its machine confirms nothing has happened in it.",
              }),
            onError: (error) => toast.error(`Could not reclaim ${item.name}`, { description: message(error) }),
          })
        }
      >
        Reclaim now
      </Button>
    </div>
  );
}
