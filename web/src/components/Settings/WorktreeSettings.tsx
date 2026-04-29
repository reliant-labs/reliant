import { useState, useEffect } from "react";
import { FolderX, Check, Info, Trash2, FolderGit2, Copy, FileX } from "lucide-react";
import { cn } from "../../lib/utils";
import { useSettingsStore, type WorktreeArchiveMode } from "../../store/settingsStore";

export function WorktreeSettings() {
  const preferences = useSettingsStore((state) => state.preferences);
  const updateWorktreePreferences = useSettingsStore((state) => state.updateWorktreePreferences);
  const updatePreferences = useSettingsStore((state) => state.updatePreferences);
  const loadPreferences = useSettingsStore((state) => state.loadPreferences);
  const [isSaving, setIsSaving] = useState(false);

  // Load preferences on mount
  useEffect(() => {
    loadPreferences();
  }, [loadPreferences]);

  const handleModeChange = async (mode: WorktreeArchiveMode) => {
    setIsSaving(true);
    try {
      await updateWorktreePreferences({ archiveMode: mode });
    } finally {
      setIsSaving(false);
    }
  };

  const handleToggleDeleteDirectory = async () => {
    setIsSaving(true);
    try {
      await updateWorktreePreferences({
        defaultDeleteDirectory: !preferences.worktree.defaultDeleteDirectory,
      });
    } finally {
      setIsSaving(false);
    }
  };

  const handleToggleDeleteBranch = async () => {
    setIsSaving(true);
    try {
      await updateWorktreePreferences({
        defaultDeleteBranch: !preferences.worktree.defaultDeleteBranch,
      });
    } finally {
      setIsSaving(false);
    }
  };

  const handleToggleBranchCopyUncommittedFiles = async () => {
    setIsSaving(true);
    try {
      await updateWorktreePreferences({
        branchCopyUncommittedFilesDefault: !preferences.worktree.branchCopyUncommittedFilesDefault,
      });
    } finally {
      setIsSaving(false);
    }
  };

  const modes: Array<{
    value: WorktreeArchiveMode;
    label: string;
    description: string;
  }> = [
    {
      value: 'ask_me',
      label: 'Ask me each time',
      description: 'Show a dialog with cleanup options when archiving (recommended)',
    },
    {
      value: 'always_cleanup',
      label: 'Always cleanup',
      description: 'Automatically delete workspace directories based on defaults below',
    },
    {
      value: 'always_keep',
      label: 'Always keep files',
      description: 'Never delete directories or branches when archiving',
    },
  ];

  return (
    <div className="space-y-6">
      {/* Header */}
      <div>
        <div className="flex items-center gap-2 mb-2">
          <FolderGit2 className="w-5 h-5 text-foreground" />
          <h2 className="text-lg font-semibold text-foreground">Workspace Settings</h2>
        </div>
        <p className="text-sm text-muted-foreground">
          Configure how Reliant handles workspace directories when archiving worktrees.
        </p>
      </div>

      {/* Archive Behavior */}
      <div className="space-y-4">
        <div>
          <h3 className="text-sm font-semibold text-foreground mb-1">Archive Behavior</h3>
          <p className="text-xs text-muted-foreground">
            Choose what happens when you archive a workspace
          </p>
        </div>

        {/* Mode Selection */}
        <div className="space-y-3">
          {modes.map((mode) => (
            <label
              key={mode.value}
              className={cn(
                "flex items-start gap-3 p-4 rounded-lg border-2 cursor-pointer transition-all",
                preferences.worktree.archiveMode === mode.value
                  ? "border-primary bg-primary/5"
                  : "border-border hover:border-primary/50 hover:bg-muted/30"
              )}
            >
              <div className="relative flex items-center justify-center mt-0.5">
                <input
                  type="radio"
                  name="archive-mode"
                  value={mode.value}
                  checked={preferences.worktree.archiveMode === mode.value}
                  onChange={() => handleModeChange(mode.value)}
                  disabled={isSaving}
                  className="sr-only"
                />
                <div
                  className={cn(
                    "w-5 h-5 rounded-full border-2 transition-all flex items-center justify-center",
                    preferences.worktree.archiveMode === mode.value
                      ? "border-primary bg-primary"
                      : "border-border bg-background"
                  )}
                >
                  {preferences.worktree.archiveMode === mode.value && (
                    <div className="w-2.5 h-2.5 rounded-full bg-white" />
                  )}
                </div>
              </div>
              <div className="flex-1">
                <div className="font-medium text-sm text-foreground mb-1">
                  {mode.label}
                </div>
                <div className="text-xs text-muted-foreground">
                  {mode.description}
                </div>
              </div>
            </label>
          ))}
        </div>
      </div>

      {/* Default Cleanup Options - only shown for "always_cleanup" mode */}
      {preferences.worktree.archiveMode === 'always_cleanup' && (
        <div className="space-y-4 pt-5 mt-5 border-t border-border/30">
          <div>
            <h3 className="text-sm font-semibold text-foreground mb-1">
              Default Cleanup Options
            </h3>
            <p className="text-xs text-muted-foreground">
              These defaults apply when "Always cleanup" mode is active
            </p>
          </div>

          {/* Delete Directory Option */}
          <label className="flex items-start gap-3 cursor-pointer group hover:bg-muted/30 p-3 rounded-lg transition-colors">
            <div className="relative flex items-center justify-center mt-0.5">
              <input
                type="checkbox"
                checked={preferences.worktree.defaultDeleteDirectory}
                onChange={handleToggleDeleteDirectory}
                disabled={isSaving}
                className="sr-only"
              />
              <div
                className={cn(
                  "w-5 h-5 rounded border-2 transition-all flex items-center justify-center",
                  preferences.worktree.defaultDeleteDirectory
                    ? "border-primary bg-primary"
                    : "border-border bg-background"
                )}
              >
                {preferences.worktree.defaultDeleteDirectory && (
                  <Check className="w-3.5 h-3.5 text-white" strokeWidth={3} />
                )}
              </div>
            </div>
            <div className="flex-1">
              <div className="flex items-center gap-2">
                <FolderX className="w-4 h-4 text-muted-foreground" />
                <span className="text-sm font-medium text-foreground">
                  Delete workspace directory
                </span>
              </div>
              <p className="text-xs text-muted-foreground mt-1">
                Removes the working directory from <code className="bg-muted px-1 py-0.5 rounded">~/.reliant/worktrees</code>
              </p>
            </div>
          </label>

          {/* Delete Branch Option */}
          <label className="flex items-start gap-3 cursor-pointer group hover:bg-muted/30 p-3 rounded-lg transition-colors">
            <div className="relative flex items-center justify-center mt-0.5">
              <input
                type="checkbox"
                checked={preferences.worktree.defaultDeleteBranch}
                onChange={handleToggleDeleteBranch}
                disabled={isSaving}
                className="sr-only"
              />
              <div
                className={cn(
                  "w-5 h-5 rounded border-2 transition-all flex items-center justify-center",
                  preferences.worktree.defaultDeleteBranch
                    ? "border-primary bg-primary"
                    : "border-border bg-background"
                )}
              >
                {preferences.worktree.defaultDeleteBranch && (
                  <Check className="w-3.5 h-3.5 text-white" strokeWidth={3} />
                )}
              </div>
            </div>
            <div className="flex-1">
              <div className="flex items-center gap-2">
                <Trash2 className="w-4 h-4 text-muted-foreground" />
                <span className="text-sm font-medium text-foreground">
                  Delete git branch
                </span>
              </div>
              <p className="text-xs text-muted-foreground mt-1">
                Permanently deletes the branch from the git repository
              </p>
              {preferences.worktree.defaultDeleteBranch && (
                <p className="text-xs text-warning font-medium mt-1 flex items-center gap-1">
                  <Info className="w-3 h-3" />
                  Make sure branches are merged before archiving!
                </p>
              )}
            </div>
          </label>
        </div>
      )}

      {/* Branching Behavior */}
      <div className="space-y-4 pt-5 mt-5 border-t border-border/30">
        <div>
          <h3 className="text-sm font-semibold text-foreground mb-1">Branching Behavior</h3>
          <p className="text-xs text-muted-foreground">
            Configure defaults when using "Branch to New Workspace"
          </p>
        </div>

        {/* Copy Uncommitted Files Option */}
        <label className="flex items-start gap-3 cursor-pointer group hover:bg-muted/30 p-3 rounded-lg transition-colors">
          <div className="relative flex items-center justify-center mt-0.5">
            <input
              type="checkbox"
              checked={preferences.worktree.branchCopyUncommittedFilesDefault}
              onChange={handleToggleBranchCopyUncommittedFiles}
              disabled={isSaving}
              className="sr-only"
            />
            <div
              className={cn(
                "w-5 h-5 rounded border-2 transition-all flex items-center justify-center",
                preferences.worktree.branchCopyUncommittedFilesDefault
                  ? "border-primary bg-primary"
                  : "border-border bg-background"
              )}
            >
              {preferences.worktree.branchCopyUncommittedFilesDefault && (
                <Check className="w-3.5 h-3.5 text-white" strokeWidth={3} />
              )}
            </div>
          </div>
          <div className="flex-1">
            <div className="flex items-center gap-2">
              <Copy className="w-4 h-4 text-muted-foreground" />
              <span className="text-sm font-medium text-foreground">
                Copy uncommitted files by default
              </span>
            </div>
            <p className="text-xs text-muted-foreground mt-1">
              When branching to a new workspace, copy modified and untracked files from the source workspace
            </p>
            <p className="text-xs text-muted-foreground mt-1">
              Git worktrees only share committed changes. Enable this to include your local changes in new workspaces.
            </p>
          </div>
        </label>
      </div>

      {/* File Operations Section */}
      <div className="border-t border-border/30 pt-5 mt-5 space-y-4">
        <div>
          <h3 className="text-sm font-semibold">File Operations</h3>
          <p className="text-xs text-muted-foreground mt-1">
            Configure file deletion behavior in workspaces
          </p>
        </div>

        {/* Skip Delete Confirmation Option */}
        <label className="flex items-start gap-3 cursor-pointer group hover:bg-muted/30 p-3 rounded-lg transition-colors">
          <div className="relative flex items-center justify-center mt-0.5">
            <input
              type="checkbox"
              checked={preferences.skipDeleteConfirmation}
              onChange={async () => {
                setIsSaving(true);
                try {
                  await updatePreferences({ skipDeleteConfirmation: !preferences.skipDeleteConfirmation });
                } catch (error) {
                  console.error("Failed to update file deletion preference:", error);
                } finally {
                  setIsSaving(false);
                }
              }}
              disabled={isSaving}
              className="sr-only"
            />
            <div
              className={cn(
                "w-5 h-5 rounded border-2 transition-all flex items-center justify-center",
                preferences.skipDeleteConfirmation
                  ? "border-primary bg-primary"
                  : "border-border bg-background"
              )}
            >
              {preferences.skipDeleteConfirmation && (
                <Check className="w-3.5 h-3.5 text-white" strokeWidth={3} />
              )}
            </div>
          </div>
          <div className="flex-1">
            <div className="flex items-center gap-2">
              <FileX className="w-4 h-4 text-muted-foreground" />
              <span className="text-sm font-medium text-foreground">
                Skip delete confirmation
              </span>
            </div>
            <p className="text-xs text-muted-foreground mt-1">
              Delete files without showing a confirmation dialog. You can still restore files using <strong>Cmd+Z</strong> (undo) or from source control.
            </p>
          </div>
        </label>
      </div>

      {/* Info Box */}
      <div className="border-t border-border/30 pt-5 mt-5"></div>
      <div className="bg-muted/30 border border-border/40 rounded-lg p-4 shadow-[inset_0_1px_0_0_rgba(255,255,255,0.03)]">
        <div className="flex gap-3 text-xs text-muted-foreground">
          <Info className="w-4 h-4 flex-shrink-0 mt-0.5 text-primary" />
          <div className="space-y-2">
            <p className="font-medium text-foreground">About workspace cleanup</p>
            <ul className="space-y-1">
              <li>• Workspaces are stored in <code className="bg-muted px-1 py-0.5 rounded">~/.reliant/worktrees/&lt;repo&gt;/&lt;name&gt;</code></li>
              <li>• Archiving marks the workspace as archived in the database</li>
              <li>• Cleanup removes the actual files from your filesystem</li>
              <li>• You can still view archived workspaces and restore them later</li>
              <li>• Deleting a branch is permanent and cannot be undone</li>
            </ul>
          </div>
        </div>
      </div>
    </div>
  );
}