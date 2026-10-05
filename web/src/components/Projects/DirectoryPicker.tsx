import { useState, useEffect, useCallback } from "react";
import {
  Folder,
  FolderPlus,
  File,
  ChevronRight,
  Loader2,
  AlertCircle,
  Eye,
  EyeOff,
  ArrowUp,
  X,
} from "lucide-react";
import { Modal } from "../ui/Modal";
import {
  listDirectory,
  createDirectory,
  type DirectoryEntry,
} from "../../api/filesystem-grpc";
import { cn } from "../../lib/utils";
import {
  containsSeparator,
  dirname,
  isPathRoot,
  joinPath,
  pathCrumbs,
} from "../../lib/pathUtils";

interface DirectoryPickerProps {
  isOpen: boolean;
  onClose: () => void;
  onSelect: (path: string) => void;
}

export function DirectoryPicker({
  isOpen,
  onClose,
  onSelect,
}: DirectoryPickerProps) {
  const [, setCurrentPath] = useState("");
  const [resolvedPath, setResolvedPath] = useState("");
  const [entries, setEntries] = useState<DirectoryEntry[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showHidden, setShowHidden] = useState(false);
  // "New folder" inline-create state.
  const [isCreatingFolder, setIsCreatingFolder] = useState(false);
  const [newFolderName, setNewFolderName] = useState("");
  const [createError, setCreateError] = useState<string | null>(null);
  const [isSubmittingFolder, setIsSubmittingFolder] = useState(false);

  const loadDirectory = useCallback(async (path: string) => {
    setIsLoading(true);
    setError(null);
    try {
      const result = await listDirectory(path);
      setResolvedPath(result.path);
      setEntries(result.entries);
    } catch (err) {
      setError(
        err instanceof Error ? err.message : "Failed to list directory"
      );
      setEntries([]);
    } finally {
      setIsLoading(false);
    }
  }, []);

  // Load home directory on open
  useEffect(() => {
    if (isOpen) {
      setCurrentPath("");
      setResolvedPath("");
      setEntries([]);
      setError(null);
      setShowHidden(false);
      setIsCreatingFolder(false);
      setNewFolderName("");
      setCreateError(null);
      loadDirectory("");
    }
  }, [isOpen, loadDirectory]);

  const navigateTo = (path: string) => {
    setIsCreatingFolder(false);
    setNewFolderName("");
    setCreateError(null);
    setCurrentPath(path);
    loadDirectory(path);
  };

  const handleCreateFolder = async () => {
    const name = newFolderName.trim();
    if (!name) {
      setCreateError("Folder name is required");
      return;
    }
    if (containsSeparator(name) || name === "." || name === "..") {
      setCreateError("Enter a single folder name (no slashes)");
      return;
    }
    if (!resolvedPath) {
      setCreateError("Open a directory first");
      return;
    }
    // joinPath keeps the daemon's own separator style, so a Windows daemon
    // gets `C:\a\new` rather than the mixed `C:\a/new` a template literal
    // would have produced.
    const parent = resolvedPath;
    const targetPath = joinPath(parent, name);
    setIsSubmittingFolder(true);
    setCreateError(null);
    try {
      await createDirectory(targetPath);
      setIsCreatingFolder(false);
      setNewFolderName("");
      // Refresh so the new folder appears and is selectable.
      await loadDirectory(parent);
    } catch (err) {
      setCreateError(
        err instanceof Error ? err.message : "Failed to create folder"
      );
    } finally {
      setIsSubmittingFolder(false);
    }
  };

  // "Up" stops at the root of whatever volume we're on: `/` on POSIX, `C:\`
  // or `\\server\share\` on Windows. Windows has no single filesystem root
  // above the drives, so the drive IS the top as far as this picker goes.
  const isAtRoot = !!resolvedPath && isPathRoot(resolvedPath);

  const navigateUp = () => {
    if (!resolvedPath || isAtRoot) return;
    navigateTo(dirname(resolvedPath));
  };

  const handleSelect = () => {
    onSelect(resolvedPath);
    onClose();
  };

  // Breadcrumbs, root first. The root crumb is the volume root as the daemon
  // reports it, which is why it is derived rather than hardcoded to "/": on
  // Windows the top of the tree is `C:\`, and a "/" button there would
  // navigate to a path that does not exist.
  const crumbs = pathCrumbs(resolvedPath);

  // Filter entries based on showHidden toggle, and sort: directories first, then alphabetically
  const filteredEntries = entries
    .filter((entry) => showHidden || !entry.isHidden)
    .sort((a, b) => {
      if (a.isDirectory && !b.isDirectory) return -1;
      if (!a.isDirectory && b.isDirectory) return 1;
      return a.name.localeCompare(b.name);
    });

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title="Choose a folder"
      size="lg"
    >
      <div className="flex flex-col gap-4">
        {/* Breadcrumb navigation */}
        <div className="flex items-center gap-1 text-sm font-mono overflow-x-auto pb-1 min-h-[32px]">
          {crumbs.map((crumb, index) => {
            const isLast = index === crumbs.length - 1;
            return (
              <span key={crumb.path} className="flex items-center gap-1 flex-shrink-0">
                {index > 0 && (
                  <ChevronRight className="w-3 h-3 text-muted-foreground/50" />
                )}
                <button
                  onClick={() => navigateTo(crumb.path)}
                  className={cn(
                    "px-1.5 py-0.5 rounded transition-colors",
                    isLast
                      ? "text-foreground font-medium"
                      : "text-muted-foreground hover:bg-muted hover:text-foreground",
                  )}
                >
                  {crumb.name}
                </button>
              </span>
            );
          })}
        </div>

        {/* Toolbar */}
        <div className="flex items-center justify-between">
          <button
            onClick={navigateUp}
            disabled={!resolvedPath || isAtRoot}
            className="flex h-8 items-center gap-1.5 rounded-md px-2.5 text-sm text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40"
          >
            <ArrowUp className="w-3.5 h-3.5" />
            Up
          </button>
          <div className="flex items-center gap-1">
            <button
              onClick={() => {
                setCreateError(null);
                setNewFolderName("");
                setIsCreatingFolder((v) => !v);
              }}
              disabled={!resolvedPath}
              className="flex h-8 items-center gap-1.5 rounded-md px-2.5 text-sm text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:cursor-not-allowed disabled:opacity-40"
              title="Create a new folder here"
            >
              <FolderPlus className="w-3.5 h-3.5" />
              New folder
            </button>
            <button
              onClick={() => setShowHidden(!showHidden)}
              className="flex h-8 items-center gap-1.5 rounded-md px-2.5 text-sm text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
              title={showHidden ? "Hide hidden files" : "Show hidden files"}
            >
              {showHidden ? (
                <EyeOff className="w-3.5 h-3.5" />
              ) : (
                <Eye className="w-3.5 h-3.5" />
              )}
              {showHidden ? "Hide hidden" : "Show hidden"}
            </button>
          </div>
        </div>

        {/* Inline "New folder" creator */}
        {isCreatingFolder && (
          <div className="flex flex-col gap-2 rounded-md border border-border/60 bg-background p-3">
            <div className="flex items-center gap-2">
              <FolderPlus className="w-4 h-4 text-primary flex-shrink-0" />
              <input
                autoFocus
                type="text"
                value={newFolderName}
                onChange={(e) => setNewFolderName(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    e.preventDefault();
                    void handleCreateFolder();
                  } else if (e.key === "Escape") {
                    e.preventDefault();
                    setIsCreatingFolder(false);
                    setNewFolderName("");
                    setCreateError(null);
                  }
                }}
                placeholder="New folder name"
                disabled={isSubmittingFolder}
                className="h-8 min-w-0 flex-1 rounded-md border border-input bg-card px-2.5 text-sm font-mono focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary disabled:opacity-60"
              />
              <button
                type="button"
                onClick={() => void handleCreateFolder()}
                disabled={isSubmittingFolder || !newFolderName.trim()}
                className="flex h-8 items-center gap-1.5 rounded-md bg-primary px-3 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-50"
              >
                {isSubmittingFolder && (
                  <Loader2 className="w-3.5 h-3.5 animate-spin" />
                )}
                Create
              </button>
              <button
                type="button"
                onClick={() => {
                  setIsCreatingFolder(false);
                  setNewFolderName("");
                  setCreateError(null);
                }}
                disabled={isSubmittingFolder}
                className="rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:opacity-40"
                aria-label="Cancel new folder"
                title="Cancel"
              >
                <X className="w-3.5 h-3.5" />
              </button>
            </div>
            {createError && (
              <span className="text-xs text-destructive-ink">{createError}</span>
            )}
          </div>
        )}

        {/* Error state */}
        {error && (
          <div className="rounded-md border border-destructive/30 bg-destructive/10 p-3 text-destructive-ink" role="alert">
            <div className="flex items-start gap-2">
              <AlertCircle className="w-4 h-4 flex-shrink-0 mt-0.5" />
              <span className="text-sm">{error}</span>
            </div>
          </div>
        )}

        {/* Directory listing */}
        <div className="min-h-[300px] max-h-[400px] overflow-y-auto rounded-md border border-border/60 bg-background">
          {isLoading ? (
            <div className="flex items-center justify-center py-12">
              <Loader2 className="w-5 h-5 animate-spin text-muted-foreground" />
              <span className="ml-2 text-sm text-muted-foreground">
                Loading...
              </span>
            </div>
          ) : filteredEntries.length === 0 && !error ? (
            <div className="flex items-center justify-center py-12 text-sm text-muted-foreground">
              Empty directory
            </div>
          ) : (
            <div className="divide-y divide-border/50">
              {filteredEntries.map((entry) => (
                <button
                  key={entry.path}
                  onClick={() => {
                    if (entry.isDirectory) {
                      navigateTo(entry.path);
                    }
                  }}
                  disabled={!entry.isDirectory}
                  className={`w-full flex items-center gap-3 px-4 py-2.5 text-left text-sm transition-colors ${
                    entry.isDirectory
                      ? "cursor-pointer text-foreground hover:bg-muted"
                      : "cursor-default text-muted-foreground/50"
                  }`}
                >
                  {entry.isDirectory ? (
                    <Folder className="w-4 h-4 text-primary flex-shrink-0" />
                  ) : (
                    <File className="w-4 h-4 text-muted-foreground/40 flex-shrink-0" />
                  )}
                  <span
                    className={`font-mono truncate ${
                      entry.isHidden ? "opacity-60" : ""
                    }`}
                  >
                    {entry.name}
                  </span>
                  {entry.isSymlink && (
                    <span className="text-xs text-muted-foreground/50 flex-shrink-0">
                      symlink
                    </span>
                  )}
                  {entry.isDirectory && (
                    <ChevronRight className="w-3.5 h-3.5 text-muted-foreground/40 ml-auto flex-shrink-0" />
                  )}
                </button>
              ))}
            </div>
          )}
        </div>

        {/* Current selection display */}
        {resolvedPath && (
          <div className="truncate rounded-md border border-border/60 bg-background px-3 py-2 font-mono text-sm text-muted-foreground" title={resolvedPath}>
            {resolvedPath}
          </div>
        )}

        {/* Actions */}
        <div className="flex justify-end gap-2 pt-1">
          <button
            type="button"
            onClick={onClose}
            className="inline-flex h-9 items-center justify-center gap-2 rounded-md border border-input bg-card px-4 text-sm font-medium text-foreground transition-colors hover:bg-muted focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={handleSelect}
            disabled={!resolvedPath}
            className="inline-flex h-9 items-center justify-center gap-2 rounded-md bg-primary px-4 text-sm font-medium text-primary-foreground shadow-sm transition-colors hover:bg-primary/90 focus:outline-none focus-visible:ring-2 focus-visible:ring-primary/40 focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:opacity-50 disabled:cursor-not-allowed"
          >
            Use this folder
          </button>
        </div>
      </div>
    </Modal>
  );
}