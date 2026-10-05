import { useState, type ReactNode } from "react";
import { cn } from "../../lib/utils";
import {
  usePreferences,
  useUpdatePreferences,
  useUpdateWorktreePreferences,
  type WorktreeArchiveMode,
} from "../../hooks/settings-queries";
import { Toggle } from "../ui/Toggle";
import Card, { CardHeader, CardInset } from "../forge-ui/card";

const ARCHIVE_MODES: Array<{ value: WorktreeArchiveMode; label: string; description: string }> = [
  {
    value: "ask_me",
    label: "Ask every time",
    description: "Show the cleanup choices each time you archive, defaulting to the options below.",
  },
  {
    value: "always_cleanup",
    label: "Clean up automatically",
    description: "Archive straight away and remove the files and branch as set below. No prompt.",
  },
  {
    value: "always_keep",
    label: "Keep everything",
    description: "Archive straight away and leave the worktree directory and branch on disk.",
  },
];

/**
 * Cleanup defaults for workspaces: what Archive does to the worktree's files
 * and branch, and how new workspaces are seeded. Every option carries a line
 * saying what it changes, because the destructive ones are not undoable.
 */
export function WorktreeSettings() {
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

  const archiveMode = preferences?.worktree.archiveMode ?? "ask_me";
  const deleteDirectory = preferences?.worktree.defaultDeleteDirectory ?? true;
  const deleteBranch = preferences?.worktree.defaultDeleteBranch ?? false;
  // "Keep everything" never touches files, so its cleanup defaults are moot.
  const cleanupDefaultsApply = archiveMode !== "always_keep";

  return (
    <div className="forge-ui flex flex-col gap-4" data-testid="worktree-settings">
      <Card>
        <CardHeader
          title="When you archive a workspace"
          description="Archiving always keeps the workspace record and its chats, so you can restore them. These settings decide what else happens to the worktree on disk."
        />

        <fieldset className="flex flex-col gap-4" disabled={isSaving}>
          <legend className="sr-only">Archive behaviour</legend>
          <div className="grid gap-2 sm:grid-cols-3">
            {ARCHIVE_MODES.map((mode) => {
              const isSelected = archiveMode === mode.value;
              return (
                <label
                  key={mode.value}
                  className={cn(
                    "flex cursor-pointer flex-col gap-1 rounded-md border p-3 transition-colors focus-within:ring-2 focus-within:ring-ring",
                    isSelected
                      ? "border-primary bg-primary/5"
                      : "border-border bg-background hover:border-border-strong",
                    isSaving && "cursor-not-allowed opacity-60",
                  )}
                >
                  <span className="flex items-center gap-2 text-sm font-medium text-foreground">
                    <input
                      type="radio"
                      name="archive-mode"
                      value={mode.value}
                      checked={isSelected}
                      onChange={() =>
                        updateSafely(() => updateWorktreePrefs.mutateAsync({ archiveMode: mode.value }))
                      }
                      className="h-3.5 w-3.5 accent-primary"
                    />
                    {mode.label}
                  </span>
                  <span className="text-pretty text-xs leading-relaxed text-muted-foreground">
                    {mode.description}
                  </span>
                </label>
              );
            })}
          </div>

          <div className="flex flex-col gap-2">
            <h4 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
              Cleanup defaults
            </h4>
            <CardInset padding="none" className="divide-y divide-border/60">
              <PreferenceRow
                title="Delete the worktree directory"
                description={
                  <>
                    Remove the workspace's files from{" "}
                    <span className="font-mono">~/.reliant/worktrees/</span> when it is archived.
                    Uncommitted changes in it are lost.
                  </>
                }
                checked={deleteDirectory}
                disabled={isSaving || !cleanupDefaultsApply}
                onChange={() =>
                  updateSafely(() =>
                    updateWorktreePrefs.mutateAsync({ defaultDeleteDirectory: !deleteDirectory }),
                  )
                }
              />
              <PreferenceRow
                title="Delete the git branch"
                description="Remove the workspace's branch from the repository when it is archived. Only turn this on if you merge or push branches before archiving."
                warning={
                  deleteBranch && cleanupDefaultsApply
                    ? "Unpushed commits on a deleted branch can't be recovered from Reliant."
                    : undefined
                }
                checked={deleteBranch}
                disabled={isSaving || !cleanupDefaultsApply}
                onChange={() =>
                  updateSafely(() =>
                    updateWorktreePrefs.mutateAsync({ defaultDeleteBranch: !deleteBranch }),
                  )
                }
              />
            </CardInset>
            {!cleanupDefaultsApply && (
              <p className="text-xs text-muted-foreground">
                Not used while archive is set to Keep everything.
              </p>
            )}
          </div>
        </fieldset>
      </Card>

      <Card>
        <CardHeader
          title="New workspaces and files"
          description="Defaults for branching a chat into a new workspace, and for deleting files in the file browser."
        />
        <CardInset padding="none" className="divide-y divide-border/60">
          <PreferenceRow
            title="Bring uncommitted changes into new workspaces"
            description="When you branch a chat, copy the source workspace's uncommitted files into the new one, so the agent starts from what you see rather than from the last commit."
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
          <PreferenceRow
            title="Delete files without confirming"
            description="Skip the confirmation when you delete a file in the file browser. Undo (⌘Z) still works where the editor supports it."
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
        </CardInset>
      </Card>
    </div>
  );
}

interface PreferenceRowProps {
  title: string;
  description: ReactNode;
  checked: boolean;
  disabled: boolean;
  warning?: string;
  onChange: () => void;
}

function PreferenceRow({ title, description, checked, disabled, warning, onChange }: PreferenceRowProps) {
  return (
    <div className={cn("flex items-start justify-between gap-4 px-3 py-3", disabled && "opacity-60")}>
      <div className="flex min-w-0 flex-col gap-0.5">
        <p className="text-sm font-medium text-foreground">{title}</p>
        <p className="text-pretty text-xs leading-relaxed text-muted-foreground">{description}</p>
        {warning && <p className="text-xs font-medium text-warning">{warning}</p>}
      </div>
      <Toggle
        checked={checked}
        onChange={onChange}
        disabled={disabled}
        srLabel={title}
        className="mt-0.5 flex-shrink-0"
      />
    </div>
  );
}
