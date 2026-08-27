/**
 * RepoSelector — reusable GitHub repo picker.
 *
 * Renders the authenticated user's GitHub repo list (via
 * `useGitRepos()` -> `gitService.listRepos`) with a search box and inline
 * "credential missing" reconnect UI. Calls `onSelect` with the chosen repo.
 *
 * Extracted from `OnboardingFlow/steps/GitHubConnectStep.tsx` so the
 * ProjectPicker's "Clone repo" affordance can render the same picker
 * inside a modal without duplicating the repo-list / search / reconnect
 * plumbing.
 */
import { useEffect, useMemo, useState } from "react";
import type { FormEvent } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { Github, Lock } from "lucide-react";
import { cn } from "@/lib/utils";
import { supabase } from "@/lib/supabase";
import { useGitRepos } from "@/hooks/useOnboardingQueries";
import { trackEvent } from "@/lib/analytics";
import { gitService } from "@/services/controlPlane/git";
import type { GitRepo } from "@/services/controlPlane/git";

export function isMissingGitCredentialError(error: unknown): boolean {
  if (error instanceof ConnectError && error.code === Code.FailedPrecondition) {
    return true;
  }
  if (error instanceof Error && error.message.includes("no git credential found")) {
    return true;
  }
  if (typeof error === "string" && error.includes("no git credential found")) {
    return true;
  }
  return false;
}

function formatUpdated(updatedAt: string): string {
  if (!updatedAt) return "";
  const d = new Date(updatedAt);
  if (isNaN(d.getTime())) return "";
  const diffMs = Date.now() - d.getTime();
  const diffDays = Math.floor(diffMs / (1000 * 60 * 60 * 24));
  if (diffDays === 0) return "today";
  if (diffDays === 1) return "yesterday";
  if (diffDays < 30) return `${diffDays}d ago`;
  if (diffDays < 365) return `${Math.floor(diffDays / 30)}mo ago`;
  return `${Math.floor(diffDays / 365)}y ago`;
}

function parseGitHubRepoUrl(value: string): Pick<GitRepo, "fullName" | "cloneUrl"> | null {
  const trimmed = value.trim();
  const match = trimmed.match(
    /^(?:https?:\/\/github\.com\/|git@github\.com:)([^/\s]+)\/([^/\s]+?)(?:\.git)?\/?$/i,
  );
  if (!match) return null;

  const owner = match[1];
  const repo = match[2];
  return {
    fullName: `${owner}/${repo}`,
    cloneUrl: `https://github.com/${owner}/${repo}.git`,
  };
}

interface RepoSelectorProps {
  /** Invoked when the user confirms a repo selection. */
  onSelect: (repo: GitRepo) => void;
  /** Optional return-path passed to the OAuth handler when the user
   *  clicks "Reconnect GitHub". Defaults to the current location. */
  oauthReturnTo?: string;
  /** Optional analytics tag to differentiate where the picker was opened. */
  analyticsPhase?: string;
}

