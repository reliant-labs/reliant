// Copyright (c) 2025 Reliant Labs

/**
 * One-click alert triage (research/feedback-loop/AUTOPILOT_TRIGGERS.md): the
 * pieces of the Enable dialog that are pure, so they are tested without it.
 */

/** The workflow input an alert no service maps to is filed under. */
export const DEFAULT_REPO_INPUT = "default_repo";

/**
 * `owner/name` of a GitHub remote, in any spelling git accepts
 * (https://github.com/o/n.git, git@github.com:o/n.git, ssh://git@github.com/o/n).
 * undefined for anything else.
 */
export function gitHubRepoSlug(remote: string): string | undefined {
  const m = remote.trim().match(/^(?:[a-z+]+:\/\/)?(?:[^@/]+@)?github\.com[:/]([^/\s]+)\/([^/\s]+?)(?:\.git)?\/?$/i);
  return m ? `${m[1]}/${m[2]}` : undefined;
}

/** The one GitHub repository among a project's remotes, or undefined when there are none or several. */
export function singleGitHubRepo(remotes: readonly string[]): string | undefined {
  const slugs = new Set(remotes.map(gitHubRepoSlug).filter((s): s is string => !!s));
  return slugs.size === 1 ? [...slugs][0] : undefined;
}

/**
 * params with default_repo filled in, when there is a repository to fill and
 * the user set none. The same object when nothing changes, so a caller can
 * tell whether the fill applied.
 */
export function withDefaultRepo(params: Record<string, unknown>, repo: string | undefined): Record<string, unknown> {
  if (!repo) return params;
  const current = params[DEFAULT_REPO_INPUT];
  if (typeof current === "string" ? current.trim() !== "" : current !== undefined) return params;
  return { ...params, [DEFAULT_REPO_INPUT]: repo };
}
