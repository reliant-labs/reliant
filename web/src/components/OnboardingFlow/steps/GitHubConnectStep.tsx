import { useState, useEffect } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { Github, Lock } from "lucide-react";
import { cn } from "@/lib/utils";
import { useProjectStore } from "@/store/projectStore";
import type { Project } from "@/store/projectStore";
import { supabase } from "@/lib/supabase";
import { useEventBus } from "@/lib/event-context";
import { useCloneRepo, useCompleteOnboarding, useCreateDaemon } from "@/hooks/useOnboardingQueries";
import { trackEvent } from "@/lib/analytics";
import { gitService } from "@/services/controlPlane/git";
import {
  finalizeOnboardingSideEffects,
  navigateAfterOnboarding,
} from "../useOnboardingComplete";
import { markOnboardingFinalized } from "../analytics";
import { DaemonConnectingGate } from "../DaemonConnectingGate";
import type { StepProps } from "../types";
import {
  DAEMON_STATUS_ACTIVE,
  listDaemons,
} from "@/services/controlPlane/daemon";
import type { GitRepo } from "@/services/controlPlane/git";
import { RepoSelector } from "@/components/Projects/RepoSelector";
import { cloudPathForRepo, repoNameFromUrl } from "@/lib/cloudProjectPath";
import { projectGrpc } from "@/api/project-grpc";

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

export function GitHubConnectStep({ plan, updatePlan, onBack }: StepProps) {
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

      let installedProjectId: string | undefined;
      try {
        const createdProject = await createProject({
          name: projectName,
          path: clonedPath,
          description: "",
          is_git_repo: true,
          default_branch: selectedBranch,
        });
        installedProjectId = createdProject.id;
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
          installedProjectId = existingProject.id;
          await useProjectStore.getState().selectProject(existingProject);
        }
      }

      // Record the (project, daemon) install so the picker on later visits
      // can show this project as "available on this daemon". Failures are
      // non-fatal — the project is still usable; we just lose the indicator
      // until a future markProjectInstalled call (e.g. from the picker).
      if (installedProjectId) {
        try {
          await projectGrpc.markProjectInstalled(
            installedProjectId,
            daemon.id,
            clonedPath,
            selectedBranch,
          );
        } catch (markErr) {
          console.warn("markProjectInstalled failed (non-fatal):", markErr);
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
      markOnboardingFinalized(plan, "github");
      // This path always shows the gate below, so navigation is always the
      // gate's job — see FinalizeOptions.navigate.
      await finalizeOnboardingSideEffects(plan.modelProvider, { navigate: false });
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
  if (showDaemonGate) {
    return (
      <div className="space-y-6">
        <DaemonConnectingGate
          daemonRef={gateDaemonRef}
          // finalize ran with `navigate: false` so this gate could render, so
          // it never set ?tour=<first-step>. This sets it on the way out.
          onContinue={() => void navigateAfterOnboarding()}
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