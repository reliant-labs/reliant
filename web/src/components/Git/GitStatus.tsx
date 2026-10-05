import { useCallback, useEffect, useRef, useState } from 'react';
import { ArrowDown, ArrowUp, GitBranch, RefreshCw } from 'lucide-react';
import { worktreeGrpc } from '../../api/worktree-grpc';
import { cn } from '../../lib/utils';
import { Tooltip } from '../ui/Tooltip';

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

// Same letters and colours as the Changes panel rows, so the two read alike.
const GROUPS = [
  { key: 'staged', label: 'staged', letter: 'S', className: 'text-success' },
  { key: 'modified', label: 'modified', letter: 'M', className: 'text-warning' },
  { key: 'untracked', label: 'untracked', letter: 'U', className: 'text-muted-foreground' },
] as const;

/** Read-only git status for a workspace: branch, sync counts, and the changed
 *  files grouped by state. */
export function GitStatus({ worktreeId, className = "" }: GitStatusProps) {
  const [status, setStatus] = useState<GitStatusData | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const requestIdRef = useRef(0);

  const fetchStatus = useCallback(async () => {
    const requestId = ++requestIdRef.current;
    setIsLoading(true);
    setError(null);
    try {
      const grpcStatus = await worktreeGrpc.getGitStatus(worktreeId);
      if (requestIdRef.current !== requestId) return;
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
      if (requestIdRef.current === requestId) {
        setError(err instanceof Error ? err.message : 'Failed to fetch git status');
      }
    } finally {
      if (requestIdRef.current === requestId) setIsLoading(false);
    }
  }, [worktreeId]);

  useEffect(() => {
    if (worktreeId) void fetchStatus();
  }, [worktreeId, fetchStatus]);

  if (isLoading && !status) {
    return (
      <div className={cn("flex flex-col gap-2", className)} aria-busy="true" aria-label="Loading git status">
        <div className="h-3 w-32 animate-pulse rounded bg-border motion-reduce:animate-none" />
        <div className="h-3 w-24 animate-pulse rounded bg-border motion-reduce:animate-none" />
      </div>
    );
  }

  if (error) {
    return (
      <div className={cn("flex items-center justify-between gap-2 text-xs", className)}>
        <span className="min-w-0 truncate text-destructive-ink" title={error}>Couldn't read git status.</span>
        <button
          type="button"
          onClick={() => void fetchStatus()}
          className="shrink-0 font-medium text-foreground underline-offset-2 hover:underline"
        >
          Retry
        </button>
      </div>
    );
  }

  if (!status) return null;

  const counts = GROUPS.map((g) => ({ ...g, files: status[g.key] ?? [] })).filter((g) => g.files.length > 0);
  const totalChanges = counts.reduce((acc, g) => acc + g.files.length, 0);

  return (
    <div className={cn("flex flex-col gap-2", className)}>
      <div className="flex items-center gap-2">
        <GitBranch className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
        <span className="min-w-0 truncate font-mono text-xs font-medium text-foreground" title={status.branch}>
          {status.branch}
        </span>
        {(status.ahead > 0 || status.behind > 0) && (
          <span className="flex shrink-0 items-center gap-1.5 font-mono text-2xs tabular-nums text-muted-foreground">
            {status.ahead > 0 && (
              <span className="flex items-center" aria-label={`${status.ahead} ahead`}>
                <ArrowUp className="h-3 w-3" aria-hidden="true" />
                {status.ahead}
              </span>
            )}
            {status.behind > 0 && (
              <span className="flex items-center" aria-label={`${status.behind} behind`}>
                <ArrowDown className="h-3 w-3" aria-hidden="true" />
                {status.behind}
              </span>
            )}
          </span>
        )}
        <Tooltip content="Refresh status" delay={300} wrapperClassName="ml-auto flex">
          <button
            type="button"
            onClick={() => void fetchStatus()}
            aria-label="Refresh status"
            className="inline-flex h-6 w-6 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
          >
            <RefreshCw className={cn("h-3 w-3", isLoading && "animate-spin")} />
          </button>
        </Tooltip>
      </div>

      {status.clean || totalChanges === 0 ? (
        <p className="flex items-center gap-1.5 text-xs text-muted-foreground">
          <span className="inline-block h-1.5 w-1.5 rounded-full bg-success" aria-hidden="true" />
          Working tree clean
        </p>
      ) : (
        <>
          <p className="text-xs text-muted-foreground">
            {counts.map((g, i) => (
              <span key={g.key}>
                {i > 0 && " · "}
                <span className={cn("font-medium tabular-nums", g.className)}>{g.files.length}</span> {g.label}
              </span>
            ))}
          </p>
          <details className="group text-xs">
            <summary className="cursor-pointer select-none text-muted-foreground transition-colors hover:text-foreground">
              {totalChanges === 1 ? "Show 1 file" : `Show ${totalChanges} files`}
            </summary>
            <ul className="mt-1.5 flex flex-col gap-0.5">
              {counts.flatMap((g) =>
                g.files.map((file) => (
                  <li key={`${g.key}:${file}`} className="flex min-w-0 items-center gap-2">
                    <span className={cn("w-3 shrink-0 text-center font-mono font-semibold", g.className)} title={g.label}>
                      {g.letter}
                    </span>
                    <span className="truncate font-mono text-foreground/80" title={file}>{file}</span>
                  </li>
                )),
              )}
            </ul>
          </details>
        </>
      )}
    </div>
  );
}
