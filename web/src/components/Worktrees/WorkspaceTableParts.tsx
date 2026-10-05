import type { ReactNode } from "react";
import Badge from "../forge-ui/badge";
import StatusDot from "../forge-ui/status_dot";
import { Tooltip } from "../ui/Tooltip";
import { cn } from "../../lib/utils";
import { formatAbsoluteTime, formatRelativeTime } from "../../lib/relativeTime";
import type { Worktree } from "../../store/worktreeStore";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import { hasCheckout, lifecycleOf, workingTreeState } from "./workspaceStatus";
import { useWorkspaceGitStatus } from "./useWorkspaceGitStatus";
import {
  workspaceColumn,
  workspaceIconButton,
  workspaceIconButtonDanger,
} from "./workspaceStyles";

interface IconActionProps {
  label: string;
  /** Tooltip text. Defaults to `label`; say WHY when the action is disabled. */
  hint?: string;
  icon: ReactNode;
  onClick: () => void;
  disabled?: boolean;
  danger?: boolean;
  testId?: string;
}

/** An icon-only row action: always a tooltip, always an accessible name. */
export function IconAction({ label, hint, icon, onClick, disabled, danger, testId }: IconActionProps) {
  return (
    <Tooltip content={hint ?? label} delay={300} wrapperClassName="inline-flex">
      <button
        type="button"
        aria-label={label}
        onClick={onClick}
        disabled={disabled}
        data-testid={testId}
        className={cn(workspaceIconButton, danger && workspaceIconButtonDanger)}
      >
        {icon}
      </button>
    </Tooltip>
  );
}

/**
 * The workspace's name plus the few badges that change what you can do with
 * it. "Active" is the default lifecycle and gets no badge; only a status the
 * user deliberately set is worth the space.
 */
export function WorkspaceNameCell({
  worktree,
  isCurrent,
  onSelect,
}: {
  worktree: Worktree;
  isCurrent?: boolean;
  onSelect?: () => void;
}) {
  const lifecycle = lifecycleOf(worktree.status);
  const showLifecycle =
    worktree.status !== WorktreeStatus.ACTIVE && worktree.status !== WorktreeStatus.UNSPECIFIED;
  const lifecycleVariant =
    worktree.status === WorktreeStatus.FAILED
      ? "error"
      : worktree.status === WorktreeStatus.CREATING
        ? "info"
        : "neutral";

  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <div className="flex min-w-0 flex-wrap items-center gap-1.5">
        {onSelect ? (
          <button
            type="button"
            onClick={onSelect}
            aria-label={`View details for ${worktree.name}`}
            className="min-w-0 truncate rounded-sm text-left text-sm font-medium text-foreground hover:text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            {worktree.name}
          </button>
        ) : (
          <span className="min-w-0 truncate text-sm font-medium text-foreground">{worktree.name}</span>
        )}
        {worktree.is_main && <Badge label="Main" size="sm" />}
        {isCurrent && <Badge label="Current" variant="info" size="sm" />}
        {showLifecycle && <Badge label={lifecycle.label} variant={lifecycleVariant} size="sm" />}
      </div>
      <span
        className={cn(
          "truncate font-mono text-2xs text-muted-foreground",
          workspaceColumn.branchSubline,
        )}
      >
        {worktree.branch}
      </span>
    </div>
  );
}

export function BranchCell({ worktree }: { worktree: Worktree }) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <span className="truncate font-mono text-xs text-foreground">{worktree.branch}</span>
      {worktree.base_branch && worktree.base_branch !== worktree.branch && (
        <span className="truncate text-2xs text-muted-foreground">
          from <span className="font-mono">{worktree.base_branch}</span>
        </span>
      )}
    </div>
  );
}

/** A relative time with the absolute one in a tooltip. */
export function TimeCell({ value, empty = "Never" }: { value?: string | null; empty?: string }) {
  const relative = value ? formatRelativeTime(value) : "";
  if (!value || !relative) {
    return <span className="text-xs text-muted-foreground">{empty}</span>;
  }
  return (
    <Tooltip content={formatAbsoluteTime(value)} delay={300} wrapperClassName="inline-flex">
      <span className="whitespace-nowrap text-xs text-muted-foreground">{relative}</span>
    </Tooltip>
  );
}

/**
 * The working tree's git state for one row: clean, uncommitted changes, or
 * commits ahead/behind. Read from the machine, so it is "unavailable" whenever
 * no machine can reach the worktree — that is not an error worth red ink.
 */
export function GitStateCell({ worktree }: { worktree: Worktree }) {
  const worktreeId = worktree.id;
  const checkedOut = hasCheckout(worktree);
  const { data, isLoading, isError } = useWorkspaceGitStatus(worktreeId, checkedOut);

  if (!checkedOut) {
    const lifecycle = lifecycleOf(worktree.status);
    return (
      <Tooltip content={lifecycle.description} delay={300} wrapperClassName="inline-flex">
        <GitStateLabel
          worktreeId={worktreeId}
          variant={lifecycle.variant}
          label={lifecycle.label}
          pulse={worktree.status === WorktreeStatus.CREATING}
        />
      </Tooltip>
    );
  }

  if (isLoading) {
    return <GitStateLabel worktreeId={worktreeId} variant="neutral" label="Checking" pulse />;
  }

  if (isError || !data) {
    return (
      <Tooltip
        content="Git status is read from your machine, which could not reach this workspace."
        delay={300}
        wrapperClassName="inline-flex"
      >
        <GitStateLabel worktreeId={worktreeId} variant="neutral" label="Unavailable" />
      </Tooltip>
    );
  }

  const state = workingTreeState(data);
  return (
    <Tooltip content={state.detail} delay={300} wrapperClassName="inline-flex">
      <GitStateLabel worktreeId={worktreeId} variant={state.variant} label={state.label} />
    </Tooltip>
  );
}

/**
 * Dot plus label. In a narrow table (the viewer tab's sidebar) the label
 * collapses to the dot, and stays available to screen readers.
 */
function GitStateLabel({
  worktreeId,
  variant,
  label,
  pulse,
}: {
  worktreeId: string;
  variant: Parameters<typeof StatusDot>[0]["variant"];
  label: string;
  pulse?: boolean;
}) {
  return (
    <span className="inline-flex items-center gap-1.5" data-testid={`git-state-${worktreeId}`}>
      <StatusDot variant={variant} size="sm" pulse={pulse} />
      <span className="sr-only whitespace-nowrap text-xs text-muted-foreground @lg:not-sr-only">
        {label}
      </span>
    </span>
  );
}
