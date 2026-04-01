import { useState, useEffect, useCallback } from "react";
import {
  Folder,
  File,
  ChevronRight,
  Loader2,
  AlertCircle,
  Eye,
  EyeOff,
  ArrowUp,
} from "lucide-react";
import { Modal } from "../ui/Modal";
import { listDirectory, type DirectoryEntry } from "../../api/filesystem-grpc";

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
  const [currentPath, setCurrentPath] = useState("");
  const [resolvedPath, setResolvedPath] = useState("");
  const [entries, setEntries] = useState<DirectoryEntry[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [showHidden, setShowHidden] = useState(false);

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
      loadDirectory("");
    }
  }, [isOpen, loadDirectory]);

  const navigateTo = (path: string) => {
    setCurrentPath(path);
    loadDirectory(path);
  };

  const navigateUp = () => {
    if (!resolvedPath || resolvedPath === "/") return;
    const parent = resolvedPath.substring(0, resolvedPath.lastIndexOf("/")) || "/";
    navigateTo(parent);
  };

  const handleSelect = () => {
    onSelect(resolvedPath);
    onClose();
  };

  // Parse path into breadcrumb segments
  const pathSegments = resolvedPath
    ? resolvedPath.split("/").filter(Boolean)
    : [];

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
      title="Select Directory"
      size="lg"
    >
      <div className="flex flex-col gap-4">
        {/* Breadcrumb navigation */}
        <div className="flex items-center gap-1 text-sm font-mono overflow-x-auto pb-1 min-h-[32px]">
          <button
            onClick={() => navigateTo("/")}
            className="px-1.5 py-0.5 rounded hover:bg-muted/80 text-muted-foreground hover:text-foreground transition-colors flex-shrink-0"
          >
            /
          </button>
          {pathSegments.map((segment, index) => {
            const segmentPath = "/" + pathSegments.slice(0, index + 1).join("/");
            const isLast = index === pathSegments.length - 1;
            return (
              <span key={segmentPath} className="flex items-center gap-1 flex-shrink-0">
                <ChevronRight className="w-3 h-3 text-muted-foreground/50" />
                <button
                  onClick={() => navigateTo(segmentPath)}
                  className={`px-1.5 py-0.5 rounded transition-colors ${
                    isLast
                      ? "text-foreground font-medium"
                      : "text-muted-foreground hover:text-foreground hover:bg-muted/80"
                  }`}
                >
                  {segment}
                </button>
              </span>
            );
          })}
        </div>

        {/* Toolbar */}
        <div className="flex items-center justify-between">
          <button
            onClick={navigateUp}
            disabled={!resolvedPath || resolvedPath === "/"}
            className="flex items-center gap-1.5 px-3 py-1.5 text-sm text-muted-foreground hover:text-foreground hover:bg-muted/80 rounded-md transition-colors disabled:opacity-40 disabled:cursor-not-allowed"
          >
            <ArrowUp className="w-3.5 h-3.5" />
            Up
          </button>
          <button
            onClick={() => setShowHidden(!showHidden)}
            className="flex items-center gap-1.5 px-3 py-1.5 text-sm text-muted-foreground hover:text-foreground hover:bg-muted/80 rounded-md transition-colors"
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

        {/* Error state */}
        {error && (
          <div className="p-3 bg-destructive/10 border border-destructive/30 text-destructive rounded-lg">
            <div className="flex items-start gap-2">
              <AlertCircle className="w-4 h-4 flex-shrink-0 mt-0.5" />
              <span className="text-sm">{error}</span>
            </div>
          </div>
        )}

        {/* Directory listing */}
        <div className="border border-border rounded-lg overflow-hidden min-h-[300px] max-h-[400px] overflow-y-auto bg-background">
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
                      ? "hover:bg-muted/60 cursor-pointer text-foreground"
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
          <div className="px-3 py-2 bg-muted/30 rounded-lg border border-border text-sm font-mono text-muted-foreground truncate">
            {resolvedPath}
          </div>
        )}

        {/* Actions */}
        <div className="flex gap-3 pt-2">
          <button
            type="button"
            onClick={onClose}
            className="flex-1 px-5 py-3 bg-muted hover:bg-muted/80 border border-border rounded-lg text-sm font-medium transition-all focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={handleSelect}
            disabled={!resolvedPath}
            className="flex-1 px-5 py-3 bg-primary text-primary-foreground hover:bg-primary/90 rounded-lg text-sm font-semibold shadow-sm transition-all focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background disabled:opacity-50 disabled:cursor-not-allowed"
          >
            Select
          </button>
        </div>
      </div>
    </Modal>
  );
}
