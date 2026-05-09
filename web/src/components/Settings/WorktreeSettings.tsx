import { useState } from "react";
import { Copy, FileX, FolderGit2, FolderX, Info, Trash2 } from "lucide-react";
import { cn } from "../../lib/utils";
import { usePreferences, useUpdatePreferences, useUpdateWorktreePreferences, type WorktreeArchiveMode } from "../../hooks/settings-queries";
import { Toggle } from "../ui/Toggle";

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

  const handleModeChange = (mode: WorktreeArchiveMode) => {
    updateSafely(() => updateWorktreePrefs.mutateAsync({ archiveMode: mode }));
  };

  const modes: Array<{
    value: WorktreeArchiveMode;
    label: string;
    description: string;
  }> = [
    {
      value: "ask_me",
      label: "Ask every time",
      description: "Show cleanup choices before archiving.",
    },
    {
      value: "always_cleanup",
      label: "Clean up",
      description: "Apply the defaults below automatically.",
    },
    {
      value: "always_keep",
      label: "Keep files",
      description: "Archive records without deleting files or branches.",
    },
  ];

  return (
    <div className="space-y-5">
      <div>
        <div className="mb-2 flex items-center gap-2">
          <FolderGit2 className="h-5 w-5 text-muted-foreground" />
          <h2 className="text-xl font-semibold tracking-tight text-foreground">
            Workspace preferences
          </h2>
        </div>
        <p className="text-sm text-muted-foreground">
          Choose safe defaults for archiving, branching, and file deletion.
        </p>
      </div>

      <section className="rounded-xl border border-border/60 bg-card p-4 shadow-sm">
        <div className="mb-4">
          <h3 className="text-sm font-semibold text-foreground">Archive behavior</h3>
          <p className="mt-1 text-xs text-muted-foreground">
            Decide what happens after you click Archive on a workspace.
          </p>
        </div>

        <div className="grid gap-2 sm:grid-cols-3">
          {modes.map((mode) => {
            const isSelected = preferences?.worktree.archiveMode === mode.value;

            return (
              <label
                key={mode.value}
                className={cn(
                  "cursor-pointer rounded-xl border p-3 transition-colors focus-within:ring-2 focus-within:ring-primary/50",
                  isSelected
                    ? "border-primary/50 bg-primary/5"
                    : "border-border/60 bg-background hover:border-primary/30 hover:bg-muted/40",
                  isSaving && "cursor-not-allowed opacity-60"
                )}
              >
                <input
                  type="radio"
                  name="archive-mode"
                  value={mode.value}
                  checked={isSelected}
                  onChange={() => handleModeChange(mode.value)}
                  disabled={isSaving}
                  className="sr-only"
                />
                <span className="flex items-center gap-2 text-sm font-medium text-foreground">
                  <span
                    className={cn(
                      "h-2.5 w-2.5 rounded-full",
                      isSelected ? "bg-primary" : "bg-muted-foreground/35"
                    )}
                  />
                  {mode.label}
                </span>
                <span className="mt-2 block text-xs leading-relaxed text-muted-foreground">
                  {mode.description}
                </span>
              </label>
            );
          })}
        </div>

        {preferences?.worktree.archiveMode === "always_cleanup" && (
          <div className="mt-4 space-y-2 rounded-xl border border-border/60 bg-background p-3">
            <PreferenceToggleRow
              icon={<FolderX className="h-4 w-4" />}
              title="Delete workspace directory"
              description="Remove files from ~/.reliant/worktrees when archiving."
              checked={preferences?.worktree.defaultDeleteDirectory ?? true}
              disabled={isSaving}
              onChange={() =>
                updateSafely(() =>
                  updateWorktreePrefs.mutateAsync({
                    defaultDeleteDirectory: !preferences?.worktree.defaultDeleteDirectory,
                  })
                )
              }
            />
            <PreferenceToggleRow
              icon={<Trash2 className="h-4 w-4" />}
              title="Delete git branch"
              description="Permanently remove the branch when cleanup runs."
              checked={preferences?.worktree.defaultDeleteBranch ?? false}
              disabled={isSaving}
              warning={preferences?.worktree.defaultDeleteBranch ? "Only enable this when branches are merged elsewhere." : undefined}
              onChange={() =>
                updateSafely(() =>
                  updateWorktreePrefs.mutateAsync({
                    defaultDeleteBranch: !preferences?.worktree.defaultDeleteBranch,
                  })
                )
              }
            />
          </div>
        )}
      </section>

      <section className="rounded-xl border border-border/60 bg-card p-4 shadow-sm">
        <div className="space-y-2">
          <PreferenceToggleRow
            icon={<Copy className="h-4 w-4" />}
            title="Copy uncommitted files to new workspaces"
            description="Use this default when branching to a new workspace from local changes."
            checked={preferences?.worktree.branchCopyUncommittedFilesDefault ?? false}
            disabled={isSaving}
            onChange={() =>
              updateSafely(() =>
                updateWorktreePrefs.mutateAsync({
                  branchCopyUncommittedFilesDefault: !preferences?.worktree.branchCopyUncommittedFilesDefault,
                })
              )
            }
          />
          <PreferenceToggleRow
            icon={<FileX className="h-4 w-4" />}
            title="Skip file delete confirmation"
            description="Delete files immediately. Undo is still available with Cmd+Z where supported."
            checked={preferences?.skipDeleteConfirmation ?? false}
            disabled={isSaving}
            onChange={() =>
              updateSafely(() =>
                updatePrefs.mutateAsync({
                  skipDeleteConfirmation: !preferences?.skipDeleteConfirmation,
                })
              )
            }
          />
        </div>
      </section>

      <div className="flex gap-3 rounded-xl border border-border/60 bg-muted/30 p-4 text-xs text-muted-foreground">
        <Info className="mt-0.5 h-4 w-4 flex-shrink-0 text-primary" />
        <p>
          Archiving always keeps the workspace record so you can restore it later. Cleanup only controls whether local files or git branches are removed during archive.
        </p>
      </div>
    </div>
  );
}

interface PreferenceToggleRowProps {
  icon: React.ReactNode;
  title: string;
  description: string;
  checked: boolean;
  disabled: boolean;
  warning?: string;
  onChange: () => void;
}

function PreferenceToggleRow({
  icon,
  title,
  description,
  checked,
  disabled,
  warning,
  onChange,
}: PreferenceToggleRowProps) {
  return (
    <div className="flex items-start justify-between gap-4 rounded-lg p-2 transition-colors hover:bg-muted/40">
      <div className="flex min-w-0 gap-3">
        <div className="mt-0.5 rounded-lg bg-muted p-2 text-muted-foreground">
          {icon}
        </div>
        <div className="min-w-0">
          <p className="text-sm font-medium text-foreground">{title}</p>
          <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{description}</p>
          {warning && <p className="mt-1 text-xs font-medium text-warning">{warning}</p>}
        </div>
      </div>
      <Toggle
        checked={checked}
        onChange={onChange}
        disabled={disabled}
        srLabel={title}
        className="mt-1 flex-shrink-0"
      />
    </div>
  );
}