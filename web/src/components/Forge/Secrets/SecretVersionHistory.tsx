// Copyright (c) 2025 Reliant Labs

/**
 * One secret's version history — the reason a versioned store was chosen.
 *
 * "Who changed this and when" is the question this panel answers, and it is the
 * only screen in the product where an immutable audit line exists at all. Every
 * row is metadata: a version number, a creation time, a deletion time, a
 * destroyed flag. No row has, or can have, a value — see services/forge/
 * secretStore.ts for why that is the storage engine's refusal rather than this
 * component's restraint.
 *
 * ── DELETE AND DESTROY ARE DRAWN DIFFERENTLY BECAUSE THEY ARE DIFFERENT ─────
 *
 * KV-v2 models them as distinct operations on distinct paths with distinct
 * capabilities, and the UI carries that distinction all the way to the pixel:
 *
 *   Delete   soft, recoverable. Renders as a muted row with an Undelete action
 *            sitting right there. The undo IS the explanation — a user who sees
 *            a restore button does not need to be told the delete was soft.
 *   Destroy  permanent. Behind a confirmation dialog, rendered in the danger
 *            variant, and the row afterwards is struck through with no action
 *            at all, because there is nothing left to do to it.
 *
 * The styling difference is load-bearing, not decorative. If both actions
 * looked alike, the recoverable one would train the muscle memory that fires on
 * the irreversible one.
 *
 * ── STRUCK-THROUGH ROWS STAY ───────────────────────────────────────────────
 *
 * A destroyed version keeps its metadata row. That is deliberate in the store
 * and deliberate here: removing the row would make the history lie by omission,
 * and "version 3 was destroyed on Tuesday" is precisely the fact an audit
 * needs. The bytes are gone; the record that they existed is not.
 */

import { useState } from "react";
import { RotateCcw, Trash2, Flame } from "lucide-react";

import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import { cn } from "@/lib/utils";
import type { ManagedSecretVersion } from "@/services/forge/secretStore";

export interface SecretVersionHistoryProps {
  name: string;
  versions: ManagedSecretVersion[];
  isLoading: boolean;
  /** Whether this surface can mutate at all — false for external/file/none. */
  canMutate: boolean;
  onDelete: (version: number) => void;
  onUndelete: (version: number) => void;
  onDestroy: (version: number) => void;
  /** A version number currently being mutated, so its row can show pending state. */
  pendingVersion: number | null;
}

/**
 * Timestamps render as absolute local time, not "3 days ago".
 *
 * Relative time is friendlier for a feed and wrong for an audit trail: "2
 * months ago" cannot be correlated with a deploy log, an incident timeline, or
 * a colleague's screenshot, which is the entire use for this column.
 */
