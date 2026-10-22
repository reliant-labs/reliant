import { ArrowDown, ArrowUp, GitBranch, RefreshCw, X } from "lucide-react";
import { Tooltip } from "../../ui/Tooltip";
import { cn } from "../../../lib/utils";
import { IconButton } from "./IconButton";
import { pluralize } from "./fileStatus";

interface BranchHeaderProps {
  branch?: string;
  /** Branch the workspace was created from. Omitted for the main checkout. */
  baseBranch?: string;
  ahead: number;
  behind: number;
  loading: boolean;
  refreshing: boolean;
  onRefresh: () => void;
  /** Rendered only when the panel owns its own chrome (not inline). */
  onClose?: () => void;
}

/** Top strip of the Changes panel: where you are (branch, base) and how far
 *  that is from the remote, plus refresh. */
export function BranchHeader({
  branch,
  baseBranch,
  ahead,
  behind,
  loading,
  refreshing,
  onRefresh,
  onClose,
}: BranchHeaderProps) {
  return (
    <div className="flex shrink-0 items-center gap-2 border-b border-border px-3 py-2">
      <GitBranch className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
      <div className="min-w-0 flex-1">
        {branch ? (
          <div className="flex min-w-0 items-center gap-2">
            <span className="truncate font-mono text-xs font-medium text-foreground" title={branch}>
              {branch}
            </span>
            <SyncCounts ahead={ahead} behind={behind} />
          </div>
        ) : loading ? (
          <div className="h-3 w-28 animate-pulse rounded bg-border motion-reduce:animate-none" />
        ) : (
          <span className="text-xs text-muted-foreground">No branch</span>
        )}
        {baseBranch && branch && baseBranch !== branch && (
          <div className="truncate text-2xs text-muted-foreground">
            from <span className="font-mono">{baseBranch}</span>
          </div>
        )}
      </div>
      <IconButton label="Refresh changes" onClick={onRefresh} disabled={refreshing}>
        <RefreshCw className={cn("h-3.5 w-3.5", refreshing && "animate-spin")} />
      </IconButton>
      {onClose && (
        <IconButton label="Close" onClick={onClose}>
          <X className="h-3.5 w-3.5" />
        </IconButton>
      )}
    </div>
  );
}

export function SyncCounts({ ahead, behind }: { ahead: number; behind: number }) {
  if (ahead <= 0 && behind <= 0) return null;
  const parts = [
    ahead > 0 ? `${pluralize(ahead, "commit")} to push` : null,
    behind > 0 ? `${pluralize(behind, "commit")} to pull` : null,
  ].filter(Boolean);
  return (
    <Tooltip content={parts.join(", ")} delay={300} wrapperClassName="flex shrink-0">
      <span className="flex items-center gap-1.5 font-mono text-2xs tabular-nums text-muted-foreground">
        {ahead > 0 && (
          <span className="flex items-center" aria-label={`${ahead} ahead`}>
            <ArrowUp className="h-3 w-3" aria-hidden="true" />
            {ahead}
          </span>
        )}
        {behind > 0 && (
          <span className="flex items-center" aria-label={`${behind} behind`}>
            <ArrowDown className="h-3 w-3" aria-hidden="true" />
            {behind}
          </span>
        )}
      </span>
    </Tooltip>
  );
}
