import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent, type MouseEvent } from "react";
import { Minus, Plus, Trash2, Undo2 } from "lucide-react";
import * as gitApi from "../../../api/git";
import { useViewerStore } from "../../../store/viewerStore";
import { useProjectStore } from "../../../store/projectStore";
import { useWorktreeStore } from "../../../store/worktreeStore";
import { logger } from "../../../lib/logger";
import { cn } from "../../../lib/utils";
import { Button } from "../../ui/Button";
import ConfirmationDialog from "../../forge-ui/confirmation_dialog";
import { PRDialog } from "../../SourceControl/PRDialog";
import { GitNotInitialized } from "../../Git/GitNotInitialized";
import { CommitHistory } from "../../Git/CommitHistory";
import { BranchHeader } from "./BranchHeader";
import { ChangeSection } from "./ChangeSection";
import { CommitComposer, type GitOp } from "./CommitComposer";
import { FileChangeRow } from "./FileChangeRow";
import { IconButton } from "./IconButton";
import { changeGroupOf, pluralize, splitPath } from "./fileStatus";
import { MOD } from "./keys";
import { useExistingPR, useRepoChanges } from "./useRepoChanges";
import type { ChangeGroup, FileChange } from "./types";

// =============================================================================
// RepoChangesPanel — the source-control body for one repo (or the single-repo
// project). Top to bottom: branch header, commit composer, the change list
// grouped Staged / Changes / Untracked, then recent commits (collapsed).
//
// `repoId` scopes every git op to one nested repo. `mode === "multi-section"`
// omits the branch header, because MultiRepoSection's collapsible header
// already shows the repo, branch and sync counts.
//
// Without a worktree (the project's main checkout) the panel is read-only:
// the worktree git API is the only write path, so no composer and no row
// actions are offered.
// =============================================================================

interface RepoChangesPanelProps {
  worktreeId?: string;
  projectId: string;
  /** When set, scopes all git ops to that nested repo. */
  repoId?: string;
  inline: boolean;
  onClose: () => void;
  onFileSelect?: (file: FileChange | null) => void;
  mode: "single" | "multi-section";
}

interface VisibleFile {
  key: string;
  file: FileChange;
  group: ChangeGroup;
}

interface PendingConfirm {
  title: string;
  description: string;
  confirmLabel: string;
  run: () => Promise<void>;
}

const GROUP_TITLES: Record<ChangeGroup, string> = {
  staged: "Staged",
  modified: "Changes",
  untracked: "Untracked",
};

const GROUP_ORDER: ChangeGroup[] = ["staged", "modified", "untracked"];

const rowKey = (group: ChangeGroup, path: string) => `${group}:${path}`;

const errorMessage = (err: unknown, fallback: string) => (err instanceof Error ? err.message : fallback);