function formatWhen(iso: string | undefined): string {
  if (!iso) return "—";
  const date = new Date(iso);
  if (!Number.isFinite(date.getTime())) return "—";
  return date.toLocaleString(undefined, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

type VersionState = "live" | "deleted" | "destroyed";

function versionState(version: ManagedSecretVersion): VersionState {
  if (version.destroyed) return "destroyed";
  if (version.deletedAt) return "deleted";
  return "live";
}

export function SecretVersionHistory({
  name,
  versions,
  isLoading,
  canMutate,
  onDelete,
  onUndelete,
  onDestroy,
  pendingVersion,
}: SecretVersionHistoryProps) {
  /** The version awaiting destroy confirmation. Null when the dialog is closed. */
  const [confirmDestroy, setConfirmDestroy] = useState<number | null>(null);

  if (isLoading) {
    return (
      <div className="space-y-2" data-testid="version-history-loading">
        {[0, 1, 2].map((i) => (
          <div key={i} className="h-9 animate-pulse rounded-md border border-border/60 bg-background" />
        ))}
      </div>
    );
  }

  if (versions.length === 0) {
    return (
      <p className="text-sm text-muted-foreground" data-testid="version-history-empty">
        No versions yet. Setting this secret creates version 1.
      </p>
    );
  }

  return (
    <div data-testid="version-history">
      {/*
       * A plain list on an inset, not a nested card. The detail panel is
       * already a surface; a card in here would be the card-in-card stacking
       * the elevation rules forbid.
       */}
      <ul className="divide-y divide-border/60 overflow-hidden rounded-md border border-border/60 bg-background">
        {versions.map((version) => {
          const state = versionState(version);
          const pending = pendingVersion === version.version;
          return (
            <li
              key={version.version}
              data-testid={`version-row-${version.version}`}
              data-state={state}
              className={cn(
                "flex items-center gap-3 px-3 py-2.5 text-sm",
                pending && "opacity-60"
              )}
            >
              {/* The version number is an identifier — mono. */}
              <span
                className={cn(
                  "w-12 shrink-0 font-mono text-xs",
                  state === "live" ? "text-foreground" : "text-muted-foreground",
                  // Struck through only when destroyed. A soft delete is not
                  // gone, so it must not be drawn as gone.
                  state === "destroyed" && "line-through"
                )}
              >
                v{version.version}
              </span>

              <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
                {state === "destroyed" ? (
                  <>Destroyed — created {formatWhen(version.createdAt)}</>
                ) : state === "deleted" ? (
                  <>Deleted {formatWhen(version.deletedAt)}</>
                ) : (
                  <>Created {formatWhen(version.createdAt)}</>
                )}
              </span>

              {canMutate && (
                <span className="flex shrink-0 items-center gap-1">
                  {state === "live" && (
                    <RowAction
                      label="Delete"
                      title={`Soft-delete version ${version.version}. Recoverable.`}
                      icon={Trash2}
                      onClick={() => onDelete(version.version)}
                      disabled={pending}
                    />
                  )}
                  {state === "deleted" && (
                    <RowAction
                      label="Undelete"
                      title={`Restore version ${version.version}.`}
                      icon={RotateCcw}
                      onClick={() => onUndelete(version.version)}
                      disabled={pending}
                    />
                  )}
                  {state !== "destroyed" && (
                    <RowAction
                      label="Destroy"
                      title={`Permanently destroy version ${version.version}. Cannot be undone.`}
                      icon={Flame}
                      danger
                      onClick={() => setConfirmDestroy(version.version)}
                      disabled={pending}
                    />
                  )}
                </span>
              )}
            </li>
          );
        })}
      </ul>

      {/*
       * Destroy is the only action here behind a confirmation, and that is the
       * point — a dialog on every action would make this one unremarkable.
       */}
      <ConfirmationDialog
        open={confirmDestroy !== null}
        variant="danger"
        title={`Destroy version ${confirmDestroy ?? ""} of ${name}?`}
        description="This permanently destroys the stored bytes for this version. It cannot be undone, and Undelete cannot bring it back — that only works on soft-deleted versions. The version's metadata row stays so the history remains accurate."
        confirmLabel="Destroy permanently"
        cancelLabel="Cancel"
        onConfirm={() => {
          if (confirmDestroy !== null) onDestroy(confirmDestroy);
          setConfirmDestroy(null);
        }}
        onCancel={() => setConfirmDestroy(null)}
      />
    </div>
  );
}

/**
 * A row action: quiet by default, colored only on hover, and never a filled
 * button. Vercel's table rows do not carry three buttons of chrome each — the
 * actions recede until the row is the one you are looking at.
 */
function RowAction({
  label,
  title,
  icon: Icon,
  onClick,
  disabled,
  danger,
}: {
  label: string;
  title: string;
  icon: typeof Trash2;
  onClick: () => void;
  disabled?: boolean;
  danger?: boolean;
}) {
  return (
    <button
      type="button"
      title={title}
      onClick={onClick}
      disabled={disabled}
      className={cn(
        "inline-flex items-center gap-1.5 rounded px-2 py-1 text-xs transition-colors",
        "text-muted-foreground",
        danger ? "hover:text-destructive" : "hover:text-foreground",
        "disabled:cursor-not-allowed disabled:opacity-40"
      )}
    >
      <Icon className="h-3 w-3" aria-hidden="true" />
      {label}
    </button>
  );
}
