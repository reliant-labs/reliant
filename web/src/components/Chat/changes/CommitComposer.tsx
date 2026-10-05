import { useId } from "react";
import { ArrowDown, ArrowUp, ArrowUpFromLine, ExternalLink, GitPullRequest, Loader2, X } from "lucide-react";
import type { ExistingPRResponse } from "../../../api/git";
import { Button } from "../../ui/Button";
import { Tooltip } from "../../ui/Tooltip";
import { cn } from "../../../lib/utils";
import { pluralize } from "./fileStatus";
import { MOD, SHIFT } from "./keys";

export type GitOp = "commit" | "commit-push" | "push" | "pull";

interface CommitComposerProps {
  branch?: string;
  message: string;
  onMessageChange: (value: string) => void;
  stagedCount: number;
  /** Modified + untracked: what "Commit all" would stage first. */
  unstagedCount: number;
  ahead: number;
  behind: number;
  op: GitOp | null;
  disabled: boolean;
  error: string | null;
  onDismissError: () => void;
  onCommit: (opts: { push: boolean }) => void;
  onPush: () => void;
  onPull: () => void;
  /** PR affordance. `null` hides it (no worktree, or on the default branch). */
  pr: {
    existing: ExistingPRResponse | null;
    ghCliMissing: boolean;
    onCreate: () => void;
  } | null;
}

/**
 * The top-to-bottom flow of the Changes panel: write a message, commit (or
 * commit and push), then sync and open a PR. Only offers what the worktree
 * git API supports today: commit, push, pull, create PR.
 */
export function CommitComposer({
  branch,
  message,
  onMessageChange,
  stagedCount,
  unstagedCount,
  ahead,
  behind,
  op,
  disabled,
  error,
  onDismissError,
  onCommit,
  onPush,
  onPull,
  pr,
}: CommitComposerProps) {
  const messageId = useId();
  const busy = op !== null || disabled;
  const hasMessage = message.trim().length > 0;
  // Nothing staged but there are changes: commit stages everything first,
  // like VS Code's smart commit. The label says so, so it is never a surprise.
  const commitsAll = stagedCount === 0 && unstagedCount > 0;
  const commitCount = commitsAll ? unstagedCount : stagedCount;
  const canCommit = hasMessage && commitCount > 0 && !busy;

  const commitLabel = commitsAll ? "Commit all changes" : stagedCount > 0 ? `Commit ${pluralize(stagedCount, "file")}` : "Commit";
  const commitHint =
    commitCount === 0
      ? "Nothing to commit"
      : !hasMessage
        ? "Write a commit message first"
        : commitsAll
          ? `Stage all ${pluralize(unstagedCount, "change")} and commit (${MOD}Enter)`
          : `Commit staged files (${MOD}Enter)`;

  const showSyncRow = ahead > 0 || behind > 0 || pr !== null;

  return (
    <div className="flex shrink-0 flex-col gap-2 border-b border-border p-3">
      {error && (
        <div
          role="alert"
          className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/10 px-2.5 py-1.5 text-xs text-destructive"
        >
          <span className="min-w-0 flex-1 break-words">{error}</span>
          <button
            type="button"
            onClick={onDismissError}
            aria-label="Dismiss error"
            className="shrink-0 rounded p-0.5 opacity-70 transition-opacity hover:opacity-100"
          >
            <X className="h-3 w-3" />
          </button>
        </div>
      )}

      <label className="sr-only" htmlFor={messageId}>
        Commit message
      </label>
      <textarea
        id={messageId}
        value={message}
        onChange={(e) => onMessageChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            e.stopPropagation();
            if (canCommit) onCommit({ push: e.shiftKey });
          }
        }}
        rows={2}
        placeholder={branch ? `Message for ${branch}` : "Commit message"}
        disabled={busy}
        className={cn(
          "field-sizing-content max-h-40 min-h-14 w-full resize-none rounded-md border border-border bg-background px-2.5 py-2 text-sm",
          "placeholder:text-muted-foreground focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/20",
          "disabled:cursor-not-allowed disabled:opacity-60",
        )}
      />

      <div className="flex w-full">
        <Tooltip content={commitHint} delay={300} wrapperClassName="flex min-w-0 flex-1">
          <Button
            variant="primary"
            size="md"
            onClick={() => onCommit({ push: false })}
            disabled={!canCommit}
            loading={op === "commit"}
            className="min-w-0 flex-1 rounded-r-none"
          >
            {op === "commit" ? "Committing…" : commitLabel}
          </Button>
        </Tooltip>
        <Tooltip content={`Commit and push (${MOD}${SHIFT}Enter)`} delay={300} wrapperClassName="flex">
          <button
            type="button"
            aria-label="Commit and push"
            onClick={() => onCommit({ push: true })}
            disabled={!canCommit}
            className={cn(
              "inline-flex h-8 w-8 items-center justify-center rounded-r-md border border-l-0 border-primary/20 bg-primary text-primary-foreground",
              "shadow-sm transition-colors hover:bg-primary/90 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50",
              "disabled:cursor-not-allowed disabled:opacity-50",
              "relative before:absolute before:inset-y-1.5 before:left-0 before:w-px before:bg-primary-foreground/25",
            )}
          >
            {op === "commit-push" ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <ArrowUpFromLine className="h-4 w-4" />
            )}
          </button>
        </Tooltip>
      </div>

      {showSyncRow && (
        <div className="flex flex-wrap gap-2">
          {ahead > 0 && (
            <Tooltip content={`Push ${pluralize(ahead, "commit")} to the remote`} delay={300} wrapperClassName="flex min-w-0 flex-1">
              <Button
                variant="outline"
                size="sm"
                onClick={onPush}
                disabled={busy}
                loading={op === "push"}
                leftIcon={<ArrowUp className="h-3.5 w-3.5" />}
                className="w-full"
              >
                Push {ahead}
              </Button>
            </Tooltip>
          )}
          {behind > 0 && (
            <Tooltip content={`Pull ${pluralize(behind, "commit")} from the remote`} delay={300} wrapperClassName="flex min-w-0 flex-1">
              <Button
                variant="outline"
                size="sm"
                onClick={onPull}
                disabled={busy}
                loading={op === "pull"}
                leftIcon={<ArrowDown className="h-3.5 w-3.5" />}
                className="w-full"
              >
                Pull {behind}
              </Button>
            </Tooltip>
          )}
          {pr && <PullRequestAction {...pr} disabled={busy} />}
        </div>
      )}
    </div>
  );
}

