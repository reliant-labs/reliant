import { useState, useEffect, useMemo } from "react";
import { useNavigate } from "@tanstack/react-router";
import { Code, ConnectError } from "@connectrpc/connect";
import { Github, Lock } from "lucide-react";
import { cn } from "@/lib/utils";
import { useProjectStore } from "@/store/projectStore";
import type { Project } from "@/store/projectStore";
import { supabase } from "@/lib/supabase";
import { useEventBus } from "@/lib/event-context";
import { useGitRepos, useCloneRepo, useCompleteOnboarding, useCreateDaemon } from "@/hooks/useOnboardingQueries";
import { trackEvent } from "@/lib/analytics";
import { gitService } from "@/services/controlPlane/git";
import { finalizeOnboardingSideEffects } from "../useOnboardingComplete";
import { DaemonConnectingGate } from "../DaemonConnectingGate";
import type { StepProps } from "../types";
import {
  DAEMON_STATUS_ACTIVE,
  listDaemons,
} from "@/services/controlPlane/daemon";
import type { GitRepo } from "@/services/controlPlane/git";

// Must match the name + type + size used by ComputeStep's createDaemon call so
// that the control-plane's CreateDaemon idempotency path (refreshManagedDaemon)
// updates the existing daemon's git_repo column instead of erroring out.
const ONBOARDING_DAEMON_NAME = "onboarding-daemon";
const DAEMON_TYPE_MANAGED = 1;
const DAEMON_SIZE_SMALL = 1;

// Entry to this step is gated by an existing GitHub credential (the
// ProjectChoiceStep "Connect GitHub" button performs the OAuth handshake
// before advancing). If the credential disappears (token revoked, server
// deletes), the picker phase surfaces a Reconnect button — no separate
// "connect" landing page is needed.
type Phase = "picker" | "confirm";

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

