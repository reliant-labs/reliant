import { useState, useEffect, memo, useMemo, useCallback } from "react";
import {
  FolderOpen,
  GitBranch,
  GitFork,
  Cloud,
  Loader2,
  Check,
  ArrowLeft,
  Monitor,
} from "lucide-react";
import { ConnectError, Code } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { useProjectStore } from "../../store/projectStore";
import type { Project as StoreProject } from "../../store/projectStore";
import { useApiKeySetupStore } from "../../store/apiKeySetupStore";
import { ProjectPickerModal } from "./ProjectPickerModal";
import { DirectoryPicker } from "./DirectoryPicker";
import { Modal } from "../ui/Modal";
import { RepoSelector } from "./RepoSelector";

import { toast } from "../../lib/toast-manager";
import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { useResumeDaemon, useCreateDaemon } from "../../hooks/useOnboardingQueries";
import { SelfHostedDaemonConnect } from "./SelfHostedDaemonConnect";
import { DaemonStatus } from "../../gen/reliant/v1/daemon_registry_pb";
import { useGitHubCredential } from "../../hooks/useGitHubCredential";
import { capabilities } from "../../services/controlPlane/capabilities";
import {
  listDaemons as listCloudDaemons,
  DAEMON_STATUS_ACTIVE,
  DAEMON_STATUS_SUSPENDED,
  type Daemon as CloudDaemon,
} from "../../services/controlPlane/daemon";
import { gitService } from "../../services/controlPlane/git";
import type { GitRepo } from "../../services/controlPlane/git";
import { projectGrpc } from "../../api/project-grpc";

import { settingsSync, SETTINGS_KEYS } from "../../services/settingsSync";
import { GradientBackground } from "../GradientBackground";
import { BrandMark } from "../icons/BrandMark";

// Use the store's Project type so refs from useProjectStore.getState().projects
// flow through without lossy widening. ProjectPicker accepts Partial fields in
// a few code paths (existing project shape from the legacy callbacks) so we
// alias rather than redefine.
type Project = StoreProject;

const CLOUD_PROJECT_ROOT = "/home/workspace/projects";

type CloneTarget = {
  daemonId: string;
  hostname: string;
};

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

interface ProjectPickerProps {
  onProjectSelected: (project: Project) => void;
}

// Treat reliant.v1.DaemonInfo.daemon_type "managed" as a cloud daemon. Only
// cloud daemons are valid clone targets — local/self-hosted daemons have
// their own filesystem and aren't reachable through the control-plane clone
// path. The string set matches normalizeRegisteredDaemonType in reliant's
// tools_daemon.go.
const CLOUD_DAEMON_TYPES = new Set(["managed", "cloud"]);
function isCloudDaemon(daemonType: string | undefined): boolean {
  return CLOUD_DAEMON_TYPES.has((daemonType ?? "").toLowerCase());
}

function cloudDaemonStatusLabel(daemon: CloudDaemon, isResuming: boolean): string {
  if (isResuming) return "resuming";
  if (daemon.status === DAEMON_STATUS_ACTIVE) return "active";
  if (daemon.status === DAEMON_STATUS_SUSPENDED) return "suspended";
  return daemon.status === 0 ? "unknown" : DaemonStatus[daemon.status]?.toLowerCase() ?? "unknown";
}

const MANAGED_DAEMON_TYPE = 1;
const MANAGED_DAEMON_SIZE_SMALL = 1;

