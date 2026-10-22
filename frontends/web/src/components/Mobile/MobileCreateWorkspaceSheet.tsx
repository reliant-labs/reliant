/**
 * Bottom sheet that creates a workspace: a git worktree on its own branch.
 *
 * This is the narrow slice of the desktop `CreateWorktreeModal`: a name, and
 * the branch it starts from. Copy paths take the desktop defaults. The branch
 * override, per-repo bases and force-recreate are left out. Each is a power or
 * recovery move a phone user can make later on desktop, and each would be one
 * more field a thumb has to scroll past to reach Create.
 *
 * Creation goes through `worktreeStore.createWorktree`, the same action the
 * desktop modal calls. That action wakes an asleep machine and retries
 * (`retryAcrossWake`) and toasts the outcome, so none of that is repeated here.
 *
 * The call returns as soon as the server has recorded the workspace (status
 * CREATING, no path yet), and the checkout finishes in the background. That
 * is what makes this safe on a phone: backgrounding the app mid-create no
 * longer cancels it, and the completion arrives as a `worktree_changes`
 * refetch that is replayed on reconnect (`emitWorktreeChanged` server-side).
 *
 * One phone-shaped failure is left: the request reaches the server, but the
 * response never makes it back because the OS suspended the tab or the
 * network dropped. The workspace exists and the client sees an error, and a
 * retry under the same name would then fail on the branch it already made.
 * So after an error the sheet re-lists workspaces and adopts a new one by
 * that name before reporting anything. See `reconcileAfterError`.
 */

import { useEffect, useMemo, useState } from "react";
import { GitBranchPlus, Loader2, X } from "lucide-react";
import { useWorktreeStore, type Worktree } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { useBranches, type GitBranch } from "../../hooks/useBranches";
import { repoGrpc, type Repo } from "../../api/repo-grpc";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import { DEFAULT_COPY_PATHS } from "../../lib/worktreeCopyPaths";
import { defaultBaseBranch, normalizeWorkspaceName } from "../../lib/workspaceNaming";
import { logger } from "../../lib/logger";
import { cn } from "../../lib/utils";
import { MOBILE_PRIMARY_ACTION } from "./MobileChrome";
import { MobileSelectRow, type MobileSettingOption } from "./MobileSettingsRow";

const MOBILE_FIELD =
  "w-full min-h-[44px] rounded-lg border border-border bg-background px-3 font-mono text-sm text-foreground placeholder:font-sans placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring/20";

/**
 * Base-branch choices, in the order the desktop picker uses: a detached HEAD
 * first (it is the checkout's actual state), then `main`, then local branches
 * by most recent commit, then remote branches.
 */
