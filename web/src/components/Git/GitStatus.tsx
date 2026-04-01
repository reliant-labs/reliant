import { useState, useEffect } from 'react';
import { GitBranch, RefreshCw, AlertCircle } from 'lucide-react';
import { worktreeGrpc } from '../../api/worktree-grpc';
import { cn } from '../../lib/utils';

interface GitStatusProps {
  worktreeId: string;
  className?: string;
}

interface GitStatusData {
  branch: string;
  clean: boolean;
  modified: string[];
  untracked: string[];
  staged: string[];
  ahead: number;
  behind: number;
}

export function GitStatus({ worktreeId, className = "" }: GitStatusProps) {
  const [status, setStatus] = useState<GitStatusData | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const fetchStatus = async () => {
    setIsLoading(true);
    setError(null);
    try {
      const grpcStatus = await worktreeGrpc.getGitStatus(worktreeId);
      // Convert gRPC response to component's expected format
      setStatus({
        branch: grpcStatus.current_branch,
        clean: grpcStatus.is_clean,
        modified: grpcStatus.modified_files,
        untracked: grpcStatus.untracked_files,
        staged: grpcStatus.staged_files,
        ahead: grpcStatus.ahead,
        behind: grpcStatus.behind,
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to fetch git status');
    } finally {
      setIsLoading(false);
    }
  };

  useEffect(() => {
    if (worktreeId) {
      fetchStatus();
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [worktreeId]);

  if (isLoading) {
    return (
      <div className={cn("flex items-center gap-2 text-xs text-muted-foreground font-mono", className)}>
        <RefreshCw className="w-3 h-3 animate-spin" />
        <span>Loading status...</span>
      </div>
    );
  }

  if (error) {
    return (
      <div className={cn("flex items-center gap-2 text-xs text-destructive font-mono", className)}>
        <AlertCircle className="w-3 h-3" />
        <span>Git not initialized</span>
      </div>
    );
  }

  if (!status) {
    return null;
  }

  const totalChanges = 
    (status.modified?.length || 0) + 
    (status.untracked?.length || 0) + 
    (status.staged?.length || 0);

  return (
    <div className={cn("flex flex-col gap-2", className)}>
      {/* Branch Info */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 text-xs font-mono">
          <GitBranch className="w-3 h-3 text-muted-foreground" />
          <span className="font-medium">{status.branch}</span>
          {status.ahead > 0 && (
            <span className="text-green-600 dark:text-green-400">
              ↑{status.ahead}
            </span>
          )}
          {status.behind > 0 && (
            <span className="text-orange-600 dark:text-orange-400">
              ↓{status.behind}
            </span>
          )}
        </div>
        <button
          onClick={fetchStatus}
          className="p-1 hover:bg-accent rounded transition-colors"
          aria-label="Refresh status"
        >
          <RefreshCw className="w-3 h-3 text-muted-foreground" />
        </button>
      </div>

      {/* Status Summary */}
      <div className="text-xs font-mono">
        {status.clean ? (
          <div className="flex items-center gap-2 text-green-600 dark:text-green-400">
            <span className="w-2 h-2 bg-green-500 rounded-full" />
            <span>Working tree clean</span>
          </div>
        ) : (
          <div className="space-y-1">
            {status.modified?.length > 0 && (
              <div className="flex items-center gap-2 text-yellow-600 dark:text-yellow-400">
                <span className="w-2 h-2 bg-yellow-500 rounded-full" />
                <span>{status.modified.length} modified</span>
              </div>
            )}
            {status.staged?.length > 0 && (
              <div className="flex items-center gap-2 text-green-600 dark:text-green-400">
                <span className="w-2 h-2 bg-green-500 rounded-full" />
                <span>{status.staged.length} staged</span>
              </div>
            )}
            {status.untracked?.length > 0 && (
              <div className="flex items-center gap-2 text-gray-600 dark:text-gray-400">
                <span className="w-2 h-2 bg-gray-500 rounded-full" />
                <span>{status.untracked.length} untracked</span>
              </div>
            )}
          </div>
        )}
      </div>

      {/* File List (collapsible) */}
      {totalChanges > 0 && (
        <details className="text-xs font-mono">
          <summary className="cursor-pointer text-muted-foreground hover:text-foreground">
            {totalChanges} file{totalChanges !== 1 ? 's' : ''} changed
          </summary>
          <div className="mt-2 space-y-1 pl-4">
            {status.modified?.map(file => (
              <div key={file} className="text-yellow-600 dark:text-yellow-400">
                M {file}
              </div>
            ))}
            {status.staged?.map(file => (
              <div key={file} className="text-green-600 dark:text-green-400">
                A {file}
              </div>
            ))}
            {status.untracked?.map(file => (
              <div key={file} className="text-gray-600 dark:text-gray-400">
                ? {file}
              </div>
            ))}
          </div>
        </details>
      )}
    </div>
  );
}