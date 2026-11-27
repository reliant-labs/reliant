// Copyright (c) 2025 Reliant Labs

/**
 * Why the new chat's composer is holding, with the actions that end it, right
 * where the user is about to type.
 *
 * The owner's report (2026-11-25): starting a chat with a project that isn't on
 * the chosen machine should offer to add it there. The machine picker already
 * had "Set up <project> on <machine>" in its list, but the composer only said
 * "Set it up there" — the action was a menu away, behind a control that looked
 * finished. So the note carries the action itself:
 *
 *   absent      "Add to <machine>" clones the project there
 *               (CreateProjectFromRepo, the same set-up the picker runs)
 *   failed      the machine's reason, and "Try again"
 *   setting_up  progress only; the composer opens once the clone lands
 *
 * A project that is a plain folder has nothing to clone from, so it is
 * explained instead, with the two ways forward: switch to a machine it is on,
 * or point at a folder already on this one (AttachProjectFolderDialog).
 */

import { useState, type ReactNode } from "react";
import { GitFork, Loader2 } from "lucide-react";

import type { PlacementState } from "@/lib/projectPlacement";

export interface ProjectPlacementNoticeProps {
  projectName: string;
  /** The machine the chat will run on, which lacks the project. */
  machineName: string;
  state: Exclude<PlacementState, "ready">;
  /** The sentence startBlockedReason gives for this pair. */
  reason: string;
  /**
   * Clone the project onto the machine; rejects with a sentence for the user.
   * Undefined when the project cannot be cloned (no repository).
   */
  onAdd?: () => Promise<void>;
  /** A machine the project IS on, to run the chat there instead. */
  switchTo?: { name: string; onSwitch: () => void };
  /** Choose a folder already on the machine as the project's checkout. */
  onUseFolder?: () => void;
  /** Rendered below the actions (the nested repositories' progress). */
  children?: ReactNode;
}

export function ProjectPlacementNotice({
  projectName,
  machineName,
  state,
  reason,
  onAdd,
  switchTo,
  onUseFolder,
  children,
}: ProjectPlacementNoticeProps) {
  const [adding, setAdding] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const settingUp = state === "setting_up";

  const add = async () => {
    if (!onAdd || adding) return;
    setAdding(true);
    setError(null);
    try {
      await onAdd();
    } catch (err) {
      setError(err instanceof Error ? err.message : `Couldn't add ${projectName} to ${machineName}.`);
    } finally {
      setAdding(false);
    }
  };

  return (
    <div data-testid="new-chat-placement" className="flex w-full max-w-md flex-col items-center gap-2 text-center">
      <p
        role="status"
        data-testid="new-chat-placement-note"
        className="flex items-center gap-1.5 text-xs text-muted-foreground"
      >
        {settingUp && <Loader2 className="h-3 w-3 shrink-0 animate-spin motion-reduce:animate-none" aria-hidden="true" />}
        {reason}
      </p>
      {!settingUp && (onAdd || switchTo || onUseFolder) && (
        <div className="flex flex-wrap items-center justify-center gap-x-3 gap-y-1.5">
          {onAdd && (
            <button
              type="button"
              onClick={() => void add()}
              disabled={adding}
              data-testid="new-chat-placement-add"
              className="inline-flex items-center gap-1.5 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-60"
            >
              {adding ? (
                <Loader2 className="h-3.5 w-3.5 animate-spin motion-reduce:animate-none" aria-hidden="true" />
              ) : (
                <GitFork className="h-3.5 w-3.5" aria-hidden="true" />
              )}
              {state === "failed" ? `Try adding to ${machineName} again` : `Add to ${machineName}`}
            </button>
          )}
          {switchTo && (
            <button
              type="button"
              onClick={switchTo.onSwitch}
              disabled={adding}
              data-testid="new-chat-placement-switch"
              className="text-xs font-medium text-primary-ink hover:underline disabled:opacity-60"
            >
              Use {switchTo.name} instead
            </button>
          )}
          {onUseFolder && (
            <button
              type="button"
              onClick={onUseFolder}
              disabled={adding}
              data-testid="new-chat-placement-use-folder"
              className="text-xs font-medium text-primary-ink hover:underline disabled:opacity-60"
            >
              {onAdd ? `Already on ${machineName}? Choose its folder` : `Choose a folder on ${machineName}`}
            </button>
          )}
        </div>
      )}
      {error && (
        <p role="alert" className="text-xs text-destructive-ink">
          {error}
        </p>
      )}
      {children}
    </div>
  );
}
