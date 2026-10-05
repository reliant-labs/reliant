import { useEffect, useState } from "react";
import { AlertCircle, ChevronRight, X } from "lucide-react";
import { worktreeGrpc, type WorktreeRepoStatus } from "../../api/worktree-grpc";
import { logger } from "../../lib/logger";
import { cn } from "../../lib/utils";
import { matchesRefetchScope, subscribeToRefetch } from "../../store/refetchStore";
import { RepoChangesPanel } from "./changes/RepoChangesPanel";
import { SyncCounts } from "./changes/BranchHeader";
import { IconButton } from "./changes/IconButton";
import { pluralize } from "./changes/fileStatus";
import type { FileChange } from "./changes/types";

// The source-control ("Changes") panel of the right sidebar. The panel body
// lives in ./changes/; this file is the public entry point and the
// multi-repo grouping.
export type { FileChange } from "./changes/types";
export { preloadGitStatus } from "./changes/gitStatusCache";

interface RecentChangesProps {
  worktreeId?: string;
  projectId: string;
  onClose: () => void;
  inline?: boolean;
  onFileSelect?: (file: FileChange | null) => void;
}

// =============================================================================
// MultiRepoSection — one nested repo of a multi-repo project: a collapsible
// header (repo, branch, sync counts, change count) over the same panel body a
// single-repo project gets.
// =============================================================================

interface MultiRepoSectionProps {
  status: WorktreeRepoStatus;
  worktreeId: string;
  projectId: string;
  defaultExpanded: boolean;
  inline: boolean;
  onClose: () => void;
  onFileSelect?: (file: FileChange | null) => void;
}

function MultiRepoSection({
  status,
  worktreeId,
  projectId,
  defaultExpanded,
  inline,
  onClose,
  onFileSelect,
}: MultiRepoSectionProps) {
  const [expanded, setExpanded] = useState(defaultExpanded);
  const hasError = !!status.error;
  const name = status.repo_name || status.repo_relative_path || status.repo_id;

  return (
    <section className="border-b border-border">
      <button
        type="button"
        onClick={() => setExpanded((e) => !e)}
        aria-expanded={expanded}
        className={cn(
          "flex w-full items-center gap-2 px-3 py-2 text-left transition-colors hover:bg-muted/50",
          "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring/50",
        )}
      >
        <ChevronRight
          className={cn("h-3.5 w-3.5 shrink-0 text-muted-foreground transition-transform", expanded && "rotate-90")}
          aria-hidden="true"
        />
        {hasError && <AlertCircle className="h-3.5 w-3.5 shrink-0 text-destructive" aria-label="Error" />}
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-semibold text-foreground">{name}</div>
          {!hasError && status.current_branch && (
            <div className="truncate font-mono text-2xs text-muted-foreground">{status.current_branch}</div>
          )}
        </div>
        {!hasError && (
          <div className="flex shrink-0 items-center gap-2">
            <SyncCounts ahead={status.ahead} behind={status.behind} />
            <span
              className={cn(
                "text-2xs tabular-nums",
                status.changed_files > 0 ? "font-medium text-foreground" : "text-muted-foreground",
              )}
            >
              {status.changed_files > 0 ? pluralize(status.changed_files, "change") : "Clean"}
            </span>
          </div>
        )}
      </button>

      {expanded &&
        (hasError ? (
          <div role="alert" className="mx-3 mb-2 rounded-md border border-destructive/30 bg-destructive/10 px-2.5 py-1.5 text-xs text-destructive">
            {status.error}
          </div>
        ) : (
          <div className="border-t border-border/60">
            <RepoChangesPanel
              worktreeId={worktreeId}
              projectId={projectId}
              repoId={status.repo_id}
              inline={inline}
              onClose={onClose}
              onFileSelect={onFileSelect}
              mode="multi-section"
            />
          </div>
        ))}
    </section>
  );
}

// =============================================================================
// RecentChanges — public entry point. Projects with 2+ nested repos get one
// collapsible section per repo; everything else (single repo, no worktree,
// or a daemon that can't list repo statuses) gets the single-repo panel.
// =============================================================================

