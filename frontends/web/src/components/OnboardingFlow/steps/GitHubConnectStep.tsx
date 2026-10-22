import { useState, useEffect } from "react";
import { useNavigate } from "@tanstack/react-router";
import { Code, ConnectError } from "@connectrpc/connect";
import { Github, Lock } from "lucide-react";
import { cn } from "@/lib/utils";
import { useProjectStore } from "@/store/projectStore";
import type { Project } from "@/store/projectStore";
import { supabase } from "@/lib/supabase";
import { useEventBus } from "@/lib/event-context";
import { useCompleteOnboarding } from "@/hooks/useOnboardingQueries";
import { trackEvent } from "@/lib/analytics";
import { gitService } from "@/services/controlPlane/git";
import { finalizeOnboardingSideEffects } from "../useOnboardingComplete";
import { leaveOnboarding } from "../leaveOnboarding";
import { markOnboardingFinalized } from "../analytics";
import { ProvisioningGate } from "../ProvisioningGate";
import { useCommitLaunchPlan } from "../useCommitLaunchPlan";
import type { StepProps } from "../types";
import type { GitRepo } from "@/services/controlPlane/git";
import { RepoSelector } from "@/components/Projects/RepoSelector";
import { cloudPathForRepo, repoNameFromUrl } from "@/lib/cloudProjectPath";
import { addRepoProject } from "../addRepoProject";

// Entry to this step is gated by an existing GitHub credential (the
// ProjectChoiceStep "Connect GitHub" button performs the OAuth handshake
// before advancing). If the credential disappears (token revoked, server
// deletes), the picker phase surfaces a Reconnect button — no separate
// "connect" landing page is needed.
type Phase = "picker" | "confirm";

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

export function GitHubConnectStep({ plan, updatePlan, onBack }: StepProps) {
  const navigate = useNavigate();
  const loadProjects = useProjectStore((state) => state.loadProjects);

  const eventBus = useEventBus();
  const completeOnboardingMutation = useCompleteOnboarding();

  const [phase, setPhase] = useState<Phase>("picker");
  const [connecting, setConnecting] = useState(false);
  const [error, setError] = useState("");
  // The commit point. This step reaches it having ALREADY needed a machine —
  // it cloned into one — so its commit is usually a no-op that observes the
  // running daemon. It still goes through the same function, because "usually"
  // is not "always" and a second path to provisioning is how the two drift.
  const { commit, runCommit, retry } = useCommitLaunchPlan(updatePlan);

  const [selectedRepo, setSelectedRepo] = useState<GitRepo | null>(null);

  // Confirmation state
  const [branch, setBranch] = useState("");

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

  const handleSelectRepo = (repo: GitRepo) => {
    setSelectedRepo(repo);
    setBranch(repo.defaultBranch || "main");
    setPhase("confirm");
  };

  const handleConfirm = async () => {
    if (!selectedRepo) return;

    const selectedBranch = branch.trim() || selectedRepo.defaultBranch || "main";
    const projectPath = cloudPathForRepo(selectedRepo);
    const projectName = repoNameFromUrl(selectedRepo.cloneUrl) || selectedRepo.fullName;
    setError("");
    setCloning(true);

    try {
      // ONE server call adds the project: it starts the clone, creates the
      // project row, and records where the checkout will live. This used to
      // be four calls from here — CloneRepo, CreateProject,
      // MarkProjectInstalled and an already-exists recovery — each able to
      // fail on its own and leave a project with no checkout or a checkout
      // with no project. See addRepoProject.ts.
      const added = await addRepoProject({
        cloneUrl: selectedRepo.cloneUrl,
        branch: selectedBranch,
        path: projectPath,
        name: projectName,
      });
      const clonedPath = added.clonedPath;

      // Say what actually happened. A queued clone means the checkout does
      // NOT exist yet, and "Opening project" there is the false success this
      // flow exists to avoid.
      eventBus.emit("toast:show", {
        message: added.queued
          ? `Queued — ${projectName} will clone when ${added.machineName || "your machine"} is ready`
          : `Opening project "${projectName}"...`,
        variant: "info",
      });

      // Select the new project so the app opens on it. The store is the
      // source of the full Project shape; the RPC returns a narrower one.
      await loadProjects();
      const opened = added.projectId
        ? useProjectStore.getState().projects.find((p) => p.id === added.projectId)
        : findProjectByPath(useProjectStore.getState().projects, clonedPath);
      if (opened) {
        await useProjectStore.getState().selectProject(opened);
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
      markOnboardingFinalized(plan, "github");
      // This path always shows the gate below, so the exit is always the
      // gate's job.
      await finalizeOnboardingSideEffects();
      await runCommit(plan);
    } catch (err) {
      if (isMissingGitCredentialError(err)) {
        setConfirmCredentialMissing(true);
        setError("");
      } else {
        setConfirmCredentialMissing(false);
        setError(err instanceof Error ? err.message : "Failed to clone repository");
      }
    } finally {
      setCloning(false);
    }
  };

  // Local pending state: the clone now runs inside addRepoProject rather than
  // a React Query mutation, so there is no mutation.isPending to read.
  const [cloning, setCloning] = useState(false);

  useEffect(() => {
    if (phase === "confirm" && confirmCredentialMissing) {
      trackEvent("github_credential_missing_shown", { phase: "confirm" });
    }
  }, [phase, confirmCredentialMissing]);

  const handleReconnect = () => {
    trackEvent("github_reconnect_clicked", { phase: "confirm" });
    setConfirmCredentialMissing(false);
    void handleConnect();
  };

  // -- Phase: Daemon connecting gate --
  // Rendered after a successful clone + completeOnboarding while we wait for
  // the daemon to come ACTIVE. Takes precedence over the picker / confirm UI.
  if (commit) {
    return (
      <div className="space-y-6">
        <ProvisioningGate
          commit={commit}
          onContinue={() =>
            void leaveOnboarding("completed_cloud_gate_continue", navigate)
          }
          onRetry={() => void retry(plan)}
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

        <RepoSelector onSelect={handleSelectRepo} analyticsPhase="picker" />

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
          Reliant will copy this repository onto your machine to work on it.
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
                <span className="inline-flex items-center gap-0.5 rounded px-1.5 py-0.5 text-2xs font-medium bg-muted text-muted-foreground">
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
                onClick={() => handleReconnect()}
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
            error && <p className="text-xs text-destructive-ink">{error}</p>
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