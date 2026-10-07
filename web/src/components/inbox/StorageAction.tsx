// Copyright (c) 2025 Reliant Labs

/**
 * The storage item's "Clean up" action. A machine keeps archived worktrees it
 * cannot prove are safe to delete. This lists them with size and reason and
 * removes only what the user confirms, and only what Clean up is able to remove.
 *
 * Clean up works on work that lives in git: uncommitted and untracked files and
 * unpushed commits. The machine saves those to a LOCAL git ref, verifies the
 * save, then removes the directory. Nothing is pushed and no branch is deleted.
 * It does NOT touch data: workspaces holding a ./data directory, .env files,
 * databases, another git repository, or files outside their repositories are
 * listed as "remove manually" and are never deleted from here.
 *
 * It runs in the background; the item shows progress. The ids sent are the ones
 * listed here, so a workspace archived while the dialog is open is never
 * removed unseen.
 */

import { useState } from "react";
import { toast } from "sonner";

import { type InboxStorage, type HeldWorktree, CleanupStorageOutcome } from "@/gen/reliant/v1/inbox_pb";
import { useCleanupStorage } from "@/hooks/inbox-queries";
import { formatBytes } from "@/lib/formatBytes";
import { Button } from "../ui/Button";
import { Modal } from "../ui/Modal";
import { CardInset } from "../forge-ui/card";

const REASON_LABEL: Record<string, string> = {
  dirty: "Uncommitted changes",
  unpushed: "Commits not pushed",
  in_use: "In use right now",
  unverified: "Could not be checked",
  "too-large-to-snapshot": "Too large to save",
  unreachable_history: "Has commits on no branch",
  kept: "Kept by your archive setting",
  data: "Contains data",
  "nested-repository": "Contains another git repository",
  "files-outside-checkout": "Has files outside its repositories",
  unmanaged: "Not managed by Reliant",
  quarantined: "Parked in quarantine",
};

export function reasonLabel(reason: string): string {
  return REASON_LABEL[reason] ?? reason;
}

/** What Clean up will do with a worktree, in the dialog's words. */
export function removalNote(held: HeldWorktree): string {
  if (!held.removable) return "Contains data Reliant will not delete. Remove it manually.";
  switch (held.reason) {
    case "in_use":
      return "Skipped while a process is running inside it.";
    case "unverified":
      return "Skipped unless its work can be saved and verified.";
    case "too-large-to-snapshot":
      return "Skipped: too large to save safely on this disk.";
    case "unreachable_history":
      return "Commits on no branch are saved to local git refs first.";
    case "kept":
      return "Nothing to save; removing it frees the space.";
    default:
      return "Work is saved to a local git ref first.";
  }
}

export function removable(held: HeldWorktree[]): HeldWorktree[] {
  return held.filter((w) => w.removable && !w.cleaning);
}

export function totalHeldBytes(held: HeldWorktree[]): number {
  return held.reduce((sum, w) => sum + Number(w.sizeBytes), 0);
}