export function RepoSelector({ onSelect, oauthReturnTo, analyticsPhase }: RepoSelectorProps) {
  const {
    data: reposData,
    isLoading: reposLoading,
    isError: reposIsError,
    error: reposQueryError,
    refetch: fetchRepos,
  } = useGitRepos();
  const repos = reposData?.repos ?? [];
  const reposError = reposQueryError instanceof Error ? reposQueryError.message : "";
  const reposCredentialMissing = isMissingGitCredentialError(reposQueryError);

  const [search, setSearch] = useState("");
  const [manualUrl, setManualUrl] = useState("");
  const [manualBranch, setManualBranch] = useState("main");
  const [manualError, setManualError] = useState("");
  const [connecting, setConnecting] = useState(false);
  const [error, setError] = useState("");

  // Auto-load on mount.
  useEffect(() => {
    if (repos.length === 0 && !reposLoading && !reposIsError) {
      void fetchRepos();
    }
  }, [repos.length, reposLoading, reposIsError, fetchRepos]);

  useEffect(() => {
    if (reposCredentialMissing) {
      trackEvent("github_credential_missing_shown", { phase: analyticsPhase ?? "repo_selector" });
    }
  }, [reposCredentialMissing, analyticsPhase]);

  const filteredRepos = useMemo(() => {
    if (!search.trim()) return repos;
    const q = search.toLowerCase();
    return repos.filter(
      (r) =>
        r.fullName.toLowerCase().includes(q) ||
        r.description?.toLowerCase().includes(q) ||
        r.language?.toLowerCase().includes(q),
    );
  }, [repos, search]);

  const handleConnect = async () => {
    setConnecting(true);
    setError("");
    try {
      const oauthURL = gitService.getOAuthURL();
      if (!oauthURL) {
        throw new Error("Control plane URL not configured");
      }
      const { data: { session } } = await supabase.auth.getSession();
      if (!session?.access_token) {
        throw new Error("Not signed in");
      }
      const returnTo = oauthReturnTo ?? `${window.location.pathname}${window.location.search}`;
      const params = new URLSearchParams({
        token: session.access_token,
        returnTo,
      });
      trackEvent("github_reconnect_clicked", { phase: analyticsPhase ?? "repo_selector" });
      window.location.href = `${oauthURL}?${params.toString()}`;
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to connect GitHub");
      setConnecting(false);
    }
  };

  const handleManualSubmit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    setManualError("");

    const parsed = parseGitHubRepoUrl(manualUrl);
    if (!parsed) {
      setManualError("Enter a GitHub URL like https://github.com/org/repo");
      return;
    }

    onSelect({
      ...parsed,
      defaultBranch: manualBranch.trim() || "main",
      description: "",
      private: false,
      language: "",
      updatedAt: "",
    });
  };

  return (
    <div className="space-y-3">
      {reposCredentialMissing && (
        <div className="rounded-lg border border-destructive/30 bg-destructive/10 p-4 space-y-3">
          <div className="space-y-1">
            <p className="text-sm font-semibold text-foreground">
              GitHub credential missing
            </p>
            <p className="text-xs text-muted-foreground">
              Reliant needs to connect to GitHub before it can list your repositories.
            </p>
          </div>
          <button
            type="button"
            onClick={handleConnect}
            disabled={connecting}
            className={cn(
              "flex items-center justify-center gap-2 w-full py-2.5 rounded-lg text-sm font-semibold transition-colors",
              connecting
                ? "bg-muted text-muted-foreground cursor-not-allowed"
                : "bg-primary text-primary-foreground hover:bg-primary/90",
            )}
          >
            <Github className="w-4 h-4" />
            {connecting ? "Connecting..." : "Reconnect GitHub"}
          </button>
        </div>
      )}

      {error && <p className="text-xs text-destructive">{error}</p>}

      <form
        onSubmit={handleManualSubmit}
        className="rounded-lg border border-border/40 bg-muted/20 p-3 space-y-2"
      >
        <div className="grid gap-2 sm:grid-cols-[minmax(0,1fr)_120px_auto]">
          <input
            type="text"
            placeholder="https://github.com/org/repo"
            value={manualUrl}
            onChange={(e) => {
              setManualUrl(e.target.value);
              setManualError("");
            }}
            className={cn(
              "min-w-0 rounded-lg border border-border/40 bg-background px-3 py-2 text-sm text-foreground",
              "placeholder:text-muted-foreground/50 focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/50",
            )}
          />
          <input
            type="text"
            placeholder="main"
            value={manualBranch}
            onChange={(e) => setManualBranch(e.target.value)}
            className={cn(
              "min-w-0 rounded-lg border border-border/40 bg-background px-3 py-2 text-sm text-foreground",
              "placeholder:text-muted-foreground/50 focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/50",
            )}
            aria-label="Branch"
          />
          <button
            type="submit"
            className={cn(
              "rounded-lg bg-primary px-4 py-2 text-sm font-semibold text-primary-foreground transition-colors",
              "hover:bg-primary/90",
            )}
          >
            Use URL
          </button>
        </div>
        {manualError ? (
          <p className="text-xs text-destructive">{manualError}</p>
        ) : (
          <p className="text-xs text-muted-foreground">
            Paste any GitHub repo URL your connected credential can access.
          </p>
        )}
      </form>

      {/* Search */}
      <div className="relative">
        <svg
          className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground/50"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={2}
            d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"
          />
        </svg>
        <input
          type="text"
          placeholder="Search repositories..."
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className={cn(
            "w-full pl-9 pr-3 py-2.5 rounded-lg text-sm transition-colors",
            "bg-background border border-border/40 text-foreground placeholder:text-muted-foreground/50",
            "focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/50",
          )}
        />
      </div>

      {/* Repo list */}
      <div className="max-h-64 overflow-y-auto rounded-lg border border-border/40">
        {reposError && !reposCredentialMissing && (
          <div className="flex items-center gap-2 border-b border-destructive/20 bg-destructive/5 px-3 py-2 text-xs text-destructive">
            {reposError}
          </div>
        )}

        {!reposLoading && filteredRepos.length === 0 && (
          <div className="px-4 py-8 text-center text-sm text-muted-foreground">
            {search ? "No repos match your search." : "No repositories found."}
          </div>
        )}

        {filteredRepos.map((repo) => (
          <button
            key={repo.fullName}
            type="button"
            onClick={() => onSelect(repo)}
            className={cn(
              "flex w-full items-start gap-3 border-b border-border/30 px-3 py-2.5 text-left transition-colors last:border-b-0",
              "hover:bg-muted/50",
            )}
          >
            <div className="min-w-0 flex-1">
              <div className="flex items-center gap-2">
                <span className="truncate text-sm font-medium text-foreground">
                  {repo.fullName}
                </span>
                {repo.private && (
                  <span className="inline-flex items-center gap-0.5 rounded px-1.5 py-0.5 text-2xs font-medium bg-muted text-muted-foreground">
                    <Lock className="w-2.5 h-2.5" />
                    Private
                  </span>
                )}
              </div>
              {repo.description && (
                <p className="mt-0.5 truncate text-xs text-muted-foreground">
                  {repo.description}
                </p>
              )}
              <div className="mt-1 flex items-center gap-3 text-xs text-muted-foreground/70">
                {repo.language && <span>{repo.language}</span>}
                {repo.defaultBranch && (
                  <span className="flex items-center gap-0.5">
                    <svg className="h-3 w-3" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                      <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M7 7h.01M7 3h5c.512 0 1.024.195 1.414.586l7 7a2 2 0 010 2.828l-7 7a2 2 0 01-2.828 0l-7-7A1.994 1.994 0 013 12V7a4 4 0 014-4z" />
                    </svg>
                    {repo.defaultBranch}
                  </span>
                )}
                {repo.updatedAt && (
                  <span>Updated {formatUpdated(repo.updatedAt)}</span>
                )}
              </div>
            </div>
          </button>
        ))}

        {reposLoading && (
          <div className="flex items-center justify-center py-4 text-sm text-muted-foreground">
            <svg className="mr-2 h-4 w-4 animate-spin" viewBox="0 0 24 24" fill="none">
              <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
              <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
            </svg>
            Loading repos...
          </div>
        )}
      </div>
    </div>
  );
}