function isMissingGitCredentialError(error: unknown): boolean {
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

export function GitHubConnectStep({ plan, updatePlan, onBack }: StepProps) {
  const navigate = useNavigate();
  const createProject = useProjectStore((state) => state.createProject);
  const loadProjects = useProjectStore((state) => state.loadProjects);
  const projects = useProjectStore((state) => state.projects);

  const eventBus = useEventBus();
  const completeOnboardingMutation = useCompleteOnboarding();

  const [phase, setPhase] = useState<Phase>("picker");
  const [connecting, setConnecting] = useState(false);
  const [error, setError] = useState("");
  // After clone + completeOnboarding succeed we show the daemon gate before
  // navigating to the chat view. The clone may have queued via JetStream
  // because the daemon was still provisioning — the gate lets the user wait
  // (or skip) instead of landing on a silent "No daemon connected" banner.
  const [showDaemonGate, setShowDaemonGate] = useState(false);
  const [gateDaemonRef, setGateDaemonRef] = useState<string | undefined>(undefined);

  // Repo picker state (via React Query)
  const {
    data: reposData,
    isLoading: reposLoading,
    isError: reposIsError,
    error: reposQueryError,
    refetch: fetchRepos,
  } = useGitRepos();
  const repos = reposData?.repos ?? [];
  const reposError = reposQueryError instanceof Error ? reposQueryError.message : "";

  const [search, setSearch] = useState("");
  const [selectedRepo, setSelectedRepo] = useState<GitRepo | null>(null);

  // Confirmation state
  const [branch, setBranch] = useState("");

  // Clone mutation (via React Query)
  const cloneRepoMutation = useCloneRepo();
  // Daemon refresh mutation — re-running CreateDaemon with the picked repo
  // hits the server-side refresh path so the daemon row stores git_repo and
  // the workspace command carries it to the controller.
  const createDaemonMutation = useCreateDaemon();

  const [confirmCredentialMissing, setConfirmCredentialMissing] = useState(false);

  const handleConnect = async () => {
    setConnecting(true);
    setError("");
    try {
      // Always go through the control-plane custom OAuth flow. Supabase's
      // GitHub provider is sign-in only (0 scopes); the long-lived repo-scoped
      // token comes from /auth/github/authorize, which writes it to
      // git_credentials.
      const oauthURL = gitService.getOAuthURL();
      if (!oauthURL) {
        throw new Error("Control plane URL not configured");
      }
      const { data: { session } } = await supabase.auth.getSession();
      if (!session?.access_token) {
        throw new Error("Not signed in");
      }
      // Preserve the onboarding step/plan params so the callback lands the
      // user back on this step instead of restarting onboarding.
      const returnTo = `${window.location.pathname}${window.location.search}`;
      const params = new URLSearchParams({
        token: session.access_token,
        returnTo,
      });
      window.location.href = `${oauthURL}?${params.toString()}`;
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to connect GitHub");
      setConnecting(false);
    }
  };

  // Load repos when entering picker phase
  useEffect(() => {
    if (phase === "picker" && repos.length === 0 && !reposLoading && !reposIsError) {
      fetchRepos();
    }
  }, [phase, repos.length, reposLoading, reposIsError, fetchRepos]);

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
    setError("");

    try {
      const { daemons } = await listDaemons();
      // Prefer an active daemon; fall back to the first daemon if it's still
      // provisioning (CloneRepo queues via JetStream when offline). The UUID
      // is what the server actually keys lookups by — names are display-only.
      const daemon =
        daemons.find((d) => d.status === DAEMON_STATUS_ACTIVE) ?? daemons[0];
      if (!daemon) {
        throw new Error("Hosted workspace is still starting. Try again in a moment.");
      }

      // Persist the picked repo on the daemon row before cloning. ComputeStep
      // created the daemon with an empty git_repo (the user hadn't picked one
      // yet); re-running CreateDaemon with the same name hits the server-side
      // refresh path, which updates daemons.git_repo and republishes the
      // workspace command with GitRepo populated so the controller / init
      // container see it. Failures here are non-fatal — the clone below still
      // populates the working tree via the daemon command path.
      try {
        await createDaemonMutation.mutateAsync({
          name: ONBOARDING_DAEMON_NAME,
          daemonType: DAEMON_TYPE_MANAGED,
          size: DAEMON_SIZE_SMALL,
          gitRepo: selectedRepo.cloneUrl,
          gitBranch: selectedBranch,
        });
      } catch (refreshErr) {
        console.warn("CreateDaemon refresh with git_repo failed (continuing with clone):", refreshErr);
      }

      // CloneRepo queues via JetStream when the daemon is offline, so a
      // success here is genuine — the message was either delivered or queued
      // for replay. A failure means the request didn't reach the gateway at
      // all (auth, network, server-side validation). That's user-visible,
      // not a silent retry, so we let it propagate to the outer catch which
      // surfaces it via the `error` state below.
      const cloneResult = await cloneRepoMutation.mutateAsync({
        daemonId: daemon.id,
        gitRepo: selectedRepo.cloneUrl,
        gitBranch: selectedBranch,
        path: projectPath,
      });
      const clonedPath = cloneResult.clonedPath;

      eventBus.emit("toast:show", {
        message: `Opening project "${projectName}"...`,
        variant: "info",
      });

      try {
        const createdProject = await createProject({
          name: projectName,
          path: clonedPath,
          description: "",
          is_git_repo: true,
          default_branch: selectedBranch,
        });
        await loadProjects();
        await useProjectStore.getState().selectProject(createdProject);
      } catch (projectError) {
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

      await completeOnboardingMutation.mutateAsync({
        compute: plan.compute,
        modelProvider: plan.modelProvider,
      });
      trackEvent("onboarding_completed", {
        provider: plan.modelProvider ?? "unknown",
        compute: plan.compute ?? "unknown",
        project_source: "github",
      });
      await finalizeOnboardingSideEffects(plan.modelProvider);
      // Show the daemon gate before navigating — the daemon may still be
      // provisioning. We hand the gate the UUID we already used for the
      // CloneRepo call so its polling looks up the same row.
      setGateDaemonRef(daemon.id);
      setShowDaemonGate(true);
    } catch (err) {
      if (isMissingGitCredentialError(err)) {
        setConfirmCredentialMissing(true);
        setError("");
      } else {
        setConfirmCredentialMissing(false);
        setError(err instanceof Error ? err.message : "Failed to clone repository");
      }
    }
  };

  const cloning = cloneRepoMutation.isPending;

  const reposCredentialMissing = isMissingGitCredentialError(reposQueryError);

  useEffect(() => {
    if (phase === "picker" && reposCredentialMissing) {
      trackEvent("github_credential_missing_shown", { phase: "picker" });
    }
  }, [phase, reposCredentialMissing]);

  useEffect(() => {
    if (phase === "confirm" && confirmCredentialMissing) {
      trackEvent("github_credential_missing_shown", { phase: "confirm" });
    }
  }, [phase, confirmCredentialMissing]);

  const handleReconnect = (phaseLabel: "picker" | "confirm") => {
    trackEvent("github_reconnect_clicked", { phase: phaseLabel });
    if (phaseLabel === "confirm") {
      setConfirmCredentialMissing(false);
    }
    void handleConnect();
  };

  // -- Phase: Daemon connecting gate --
  // Rendered after a successful clone + completeOnboarding while we wait for
  // the daemon to come ACTIVE. Takes precedence over the picker / confirm UI.
  if (showDaemonGate) {
    return (
      <div className="space-y-6">
        <DaemonConnectingGate
          daemonRef={gateDaemonRef}
          onContinue={() =>
            navigate({ to: "/", search: { step: undefined, plan: undefined } })
          }
        />
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
                onClick={() => handleReconnect("picker")}
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

          {confirmCredentialMissing ? (
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
                onClick={() => handleReconnect("confirm")}
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
          ) : (
            error && <p className="text-xs text-destructive">{error}</p>
          )}

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