export function StorageAction({ storage, label }: { storage: InboxStorage; label: string }) {
  const [open, setOpen] = useState(false);
  const cleanup = useCleanupStorage();
  // Frozen at open so a refetch while the user is reading cannot change what a
  // click on Remove acts on.
  const [shown, setShown] = useState<HeldWorktree[]>([]);

  if (storage.held.length === 0) return null;
  const cleaning = storage.held.filter((w) => w.cleaning).length;
  const canClean = removable(storage.held).length > 0;

  const openDialog = () => {
    setShown(storage.held);
    setOpen(true);
  };

  const toRemove = removable(shown);
  const manual = shown.filter((w) => !w.removable);

  const confirm = () => {
    cleanup.mutate(
      { daemonId: storage.daemonId, worktreeIds: toRemove.map((w) => w.worktreeId) },
      {
        onSuccess: (response) => {
          const started = response.results.filter((r) => r.outcome === CleanupStorageOutcome.ACCEPTED).length;
          const skipped = response.results.length - started;
          toast.success(started > 0 ? `Cleaning up ${started} ${started === 1 ? "workspace" : "workspaces"}` : "Nothing was started", {
            description: [
              started > 0 ? "This runs in the background; the item updates as each one finishes." : "",
              skipped > 0 ? `${skipped} left in place.` : "",
            ]
              .filter(Boolean)
              .join(" "),
          });
          setOpen(false);
        },
        onError: (error) =>
          toast.error("Could not start the clean-up", { description: error instanceof Error ? error.message : String(error) }),
      },
    );
  };

  return (
    <>
      <Button
        size="sm"
        variant="outline"
        onClick={openDialog}
        disabled={!storage.online || cleaning > 0 || !canClean}
        title={
          !storage.online
            ? `${label} is offline`
            : cleaning > 0
              ? "A clean-up is already running"
              : !canClean
                ? "Nothing here can be removed by Clean up; remove it manually"
                : undefined
        }
      >
        {cleaning > 0 ? "Cleaning up…" : "Clean up"}
      </Button>
      <Modal isOpen={open} onClose={() => !cleanup.isPending && setOpen(false)} title={`Clean up ${label}`} size="md">
        <div className="flex flex-col gap-4">
          <p className="text-pretty text-sm text-muted-foreground">
            Removing these archived workspaces frees about{" "}
            <span className="font-medium text-foreground">{formatBytes(totalHeldBytes(toRemove))}</span>. Uncommitted
            changes, untracked files and unpushed commits are first saved to a local git ref on that machine; nothing is
            pushed and no branch is deleted. Ignored files that are only build output are deleted with the directory.
          </p>
          <ul className="flex max-h-56 flex-col gap-2 overflow-y-auto" aria-label="Workspaces to remove">
            {toRemove.map((w) => (
              <WorktreeRow key={w.worktreeId} w={w} />
            ))}
          </ul>
          {manual.length > 0 && (
            <div className="flex flex-col gap-2">
              <h4 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">Remove manually</h4>
              <ul className="flex max-h-40 flex-col gap-2 overflow-y-auto" aria-label="Workspaces to remove manually">
                {manual.map((w) => (
                  <WorktreeRow key={w.worktreeId} w={w} />
                ))}
              </ul>
            </div>
          )}
          <div className="flex justify-end gap-2">
            <Button variant="outline" size="sm" onClick={() => setOpen(false)} disabled={cleanup.isPending}>
              Cancel
            </Button>
            <Button size="sm" onClick={confirm} disabled={cleanup.isPending || toRemove.length === 0}>
              {cleanup.isPending ? "Starting…" : `Remove ${toRemove.length} ${toRemove.length === 1 ? "workspace" : "workspaces"}`}
            </Button>
          </div>
        </div>
      </Modal>
    </>
  );
}

function WorktreeRow({ w }: { w: HeldWorktree }) {
  return (
    <li>
      <CardInset padding="sm">
        <div className="flex items-baseline justify-between gap-3">
          <span className="min-w-0 truncate text-sm font-medium text-foreground" title={w.path}>
            {w.name}
            {w.projectName ? <span className="font-normal text-muted-foreground"> · {w.projectName}</span> : null}
          </span>
          <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
            {w.sizeBytes > 0n ? formatBytes(w.sizeBytes) : "size unknown"}
          </span>
        </div>
        <p className="mt-0.5 text-xs text-muted-foreground">
          <span className="text-foreground/80">{reasonLabel(w.reason)}</span>
          {w.detail ? ` — ${w.detail}` : ""}. {removalNote(w)}
        </p>
        {!w.removable && (
          <p className="mt-0.5 break-all font-mono text-xs text-muted-foreground" title={w.path}>
            {w.path}
          </p>
        )}
      </CardInset>
    </li>
  );
}
