/**
 * Naming rules for a new workspace, shared by every surface that creates one —
 * the desktop `CreateWorktreeModal` and the mobile create sheet.
 *
 * The name a user types is also the git branch it creates, so the surfaces
 * have to agree on how one becomes the other. A phone and a laptop turning
 * "fix login" into two different branches is a bug neither side could see.
 */

import type { GitBranch } from "../hooks/useBranches";

/** Spaces are not legal in a branch name, so runs of them collapse to one hyphen. */
export function normalizeWorkspaceName(value: string): string {
  return value.trim().replace(/\s+/g, "-");
}

/**
 * The branch a new workspace starts from when the user doesn't pick one: the
 * main checkout's current branch. A detached HEAD resolves to its full commit
 * SHA, because the branch list names it by a short SHA for display only.
 *
 * Undefined when the branch list has nothing to say (not loaded, failed, or no
 * local branch is checked out). Sending no base branch lets the daemon detect
 * the repo's default branch (`getRepositoryDefaultBranch`), which beats
 * guessing "main" for a repo whose default is `master` or `develop`.
 */
export function defaultBaseBranch(branches: GitBranch[]): string | undefined {
  const current = branches.find((b) => b.is_current && !b.is_remote);
  if (!current) return undefined;
  if (current.is_detached && current.commit_sha) return current.commit_sha;
  return current.name;
}
