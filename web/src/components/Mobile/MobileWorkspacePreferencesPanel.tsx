/**
 * Mobile-native "Workspace preferences" panel — reuses the exact same
 * `usePreferences` / `useUpdatePreferences` / `useUpdateWorktreePreferences`
 * hooks desktop `WorktreeSettings` uses, so a choice made here is the same
 * persisted preference desktop shows.
 *
 * The archive-mode picker is 3 options, under the sheet threshold, so it
 * renders as a vertical list of full-width radio rows rather than opening a
 * bottom sheet — each option's description needs to stay visible, which a
 * 3-row list does without extra taps.
 */

import { useState } from "react";
import { Copy, FileX, FolderX, Info, Trash2 } from "lucide-react";
import { cn } from "../../lib/utils";
import {
  usePreferences,
  useUpdatePreferences,
  useUpdateWorktreePreferences,
  type WorktreeArchiveMode,
} from "../../hooks/settings-queries";
import { MobileToggleRow } from "./MobileSettingsRow";

const ARCHIVE_MODES: Array<{
  value: WorktreeArchiveMode;
  label: string;
  description: string;
}> = [
  { value: "ask_me", label: "Ask every time", description: "Show cleanup choices before archiving." },
  { value: "always_cleanup", label: "Clean up", description: "Apply the defaults below automatically." },
  { value: "always_keep", label: "Keep files", description: "Archive records without deleting files or branches." },
];

export function MobileWorkspacePreferencesPanel() {
  const { data: preferences } = usePreferences();
  const updateWorktreePrefs = useUpdateWorktreePreferences();
  const updatePrefs = useUpdatePreferences();
  const [isSaving, setIsSaving] = useState(false);

  const updateSafely = async (update: () => Promise<unknown>) => {
    setIsSaving(true);
    try {
      await update();
    } finally {
      setIsSaving(false);
    }
  };

  const handleModeChange = (mode: WorktreeArchiveMode) => {
    updateSafely(() => updateWorktreePrefs.mutateAsync({ archiveMode: mode }));
  };

  return (
    <div className="divide-y divide-border">
      <div className="bg-muted/30 px-4 py-2">
        <p className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          Archive behavior
        </p>
        <p className="mt-0.5 text-xs text-muted-foreground">
          Decide what happens after you archive a workspace.
        </p>
      </div>

      <div className="divide-y divide-border/60">
        {ARCHIVE_MODES.map((mode) => {
          const isSelected = preferences?.worktree.archiveMode === mode.value;
          return (
            <button
              key={mode.value}
              type="button"
              disabled={isSaving}
              onClick={() => handleModeChange(mode.value)}
              className={cn(
                "flex min-h-[44px] w-full items-center gap-3 px-4 py-3 text-left active:bg-muted/50",
                isSaving && "opacity-60",
              )}
            >
              <span
                className={cn(
                  "h-2.5 w-2.5 shrink-0 rounded-full",
                  isSelected ? "bg-primary" : "bg-muted-foreground/35",
                )}
              />
              <div className="min-w-0">
                <p className="text-sm font-medium text-foreground">{mode.label}</p>
                <p className="mt-0.5 text-xs text-muted-foreground">{mode.description}</p>
              </div>
            </button>
          );
        })}
      </div>

      {preferences?.worktree.archiveMode === "always_cleanup" && (
        <div className="divide-y divide-border/60 bg-muted/10">
          <MobileToggleRow
            icon={<FolderX className="h-4 w-4" />}
            label="Delete workspace directory"
            description="Delete the workspace's files from disk when archiving."
            checked={preferences?.worktree.defaultDeleteDirectory ?? true}
            disabled={isSaving}
            onChange={() =>
              updateSafely(() =>
                updateWorktreePrefs.mutateAsync({
                  defaultDeleteDirectory: !preferences?.worktree.defaultDeleteDirectory,
                }),
              )
            }
          />
          <MobileToggleRow
            icon={<Trash2 className="h-4 w-4" />}
            label="Delete git branch"
            description={
              preferences?.worktree.defaultDeleteBranch
                ? "Only enable this when branches are merged elsewhere."
                : "Permanently remove the branch when cleanup runs."
            }
            checked={preferences?.worktree.defaultDeleteBranch ?? false}
            disabled={isSaving}
            onChange={() =>
              updateSafely(() =>
                updateWorktreePrefs.mutateAsync({
                  defaultDeleteBranch: !preferences?.worktree.defaultDeleteBranch,
                }),
              )
            }
          />
        </div>
      )}

      <MobileToggleRow
        icon={<Copy className="h-4 w-4" />}
        label="Copy uncommitted files to new workspaces"
        description="Use this default when branching from local changes."
        checked={preferences?.worktree.branchCopyUncommittedFilesDefault ?? false}
        disabled={isSaving}
        onChange={() =>
          updateSafely(() =>
            updateWorktreePrefs.mutateAsync({
              branchCopyUncommittedFilesDefault:
                !preferences?.worktree.branchCopyUncommittedFilesDefault,
            }),
          )
        }
      />
      <MobileToggleRow
        icon={<FileX className="h-4 w-4" />}
        label="Skip file delete confirmation"
        description="Delete files immediately. Undo is still available where supported."
        checked={preferences?.skipDeleteConfirmation ?? false}
        disabled={isSaving}
        onChange={() =>
          updateSafely(() =>
            updatePrefs.mutateAsync({
              skipDeleteConfirmation: !preferences?.skipDeleteConfirmation,
            }),
          )
        }
      />

      <div className="flex items-start gap-3 bg-muted/30 p-4">
        <Info className="mt-0.5 h-4 w-4 shrink-0 text-primary" />
        <p className="text-xs text-muted-foreground">
          Archiving always keeps the workspace record so you can restore it later.
          Cleanup only controls whether local files or git branches are removed.
        </p>
      </div>
    </div>
  );
}
