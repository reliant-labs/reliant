import { useState, useEffect, useCallback, useMemo } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { Github, Lock } from "lucide-react";
import { cn } from "@/lib/utils";
import { useProjectStore } from "@/store/projectStore";
import type { Project } from "@/store/projectStore";
import { useAuthStore } from "@/store/authStore";
import { toast } from "@/lib/toast-manager";
import type { StepProps } from "../types";
import {
  cloneRepo,
  getActiveDaemonName,
  listDaemons,
  listGitRepos,
} from "../api";
import type { GitRepo } from "../api";

type Phase = "connect" | "picker" | "confirm";

const PER_PAGE = 20;
const CLOUD_PROJECT_ROOT = "/home/workspace/projects";

function slugify(value: string): string {
  return value
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 48) || "github-repo";
}

function repoNameFromUrl(url: string): string {
  const withoutGitSuffix = url.replace(/\.git$/, "");
  return withoutGitSuffix.split("/").filter(Boolean).pop() || "github-repo";
}

function cloudPathForRepo(repo: GitRepo): string {
  return `${CLOUD_PROJECT_ROOT}/${slugify(repoNameFromUrl(repo.cloneUrl) || repo.fullName)}`;
}

function isAlreadyExistsError(error: unknown): boolean {
  return (
    (error instanceof ConnectError && error.code === Code.AlreadyExists) ||
    (error instanceof Error &&
      (error.message.includes("already exists") || error.message.includes("409")))
  );
}