// ConnectDaemonModal — the in-place "Connect a new daemon" flow. Offers BOTH
// connect paths without bouncing the user into the onboarding wizard:
//   - Managed cloud daemon: provisions a hosted daemon via CreateDaemon (only
//     offered when this deployment has cloud daemons).
//   - Self-hosted daemon: the SelfHostedDaemonConnect panel (shared with
//     onboarding's ComputeStep) — generates a token + shows the
//     `reliant daemon start --token` install steps.
// The modal auto-dismisses once a daemon connects (the picker rerenders into
// its normal project list as soon as useDaemonStatus reports an active daemon).
function ConnectDaemonModal({
  isOpen,
  onClose,
}: {
  isOpen: boolean;
  onClose: () => void;
}) {
  const hasCloud = capabilities.cloudDaemons;
  // "cloud" | "self" | null — null shows the choice, the others show the
  // respective flow. Default to the choice screen so the user explicitly
  // picks how they want to connect.
  const [mode, setMode] = useState<"cloud" | "self" | null>(null);
  const [cloudError, setCloudError] = useState<string | null>(null);
  const [cloudStarted, setCloudStarted] = useState(false);

  const createDaemonMutation = useCreateDaemon({
    onError: (err) => {
      const msg = err instanceof Error ? err.message : "Failed to start cloud daemon";
      setCloudError(msg);
    },
  });

  // Reset to the choice screen each time the modal opens so a prior session's
  // mode doesn't leak in.
  useEffect(() => {
    if (isOpen) {
      setMode(null);
      setCloudError(null);
      setCloudStarted(false);
    }
  }, [isOpen]);

  const startCloudDaemon = useCallback(async () => {
    setCloudError(null);
    try {
      const { listDaemons, hasActiveDaemon } = await import(
        "../../services/controlPlane/daemon"
      );
      const { daemons } = await listDaemons();
      if (hasActiveDaemon(daemons)) {
        // Already have an active daemon — nothing to provision; the picker
        // will pick it up on the next poll.
        setCloudStarted(true);
        return;
      }
      if (daemons.length > 0) {
        // A suspended/disconnected daemon exists — resume it instead of
        // creating a duplicate. Resume failures are non-fatal; the user can
        // retry from the "Resume a daemon" list.
        const id = daemons[0]?.id ?? "";
        if (id) {
          const { resumeDaemon } = await import(
            "../../services/controlPlane/daemon"
          );
          await resumeDaemon(id);
          setCloudStarted(true);
          return;
        }
      }
      await createDaemonMutation.mutateAsync({
        name: "workspace-daemon",
        daemonType: MANAGED_DAEMON_TYPE,
        size: MANAGED_DAEMON_SIZE_SMALL,
        gitRepo: "",
        gitBranch: "main",
      });
      setCloudStarted(true);
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to start cloud daemon";
      setCloudError(msg);
    }
  }, [createDaemonMutation]);

  const startingCloud = createDaemonMutation.isPending;

  return (
    <Modal isOpen={isOpen} onClose={onClose} title="Connect a daemon" size="lg">
      {mode === null && (
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            A daemon runs your code. Connect one to start working — either a
            hosted Reliant Cloud daemon or your own self-hosted machine.
          </p>
          <div className="grid gap-3 sm:grid-cols-2">
            <button
              type="button"
              onClick={() => {
                setMode("cloud");
                void startCloudDaemon();
              }}
              disabled={!hasCloud}
              className={`flex min-w-0 flex-col items-start gap-3 rounded-xl border-2 p-5 text-left transition-all ${
                hasCloud
                  ? "border-primary/25 bg-primary/5 hover:border-primary/50 hover:bg-primary/10"
                  : "cursor-not-allowed border-border/50 bg-muted/30 opacity-70"
              }`}
            >
              <div className="rounded-lg bg-primary/15 p-2.5 text-primary">
                <Cloud className="h-6 w-6" />
              </div>
              <div className="space-y-1">
                <span className="block text-sm font-semibold text-foreground">
                  Reliant Cloud
                </span>
                <span className="block text-xs leading-relaxed text-muted-foreground">
                  {hasCloud
                    ? "Provision a hosted daemon now. No install required."
                    : "Cloud daemons are not enabled for this deployment."}
                </span>
              </div>
            </button>

            <button
              type="button"
              onClick={() => setMode("self")}
              className="flex min-w-0 flex-col items-start gap-3 rounded-xl border-2 border-border/50 bg-background p-5 text-left transition-all hover:border-primary/50 hover:bg-muted/50"
            >
              <div className="rounded-lg bg-muted p-2.5 text-muted-foreground">
                <Monitor className="h-6 w-6" />
              </div>
              <div className="space-y-1">
                <span className="block text-sm font-semibold text-foreground">
                  Self-hosted
                </span>
                <span className="block text-xs leading-relaxed text-muted-foreground">
                  Run the daemon on your own laptop or server with a token.
                </span>
              </div>
            </button>
          </div>
        </div>
      )}

      {mode === "cloud" && (
        <div className="space-y-4">
          <button
            type="button"
            onClick={() => setMode(null)}
            className="inline-flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
          >
            <ArrowLeft className="h-3.5 w-3.5" />
            Back
          </button>
          <div className="flex items-center gap-3 rounded-xl border border-border/50 bg-muted/30 p-4">
            {startingCloud ? (
              <Loader2 className="h-5 w-5 animate-spin text-primary" />
            ) : cloudStarted ? (
              <Check className="h-5 w-5 text-emerald-500" />
            ) : (
              <Cloud className="h-5 w-5 text-primary" />
            )}
            <div className="min-w-0">
              <h3 className="text-sm font-medium text-foreground">
                {startingCloud
                  ? "Requesting a cloud daemon..."
                  : cloudStarted
                    ? "Cloud daemon requested"
                    : "Reliant Cloud"}
              </h3>
              <p className="mt-0.5 text-xs text-muted-foreground">
                {cloudStarted
                  ? "It may take a few minutes to provision. This screen will refresh once it connects."
                  : "Provisioning a hosted daemon for your account."}
              </p>
            </div>
          </div>
          {cloudError && (
            <div className="space-y-2">
              <p className="text-xs text-destructive">{cloudError}</p>
              <button
                type="button"
                onClick={() => void startCloudDaemon()}
                disabled={startingCloud}
                className="w-full rounded-lg bg-primary px-4 py-2.5 text-sm font-semibold text-primary-foreground transition-colors hover:bg-primary/90 disabled:opacity-60"
              >
                Try again
              </button>
            </div>
          )}
        </div>
      )}

      {mode === "self" && (
        <div className="space-y-4">
          <button
            type="button"
            onClick={() => setMode(null)}
            className="inline-flex items-center gap-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground"
          >
            <ArrowLeft className="h-3.5 w-3.5" />
            Back
          </button>
          {/* Shared with onboarding's ComputeStep — generates a token and
              shows `reliant daemon start --token` install steps. Closes the
              modal the moment the daemon connects. */}
          <SelfHostedDaemonConnect onConnected={onClose} />
        </div>
      )}
    </Modal>
  );
}