export function baseBranchOptions(branches: GitBranch[]): MobileSettingOption[] {
  const local = branches
    .filter((b) => !b.is_remote)
    .sort((a, b) => {
      if (!!a.is_detached !== !!b.is_detached) return a.is_detached ? -1 : 1;
      if (a.name === "main" || b.name === "main") return a.name === "main" ? -1 : 1;
      return (a.last_commit_age ?? Infinity) - (b.last_commit_age ?? Infinity);
    })
    .map((b) =>
      b.is_detached && b.commit_sha
        ? { value: b.commit_sha, label: b.name, description: "detached HEAD" }
        : { value: b.name, label: b.name, description: b.is_current ? "current" : undefined },
    );
  const remote = branches
    .filter((b) => b.is_remote)
    .map((b) => ({ value: b.name, label: b.name.replace(/^origin\//, ""), description: "remote" }));
  return [...local, ...remote];
}

/**
 * After a failed create, find the workspace the server made anyway.
 *
 * Matches on a name the list did not have before the attempt, so an existing
 * workspace that merely shares the name is never mistaken for this one. If the
 * re-list itself fails (still offline), there is nothing to adopt and the
 * original error stands.
 */
export async function reconcileAfterError(
  projectId: string,
  name: string,
  knownIds: ReadonlySet<string>,
): Promise<Worktree | undefined> {
  try {
    await useWorktreeStore.getState().refreshWorktrees(projectId);
  } catch {
    return undefined;
  }
  return useWorktreeStore
    .getState()
    .worktrees.find((w) => !knownIds.has(w.id) && w.name === name && !w.deleted_at);
}

/** A label/value row that isn't tappable, for states with nothing to pick. */
function StaticRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex min-h-[44px] items-center justify-between gap-3 px-4 py-3">
      <p className="text-sm font-medium text-foreground">{label}</p>
      <span className="truncate text-sm text-muted-foreground">{value}</span>
    </div>
  );
}

export function MobileCreateWorkspaceSheet({
  projectId,
  onClose,
  onCreated,
}: {
  projectId: string;
  onClose: () => void;
  onCreated: (worktree: Worktree) => void;
}) {
  const createWorktree = useWorktreeStore((s) => s.createWorktree);
  const isGitRepo = useProjectStore((s) => s.currentProject?.is_git_repo ?? true);

  const [name, setName] = useState("");
  // null means "the default": resolved from the branch list at submit time, so
  // a list that lands after the user started typing is still honoured.
  const [pickedBase, setPickedBase] = useState<string | null>(null);
  const [isCreating, setIsCreating] = useState(false);
  const [error, setError] = useState("");

  // Every workspace spans all of a project's nested repos. The repo list only
  // decides whether one base-branch picker makes sense: in a multi-repo
  // project there is no single branch list to pick from (the branches RPC
  // rejects a request without a repo id), so each repo uses its own default.
  const [repos, setRepos] = useState<Repo[] | null>(null);
  useEffect(() => {
    let cancelled = false;
    repoGrpc
      .list(projectId)
      .then(({ repos: loaded }) => {
        if (!cancelled) setRepos(loaded);
      })
      .catch((err) => {
        // Same fallback as desktop: treat it as a single-repo project.
        logger.warn("[MobileCreateWorkspaceSheet] Failed to list repos", { err });
        if (!cancelled) setRepos([]);
      });
    return () => {
      cancelled = true;
    };
  }, [projectId]);

  const isMultiRepo = (repos?.length ?? 0) > 1;
  const branchRepoId = repos?.length === 1 ? repos[0].id : undefined;
  const { branches, isLoading: branchesLoading, error: branchesError } = useBranches(
    repos !== null && !isMultiRepo && isGitRepo ? projectId : undefined,
    branchRepoId,
  );

  const options = useMemo(() => baseBranchOptions(branches), [branches]);
  const baseBranch = pickedBase ?? defaultBaseBranch(branches);

  const finalName = normalizeWorkspaceName(name);
  const canSubmit = finalName.length > 0 && !isCreating;

  const submit = async () => {
    if (!canSubmit) return;
    setIsCreating(true);
    setError("");
    const knownIds = new Set(useWorktreeStore.getState().worktrees.map((w) => w.id));
    try {
      const worktree = await createWorktree({
        project_id: projectId,
        name: finalName,
        branch: finalName,
        base_branch: isMultiRepo ? undefined : baseBranch,
        copy_files: [...DEFAULT_COPY_PATHS],
        status: WorktreeStatus.ACTIVE,
        force: false,
      });
      onCreated(worktree);
    } catch (err) {
      const adopted = await reconcileAfterError(projectId, finalName, knownIds);
      if (adopted) {
        onCreated(adopted);
        return;
      }
      // Stay open with the name intact so a retry is one tap.
      setError(err instanceof Error ? err.message : "Could not create the workspace");
      setIsCreating(false);
    }
  };

  const branchRow = (() => {
    if (isMultiRepo) return <StaticRow label="Branch from" value="Each repo's default" />;
    if (repos === null || branchesLoading) return <StaticRow label="Branch from" value="Loading…" />;
    if (branchesError || options.length === 0) {
      return <StaticRow label="Branch from" value="Default branch" />;
    }
    return (
      <MobileSelectRow
        label="Branch from"
        value={baseBranch ?? ""}
        placeholder="Default branch"
        sheetTitle="Start the new branch from"
        options={options}
        onChange={setPickedBase}
      />
    );
  })();

  return (
    <div className="fixed inset-0 z-50 flex flex-col justify-end">
      <button
        type="button"
        aria-label="Dismiss"
        onClick={isCreating ? undefined : onClose}
        disabled={isCreating}
        className="absolute inset-0 bg-black/40"
      />
      <div
        role="dialog"
        aria-modal="true"
        aria-label="New workspace"
        className="relative flex max-h-[90vh] flex-col rounded-t-2xl border-t border-border bg-background shadow-lg"
        style={{ paddingBottom: "env(safe-area-inset-bottom)" }}
      >
        <div className="flex min-h-[56px] items-center justify-between border-b border-border px-4 py-2">
          <span className="text-sm font-semibold text-foreground">New workspace</span>
          <button
            type="button"
            onClick={onClose}
            disabled={isCreating}
            aria-label="Close"
            className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted disabled:opacity-40"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        {!isGitRepo ? (
          <div className="space-y-2 p-4 pb-6">
            <p className="text-sm font-medium text-foreground">
              This project isn't a git repository yet
            </p>
            <p className="text-sm text-muted-foreground">
              Each workspace is a git worktree on its own branch, so the project
              needs git first. Initialize it from the desktop app.
            </p>
          </div>
        ) : (
          <form
            className="min-h-0 flex-1 space-y-4 overflow-y-auto p-4"
            onSubmit={(event) => {
              event.preventDefault();
              void submit();
            }}
          >
            {error && (
              <div
                role="alert"
                className="rounded-lg border border-destructive/30 bg-destructive/10 p-3"
              >
                <p className="text-sm text-destructive-ink">{error}</p>
              </div>
            )}

            <div>
              <label
                htmlFor="mobile-workspace-name"
                className="mb-1 block text-xs font-semibold uppercase tracking-wide text-muted-foreground"
              >
                Name
              </label>
              <input
                id="mobile-workspace-name"
                value={name}
                onChange={(event) => setName(event.target.value)}
                placeholder="fix-login-redirect"
                // Branch names are case- and character-sensitive: iOS's
                // auto-capitalize and autocorrect would rewrite them.
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
                enterKeyHint="go"
                disabled={isCreating}
                autoFocus
                className={cn(MOBILE_FIELD, "disabled:opacity-60")}
              />
              {/* The name is also the branch, and a space can't be in one —
                  say what will actually be created instead of surprising the
                  user with it in the branch list later. */}
              <p className="mt-1 truncate text-xs text-muted-foreground">
                {finalName ? (
                  <>
                    New branch <span className="font-mono text-foreground">{finalName}</span>
                  </>
                ) : (
                  "Also the name of its new git branch."
                )}
              </p>
            </div>

            <div className="overflow-hidden rounded-lg elevation-1">{branchRow}</div>

            <button
              type="submit"
              disabled={!canSubmit}
              className={cn(MOBILE_PRIMARY_ACTION, "w-full")}
            >
              {isCreating ? (
                <Loader2 className="h-4 w-4 animate-spin" />
              ) : (
                <GitBranchPlus className="h-4 w-4" />
              )}
              {isCreating ? "Creating…" : "Create workspace"}
            </button>
          </form>
        )}
      </div>
    </div>
  );
}
