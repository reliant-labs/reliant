import { useState, useEffect, useMemo } from "react";
import {
  Loader2,
  Search,
  CheckCircle2,
  Circle,
  FolderGit2,
} from "lucide-react";
import { Modal } from "../ui/Modal";
import { Button } from "../ui/Button";
import { useWorktreeStore } from "../../store/worktreeStore";
import { cn } from "../../lib/utils";
import {
  deriveVisibleWorktrees,
  type DiscoverSortDirection,
  type DiscoverSortField,
  type DiscoverStatusFilter,
} from "./discoverWorktreesUtils";

interface DiscoverWorktreesModalProps {
  isOpen: boolean;
  onClose: () => void;
  onWorktreesImported: (importedWorktreeIds?: string[]) => void | Promise<void>;
  projectId: string;
}

export function DiscoverWorktreesModal({
  isOpen,
  onClose,
  onWorktreesImported,
  projectId,
}: DiscoverWorktreesModalProps) {
  const discoveredWorktrees = useWorktreeStore((state) => state.discoveredWorktrees);
  const isDiscovering = useWorktreeStore((state) => state.isDiscovering);
  const error = useWorktreeStore((state) => state.error);
  const discoverWorktrees = useWorktreeStore((state) => state.discoverWorktrees);
  const importWorktree = useWorktreeStore((state) => state.importWorktree);

  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [searchQuery, setSearchQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState<DiscoverStatusFilter>("unimported");
  const [sortField, setSortField] = useState<DiscoverSortField>("name");
  const [sortDirection, setSortDirection] = useState<DiscoverSortDirection>("asc");
  const [importing, setImporting] = useState<Set<string>>(new Set());
  const [importErrors, setImportErrors] = useState<Map<string, string>>(new Map());

  // Discover worktrees when modal opens
  useEffect(() => {
    if (isOpen && projectId) {
      discoverWorktrees(projectId);
      setSelected(new Set());
      setImporting(new Set());
      setImportErrors(new Map());
      setSearchQuery("");
      setStatusFilter("unimported");
      setSortField("name");
      setSortDirection("asc");
    }
  }, [isOpen, projectId, discoverWorktrees]);

  const visibleWorktrees = useMemo(
    () =>
      deriveVisibleWorktrees(discoveredWorktrees, {
        search: searchQuery,
        statusFilter,
        sortField,
        sortDirection,
      }),
    [
      discoveredWorktrees,
      searchQuery,
      statusFilter,
      sortField,
      sortDirection,
    ]
  );

  const selectablePaths = useMemo(
    () => new Set(discoveredWorktrees.filter((w) => !w.is_imported).map((w) => w.path)),
    [discoveredWorktrees]
  );

  useEffect(() => {
    setSelected((prev) => new Set(Array.from(prev).filter((path) => selectablePaths.has(path))));
  }, [selectablePaths]);

  const selectableWorktrees = visibleWorktrees.filter((w) => !w.is_imported);
  const visibleSelectedCount = selectableWorktrees.filter((w) => selected.has(w.path)).length;
  const allVisibleSelected =
    selectableWorktrees.length > 0 && visibleSelectedCount === selectableWorktrees.length;

  const hasAnyDiscovered = discoveredWorktrees.length > 0;
  const hasAnyUnimported = discoveredWorktrees.some((w) => !w.is_imported);
  const hasActiveQuery =
    searchQuery.trim().length > 0 || statusFilter !== "unimported";

  const clearFilters = () => {
    setSearchQuery("");
    setStatusFilter("unimported");
    setSortField("name");
    setSortDirection("asc");
  };

  const handleToggle = (path: string) => {
    const newSelected = new Set(selected);
    if (newSelected.has(path)) {
      newSelected.delete(path);
    } else {
      newSelected.add(path);
    }
    setSelected(newSelected);
  };

  const handleSelectAll = () => {
    const newSelected = new Set(selected);
    selectableWorktrees.forEach((w) => newSelected.add(w.path));
    setSelected(newSelected);
  };

  const handleDeselectAll = () => {
    const newSelected = new Set(selected);
    selectableWorktrees.forEach((w) => newSelected.delete(w.path));
    setSelected(newSelected);
  };

  const handleImport = async () => {
    const toImport = Array.from(selected);
    if (toImport.length === 0) return;

    const newImporting = new Set(toImport);
    setImporting(newImporting);
    setImportErrors(new Map());

    let successCount = 0;
    const errors = new Map<string, string>();
    const importedWorktreeIds: string[] = [];

    for (const path of toImport) {
      const worktree = discoveredWorktrees.find((w) => w.path === path);
      if (!worktree) continue;

      try {
        const importedWorktree = await importWorktree({
          path: worktree.path,
          name: worktree.name,
          project_id: projectId,
        });
        importedWorktreeIds.push(importedWorktree.id);
        successCount++;
        selected.delete(path);
      } catch (err) {
        const errorMsg = err instanceof Error ? err.message : "Failed to import";
        errors.set(path, errorMsg);
      }
    }

    setImporting(new Set());
    setImportErrors(errors);
    setSelected(new Set(selected));

    if (successCount > 0) {
      await onWorktreesImported(importedWorktreeIds);

      if (errors.size === 0) {
        setTimeout(() => {
          onClose();
        }, 500);
      }
    }
  };

  return (
    <Modal isOpen={isOpen} onClose={onClose} title="Discover Worktrees" size="xl">
      <div className="space-y-4">
        {error && (
          <div className="p-4 bg-destructive/10 border border-destructive/30 text-destructive rounded-lg text-sm">
            <div className="flex items-start gap-2">
              <span className="text-destructive mt-0.5">⚠️</span>
              <span className="flex-1">{error}</span>
            </div>
          </div>
        )}

        <div className="space-y-3">
          <div className="grid grid-cols-1 md:grid-cols-4 gap-2">
            <div className="relative md:col-span-2">
              <Search className="w-4 h-4 text-muted-foreground absolute left-3 top-1/2 -translate-y-1/2" />
              <input
                type="text"
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                placeholder="Search name, branch, or path"
                className="w-full h-9 rounded-md border border-border bg-background pl-9 pr-3 text-sm focus:outline-none focus:ring-2 focus:ring-primary/30"
              />
            </div>

            <select
              value={statusFilter}
              onChange={(e) => setStatusFilter(e.target.value as DiscoverStatusFilter)}
              className="h-9 rounded-md border border-border bg-background px-3 text-sm focus:outline-none focus:ring-2 focus:ring-primary/30"
            >
              <option value="unimported">Unimported</option>
              <option value="imported">Imported</option>
              <option value="all">All</option>
            </select>

            <div className="flex gap-2">
              <select
                value={sortField}
                onChange={(e) => setSortField(e.target.value as DiscoverSortField)}
                className="h-9 flex-1 rounded-md border border-border bg-background px-3 text-sm focus:outline-none focus:ring-2 focus:ring-primary/30"
              >
                <option value="name">Sort: Name</option>
                <option value="branch">Sort: Branch</option>
                <option value="path">Sort: Path</option>
              </select>
              <button
                onClick={() =>
                  setSortDirection((current) => (current === "asc" ? "desc" : "asc"))
                }
                className="h-9 min-w-20 px-3 rounded-md border border-border bg-background text-sm hover:bg-muted transition-colors"
              >
                {sortDirection === "asc" ? "Asc" : "Desc"}
              </button>
            </div>
          </div>

          <div className="flex items-center justify-end gap-3">
            {hasActiveQuery && (
              <button
                onClick={clearFilters}
                className="text-xs text-primary hover:underline"
              >
                Clear search & filters
              </button>
            )}
          </div>

          <div className="flex items-center justify-end gap-2">
            {selectableWorktrees.length > 0 && (
              <>
                <button
                  onClick={handleSelectAll}
                  className="text-xs text-primary hover:underline"
                  disabled={allVisibleSelected}
                >
                  Select All
                </button>
                <span className="text-xs text-muted-foreground">|</span>
                <button
                  onClick={handleDeselectAll}
                  className="text-xs text-primary hover:underline"
                  disabled={visibleSelectedCount === 0}
                >
                  Deselect All
                </button>
              </>
            )}
          </div>
        </div>

        <div className="border border-border rounded-lg overflow-hidden">
          {isDiscovering ? (
            <div className="flex flex-col items-center justify-center p-12 text-center">
              <Loader2 className="w-8 h-8 text-primary animate-spin mb-3" />
              <p className="text-sm font-mono text-muted-foreground">Scanning for worktrees...</p>
            </div>
          ) : visibleWorktrees.length === 0 ? (
            <div className="flex flex-col items-center justify-center p-12 text-center">
              <Search className="w-8 h-8 text-muted-foreground opacity-50 mb-3" />
              <p className="text-sm font-mono text-muted-foreground">
                {!hasAnyDiscovered
                  ? "No workspaces discovered"
                  : statusFilter === "unimported" && !hasAnyUnimported && !hasActiveQuery
                    ? "No new workspaces found"
                    : "No workspaces match your search/filters"}
              </p>
              <p className="text-xs font-mono text-muted-foreground mt-1">
                {!hasAnyDiscovered
                  ? "This project may not be a git repository, or has no workspaces"
                  : statusFilter === "unimported" && !hasAnyUnimported && !hasActiveQuery
                    ? "All discovered workspaces are already imported"
                    : "Try adjusting your search or filter settings"}
              </p>
              {hasActiveQuery && (
                <button
                  onClick={clearFilters}
                  className="mt-3 text-xs text-primary hover:underline"
                >
                  Clear search & filters
                </button>
              )}
            </div>
          ) : (
            <div className="max-h-96 overflow-y-auto">
              {visibleWorktrees.map((worktree) => {
                const isImporting = importing.has(worktree.path);
                const importError = importErrors.get(worktree.path);
                const isSelected = selected.has(worktree.path);

                return (
                  <div
                    key={worktree.path}
                    className={cn(
                      "border-b border-border last:border-b-0",
                      importError && "bg-destructive/5"
                    )}
                  >
                    <div
                      className={cn(
                        "flex items-start gap-3 p-4 hover:elevation-1 transition-colors",
                        worktree.is_imported && "opacity-60"
                      )}
                    >
                      {!worktree.is_imported && (
                        <button
                          onClick={() => handleToggle(worktree.path)}
                          disabled={isImporting}
                          className="flex items-center justify-center mt-0.5"
                        >
                          {isSelected ? (
                            <CheckCircle2 className="w-5 h-5 text-primary" />
                          ) : (
                            <Circle className="w-5 h-5 text-muted-foreground hover:text-primary transition-colors" />
                          )}
                        </button>
                      )}
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2 mb-1">
                          <FolderGit2 className="w-4 h-4 text-muted-foreground flex-shrink-0" />
                          <span className="font-mono font-semibold text-sm truncate">
                            {worktree.name}
                          </span>
                          {worktree.is_imported && (
                            <span className="text-xs px-2 py-0.5 bg-success/10 text-success rounded-full font-mono">
                              Imported
                            </span>
                          )}
                          {isImporting && (
                            <Loader2 className="w-4 h-4 text-primary animate-spin" />
                          )}
                        </div>
                        <div className="space-y-1">
                          <div className="text-xs font-mono text-muted-foreground">
                            <span className="text-foreground/70">Branch:</span> {worktree.branch}
                          </div>
                          <div className="text-xs font-mono text-muted-foreground truncate">
                            <span className="text-foreground/70">Path:</span> {worktree.path}
                          </div>
                        </div>
                        {importError && (
                          <div className="mt-2 text-xs text-destructive font-mono">
                            ⚠️ {importError}
                          </div>
                        )}
                      </div>
                    </div>
                  </div>
                );
              })}
            </div>
          )}
        </div>

        <div className="flex items-center justify-between gap-3 pt-4 border-t border-border">
          <div className="text-xs font-mono text-muted-foreground">
            {selected.size > 0 && (
              <span>
                {selected.size} selected
                {visibleSelectedCount !== selected.size
                  ? ` (${visibleSelectedCount} visible)`
                  : ""}
              </span>
            )}
          </div>
          <div className="flex gap-3">
            <Button
              onClick={onClose}
              variant="secondary"
              size="sm"
              disabled={importing.size > 0}
            >
              {importing.size > 0 ? "Importing..." : "Close"}
            </Button>
            <Button
              onClick={handleImport}
              variant="primary"
              size="sm"
              disabled={selected.size === 0 || importing.size > 0}
              leftIcon={
                importing.size > 0 ? <Loader2 className="w-3 h-3 animate-spin" /> : undefined
              }
            >
              {importing.size > 0
                ? `Importing ${importing.size}...`
                : `Import ${selected.size > 0 ? `(${selected.size})` : ""}`}
            </Button>
          </div>
        </div>
      </div>
    </Modal>
  );
}