export function RepoChangesPanel({
  worktreeId,
  projectId,
  repoId,
  inline,
  onClose,
  onFileSelect,
  mode,
}: RepoChangesPanelProps) {
  const currentProject = useProjectStore((state) => state.currentProject);
  const openDiffViewer = useViewerStore((state) => state.openDiffViewer);
  const baseBranch = useWorktreeStore(
    (state) => (worktreeId ? state.worktrees.find((w) => w.id === worktreeId)?.base_branch : undefined),
  );
  // Default to true so a project that hasn't loaded yet doesn't flash the
  // "no repository" state.
  const isGitRepo = currentProject?.is_git_repo ?? true;
  const canWrite = !!worktreeId;

  // ---- Selection --------------------------------------------------------
  const [selectedKeys, setSelectedKeys] = useState<Set<string>>(new Set());
  const [cursorKey, setCursorKey] = useState<string | null>(null);
  const [anchorKey, setAnchorKey] = useState<string | null>(null);
  const cursorRowRef = useRef<HTMLDivElement>(null);
  const scrollCursorIntoViewRef = useRef(false);

  const resetSelection = useCallback(() => {
    setSelectedKeys(new Set());
    setCursorKey(null);
    setAnchorKey(null);
  }, []);

  // ---- Per-scope UI state ------------------------------------------------
  const [commitMessage, setCommitMessage] = useState("");
  const [op, setOp] = useState<GitOp | null>(null);
  const [opError, setOpError] = useState<string | null>(null);
  const [pendingPaths, setPendingPaths] = useState<Set<string>>(new Set());
  const [confirm, setConfirm] = useState<PendingConfirm | null>(null);
  const [isPRDialogOpen, setIsPRDialogOpen] = useState(false);
  const [prRefreshKey, setPrRefreshKey] = useState(0);
  const [historyKey, setHistoryKey] = useState(0);
  const [expanded, setExpanded] = useState<Record<ChangeGroup | "history", boolean>>({
    staged: true,
    modified: true,
    untracked: true,
    history: false,
  });

  const onScopeChange = useCallback(() => {
    resetSelection();
    setOpError(null);
    setPendingPaths(new Set());
    setConfirm(null);
  }, [resetSelection]);

  const { data, loading, loadError, refreshing, reload, refresh, retry } = useRepoChanges({
    worktreeId,
    projectId,
    repoId,
    enabled: isGitRepo,
    projectDefaultBranch: currentProject?.default_branch ?? "",
    onScopeChange,
  });

  const { existingPR, ghCliMissing } = useExistingPR(worktreeId, repoId, true, prRefreshKey);

  // ---- Derived file lists -----------------------------------------------
  const groups = useMemo(() => {
    const byGroup: Record<ChangeGroup, FileChange[]> = { staged: [], modified: [], untracked: [] };
    for (const file of data?.files ?? []) {
      const group = changeGroupOf(file);
      if (group) byGroup[group].push(file);
    }
    return byGroup;
  }, [data?.files]);

  const visibleFiles = useMemo<VisibleFile[]>(
    () =>
      GROUP_ORDER.flatMap((group) =>
        expanded[group] ? groups[group].map((file) => ({ key: rowKey(group, file.path), file, group })) : [],
      ),
    [groups, expanded],
  );

  const indexByKey = useMemo(() => new Map(visibleFiles.map((v, i) => [v.key, i])), [visibleFiles]);
  const selectedFiles = useMemo(
    () => visibleFiles.filter((v) => selectedKeys.has(v.key)),
    [visibleFiles, selectedKeys],
  );

  const totalChanges = groups.staged.length + groups.modified.length + groups.untracked.length;
  const ahead = data?.ahead ?? 0;
  const behind = data?.behind ?? 0;
  const isDefaultBranch = !!data?.default_branch && data.branch === data.default_branch;

  useEffect(() => {
    if (!scrollCursorIntoViewRef.current) return;
    scrollCursorIntoViewRef.current = false;
    cursorRowRef.current?.scrollIntoView?.({ block: "nearest" });
  }, [cursorKey]);

  // ---- Opening diffs ----------------------------------------------------
  const openFiles = useCallback(
    (files: FileChange[]) => {
      for (const file of files) {
        openDiffViewer(file, projectId);
        if (inline && onFileSelect) onFileSelect(file);
      }
    },
    [openDiffViewer, projectId, inline, onFileSelect],
  );

  const selectRange = (fromKey: string, toKey: string) => {
    const from = indexByKey.get(fromKey) ?? 0;
    const to = indexByKey.get(toKey) ?? 0;
    const [lo, hi] = from <= to ? [from, to] : [to, from];
    setSelectedKeys(new Set(visibleFiles.slice(lo, hi + 1).map((v) => v.key)));
  };

  const handleRowClick = (entry: VisibleFile, e: MouseEvent) => {
    if (e.shiftKey && anchorKey && indexByKey.has(anchorKey)) {
      selectRange(anchorKey, entry.key);
      setCursorKey(entry.key);
      return;
    }
    if (e.metaKey || e.ctrlKey) {
      setSelectedKeys((prev) => {
        const next = new Set(prev);
        if (next.has(entry.key)) next.delete(entry.key);
        else next.add(entry.key);
        return next;
      });
      setCursorKey(entry.key);
      setAnchorKey(entry.key);
      return;
    }
    // Plain click on a row inside a multi-selection opens the whole
    // selection, with the clicked file opened last so it ends up focused.
    if (selectedKeys.size > 1 && selectedKeys.has(entry.key)) {
      openFiles([...selectedFiles.filter((v) => v.key !== entry.key).map((v) => v.file), entry.file]);
    } else {
      openFiles([entry.file]);
    }
    setSelectedKeys(new Set([entry.key]));
    setCursorKey(entry.key);
    setAnchorKey(entry.key);
  };

  // ---- Git writes -------------------------------------------------------
  const withPending = async (paths: string[], fn: () => Promise<void>) => {
    setPendingPaths((prev) => new Set([...prev, ...paths]));
    try {
      await fn();
    } finally {
      setPendingPaths((prev) => {
        const next = new Set(prev);
        paths.forEach((p) => next.delete(p));
        return next;
      });
    }
  };

  const stagePaths = (paths: string[]) =>
    withPending(paths, async () => {
      if (!worktreeId || paths.length === 0) return;
      setOpError(null);
      try {
        await gitApi.stageFiles(worktreeId, paths, repoId);
        await reload();
        setSelectedKeys(new Set());
      } catch (err) {
        setOpError(errorMessage(err, "Failed to stage files"));
        logger.error("Failed to stage files:", err);
      }
    });

  const unstagePaths = (paths: string[]) =>
    withPending(paths, async () => {
      if (!worktreeId || paths.length === 0) return;
      setOpError(null);
      try {
        await gitApi.unstageFiles(worktreeId, paths, repoId);
        await reload();
        setSelectedKeys(new Set());
      } catch (err) {
        setOpError(errorMessage(err, "Failed to unstage files"));
        logger.error("Failed to unstage files:", err);
      }
    });

  const discardPaths = (paths: string[]) =>
    withPending(paths, async () => {
      if (!worktreeId || paths.length === 0) return;
      setOpError(null);
      try {
        const result = await gitApi.revertFiles(worktreeId, paths, repoId);
        if (result.message?.includes("error(s):")) {
          setOpError(result.message);
          logger.warn("Discard completed with errors", { message: result.message, files: paths });
        }
        await reload();
        setSelectedKeys(new Set());
      } catch (err) {
        setOpError(errorMessage(err, "Failed to discard changes"));
        logger.error("Failed to discard changes:", err);
      }
    });

  /** Ask before discarding. Staged entries are never discarded from here —
   *  the server would only unstage them, which the Unstage action says. */
  const requestDiscard = (entries: VisibleFile[]) => {
    const targets = entries.filter((v) => v.group !== "staged");
    if (targets.length === 0) return;
    const untracked = targets.filter((v) => v.group === "untracked").length;
    const tracked = targets.length - untracked;
    const paths = targets.map((v) => v.file.path);

    let title: string;
    let description: string;
    let confirmLabel: string;
    if (targets.length === 1) {
      const { name } = splitPath(targets[0].file.path);
      if (untracked) {
        title = `Delete ${name}?`;
        description = "This untracked file will be permanently deleted. This can't be undone.";
        confirmLabel = "Delete file";
      } else {
        title = `Discard changes to ${name}?`;
        description = "The file goes back to its last committed state. This can't be undone.";
        confirmLabel = "Discard changes";
      }
    } else if (tracked && untracked) {
      title = `Discard ${pluralize(targets.length, "file")}?`;
      description = `${pluralize(tracked, "tracked file")} will go back to the last commit and ${pluralize(untracked, "untracked file")} will be deleted. This can't be undone.`;
      confirmLabel = "Discard all";
    } else if (untracked) {
      title = `Delete ${pluralize(untracked, "untracked file")}?`;
      description = "These files will be permanently deleted. This can't be undone.";
      confirmLabel = "Delete files";
    } else {
      title = `Discard changes to ${pluralize(tracked, "file")}?`;
      description = "These files go back to their last committed state. This can't be undone.";
      confirmLabel = "Discard changes";
    }

    setConfirm({ title, description, confirmLabel, run: () => discardPaths(paths) });
  };

  /** Run a commit / push / pull, then refresh status, history and the PR. */
  const runOp = async (kind: GitOp, fn: (wt: string) => Promise<void>, fallback: string) => {
    if (!worktreeId || op) return;
    setOp(kind);
    setOpError(null);
    try {
      await fn(worktreeId);
      logger.info(`git ${kind} succeeded`, { worktreeId, repoId });
    } catch (err) {
      setOpError(errorMessage(err, fallback));
      logger.error(`git ${kind} failed`, err);
    } finally {
      await reload();
      setHistoryKey((k) => k + 1);
      setOp(null);
    }
  };

  const handleCommit = ({ push }: { push: boolean }) => {
    const message = commitMessage.trim();
    if (!message) return;
    // Nothing staged: commit everything that changed, VS Code smart-commit
    // style. The composer labels the button "Commit all changes" in this case.
    const toStage =
      groups.staged.length === 0 ? [...groups.modified, ...groups.untracked].map((f) => f.path) : [];
    if (groups.staged.length === 0 && toStage.length === 0) return;

    void runOp(
      push ? "commit-push" : "commit",
      async (wt) => {
        if (toStage.length > 0) await gitApi.stageFiles(wt, toStage, repoId);
        await gitApi.commitChanges(wt, message, repoId);
        setCommitMessage("");
        if (push) await gitApi.pushChanges(wt, repoId);
      },
      push ? "Failed to commit and push" : "Failed to commit changes",
    );
  };

  const handlePush = () =>
    void runOp("push", async (wt) => void (await gitApi.pushChanges(wt, repoId)), "Failed to push changes");
  const handlePull = () =>
    void runOp("pull", async (wt) => void (await gitApi.pullChanges(wt, repoId)), "Failed to pull changes");

  // ---- Keyboard ---------------------------------------------------------
  const handleKeyDown = (e: KeyboardEvent) => {
    const target = e.target as HTMLElement;
    if (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable) return;
    if (visibleFiles.length === 0) return;

    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const current = cursorKey !== null ? (indexByKey.get(cursorKey) ?? -1) : -1;
      const nextIndex =
        e.key === "ArrowDown" ? Math.min(current + 1, visibleFiles.length - 1) : Math.max(current - 1, 0);
      const nextKey = visibleFiles[nextIndex].key;
      scrollCursorIntoViewRef.current = true;
      setCursorKey(nextKey);
      if (e.shiftKey) {
        const anchor = anchorKey && indexByKey.has(anchorKey) ? anchorKey : (cursorKey ?? nextKey);
        setAnchorKey(anchor);
        selectRange(anchor, nextKey);
      } else {
        setAnchorKey(nextKey);
        setSelectedKeys(new Set([nextKey]));
      }
    } else if (e.key === "Enter") {
      const toOpen =
        selectedFiles.length > 0
          ? selectedFiles.map((v) => v.file)
          : visibleFiles.filter((v) => v.key === cursorKey).map((v) => v.file);
      if (toOpen.length > 0) {
        e.preventDefault();
        openFiles(toOpen);
      }
    } else if (e.key === "Escape") {
      setSelectedKeys(new Set());
      setAnchorKey(null);
    } else if ((e.metaKey || e.ctrlKey) && canWrite && selectedFiles.length > 0) {
      const key = e.key.toLowerCase();
      if (key === "s") {
        e.preventDefault();
        void stagePaths(selectedFiles.filter((v) => v.group !== "staged").map((v) => v.file.path));
      } else if (key === "u") {
        e.preventDefault();
        void unstagePaths(selectedFiles.filter((v) => v.group === "staged").map((v) => v.file.path));
      } else if (key === "d") {
        e.preventDefault();
        requestDiscard(selectedFiles);
      }
    }
  };

  // ---- Rendering --------------------------------------------------------
  const rowActions = (entry: VisibleFile) => {
    if (!canWrite) return undefined;
    // An action on a row inside a multi-selection applies to the selection.
    const inSelection = selectedKeys.size > 1 && selectedKeys.has(entry.key);
    const scope = inSelection ? selectedFiles : [entry];
    const n = scope.length;
    const suffix = n > 1 ? ` ${pluralize(n, "file")}` : "";
    const busy = pendingPaths.size > 0 || op !== null;

    if (entry.group === "staged") {
      const paths = scope.filter((v) => v.group === "staged").map((v) => v.file.path);
      return (
        <IconButton label={`Unstage${suffix}`} tooltip={`Unstage${suffix} (${MOD}U)`} onClick={() => void unstagePaths(paths)} disabled={busy}>
          <Minus className="h-3.5 w-3.5" />
        </IconButton>
      );
    }

    const isUntracked = entry.group === "untracked";
    const discardLabel = n > 1 ? `Discard${suffix}` : isUntracked ? "Delete file" : "Discard changes";
    return (
      <>
        <IconButton label={discardLabel} tooltip={`${discardLabel} (${MOD}D)`} tone="danger" onClick={() => requestDiscard(scope)} disabled={busy}>
          {isUntracked && n === 1 ? <Trash2 className="h-3.5 w-3.5" /> : <Undo2 className="h-3.5 w-3.5" />}
        </IconButton>
        <IconButton
          label={`Stage${suffix}`}
          tooltip={`Stage${suffix} (${MOD}S)`}
          onClick={() => void stagePaths(scope.filter((v) => v.group !== "staged").map((v) => v.file.path))}
          disabled={busy}
        >
          <Plus className="h-3.5 w-3.5" />
        </IconButton>
      </>
    );
  };

  const sectionActions = (group: ChangeGroup) => {
    if (!canWrite) return undefined;
    const files = groups[group];
    const busy = pendingPaths.size > 0 || op !== null;
    const entries = files.map((file) => ({ key: rowKey(group, file.path), file, group }));
    if (group === "staged") {
      return (
        <IconButton label="Unstage all" onClick={() => void unstagePaths(files.map((f) => f.path))} disabled={busy}>
          <Minus className="h-3.5 w-3.5" />
        </IconButton>
      );
    }
    return (
      <>
        <IconButton
          label={group === "untracked" ? "Delete all untracked files" : "Discard all changes"}
          tone="danger"
          onClick={() => requestDiscard(entries)}
          disabled={busy}
        >
          {group === "untracked" ? <Trash2 className="h-3.5 w-3.5" /> : <Undo2 className="h-3.5 w-3.5" />}
        </IconButton>
        <IconButton label="Stage all" onClick={() => void stagePaths(files.map((f) => f.path))} disabled={busy}>
          <Plus className="h-3.5 w-3.5" />
        </IconButton>
      </>
    );
  };

  const toggle = (key: ChangeGroup | "history") => setExpanded((prev) => ({ ...prev, [key]: !prev[key] }));

  const containerClass = cn("flex flex-col", mode === "single" && "h-full", mode === "single" && !inline && "bg-card");

  if (!isGitRepo && currentProject && mode === "single") {
    return (
      <div className={containerClass}>
        {!inline && (
          <BranchHeader ahead={0} behind={0} loading={false} refreshing={false} onRefresh={() => {}} onClose={onClose} />
        )}
        <GitNotInitialized projectId={projectId} projectName={currentProject.name} className="flex-1" />
      </div>
    );
  }

  const fileList = (
    <div
      role="list"
      aria-label="Changed files"
      aria-multiselectable="true"
      tabIndex={0}
      onKeyDown={handleKeyDown}
      className={cn("flex flex-col gap-1 px-1.5 py-2 focus:outline-none", mode === "single" && "flex-1 overflow-y-auto")}
    >
      {loadError && !data ? (
        <div role="alert" className="mx-1.5 flex flex-col items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/10 p-3">
          <p className="text-sm font-semibold text-destructive">Couldn't load changes</p>
          <p className="break-words text-xs text-destructive/90">{loadError}</p>
          <Button variant="outline" size="sm" onClick={retry}>
            Retry
          </Button>
        </div>
      ) : loading && !data ? (
        <ChangesSkeleton />
      ) : totalChanges === 0 ? (
        <div className="flex flex-col items-center gap-1 px-4 py-8 text-center">
          <p className="text-sm font-semibold text-foreground">No changes</p>
          <p className="text-pretty text-xs text-muted-foreground">
            The working tree is clean. Edits from the agent or your editor show up here.
          </p>
        </div>
      ) : (
        <>
          {loadError && (
            <p role="alert" className="mx-1.5 text-xs text-destructive">
              Refresh failed: {loadError}
            </p>
          )}
          {GROUP_ORDER.map((group) =>
            groups[group].length === 0 ? null : (
              <ChangeSection
                key={group}
                title={GROUP_TITLES[group]}
                count={groups[group].length}
                expanded={expanded[group]}
                onToggle={() => toggle(group)}
                actions={sectionActions(group)}
              >
                <div className="flex flex-col">
                  {groups[group].map((file) => {
                    const key = rowKey(group, file.path);
                    const entry = { key, file, group };
                    const selected = selectedKeys.has(key) || cursorKey === key;
                    return (
                      <FileChangeRow
                        key={key}
                        ref={cursorKey === key ? cursorRowRef : undefined}
                        file={file}
                        selected={selected}
                        processing={pendingPaths.has(file.path)}
                        pinActions={selected}
                        onClick={(e) => handleRowClick(entry, e)}
                        actions={rowActions(entry)}
                      />
                    );
                  })}
                </div>
              </ChangeSection>
            ),
          )}
        </>
      )}

      {worktreeId && data && (
        <div className="mt-1 border-t border-border pt-2">
          <ChangeSection title="Recent commits" expanded={expanded.history} onToggle={() => toggle("history")}>
            <CommitHistory key={historyKey} worktreeId={worktreeId} repoId={repoId} className="px-2 pb-1" />
          </ChangeSection>
        </div>
      )}
    </div>
  );

  return (
    <div className={containerClass}>
      {mode === "single" && (
        <BranchHeader
          branch={data?.branch}
          baseBranch={baseBranch}
          ahead={ahead}
          behind={behind}
          loading={loading}
          refreshing={refreshing}
          onRefresh={() => void refresh()}
          onClose={inline ? undefined : onClose}
        />
      )}

      {canWrite && (
        <CommitComposer
          branch={data?.branch}
          message={commitMessage}
          onMessageChange={(value) => {
            setCommitMessage(value);
            if (opError) setOpError(null);
          }}
          stagedCount={groups.staged.length}
          unstagedCount={groups.modified.length + groups.untracked.length}
          ahead={ahead}
          behind={behind}
          op={op}
          disabled={!data}
          error={opError}
          onDismissError={() => setOpError(null)}
          onCommit={handleCommit}
          onPush={handlePush}
          onPull={handlePull}
          pr={
            data && !isDefaultBranch
              ? { existing: existingPR, ghCliMissing, onCreate: () => setIsPRDialogOpen(true) }
              : null
          }
        />
      )}

      {fileList}

      {worktreeId && (
        <PRDialog
          isOpen={isPRDialogOpen}
          onClose={() => setIsPRDialogOpen(false)}
          onPRCreated={() => setPrRefreshKey((k) => k + 1)}
          worktreeId={worktreeId}
          defaultBranch={data?.default_branch}
          currentBranch={data?.branch}
          repoId={repoId}
        />
      )}

      {/* forge-ui scope: ConfirmationDialog's danger button reads forge tokens. */}
      <div className="forge-ui">
        <ConfirmationDialog
          open={confirm !== null}
          title={confirm?.title ?? ""}
          description={confirm?.description}
          confirmLabel={confirm?.confirmLabel}
          cancelLabel="Cancel"
          variant="danger"
          onCancel={() => setConfirm(null)}
          onConfirm={() => {
            const pending = confirm;
            setConfirm(null);
            if (pending) void pending.run();
          }}
        />
      </div>
    </div>
  );
}

function ChangesSkeleton() {
  return (
    <div className="flex flex-col gap-2 px-2 py-1" aria-busy="true" aria-label="Loading changes">
      <div className="h-3 w-16 animate-pulse rounded bg-border motion-reduce:animate-none" />
      {[72, 56, 64, 48].map((width) => (
        <div key={width} className="flex items-center gap-2">
          <div className="h-4 w-4 animate-pulse rounded bg-border motion-reduce:animate-none" />
          <div
            className="h-3 animate-pulse rounded bg-border motion-reduce:animate-none"
            style={{ width: `${width}%` }}
          />
        </div>
      ))}
    </div>
  );
}
