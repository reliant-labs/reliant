// Copyright (c) 2025 Reliant Labs

/**
 * A repository's DEFAULT branch, for the workload page's "Open PR" (decision
 * Replicas are declared in code, so Scale proposes a change to the
 * declaration).
 *
 * WHY THE DEFAULT BRANCH AND NOTHING ELSE. The branch a release was cut from
 * is forge's `git symbolic-ref` at cut time: a claim, often a local worktree
 * branch that was never pushed, or one merged and deleted since. And GitHub's
 * web editor on an unprotected branch you can write to commits DIRECTLY to it.
 * The default branch exists by definition and is the one a team protects, so
 * it is the only branch an "Open PR" link may edit. When it cannot be learned,
 * nothing is guessed: the page offers "View at commit" and "Copy path".
 *
 * The answer comes from GitHub's public repository API: public repositories
 * answer without credentials, a private one answers 404 and reads as unknown.
 * Behind a context so tests and the preview harness serve fixtures.
 */

import { createContext, useContext, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";

export interface RepoMetaApi {
  /** The default branch of `owner/repo` on GitHub, or null when it cannot be learned. */
  defaultBranch: (repo: string) => Promise<string | null>;
}

export async function githubDefaultBranch(repo: string): Promise<string | null> {
  try {
    const res = await fetch(`https://api.github.com/repos/${repo}`, { headers: { Accept: "application/vnd.github+json" } });
    if (!res.ok) return null;
    const body = (await res.json()) as { default_branch?: unknown };
    return typeof body.default_branch === "string" && body.default_branch !== "" ? body.default_branch : null;
  } catch {
    return null;
  }
}

export const githubRepoMetaApi: RepoMetaApi = { defaultBranch: githubDefaultBranch };

const RepoMetaContext = createContext<RepoMetaApi>(githubRepoMetaApi);

export function RepoMetaProvider({ api, children }: { api: RepoMetaApi; children: ReactNode }) {
  return <RepoMetaContext.Provider value={api}>{children}</RepoMetaContext.Provider>;
}

/** `owner/repo`'s default branch: undefined while asking, null when unknown. A repository's default rarely moves, so it is cached for the session. */
export function useDefaultBranch(repo: string | null): { branch: string | null | undefined; loading: boolean } {
  const api = useContext(RepoMetaContext);
  const query = useQuery({
    queryKey: ["repo-meta", "default-branch", repo ?? ""],
    queryFn: () => api.defaultBranch(repo as string),
    enabled: !!repo,
    staleTime: Infinity,
    retry: false,
  });
  return { branch: repo ? query.data : null, loading: !!repo && query.isLoading };
}
