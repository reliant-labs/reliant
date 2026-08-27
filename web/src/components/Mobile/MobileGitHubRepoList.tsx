/**
 * Repo browser reachable from the GitHub settings screen once a credential
 * is connected. Backed by `gitService.listRepos`, the exact call desktop's
 * `RepoSelector`/`useGitRepos` make — same pagination contract (page,
 * perPage, sort, `hasMore`), just driven by scroll instead of a fixed
 * 100-per-page fetch-once.
 *
 * `listRepos` is a page-number API, not a cursor, so there's no stable key
 * to hand `useInfiniteQuery` beyond the page number itself — and re-running
 * a `getNextPageParam` off `hasMore` buys nothing over tracking `page` and
 * appending results locally. Plain component state keeps this file
 * readable without a dependency most of this surface's data doesn't need.
 */

import { useEffect, useState } from "react";
import { Github, Loader2, Lock, Search } from "lucide-react";
import { Virtuoso } from "react-virtuoso";
import { useGitHubCredential } from "@/hooks/useGitHubCredential";
import { gitService } from "@/services/controlPlane/git";
import type { GitRepo } from "@/services/controlPlane/git/types";
import { MobileBackButton, MobileEmptyState, MobileScreenHeader } from "./MobileChrome";
import { MobileGitHubCloneSheet } from "./MobileGitHubCloneSheet";

const PER_PAGE = 30;

function formatUpdated(updatedAt: string): string {
  if (!updatedAt) return "";
  const d = new Date(updatedAt);
  if (isNaN(d.getTime())) return "";
  const diffDays = Math.floor((Date.now() - d.getTime()) / (1000 * 60 * 60 * 24));
  if (diffDays <= 0) return "today";
  if (diffDays === 1) return "yesterday";
  if (diffDays < 30) return `${diffDays}d ago`;
  if (diffDays < 365) return `${Math.floor(diffDays / 30)}mo ago`;
  return `${Math.floor(diffDays / 365)}y ago`;
}

function RepoRow({ repo, onClone }: { repo: GitRepo; onClone: (repo: GitRepo) => void }) {
  return (
    <div className="px-4 pb-2">
      <button
        type="button"
        onClick={() => onClone(repo)}
        className="flex min-h-16 w-full flex-col gap-1 rounded-lg border-b-0 px-4 py-3 text-left elevation-1 active:bg-foreground/5"
      >
        <div className="flex items-center gap-2">
          <span className="min-w-0 flex-1 truncate text-sm font-medium text-foreground">
            {repo.fullName}
          </span>
          {repo.private && (
            <span className="flex shrink-0 items-center gap-1 rounded px-1.5 py-0.5 text-2xs font-medium text-muted-foreground ring-1 ring-inset ring-border">
              <Lock className="h-2.5 w-2.5" />
              Private
            </span>
          )}
        </div>
        {repo.description && (
          <p className="truncate text-xs text-muted-foreground">{repo.description}</p>
        )}
        <div className="flex items-center gap-2 text-xs text-muted-foreground/80">
          {repo.language && <span>{repo.language}</span>}
          {repo.language && repo.updatedAt && <span aria-hidden>·</span>}
          {repo.updatedAt && <span>Updated {formatUpdated(repo.updatedAt)}</span>}
        </div>
      </button>
    </div>
  );
}

export function MobileGitHubRepoList({ onBack }: { onBack: () => void }) {
  const { hasToken, isLoading: credentialLoading } = useGitHubCredential();

  const [repos, setRepos] = useState<GitRepo[]>([]);
  const [page, setPage] = useState(0);
  const [hasMore, setHasMore] = useState(true);
  const [loading, setLoading] = useState(false);
  const [initialLoaded, setInitialLoaded] = useState(false);
  const [error, setError] = useState("");
  const [search, setSearch] = useState("");
  const [cloningRepo, setCloningRepo] = useState<GitRepo | null>(null);

  const loadMore = async () => {
    if (loading || !hasMore || !hasToken) return;
    setLoading(true);
    setError("");
    try {
      const nextPage = page + 1;
      const result = await gitService.listRepos(nextPage, PER_PAGE, "updated");
      setRepos((prev) => [...prev, ...result.repos]);
      setPage(nextPage);
      setHasMore(result.hasMore);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load repositories");
    } finally {
      setLoading(false);
      setInitialLoaded(true);
    }
  };

  useEffect(() => {
    if (hasToken && !initialLoaded && !loading) void loadMore();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [hasToken]);

  const filteredRepos = search.trim()
    ? repos.filter((r) => {
        const q = search.toLowerCase();
        return (
          r.fullName.toLowerCase().includes(q) ||
          r.description?.toLowerCase().includes(q) ||
          r.language?.toLowerCase().includes(q)
        );
      })
    : repos;

  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileScreenHeader
        title="Clone a repo"
        leading={<MobileBackButton onClick={onBack} label="Back to GitHub" />}
      />

      {!credentialLoading && !hasToken ? (
        <MobileEmptyState
          icon={Github}
          title="Connect GitHub first"
          description="Connect your GitHub account to browse and clone your repositories."
        />
      ) : (
        <>
          <div className="shrink-0 border-b border-border px-4 py-3">
            <div className="relative">
              <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground/60" />
              <input
                type="text"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Search repositories…"
                className="w-full min-h-[44px] rounded-lg border border-border bg-background pl-9 pr-3 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring/20"
              />
            </div>
          </div>

          {error && (
            <p className="shrink-0 px-4 py-2 text-xs text-destructive">{error}</p>
          )}

          {!initialLoaded ? (
            <div className="flex flex-1 items-center justify-center">
              <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
            </div>
          ) : filteredRepos.length === 0 && !loading ? (
            <MobileEmptyState
              icon={Github}
              title={search ? "No matches" : "No repositories found"}
              description={search ? "Try a different search." : undefined}
            />
          ) : (
            <Virtuoso
              className="min-h-0 flex-1"
              data={filteredRepos}
              computeItemKey={(_, repo) => repo.fullName}
              itemContent={(_, repo) => <RepoRow repo={repo} onClone={setCloningRepo} />}
              endReached={() => void loadMore()}
              components={{
                Header: () => <div className="h-4" />,
                Footer: () =>
                  loading ? (
                    <div className="flex items-center justify-center py-4">
                      <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
                    </div>
                  ) : (
                    <div className="h-6" />
                  ),
              }}
            />
          )}
        </>
      )}

      {cloningRepo && (
        <MobileGitHubCloneSheet repo={cloningRepo} onClose={() => setCloningRepo(null)} />
      )}
    </div>
  );
}
