import { useCallback, useEffect, useRef, useState } from 'react';
import { RefreshCw } from 'lucide-react';
import { worktreeGrpc, type GitCommit } from '../../api/worktree-grpc';
import { cn } from '../../lib/utils';
import { toast } from '../../lib/toast-manager';
import { Tooltip } from '../ui/Tooltip';

interface CommitHistoryProps {
  worktreeId: string;
  /** Scope to one nested repo of a multi-repo project. */
  repoId?: string;
  className?: string;
  limit?: number;
  initialDisplay?: number;
}

// Local interface matching what gRPC returns
interface CommitHistoryData {
  commits: GitCommit[];
  total: number;
  branch: string;
  base_branch: string;
  comparison_mode: boolean;
  comparison_ref: string;
  current_branch: string;
  error?: string;
}

export function formatCommitDate(dateStr: string, now: Date = new Date()): string {
  const date = new Date(dateStr);
  if (isNaN(date.getTime())) return dateStr;

  const diffMs = now.getTime() - date.getTime();
  const diffMins = Math.max(0, Math.floor(diffMs / 60000));
  const diffHours = Math.floor(diffMs / 3600000);
  const diffDays = Math.floor(diffMs / 86400000);

  if (diffMins < 1) return 'just now';
  if (diffMins < 60) return `${diffMins}m ago`;
  if (diffHours < 24) return `${diffHours}h ago`;
  if (diffDays < 7) return `${diffDays}d ago`;
  return date.toLocaleDateString('en-US', {
    month: 'short',
    day: 'numeric',
    year: date.getFullYear() !== now.getFullYear() ? 'numeric' : undefined,
  });
}

/**
 * Commits on this workspace's branch that are not on its base branch.
 * Dense list: one line per commit (message, then short hash and age), the
 * hash copies on click.
 */
export function CommitHistory({ worktreeId, repoId, className = "", limit = 20, initialDisplay = 5 }: CommitHistoryProps) {
  const [data, setData] = useState<CommitHistoryData | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [isExpanded, setIsExpanded] = useState(false);
  const requestIdRef = useRef(0);

  const fetchCommits = useCallback(async () => {
    const requestId = ++requestIdRef.current;
    setIsLoading(true);
    setError(null);
    try {
      const grpcData = await worktreeGrpc.getCommits(worktreeId, limit, repoId);
      if (requestIdRef.current === requestId) setData(grpcData);
    } catch (err) {
      if (requestIdRef.current === requestId) {
        setError(err instanceof Error ? err.message : 'Failed to fetch commit history');
      }
    } finally {
      if (requestIdRef.current === requestId) setIsLoading(false);
    }
  }, [worktreeId, limit, repoId]);

  useEffect(() => {
    if (worktreeId) void fetchCommits();
  }, [worktreeId, fetchCommits]);

  const copyToClipboard = async (text: string, shortHash: string) => {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(`Copied ${shortHash} to clipboard`);
    } catch (err) {
      console.error('Failed to copy:', err);
      toast.error('Failed to copy to clipboard');
    }
  };

  if (isLoading && !data) {
    return (
      <div className={cn("flex flex-col gap-2", className)} aria-busy="true" aria-label="Loading commits">
        {[0, 1, 2].map((i) => (
          <div key={i} className="flex flex-col gap-1">
            <div className="h-3 w-4/5 animate-pulse rounded bg-border motion-reduce:animate-none" />
            <div className="h-2.5 w-1/3 animate-pulse rounded bg-border motion-reduce:animate-none" />
          </div>
        ))}
      </div>
    );
  }

  if (error) {
    return (
      <div className={cn("flex items-center justify-between gap-2 text-xs text-muted-foreground", className)}>
        <span className="min-w-0 truncate" title={error}>Couldn't load commits.</span>
        <button
          type="button"
          onClick={() => void fetchCommits()}
          className="shrink-0 font-medium text-foreground underline-offset-2 hover:underline"
        >
          Retry
        </button>
      </div>
    );
  }

  if (!data) return null;

  const compareLabel =
    data.comparison_mode && data.comparison_ref
      ? `${data.current_branch || data.branch} vs ${data.comparison_ref}`
      : null;
  const visible = data.commits.slice(0, isExpanded ? data.commits.length : initialDisplay);
  const hidden = data.commits.length - initialDisplay;

  return (
    <div className={cn("flex flex-col gap-2", className)}>
      <div className="flex items-center justify-between gap-2">
        <p className="min-w-0 truncate text-xs text-muted-foreground">
          <span className="font-medium tabular-nums text-foreground">{data.total}</span>{" "}
          {data.total === 1 ? "commit" : "commits"}
          {compareLabel && <span className="font-mono"> · {compareLabel}</span>}
        </p>
        <Tooltip content="Refresh commits" delay={300} wrapperClassName="flex">
          <button
            type="button"
            onClick={() => void fetchCommits()}
            aria-label="Refresh commits"
            className="inline-flex h-6 w-6 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          >
            <RefreshCw className={cn("h-3 w-3", isLoading && "animate-spin")} />
          </button>
        </Tooltip>
      </div>

      {!data.comparison_mode && data.total > 0 && (
        <p className="text-xs text-warning">
          Base branch not found, so this lists every commit on {data.current_branch || data.branch}.
        </p>
      )}

      {data.commits.length === 0 ? (
        <p className="text-xs text-muted-foreground">
          {data.comparison_mode ? "No commits on this branch yet." : "No commits found."}
        </p>
      ) : (
        <ol className="flex flex-col">
          {visible.map((commit) => (
            <li key={commit.hash} className="flex flex-col gap-0.5 border-b border-border/60 py-1.5 last:border-b-0">
              <p className="truncate text-xs text-foreground" title={commit.message}>
                {commit.message}
              </p>
              <div className="flex min-w-0 items-center gap-2 text-2xs text-muted-foreground">
                <Tooltip content="Copy full hash" delay={300} wrapperClassName="flex">
                  <button
                    type="button"
                    onClick={() => void copyToClipboard(commit.hash, commit.short_hash)}
                    aria-label={`Copy commit hash ${commit.short_hash}`}
                    className="font-mono transition-colors hover:text-foreground"
                  >
                    {commit.short_hash}
                  </button>
                </Tooltip>
                <span className="min-w-0 truncate" title={commit.author}>{commit.author}</span>
                <span className="ml-auto shrink-0 tabular-nums" title={commit.date}>{formatCommitDate(commit.date)}</span>
              </div>
            </li>
          ))}
        </ol>
      )}

      {hidden > 0 && (
        <button
          type="button"
          onClick={() => setIsExpanded(!isExpanded)}
          className="self-start text-xs font-medium text-muted-foreground transition-colors hover:text-foreground"
        >
          {isExpanded ? "Show fewer" : `Show ${hidden} more`}
        </button>
      )}

      {data.total >= limit && isExpanded && (
        <p className="text-2xs text-muted-foreground">Showing the {limit} most recent commits.</p>
      )}
    </div>
  );
}
