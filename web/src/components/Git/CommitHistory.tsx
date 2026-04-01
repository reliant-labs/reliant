import { useState, useEffect } from 'react';
import { GitCommit as GitCommitIcon, RefreshCw, AlertCircle, Clock, User, ChevronDown, ChevronUp } from 'lucide-react';
import { worktreeGrpc, type GitCommit } from '../../api/worktree-grpc';
import { cn } from '../../lib/utils';
import { toast } from '../../lib/toast-manager';

interface CommitHistoryProps {
  worktreeId: string;
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

export function CommitHistory({ worktreeId, className = "", limit = 20, initialDisplay = 5 }: CommitHistoryProps) {
  const [data, setData] = useState<CommitHistoryData | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [isExpanded, setIsExpanded] = useState(false);

  const fetchCommits = async () => {
    setIsLoading(true);
    setError(null);
    try {
      const grpcData = await worktreeGrpc.getCommits(worktreeId, limit);
      setData(grpcData);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to fetch commit history');
    } finally {
      setIsLoading(false);
    }
  };

  useEffect(() => {
    if (worktreeId) {
      fetchCommits();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [worktreeId, limit]);

  const formatDate = (dateStr: string) => {
    try {
      const date = new Date(dateStr);
      if (isNaN(date.getTime())) return dateStr;
      
      const now = new Date();
      const diffMs = now.getTime() - date.getTime();
      const diffMins = Math.floor(diffMs / 60000);
      const diffHours = Math.floor(diffMs / 3600000);
      const diffDays = Math.floor(diffMs / 86400000);

      if (diffMins < 60) {
        return `${diffMins}m ago`;
      } else if (diffHours < 24) {
        return `${diffHours}h ago`;
      } else if (diffDays < 7) {
        return `${diffDays}d ago`;
      } else {
        return date.toLocaleDateString('en-US', { 
          month: 'short', 
          day: 'numeric',
          year: date.getFullYear() !== now.getFullYear() ? 'numeric' : undefined
        });
      }
    } catch {
      return dateStr;
    }
  };

  const copyToClipboard = async (text: string, shortHash: string) => {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(`Copied ${shortHash} to clipboard`);
    } catch (err) {
      console.error('Failed to copy:', err);
      toast.error('Failed to copy to clipboard');
    }
  };

  if (isLoading) {
    return (
      <div className={cn("flex items-center gap-2 text-xs text-muted-foreground font-mono", className)}>
        <RefreshCw className="w-3 h-3 animate-spin" />
        <span>Loading commits...</span>
      </div>
    );
  }

  if (error) {
    return (
      <div className={cn("flex items-center gap-2 text-xs text-muted-foreground font-mono", className)}>
        <AlertCircle className="w-3 h-3" />
        <span>No commits yet</span>
      </div>
    );
  }

  if (!data || data.commits.length === 0) {
    return (
      <div className={cn("flex items-center gap-2 text-xs text-muted-foreground font-mono", className)}>
        <AlertCircle className="w-3 h-3" />
        <span>No commits found</span>
      </div>
    );
  }

  return (
    <div className={cn("flex flex-col gap-2", className)}>
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="flex flex-col gap-1">
          <div className="flex items-center gap-2 text-xs font-mono">
            <GitCommitIcon className="w-3 h-3 text-muted-foreground" />
            <span className="font-medium">{data.total} commit{data.total !== 1 ? 's' : ''}</span>
            {data.comparison_mode && data.comparison_ref ? (
              <span className="text-muted-foreground">
                ({data.current_branch || data.branch} vs {data.comparison_ref})
              </span>
            ) : (
              data.total > 0 && (
                <span className="text-warning text-xs">
                  (showing all commits - no base branch)
                </span>
              )
            )}
          </div>
          {data.total === 0 && data.comparison_mode && (
            <p className="text-xs text-muted-foreground font-mono pl-5">
              No new commits on this branch
            </p>
          )}
          {!data.comparison_mode && data.total > 0 && (
            <p className="text-xs text-warning font-mono pl-5">
              ⚠ Base branch not found. Showing all commits on {data.current_branch || data.branch}.
            </p>
          )}
        </div>
        <button
          onClick={fetchCommits}
          className="p-1 hover:bg-accent rounded transition-colors"
          aria-label="Refresh commits"
        >
          <RefreshCw className="w-3 h-3 text-muted-foreground" />
        </button>
      </div>

      {/* Commit List */}
      {data.commits.length > 0 && (
        <>
          <div className="space-y-2">
            {data.commits.slice(0, isExpanded ? data.commits.length : initialDisplay).map((commit) => (
              <div
                key={commit.hash}
                className="flex flex-col gap-1 p-2 rounded-md hover:bg-muted/50 transition-colors"
              >
                {/* Commit message */}
                <div className="flex items-start gap-2">
                  <GitCommitIcon className="w-3 h-3 text-muted-foreground mt-0.5 flex-shrink-0" />
                  <div className="flex-1 min-w-0">
                    <p className="text-xs font-mono text-foreground break-words">
                      {commit.message}
                    </p>
                  </div>
                </div>

                {/* Commit metadata */}
                <div className="flex items-center gap-3 text-xs font-mono text-muted-foreground pl-5">
                  <button
                    onClick={() => copyToClipboard(commit.hash, commit.short_hash)}
                    className="hover:text-foreground transition-colors font-medium"
                    title="Click to copy full hash"
                  >
                    {commit.short_hash}
                  </button>
                  
                  <div className="flex items-center gap-1">
                    <User className="w-3 h-3" />
                    <span className="truncate max-w-[150px]" title={commit.author}>
                      {commit.author}
                    </span>
                  </div>
                  
                  <div className="flex items-center gap-1">
                    <Clock className="w-3 h-3" />
                    <span title={commit.date}>{formatDate(commit.date)}</span>
                  </div>
                </div>
              </div>
            ))}
          </div>

          {/* Expand/Collapse button */}
          {data.commits.length > initialDisplay && (
            <button
              onClick={() => setIsExpanded(!isExpanded)}
              className="w-full flex items-center justify-center gap-2 py-2 text-xs font-mono text-muted-foreground hover:text-foreground transition-colors border-t border-border/40"
            >
              {isExpanded ? (
                <>
                  <ChevronUp className="w-3 h-3" />
                  <span>Show less</span>
                </>
              ) : (
                <>
                  <ChevronDown className="w-3 h-3" />
                  <span>Show {data.commits.length - initialDisplay} more commit{data.commits.length - initialDisplay !== 1 ? 's' : ''}</span>
                </>
              )}
            </button>
          )}

          {/* Total commits hint */}
          {data.total >= limit && isExpanded && (
            <div className="text-xs text-muted-foreground font-mono text-center pt-2 border-t border-border/40">
              Showing {limit} most recent commits
            </div>
          )}
        </>
      )}
    </div>
  );
}