function findProjectByPath(projects: Project[], path: string): Project | undefined {
  return projects.find((project) => project.path === path);
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

export function GitHubConnectStep({ plan, updatePlan, onNext, onBack }: StepProps) {
  const createProject = useProjectStore((state) => state.createProject);
  const loadProjects = useProjectStore((state) => state.loadProjects);
  const projects = useProjectStore((state) => state.projects);

  const user = useAuthStore((state) => state.user);
  const linkGithubAccount = useAuthStore((state) => state.linkGithubAccount);

  const hasGithub = useMemo(
    () => user?.identities?.some((i) => i.provider === "github") ?? false,
    [user?.identities],
  );

  const [phase, setPhase] = useState<Phase>(() => (hasGithub ? "picker" : "connect"));
  const [connecting, setConnecting] = useState(false);
  const [error, setError] = useState("");

  // Repo picker state
  const [repos, setRepos] = useState<GitRepo[]>([]);
  const [reposLoading, setReposLoading] = useState(false);
  const [reposError, setReposError] = useState("");
  const [page, setPage] = useState(1);
  const [hasMore, setHasMore] = useState(false);
  const [search, setSearch] = useState("");
  const [selectedRepo, setSelectedRepo] = useState<GitRepo | null>(null);

  // Confirmation state
  const [branch, setBranch] = useState("");
  const [cloning, setCloning] = useState(false);

  // When GitHub identity appears (after OAuth callback), transition to picker
  useEffect(() => {
    if (hasGithub && phase === "connect") {
      setPhase("picker");
    }
  }, [hasGithub, phase]);

  const signInWithGithub = useAuthStore((state) => state.signInWithGithub);

  const handleConnect = async () => {
    setConnecting(true);
    setError("");
    try {
      await linkGithubAccount();
    } catch (err) {
      const message = err instanceof Error ? err.message : "Failed to connect GitHub";
      if (
        message.includes("identity already exists") ||
        message.includes("already linked")
      ) {
        setError(
          "This GitHub account is already linked to another Reliant account. Please sign out and sign in with GitHub instead.",
        );
      } else {
        // linkIdentity can fail for anonymous users — fall back to full OAuth sign-in
        try {
          await signInWithGithub();
        } catch (fallbackErr) {
          setError(
            fallbackErr instanceof Error ? fallbackErr.message : "Failed to connect GitHub",
          );
        }
      }
    } finally {
      setConnecting(false);
    }
  };

  const fetchRepos = useCallback(async (pageNum: number, append: boolean) => {
    setReposLoading(true);
    setReposError("");
    try {
      const res = await listGitRepos(pageNum, PER_PAGE, "updated");
      setRepos(prev => append ? [...prev, ...res.repos] : res.repos);
      setHasMore(res.hasMore);
      setPage(pageNum);
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to load repos";
      setReposError(msg);
    } finally {
      setReposLoading(false);
    }
  }, []);

  // Load repos when entering picker phase
  useEffect(() => {
    if (phase === "picker" && repos.length === 0 && !reposLoading) {
      fetchRepos(1, false);
    }
  }, [phase, repos.length, reposLoading, fetchRepos]);

  // Client-side search filter
  const filteredRepos = useMemo(() => {
    if (!search.trim()) return repos;
    const q = search.toLowerCase();
    return repos.filter(
      r =>
        r.fullName.toLowerCase().includes(q) ||
        r.description?.toLowerCase().includes(q) ||
        r.language?.toLowerCase().includes(q),
    );
  }, [repos, search]);

  const handleSelectRepo = (repo: GitRepo) => {
    if (selectedRepo?.fullName === repo.fullName) {
      setSelectedRepo(null);
      setBranch("");
    } else {
      setSelectedRepo(repo);
      setBranch(repo.defaultBranch || "main");
      setPhase("confirm");
    }
  };

  const handleConfirm = async () => {
    if (!selectedRepo) return;

    const selectedBranch = branch.trim() || selectedRepo.defaultBranch || "main";
    const projectPath = cloudPathForRepo(selectedRepo);
    const projectName = repoNameFromUrl(selectedRepo.cloneUrl) || selectedRepo.fullName;
    setCloning(true);
    setError("");

    try {
      const { daemons } = await listDaemons();
      const daemonName = getActiveDaemonName(daemons);
      if (!daemonName && plan.daemonProvisioning) {
        updatePlan({
          repo: {
            provider: "github",
            url: selectedRepo.cloneUrl,
            branch: selectedBranch,
          },
          localPath: projectPath,
          projectName,
        });
        onNext();
        return;
      }
      if (!daemonName) {
        throw new Error("Hosted workspace is still starting. Try again in a moment.");
      }

      const result = await cloneRepo({
        daemonName,
        gitRepo: selectedRepo.cloneUrl,
        gitBranch: selectedBranch,
        path: projectPath,
      });
      const clonedPath = result.clonedPath;
      const loadingToast = toast.loading(`Opening project "${projectName}"...`);

      try {
        const createdProject = await createProject({
          name: projectName,
          path: clonedPath,
          description: "",
          is_git_repo: true,
          default_branch: selectedBranch,
        });
        toast.dismiss(loadingToast);
        await loadProjects();
        await useProjectStore.getState().selectProject(createdProject);
      } catch (projectError) {
        toast.dismiss(loadingToast);
        if (!isAlreadyExistsError(projectError)) {
          throw projectError;
        }

        await loadProjects();
        const existingProject =
          findProjectByPath(projects, clonedPath) ||
          findProjectByPath(useProjectStore.getState().projects, clonedPath);
        if (existingProject) {
          await useProjectStore.getState().selectProject(existingProject);
        }
      }

      updatePlan({
        repo: {
          provider: "github",
          url: selectedRepo.cloneUrl,
          branch: selectedBranch,
        },
        localPath: clonedPath,
        projectName,
      });
      onNext();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to clone repository");
    } finally {
      setCloning(false);
    }
  };

  // -- Phase: Connect GitHub via OAuth --

  if (phase === "connect") {
    return (
      <div className="space-y-6">
        <div className="text-center space-y-2">
          <h2 className="text-xl font-semibold text-foreground">
            Connect your GitHub repos
          </h2>
          <p className="text-sm text-muted-foreground">
            Sign in with GitHub to give Reliant access to your repositories.
          </p>
        </div>

        <div className="space-y-4">
          {error && (
            <p className="text-xs text-destructive">{error}</p>
          )}

          <button
            onClick={handleConnect}
            disabled={connecting}
            className={cn(
              "flex items-center justify-center gap-2 w-full py-3 rounded-lg text-sm font-semibold transition-colors",
              connecting
                ? "bg-muted text-muted-foreground cursor-not-allowed"
                : "bg-primary text-primary-foreground hover:bg-primary/90",
            )}
          >
            <Github className="w-4 h-4" />
            {connecting ? "Connecting..." : "Connect with GitHub"}
          </button>
        </div>

        <button
          onClick={onBack}
          className="w-full text-center text-xs text-muted-foreground hover:text-foreground transition-colors py-1"
        >
          Back
        </button>
      </div>
    );
  }

  // -- Phase: Repo picker --

  if (phase === "picker") {
    return (
      <div className="space-y-6">
        <div className="text-center space-y-2">
          <h2 className="text-xl font-semibold text-foreground">
            Choose a repository
          </h2>
          <p className="text-sm text-muted-foreground">
            Select the repo you want to work on.
          </p>
        </div>

        <div className="space-y-3">
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
            {reposError && (
              <div className="flex items-center gap-2 border-b border-destructive/20 bg-destructive/5 px-3 py-2 text-xs text-destructive">
                {reposError}
              </div>
            )}

            {!reposLoading && filteredRepos.length === 0 && (
              <div className="px-4 py-8 text-center text-sm text-muted-foreground">
                {search ? "No repos match your search." : "No repositories found."}
              </div>
            )}

            {filteredRepos.map((repo) => {
              const isSelected = selectedRepo?.fullName === repo.fullName;
              return (
                <button
                  key={repo.fullName}
                  type="button"
                  onClick={() => handleSelectRepo(repo)}
                  className={cn(
                    "flex w-full items-start gap-3 border-b border-border/30 px-3 py-2.5 text-left transition-colors last:border-b-0",
                    isSelected
                      ? "bg-primary/10 ring-1 ring-inset ring-primary/30"
                      : "hover:bg-muted/50",
                  )}
                >
                  {/* Checkbox */}
                  <div
                    className={cn(
                      "mt-0.5 flex h-4 w-4 flex-shrink-0 items-center justify-center rounded border",
                      isSelected ? "border-primary bg-primary" : "border-border",
                    )}
                  >
                    {isSelected && (
                      <svg className="h-3 w-3 text-primary-foreground" fill="none" stroke="currentColor" viewBox="0 0 24 24">
                        <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={3} d="M5 13l4 4L19 7" />
                      </svg>
                    )}
                  </div>

                  {/* Repo info */}
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="truncate text-sm font-medium text-foreground">
                        {repo.fullName}
                      </span>
                      {repo.private && (
                        <span className="inline-flex items-center gap-0.5 rounded px-1.5 py-0.5 text-[10px] font-medium bg-muted text-muted-foreground">
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
              );
            })}

            {/* Loading spinner */}
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

          {/* Load more */}
          {hasMore && !reposLoading && (
            <button
              type="button"
              onClick={() => fetchRepos(page + 1, true)}
              className="w-full rounded-lg border border-border/40 py-2 text-center text-sm font-medium text-muted-foreground hover:bg-muted/50 transition-colors"
            >
              Load more
            </button>
          )}
        </div>

        <button
          onClick={onBack}
          className="w-full text-center text-xs text-muted-foreground hover:text-foreground transition-colors py-1"
        >
          Back
        </button>
      </div>
    );
  }

  // -- Phase: Confirmation --

  return (
    <div className="space-y-6">
      <div className="text-center space-y-2">
        <h2 className="text-xl font-semibold text-foreground">
          Confirm your selection
        </h2>
        <p className="text-sm text-muted-foreground">
          We'll clone this repo into your cloud workspace.
        </p>
      </div>

      {selectedRepo && (
        <div className="space-y-4">
          {/* Selected repo card */}
          <div className="rounded-lg border border-border/40 p-4 space-y-2">
            <div className="flex items-center gap-2">
              <span className="text-sm font-medium text-foreground">
                {selectedRepo.fullName}
              </span>
              {selectedRepo.private && (
                <span className="inline-flex items-center gap-0.5 rounded px-1.5 py-0.5 text-[10px] font-medium bg-muted text-muted-foreground">
                  <Lock className="w-2.5 h-2.5" />
                  Private
                </span>
              )}
            </div>
            {selectedRepo.description && (
              <p className="text-xs text-muted-foreground">{selectedRepo.description}</p>
            )}
            <button
              type="button"
              onClick={() => {
                setPhase("picker");
                setSelectedRepo(null);
                setBranch("");
              }}
              className="text-xs text-primary hover:text-primary/80 transition-colors"
            >
              Change repo
            </button>
          </div>

          {/* Branch input */}
          <div className="space-y-1.5">
            <label htmlFor="branch-input" className="block text-xs text-muted-foreground">
              Branch
            </label>
            <input
              id="branch-input"
              type="text"
              value={branch}
              onChange={(e) => setBranch(e.target.value)}
              placeholder={selectedRepo.defaultBranch || "main"}
              className={cn(
                "w-full px-3 py-2.5 rounded-lg text-sm transition-colors",
                "bg-background border border-border/40 text-foreground placeholder:text-muted-foreground/50",
                "focus:outline-none focus:ring-2 focus:ring-primary/30 focus:border-primary/50",
              )}
            />
          </div>

          {error && <p className="text-xs text-destructive">{error}</p>}

          <button
            onClick={handleConfirm}
            disabled={cloning}
            className={cn(
              "w-full py-3 rounded-lg text-sm font-semibold transition-colors",
              cloning
                ? "bg-muted text-muted-foreground cursor-not-allowed"
                : "bg-primary text-primary-foreground hover:bg-primary/90",
            )}
          >
            {cloning ? "Cloning repository..." : "Clone and continue"}
          </button>
        </div>
      )}

      <button
        onClick={() => setPhase("picker")}
        className="w-full text-center text-xs text-muted-foreground hover:text-foreground transition-colors py-1"
      >
        Back to repos
      </button>
    </div>
  );
}