export function RecentChanges(props: RecentChangesProps) {
  const { worktreeId, projectId, onClose, inline = false, onFileSelect } = props;
  const [repoStatuses, setRepoStatuses] = useState<WorktreeRepoStatus[] | null>(null);
  const [statusesLoaded, setStatusesLoaded] = useState(false);

  // Fetch the per-repo status list. Empty / single → single-repo panel.
  // Errors fall back to it too (older daemons or stubbed test envs).
  useEffect(() => {
    if (!worktreeId) {
      setRepoStatuses(null);
      setStatusesLoaded(true);
      return;
    }
    let cancelled = false;
    setStatusesLoaded(false);
    const load = async () => {
      try {
        const fn = (worktreeGrpc as { listRepoStatuses?: typeof worktreeGrpc.listRepoStatuses }).listRepoStatuses;
        if (typeof fn !== "function") {
          if (!cancelled) {
            setRepoStatuses(null);
            setStatusesLoaded(true);
          }
          return;
        }
        const statuses = await worktreeGrpc.listRepoStatuses(worktreeId);
        if (!cancelled) {
          setRepoStatuses(statuses);
          setStatusesLoaded(true);
        }
      } catch (err) {
        logger.debug("[RecentChanges] listRepoStatuses unavailable, falling back to single-repo panel", err);
        if (!cancelled) {
          setRepoStatuses(null);
          setStatusesLoaded(true);
        }
      }
    };
    void load();
    return () => {
      cancelled = true;
    };
  }, [worktreeId]);

  // Refetch repo statuses when the backend signals a worktree change.
  useEffect(() => {
    if (!worktreeId) return;
    let debounceTimer: ReturnType<typeof setTimeout> | null = null;
    const unsubscribe = subscribeToRefetch("worktree_changes", (event) => {
      if (!matchesRefetchScope(event, { worktreeId, projectId })) return;
      if (debounceTimer) clearTimeout(debounceTimer);
      debounceTimer = setTimeout(async () => {
        debounceTimer = null;
        try {
          const fn = (worktreeGrpc as { listRepoStatuses?: typeof worktreeGrpc.listRepoStatuses }).listRepoStatuses;
          if (typeof fn !== "function") return;
          const statuses = await worktreeGrpc.listRepoStatuses(worktreeId);
          setRepoStatuses(statuses);
        } catch (err) {
          logger.debug("[RecentChanges] failed to refetch repo statuses", err);
        }
      }, 500);
    });
    return () => {
      unsubscribe();
      if (debounceTimer) clearTimeout(debounceTimer);
    };
  }, [worktreeId, projectId]);

  if (statusesLoaded && repoStatuses && repoStatuses.length >= 2 && worktreeId) {
    // Open the first repo with changes; if none has any, open the first so
    // the panel isn't a wall of collapsed headers.
    const firstWithChanges = repoStatuses.findIndex((s) => s.has_changes && !s.error);
    const openIndex = firstWithChanges >= 0 ? firstWithChanges : 0;
    const totalChanges = repoStatuses.reduce((acc, s) => acc + (s.error ? 0 : s.changed_files), 0);
    const reposWithErrors = repoStatuses.filter((s) => !!s.error).length;

    return (
      <div className={cn("flex h-full flex-col", !inline && "bg-card")}>
        <div className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-2">
          <p className="min-w-0 flex-1 truncate text-xs text-muted-foreground">
            <span className="font-medium text-foreground">{pluralize(repoStatuses.length, "repo")}</span>
            {" · "}
            {pluralize(totalChanges, "change")}
            {reposWithErrors > 0 && <span className="text-destructive"> · {reposWithErrors} with errors</span>}
          </p>
          {!inline && (
            <IconButton label="Close" onClick={onClose}>
              <X className="h-3.5 w-3.5" />
            </IconButton>
          )}
        </div>
        <div className="flex-1 overflow-y-auto">
          {repoStatuses.map((status, idx) => (
            <MultiRepoSection
              key={status.repo_id || `${status.repo_relative_path}-${idx}`}
              status={status}
              worktreeId={worktreeId}
              projectId={projectId}
              defaultExpanded={idx === openIndex}
              inline={inline}
              onClose={onClose}
              onFileSelect={onFileSelect}
            />
          ))}
        </div>
      </div>
    );
  }

  // Single-repo / no worktree / loading / 0-repos → the single-repo panel
  // with repoId unset. The backend accepts an empty repo_id for 0/1-repo
  // projects, and leaving it out avoids a refetch flicker when
  // listRepoStatuses resolves.
  return (
    <RepoChangesPanel
      worktreeId={worktreeId}
      projectId={projectId}
      inline={inline}
      onClose={onClose}
      onFileSelect={onFileSelect}
      mode="single"
    />
  );
}