// NoCloudDaemonsState — rendered when the user is in web mode with no active
// local daemon. Two sub-cases:
//   - No cloud daemons exist for the user → CTA routes to onboarding ComputeStep.
//   - Cloud daemon(s) exist but none active → list Resume buttons; secondary
//     link still routes to onboarding for "connect a new daemon".
function NoActiveDaemonState() {
  const [error, setError] = useState<string | null>(null);
  const [connectOpen, setConnectOpen] = useState(false);
  const hasCloud = capabilities.cloudDaemons;

  const {
    data: cloudDaemons,
    isLoading,
    refetch,
  } = useQuery<CloudDaemon[]>({
    queryKey: ["projectPicker", "cloudDaemons"],
    queryFn: async () => {
      const { daemons } = await listCloudDaemons();
      return daemons;
    },
    enabled: hasCloud,
    refetchInterval: 8_000,
    refetchIntervalInBackground: false,
    staleTime: 5_000,
  });

  // The hook owns the ResourceExhausted-with-reason suppression (the global
  // upgradeInterceptor already opened the modal); we only see non-reasoned
  // errors here and surface them as the inline banner + toast.
  const resumeDaemonMutation = useResumeDaemon({
    onSuccess: async () => {
      // Refetch so the user sees the status flip toward ACTIVE; the picker
      // page itself will rerender once useDaemonStatus picks up the new
      // active daemon and the showConnectionInstructions branch flips off.
      await refetch();
    },
    onError: (err) => {
      const msg = err instanceof Error ? err.message : "Failed to resume daemon";
      setError(msg);
      toast.error(msg);
    },
  });
  const resumingId =
    resumeDaemonMutation.isPending && typeof resumeDaemonMutation.variables === "string"
      ? resumeDaemonMutation.variables
      : null;

  const handleResume = (daemon: CloudDaemon) => {
    if (daemon.status !== DAEMON_STATUS_SUSPENDED) return;
    setError(null);
    resumeDaemonMutation.mutate(daemon.id);
  };

  if (!hasCloud) {
    // Local-only deployment: nothing to resume; surface the self-hosted
    // connect instructions in-place instead of bouncing into onboarding.
    return (
      <div className="relative backdrop-blur-2xl bg-card/90 border border-border/50 rounded-2xl mb-6 overflow-hidden p-6">
        <h3 className="text-lg font-semibold text-foreground mb-1">
          Connect a daemon
        </h3>
        <p className="text-sm text-muted-foreground mb-4">
          Reliant needs a running daemon to access your projects. Connect one
          here — no onboarding required.
        </p>
        <button
          onClick={() => setConnectOpen(true)}
          className="w-full px-4 py-2.5 bg-primary text-primary-foreground hover:bg-primary/90 rounded-lg text-sm font-semibold transition-colors"
        >
          Connect a daemon
        </button>
        <ConnectDaemonModal
          isOpen={connectOpen}
          onClose={() => setConnectOpen(false)}
        />
      </div>
    );
  }

  const daemons = cloudDaemons ?? [];
  const hasAnyCloudDaemon = daemons.length > 0;

  return (
    <div className="relative backdrop-blur-2xl bg-card/90 border border-border/50 rounded-2xl mb-6 overflow-hidden p-6">
      <h3 className="text-lg font-semibold text-foreground mb-1">
        {hasAnyCloudDaemon ? "Resume a daemon" : "Start a daemon"}
      </h3>
      <p className="text-sm text-muted-foreground mb-4">
        {hasAnyCloudDaemon
          ? "Pick a daemon to wake up. The picker will refresh once it's connected."
          : "You don't have a daemon yet. Onboarding will create one in the cloud."}
      </p>

      {isLoading && (
        <div className="flex items-center gap-2 text-sm text-muted-foreground py-2">
          <Loader2 className="w-4 h-4 animate-spin" />
          Loading daemons...
        </div>
      )}

      {hasAnyCloudDaemon && (
        <div className="space-y-2">
          {daemons.map((daemon) => {
            const isResuming = resumingId === daemon.id;
            const isSuspended = daemon.status === DAEMON_STATUS_SUSPENDED;
            const statusLabel = cloudDaemonStatusLabel(daemon, isResuming);
            return (
              <button
                key={daemon.id}
                type="button"
                onClick={() => handleResume(daemon)}
                disabled={isResuming || !isSuspended}
                className="w-full flex items-center justify-between gap-3 px-4 py-3 rounded-lg bg-background/80 border border-border/60 hover:border-primary/40 disabled:opacity-60 disabled:cursor-not-allowed transition-colors text-left"
              >
                <div className="flex items-center gap-3 min-w-0">
                  <Cloud className="w-4 h-4 text-muted-foreground flex-shrink-0" />
                  <div className="min-w-0">
                    <div className="text-sm font-medium text-foreground truncate">
                      Resume {daemon.name || "daemon"}
                    </div>
                    {daemon.lastStatusMessage && (
                      <div className="text-xs text-muted-foreground truncate">
                        {daemon.lastStatusMessage}
                      </div>
                    )}
                  </div>
                </div>
                <div className="flex items-center gap-2 flex-shrink-0">
                  {isResuming && <Loader2 className="w-4 h-4 animate-spin text-muted-foreground" />}
                  <span className="text-xs font-mono uppercase tracking-wide text-muted-foreground">
                    {statusLabel}
                  </span>
                </div>
              </button>
            );
          })}
        </div>
      )}

      {error && <p className="mt-3 text-xs text-destructive">{error}</p>}

      <button
        type="button"
        onClick={() => setConnectOpen(true)}
        className="mt-4 w-full text-center text-xs text-muted-foreground hover:text-primary transition-colors py-1"
      >
        {hasAnyCloudDaemon ? "Connect a new daemon" : "Connect a daemon"}
      </button>

      <ConnectDaemonModal
        isOpen={connectOpen}
        onClose={() => setConnectOpen(false)}
      />
    </div>
  );
}