function PullRequestAction({
  existing,
  ghCliMissing,
  onCreate,
  disabled,
}: {
  existing: ExistingPRResponse | null;
  ghCliMissing: boolean;
  onCreate: () => void;
  disabled: boolean;
}) {
  if (existing?.exists && existing.url) {
    const state = existing.state ? existing.state.toLowerCase() : "";
    return (
      <Tooltip content={existing.title ? `Open “${existing.title}” on GitHub` : "Open pull request on GitHub"} delay={300} wrapperClassName="flex min-w-0 flex-1">
        <a
          href={existing.url}
          target="_blank"
          rel="noopener noreferrer"
          className={cn(
            "inline-flex h-7 w-full min-w-0 items-center justify-center gap-1.5 rounded-md border border-border bg-background px-2.5 text-xs font-medium text-foreground",
            "transition-colors hover:bg-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
          )}
        >
          <GitPullRequest className="h-3.5 w-3.5 shrink-0 text-primary" />
          <span className="truncate">PR #{existing.number}</span>
          {state && state !== "open" && <span className="text-muted-foreground">{state}</span>}
          <ExternalLink className="h-3 w-3 shrink-0 text-muted-foreground" />
        </a>
      </Tooltip>
    );
  }

  return (
    <Tooltip
      content={ghCliMissing ? "Install the GitHub CLI (gh) from cli.github.com to create pull requests" : "Open a pull request for this branch"}
      delay={300}
      wrapperClassName="flex min-w-0 flex-1"
    >
      <Button
        variant="outline"
        size="sm"
        onClick={onCreate}
        disabled={disabled || ghCliMissing}
        leftIcon={<GitPullRequest className="h-3.5 w-3.5" />}
        className="w-full"
      >
        Create PR
      </Button>
    </Tooltip>
  );
}
