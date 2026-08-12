/**
 * Where a cloned repository lands on a cloud machine.
 *
 * The daemon's per-request project resolver looks for checkouts under
 * `/home/workspace/projects/<slug>`, so this is a contract with the workspace
 * image rather than a cosmetic choice — the operator sets `DAEMON_WORKING_DIR`
 * to the same root.
 *
 * Extracted because three call sites (onboarding's GitHub step, the project
 * picker, and the connector consent screen) each need to name the same
 * destination, and a fourth private copy would eventually disagree with the
 * others about slugging.
 */

import type { GitRepo } from "@/services/controlPlane/git";

export const CLOUD_PROJECT_ROOT = "/home/workspace/projects";

/** Lowercase, hyphenated, bounded — safe as a single path segment. */
export function slugifyRepoName(value: string): string {
  return (
    value
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 48) || "github-repo"
  );
}

/** The bare repository name from a clone URL, ignoring any `.git` suffix. */
export function repoNameFromUrl(url: string): string {
  const withoutGitSuffix = url.replace(/\.git$/, "");
  return withoutGitSuffix.split("/").filter(Boolean).pop() || "github-repo";
}

/** Absolute destination path for `repo` on a cloud machine. */
export function cloudPathForRepo(repo: Pick<GitRepo, "cloneUrl" | "fullName">): string {
  return `${CLOUD_PROJECT_ROOT}/${slugifyRepoName(
    repoNameFromUrl(repo.cloneUrl) || repo.fullName
  )}`;
}