function ProjectPickerComponent({ onProjectSelected }: ProjectPickerProps) {
  const projects = useProjectStore((state) => state.projects);
  const loadProjects = useProjectStore((state) => state.loadProjects);
  const createProject = useProjectStore((state) => state.createProject);
  const selectProject = useProjectStore((state) => state.selectProject);
  const ensureApiKeyOrShowModal = useApiKeySetupStore((state) => state.ensureApiKeyOrShowModal);
  const [isCreateModalOpen, setIsCreateModalOpen] = useState(false);
  const [isDirectoryPickerOpen, setIsDirectoryPickerOpen] = useState(false);
  const [isCloneModalOpen, setIsCloneModalOpen] = useState(false);
  const [cloneStatus, setCloneStatus] = useState<string | null>(null);
  const [hoveredProjectId, setHoveredProjectId] = useState<string | null>(null);
  const [isOpenButtonHovered, setIsOpenButtonHovered] = useState(false);
  const [isCloneButtonHovered, setIsCloneButtonHovered] = useState(false);
  const [showAllProjects, setShowAllProjects] = useState(false);

  useEffect(() => {
    // Initialize theme based on database or system preference
    const theme = settingsSync.getSetting(SETTINGS_KEYS.THEME, "");
    const isDarkMode =
      theme === "dark" ||
      (!theme && window.matchMedia("(prefers-color-scheme: dark)").matches);

    if (isDarkMode) {
      document.documentElement.classList.add("dark");
    } else {
      document.documentElement.classList.remove("dark");
    }
  }, []);

  // Check for API key and show setup modal if not configured
  useEffect(() => {
    ensureApiKeyOrShowModal();
  }, [ensureApiKeyOrShowModal]);



  const handleProjectClick = (project: Project) => {
    // Always attempt to open the project. Daemon resolution happens at open
    // time inside onProjectSelected (and downstream), so the picker never
    // gates a row on the project_daemons join — that table is only populated
    // by clone/pull flows and would otherwise hide locally-created projects.
    onProjectSelected(project);
  };

  // ProjectPickerModal's onProjectCreated callback emits a narrow Project
  // shape; we hydrate via the store after reload to get the full StoreProject
  // (including last_active, remote_url) before forwarding to onProjectSelected.
  const handleProjectCreated = async (createdProject?: { id: string }) => {
    await loadProjects();
    setIsCreateModalOpen(false);
    if (createdProject) {
      const full = useProjectStore
        .getState()
        .projects.find((p) => p.id === createdProject.id);
      if (full) handleProjectClick(full);
    }
  };

  const isElectron = !!window.electronAPI?.selectDirectory;
  const isWebMode = !isElectron;
  const { daemons, activeDaemon, loading: daemonLoading } = useDaemonStatus();
  const showConnectionInstructions = isWebMode && !activeDaemon && !daemonLoading;

  const { hasToken: hasGitHubCredential } = useGitHubCredential();
  const { data: controlPlaneDaemons } = useQuery<CloudDaemon[]>({
    queryKey: ["projectPicker", "controlPlaneDaemons"],
    queryFn: async () => {
      if (!capabilities.cloudDaemons) return [];
      const res = await listCloudDaemons();
      return res.daemons;
    },
    enabled: capabilities.cloudDaemons,
    refetchInterval: 5_000,
    staleTime: 0,
  });

  // Cloud daemons are the only valid clone targets. Self-hosted daemons can
  // still be "viewed" via the switcher (you see which projects live on
  // them), but the clone affordances stay disabled — cloning requires a
  // managed daemon that the control plane can reach.
  const cloudDaemons = useMemo(
    () => daemons.filter((d) => isCloudDaemon(d.daemonType)),
    [daemons],
  );
  const activeControlPlaneDaemons = useMemo(
    () => (controlPlaneDaemons ?? []).filter((d) => d.status === DAEMON_STATUS_ACTIVE),
    [controlPlaneDaemons],
  );

  // Hostname lookup for naming daemons in the clone status toast. Falls back
  // to a short id slice when the daemon row hasn't loaded yet.
  const hostnameFor = useCallback(
    (daemonId: string) => {
      const d = daemons.find((x) => x.daemonId === daemonId);
      return d?.hostname || `daemon ${daemonId.slice(0, 8)}`;
    },
    [daemons],
  );

  // selectedCloneDaemon — the cloud daemon the top-level "Clone repo" flow
  // installs onto. We pick the first ACTIVE cloud daemon (registry first,
  // then control-plane). Suspended cloud daemons are excluded because the
  // gateway can't forward the clone command until the daemon resumes.
  const selectedCloneDaemon = useMemo<CloneTarget | null>(() => {
    const activeCloud = cloudDaemons.find(
      (d) => d.status === DaemonStatus.ACTIVE,
    );
    if (activeCloud) {
      return {
        daemonId: activeCloud.daemonId,
        hostname: activeCloud.hostname || hostnameFor(activeCloud.daemonId),
      };
    }
    const cpDaemon = activeControlPlaneDaemons[0];
    if (!cpDaemon) return null;
    return {
      daemonId: cpDaemon.id,
      hostname:
        cpDaemon.hostname || cpDaemon.name || `daemon ${cpDaemon.id.slice(0, 8)}`,
    };
  }, [activeControlPlaneDaemons, cloudDaemons, hostnameFor]);

  // canCloneToSelected drives the picker's top "Clone repo" button. Visible
  // whenever a clone-capable cloud daemon is available. RepoSelector owns the
  // GitHub credential-missing state and shows the reconnect UI; hiding the
  // entry point here leaves users stranded.
  const canCloneToSelected =
    capabilities.cloudDaemons && !!selectedCloneDaemon;

  // Run a clone for a known repo onto an explicit target daemon, then
  // create the Project (or open an existing one) and mark it installed.
  // The caller passes targetDaemonId so the picker can support cloning to
  // *any* of the user's cloud daemons (the per-row menu or the top-level
  // "Clone repo" affordance into the currently-selected daemon). The caller
  // is responsible for having vetted the target (status ACTIVE, cloud type,
  // GH credential present); cloneAndOpen does not re-check.
  const cloneAndOpen = useCallback(
    async (
      repo: { cloneUrl: string; defaultBranch: string; fullName?: string },
      destinationPath: string,
      projectName: string,
      targetDaemonId: string,
    ): Promise<void> => {
      if (!targetDaemonId) {
        throw new Error("No daemon selected to clone into");
      }
      const branch = repo.defaultBranch || "main";
      const targetHost = hostnameFor(targetDaemonId);
      setCloneStatus(`Cloning ${projectName} to ${targetHost}...`);
      const cloneResult = await gitService.cloneRepo({
        daemonId: targetDaemonId,
        gitRepo: repo.cloneUrl,
        gitBranch: branch,
        path: destinationPath,
      });
      const clonedPath = cloneResult.clonedPath;

      setCloneStatus(`Opening ${projectName}...`);
      let openedProject: Project | undefined;
      try {
        const createdProject = await createProject({
          name: projectName,
          path: clonedPath,
          description: "",
          is_git_repo: true,
          default_branch: branch,
        });
        openedProject = createdProject;
      } catch (err) {
        // Already-exists is a legitimate "you've cloned this somewhere
        // before" case. Look the project up and reuse it.
        const isAlreadyExists =
          (err instanceof ConnectError && err.code === Code.AlreadyExists) ||
          (err instanceof Error &&
            (err.message.includes("already exists") || err.message.includes("409")));
        if (!isAlreadyExists) throw err;
        await loadProjects();
        const refreshed = useProjectStore.getState().projects;
        openedProject =
          refreshed.find((p) => p.path === clonedPath) ||
          refreshed.find((p) => p.remote_url === repo.cloneUrl);
      }

      if (openedProject) {
        try {
          await projectGrpc.markProjectInstalled(
            openedProject.id,
            targetDaemonId,
            clonedPath,
            branch,
          );
        } catch (markErr) {
          console.warn("markProjectInstalled failed (non-fatal):", markErr);
        }
        await loadProjects();
        await selectProject(openedProject);
        onProjectSelected(openedProject);
      }
      setCloneStatus(null);
    },
    [
      createProject,
      hostnameFor,
      loadProjects,
      onProjectSelected,
      selectProject,
    ],
  );

  // Modal "Clone repo" → user picks a fresh repo to install on the active
  // cloud daemon. The clone target may come from the local registry or the
  // control-plane daemon list.
  const handleRepoSelectedFromModal = async (repo: GitRepo) => {
    setIsCloneModalOpen(false);
    if (!selectedCloneDaemon) {
      toast.error("No daemon selected");
      return;
    }
    const projectName = repoNameFromUrl(repo.cloneUrl) || repo.fullName;
    const destinationPath = cloudPathForRepo(repo);
    const loadingToast = toast.loading(`Cloning "${projectName}"...`);
    try {
      await cloneAndOpen(repo, destinationPath, projectName, selectedCloneDaemon.daemonId);
      toast.success(`Cloned "${projectName}"`);
    } catch (err) {
      console.error("Clone-from-modal failed:", err);
      toast.error(err instanceof Error ? err.message : "Failed to clone repository");
      setCloneStatus(null);
    } finally {
      toast.dismiss(loadingToast);
    }
  };

  const handleOpenExistingProject = async () => {
    // In browser mode, open the directory picker to browse the filesystem
    if (!isElectron) {
      setIsDirectoryPickerOpen(true);
      return;
    }

    let selectedPath: string | null = null;
    try {
      selectedPath = await window.electronAPI!.selectDirectory();
    } catch (err) {
      console.error("Failed to select directory via Electron:", err);
    }
    
    if (selectedPath) {
      // Create a project for the selected directory
      const projectName = selectedPath.split("/").pop() || selectedPath || "Untitled Project";
      const projectData = {
        name: projectName,
        path: selectedPath,
        description: "",
        is_git_repo: false, // Will be determined by the backend
        default_branch: "main",
      };

      // Create the project in the store with toast notification
      const loadingToast = toast.loading(
        `Opening project "${projectName}"...`
      );
      try {
        const createdProject = await createProject(projectData);
        toast.dismiss(loadingToast);

        // Reload the projects list to get initialization status
        await loadProjects();

        // Select the newly created project (it will go through initialization check)
        if (createdProject) {
          handleProjectClick(createdProject);
        }
      } catch (error) {
        toast.dismiss(loadingToast);
        // If project already exists at this path, find and open it
        const isAlreadyExists =
          (error instanceof ConnectError && error.code === Code.AlreadyExists) ||
          (error instanceof Error && (error.message.includes("already exists") || error.message.includes("409")));
        if (isAlreadyExists) {
          const existing = projects.find((p) => p.path === selectedPath);
          if (existing) {
            toast.success(`Opening existing project "${existing.name}"`);
            handleProjectClick(existing);
            return;
          }
          // Project might not be in our loaded list yet; reload and try again
          await loadProjects();
          const refreshed = useProjectStore.getState().projects;
          const found = refreshed.find((p) => p.path === selectedPath);
          if (found) {
            toast.success(`Opening existing project "${found.name}"`);
            handleProjectClick(found);
            return;
          }
        }
        console.error("Failed to create project:", error);
      }
    }
  };

  const handleDirectoryPickerSelect = async (selectedPath: string) => {
    const projectName = selectedPath.split("/").pop() || selectedPath || "Untitled Project";
    const projectData = {
      name: projectName,
      path: selectedPath,
      description: "",
      is_git_repo: false,
      default_branch: "main",
    };

    const loadingToast = toast.loading(`Opening project "${projectName}"...`);
    try {
      const createdProject = await createProject(projectData);
      toast.dismiss(loadingToast);
      await loadProjects();
      if (createdProject) {
        handleProjectClick(createdProject);
      }
    } catch (error) {
      toast.dismiss(loadingToast);
      const isAlreadyExists =
        (error instanceof ConnectError && error.code === Code.AlreadyExists) ||
        (error instanceof Error && (error.message.includes("already exists") || error.message.includes("409")));
      if (isAlreadyExists) {
        const existing = projects.find((p) => p.path === selectedPath);
        if (existing) {
          toast.success(`Opening existing project "${existing.name}"`);
          handleProjectClick(existing);
          return;
        }
        await loadProjects();
        const refreshed = useProjectStore.getState().projects;
        const found = refreshed.find((p) => p.path === selectedPath);
        if (found) {
          toast.success(`Opening existing project "${found.name}"`);
          handleProjectClick(found);
          return;
        }
      }
      console.error("Failed to create project:", error);
    }
  };

  // Sort projects by last active, optionally limited to 5 most recent
  const sortedProjects = useMemo(() => {
    return [...projects].sort(
      (a, b) =>
        new Date(b.last_active).getTime() - new Date(a.last_active).getTime()
    );
  }, [projects]);

  const displayedProjects = useMemo(() => {
    return showAllProjects ? sortedProjects : sortedProjects.slice(0, 5);
  }, [sortedProjects, showAllProjects]);

  // The most recently active project — the "main app / workspace" the user
  // would expect to return to. Used by the "Back to <project>" affordance;
  // when there are no projects (genuine first-run) it's undefined and the
  // affordance hides.
  const mostRecentProject = sortedProjects[0];

  // Re-select the most recent project to leave the picker and restore the
  // workspace/chat shell. We route through handleProjectClick so cross-daemon
  // state (clone-on-other-daemon) is respected exactly as a normal row click.
  const handleBackToApp = useCallback(() => {
    if (mostRecentProject) handleProjectClick(mostRecentProject);
    // handleProjectClick is a stable-enough closure over store setters; the
    // picker re-renders on project changes so we intentionally key only on
    // the target project.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mostRecentProject]);

  return (
    <div
      className="h-full bg-background relative overflow-hidden"
      data-testid="project-picker"
    >
      {/* Background ambient glow effects - Mesh Grid Pattern */}
      <GradientBackground />

      {/* Content */}
      <div className="relative z-10 h-full flex flex-col">
        <div className="flex-1 flex items-center justify-center px-6 py-12">
          <div className="w-full max-w-3xl">
              {/* Logo and Brand Header - Aligned with content */}
              <div className="flex items-center justify-between gap-4 mb-8">
                <div className="flex items-center gap-4">
                  <BrandMark className="w-16 h-16" />
                  <h1 className="text-4xl font-bold text-foreground">Reliant</h1>
                </div>
                {/* Back to the active workspace. The picker is reached by
                    deselecting the current project (handleNavigateToProjectPicker
                    in ModernApp clears currentProject); re-selecting the most
                    recently active project restores the chat/workspace shell.
                    Hidden on genuine first-run (no projects yet) — there's
                    nothing to return to. */}
                {mostRecentProject && (
                  <button
                    type="button"
                    onClick={handleBackToApp}
                    className="flex items-center gap-1.5 px-3 py-1.5 rounded-md border border-border/60 bg-card/80 hover:bg-card text-xs font-medium text-muted-foreground hover:text-foreground transition-colors"
                    data-testid="project-picker-back-to-app"
                  >
                    <ArrowLeft className="w-3.5 h-3.5" />
                    Back to {mostRecentProject.name}
                  </button>
                )}
              </div>
              {showConnectionInstructions ? (
                <NoActiveDaemonState />
              ) : (
                <>
                  <div className="relative backdrop-blur-2xl bg-card/90 border border-border/50 rounded-2xl mb-6 overflow-hidden">
                    <button
                      onClick={handleOpenExistingProject}
                      onMouseEnter={() => setIsOpenButtonHovered(true)}
                      onMouseLeave={() => setIsOpenButtonHovered(false)}
                      className="group w-full p-6 transition-all duration-150 text-left active:scale-[0.99]"
                      style={{
                        backgroundColor: isOpenButtonHovered
                          ? "hsl(var(--primary) / 0.15)"
                          : "hsl(var(--primary) / 0.1)",
                      }}
                    >
                      <div className="flex items-center justify-between">
                        <div className="flex items-center gap-4">
                          <div className="w-12 h-12 rounded-xl bg-primary/20 flex items-center justify-center">
                            <FolderOpen className="w-6 h-6 text-primary" />
                          </div>
                          <div className="text-left">
                            <h3 className="text-xl font-bold text-primary">
                              Open Project
                            </h3>
                            <p className="text-sm text-muted-foreground">
                              Browse and select your project directory
                            </p>
                          </div>
                        </div>
                      </div>
                    </button>
                    {canCloneToSelected && (
                      <button
                        onClick={() => setIsCloneModalOpen(true)}
                        onMouseEnter={() => setIsCloneButtonHovered(true)}
                        onMouseLeave={() => setIsCloneButtonHovered(false)}
                        className="group w-full p-6 border-t border-border/50 transition-all duration-150 text-left active:scale-[0.99]"
                        style={{
                          backgroundColor: isCloneButtonHovered
                            ? "hsl(var(--primary) / 0.08)"
                            : "transparent",
                        }}
                      >
                        <div className="flex items-center gap-4">
                          <div className="w-12 h-12 rounded-xl bg-muted flex items-center justify-center">
                            <GitFork className="w-6 h-6 text-foreground" />
                          </div>
                          <div className="text-left">
                            <h3 className="text-xl font-bold text-foreground">
                              Clone repo
                            </h3>
                            <p className="text-sm text-muted-foreground">
                              {hasGitHubCredential
                                ? `Pull a GitHub repo onto ${selectedCloneDaemon?.hostname || "this daemon"}`
                                : "Connect GitHub to clone a repository"}
                            </p>
                          </div>
                        </div>
                      </button>
                    )}
                  </div>
                </>
              )}

              {/* Recent Projects - Compact List */}
              {displayedProjects.length > 0 && (
                <div className="relative backdrop-blur-2xl bg-card/90 border border-border/50 rounded-2xl p-6">
                  <div className="flex items-center justify-between mb-4">
                    <h2 className="text-base font-semibold text-foreground">
                      {showAllProjects ? "All Projects" : "Recent Projects"}
                    </h2>
                    {projects.length > 5 && (
                      <button
                        onClick={() => setShowAllProjects((prev) => !prev)}
                        className="text-sm text-muted-foreground hover:text-primary transition-colors font-mono"
                      >
                        {showAllProjects ? "Show recent" : `View all (${projects.length})`}
                      </button>
                    )}
                  </div>

                  <div className="space-y-1">
                    {displayedProjects.map((project) => {
                      const isHovered = hoveredProjectId === project.id;
                      // Every project row is always clickable. Daemon
                      // resolution happens at open time inside
                      // handleProjectClick / onProjectSelected — the picker
                      // never gates a row on the project_daemons join.
                      return (
                        <div key={project.id} className="relative">
                          <button
                            onClick={() => handleProjectClick(project)}
                            onMouseEnter={() => setHoveredProjectId(project.id)}
                            onMouseLeave={() => setHoveredProjectId(null)}
                            className="group w-full px-2.5 py-2 rounded-md transition-all duration-150 font-mono text-left text-xs bg-transparent active:scale-[0.99] text-foreground/80 hover:text-foreground"
                            style={{
                              backgroundColor: isHovered
                                ? "hsl(var(--primary) / 0.1)"
                                : undefined,
                            }}
                            data-testid="project-item"
                          >
                            <div className="flex items-center justify-between gap-4">
                              {/* Left: Project Name */}
                              <div className="flex items-center gap-2 min-w-0 flex-shrink">
                                <span className="font-mono font-medium truncate transition-colors duration-200">
                                  {project.name}
                                </span>
                                {project.is_git_repo && (
                                  <GitBranch className="w-3 h-3 text-muted-foreground/50 flex-shrink-0" />
                                )}
                              </div>

                              {/* Right: Path */}
                              <div className="flex items-center gap-2 flex-shrink-0">
                                <span className="text-sm text-muted-foreground/60 font-mono">
                                  {project.path.replace(/^\/(?:Users|home)\/[^/]+/, '~')}
                                </span>
                              </div>
                            </div>
                          </button>
                        </div>
                      );
                    })}
                  </div>
                </div>
              )}
            </div>
        </div>
      </div>

      {/* Create Project Modal */}
      <ProjectPickerModal
        isOpen={isCreateModalOpen}
        onClose={() => setIsCreateModalOpen(false)}
        onProjectCreated={handleProjectCreated}
      />

      {/* Directory Picker (browser mode) */}
      <DirectoryPicker
        isOpen={isDirectoryPickerOpen}
        onClose={() => setIsDirectoryPickerOpen(false)}
        onSelect={handleDirectoryPickerSelect}
      />

      {/* Clone GitHub repo onto the active daemon */}
      <Modal
        isOpen={isCloneModalOpen}
        onClose={() => setIsCloneModalOpen(false)}
        title="Clone a repository"
        size="lg"
      >
        <RepoSelector
          onSelect={(repo) => {
            void handleRepoSelectedFromModal(repo);
          }}
          analyticsPhase="project_picker"
        />
      </Modal>

      {cloneStatus && (
        <div className="fixed bottom-4 left-1/2 -translate-x-1/2 z-50 flex items-center gap-2 rounded-lg border border-border bg-card/95 px-4 py-2 text-sm text-foreground shadow-lg">
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
          {cloneStatus}
        </div>
      )}
    </div>
  );
}

export const ProjectPicker = memo(ProjectPickerComponent);