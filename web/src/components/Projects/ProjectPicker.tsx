import { useState, useEffect, memo, useMemo, useCallback, useRef } from "react";
import {
  FolderOpen,
  GitBranch,
  GitFork,
  Cloud,
  Loader2,
  ChevronDown,
  Check,
} from "lucide-react";
import { ConnectError, Code } from "@connectrpc/connect";
import { useNavigate } from "@tanstack/react-router";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useProjectStore } from "../../store/projectStore";
import type { Project as StoreProject } from "../../store/projectStore";
import { useApiKeySetupStore } from "../../store/apiKeySetupStore";
import { ProjectPickerModal } from "./ProjectPickerModal";
import { DirectoryPicker } from "./DirectoryPicker";
import { Modal } from "../ui/Modal";
import { RepoSelector } from "./RepoSelector";

import { toast } from "../../lib/toast-manager";
import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { DaemonStatus } from "../../gen/reliant/v1/daemon_registry_pb";
import { useGitHubCredential } from "../../hooks/useGitHubCredential";
import { capabilities } from "../../services/controlPlane/capabilities";
import {
  listDaemons as listCloudDaemons,
  resumeDaemon,
  DAEMON_STATUS_ACTIVE,
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

// Query key for the cross-daemon project_daemons view — every (project,
// daemon) row across all the user's daemons. The picker uses this to render
// per-row badges that name *which* daemon hosts a project (previously the
// picker only fetched rows for the active daemon, so "Only on another daemon"
// couldn't say which one).
const PROJECT_DAEMONS_ALL_KEY = ["projectDaemons", "all"] as const;

function useAllProjectDaemons() {
  return useQuery({
    queryKey: PROJECT_DAEMONS_ALL_KEY,
    queryFn: () => projectGrpc.listProjectDaemons(),
    staleTime: 10_000,
  });
}

// Treat reliant.v1.DaemonInfo.daemon_type "managed" as a cloud daemon. Only
// cloud daemons are valid clone targets — local/self-hosted daemons have
// their own filesystem and aren't reachable through the control-plane clone
// path. The string set matches normalizeRegisteredDaemonType in reliant's
// tools_daemon.go.
const CLOUD_DAEMON_TYPES = new Set(["managed"]);
function isCloudDaemon(daemonType: string | undefined): boolean {
  return CLOUD_DAEMON_TYPES.has((daemonType ?? "").toLowerCase());
}

// NoCloudDaemonsState — rendered when the user is in web mode with no active
// local daemon. Two sub-cases:
//   - No cloud daemons exist for the user → CTA routes to onboarding ComputeStep.
//   - Cloud daemon(s) exist but none active → list Resume buttons; secondary
//     link still routes to onboarding for "connect a new daemon".
function NoActiveDaemonState() {
  const navigate = useNavigate();
  const [resumingId, setResumingId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
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

  const goToOnboarding = useCallback(() => {
    // Clear the launch plan so onboarding starts at ComputeStep (it's the
    // first step whenever plan.compute is unset — see deriveStep in
    // OnboardingFlow/stepConfig.ts).
    navigate({ to: "/onboarding", search: { plan: undefined } });
  }, [navigate]);

  const handleResume = async (daemon: CloudDaemon) => {
    setResumingId(daemon.id);
    setError(null);
    try {
      await resumeDaemon(daemon.id);
      // Refetch so the user sees the status flip toward ACTIVE; the picker
      // page itself will rerender once useDaemonStatus picks up the new
      // active daemon and the showConnectionInstructions branch flips off.
      await refetch();
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to resume daemon";
      setError(msg);
      toast.error(msg);
    } finally {
      setResumingId(null);
    }
  };

  if (!hasCloud) {
    // Local-only deployment: nothing to resume; tell the user to start a
    // local daemon and route them to onboarding which surfaces the
    // install/connect instructions for local daemons.
    return (
      <div className="relative backdrop-blur-2xl bg-card/90 border border-border/50 rounded-2xl mb-6 overflow-hidden p-6">
        <h3 className="text-lg font-semibold text-foreground mb-1">
          Connect a daemon
        </h3>
        <p className="text-sm text-muted-foreground mb-4">
          Reliant needs a running daemon to access your projects. Run onboarding
          to install and connect one.
        </p>
        <button
          onClick={goToOnboarding}
          className="w-full px-4 py-2.5 bg-primary text-primary-foreground hover:bg-primary/90 rounded-lg text-sm font-semibold transition-colors"
        >
          Start onboarding
        </button>
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
            const isActive = daemon.status === DAEMON_STATUS_ACTIVE;
            return (
              <button
                key={daemon.id}
                type="button"
                onClick={() => handleResume(daemon)}
                disabled={isResuming || isActive}
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
                    {isActive ? "active" : isResuming ? "resuming" : "paused"}
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
        onClick={goToOnboarding}
        className="mt-4 w-full text-center text-xs text-muted-foreground hover:text-primary transition-colors py-1"
      >
        {hasAnyCloudDaemon ? "Connect a new daemon" : "Start onboarding"}
      </button>
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
  // Confirm-prompt state for "Clone <name> to <daemon>?" — carries both the
  // project and the chosen target daemon. The target is no longer implicit
  // (was "the one active daemon"); the per-row clone menu now lets users
  // pick from multiple cloud daemons.
  const [cloneConfirm, setCloneConfirm] = useState<
    { project: Project; daemonId: string } | null
  >(null);
  // selectedDaemonId — which daemon's perspective the row list is showing.
  // Initialized once to the first ACTIVE cloud daemon (falling back to first
  // ACTIVE, then first daemon). Subsequent daemon-list polls do NOT clobber
  // this so the user's switch sticks.
  const [selectedDaemonId, setSelectedDaemonId] = useState<string | null>(null);
  const [isDaemonSwitcherOpen, setIsDaemonSwitcherOpen] = useState(false);
  const daemonSwitcherRef = useRef<HTMLDivElement | null>(null);
  // openCloneMenuFor — project id whose per-row "Clone to <daemon>" menu is
  // open. Single-slot so opening one closes any other.
  const [openCloneMenuFor, setOpenCloneMenuFor] = useState<string | null>(null);
  const cloneMenuRef = useRef<HTMLDivElement | null>(null);
  const queryClient = useQueryClient();

  // Close switcher / clone menu on outside-click. Both popovers are
  // single-slot so one effect with a couple refs is enough; we don't bother
  // with a focus-trap because the menus are short-lived.
  useEffect(() => {
    if (!isDaemonSwitcherOpen && !openCloneMenuFor) return;
    const handler = (e: MouseEvent) => {
      const target = e.target as Node | null;
      if (
        isDaemonSwitcherOpen &&
        daemonSwitcherRef.current &&
        target &&
        !daemonSwitcherRef.current.contains(target)
      ) {
        setIsDaemonSwitcherOpen(false);
      }
      if (
        openCloneMenuFor &&
        cloneMenuRef.current &&
        target &&
        !cloneMenuRef.current.contains(target)
      ) {
        setOpenCloneMenuFor(null);
      }
    };
    window.addEventListener("mousedown", handler);
    return () => window.removeEventListener("mousedown", handler);
  }, [isDaemonSwitcherOpen, openCloneMenuFor]);

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
    // If we know the selected daemon hosts a clone of this project, or the
    // project lookup is unscoped (no selected daemon at all, e.g. Electron
    // local-only), open the project normally.
    if (!selectedDaemonId || installedProjectIds.has(project.id)) {
      onProjectSelected(project);
      return;
    }
    // Not on the selected daemon. If the project has a remote_url and we
    // have at least one cloud daemon to clone into, open the per-row
    // "Clone to <hostname>" menu so the user picks the target explicitly.
    // Otherwise it's a local-only project — disabled rows shouldn't reach
    // this branch, but guard it for safety.
    if (project.remote_url && cloneTargets.length > 0) {
      setOpenCloneMenuFor((prev) => (prev === project.id ? null : project.id));
      return;
    }
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

  // Cloud daemons are the only valid clone targets. Self-hosted daemons can
  // still be "viewed" via the switcher (you see which projects live on
  // them), but the clone affordances stay disabled — cloning requires a
  // managed daemon that the control plane can reach.
  const cloudDaemons = useMemo(
    () => daemons.filter((d) => isCloudDaemon(d.daemonType)),
    [daemons],
  );

  // Pick the initial selectedDaemonId once daemons load. We prefer:
  //   1. An ACTIVE cloud daemon (the normal clone-ready case)
  //   2. Any ACTIVE daemon (so local-only users still see "their" data)
  //   3. The first daemon at all (a SUSPENDED cloud daemon is still useful —
  //      the user can switch to it and see what's installed before resuming)
  // We only auto-set when selectedDaemonId is null so that a manual switch
  // sticks across daemon-list refreshes.
  useEffect(() => {
    if (selectedDaemonId) return;
    if (daemons.length === 0) return;
    const activeCloud = cloudDaemons.find(
      (d) => d.status === DaemonStatus.ACTIVE,
    );
    const anyActive = daemons.find((d) => d.status === DaemonStatus.ACTIVE);
    const pick = activeCloud ?? anyActive ?? daemons[0];
    if (pick?.daemonId) setSelectedDaemonId(pick.daemonId);
  }, [daemons, cloudDaemons, selectedDaemonId]);

  const selectedDaemon = useMemo(
    () => daemons.find((d) => d.daemonId === selectedDaemonId),
    [daemons, selectedDaemonId],
  );

  // "Clone to <hostname>" target list — every ACTIVE cloud daemon. Suspended
  // cloud daemons are intentionally excluded because the gateway can't
  // forward the clone command until the daemon resumes; the user would get
  // a confusing "queued" state instead of fast feedback.
  const cloneTargets = useMemo(
    () =>
      hasGitHubCredential && capabilities.cloudDaemons
        ? cloudDaemons.filter((d) => d.status === DaemonStatus.ACTIVE)
        : [],
    [cloudDaemons, hasGitHubCredential],
  );

  // canCloneToSelected drives the picker's top "Clone repo" button. True
  // when the user can clone *into the currently-selected daemon*.
  const canCloneToSelected =
    !!selectedDaemon &&
    isCloudDaemon(selectedDaemon.daemonType) &&
    selectedDaemon.status === DaemonStatus.ACTIVE &&
    hasGitHubCredential &&
    capabilities.cloudDaemons;

  // Cross-daemon view: every (project, daemon) installation row for projects
  // the user owns. The picker derives two structures from this:
  //   - installedProjectIds: set of project ids installed on selectedDaemon
  //     (drives the "Not on this daemon" badge & disabled state)
  //   - hostsByProject: project_id -> sorted daemonId[] for naming the OTHER
  //     daemons in the "Only on <hostname>" badge
  const { data: allProjectDaemons } = useAllProjectDaemons();
  const installedProjectIds = useMemo(() => {
    const s = new Set<string>();
    if (!selectedDaemonId) return s;
    (allProjectDaemons ?? []).forEach((row) => {
      if (row.daemon_id === selectedDaemonId) s.add(row.project_id);
    });
    return s;
  }, [allProjectDaemons, selectedDaemonId]);
  const hostsByProject = useMemo(() => {
    const m = new Map<string, string[]>();
    (allProjectDaemons ?? []).forEach((row) => {
      const arr = m.get(row.project_id) ?? [];
      arr.push(row.daemon_id);
      m.set(row.project_id, arr);
    });
    return m;
  }, [allProjectDaemons]);

  // Hostname lookup for naming daemons in badges/menus. Falls back to a
  // short id slice when the daemon row hasn't loaded yet (rare: cross-daemon
  // rows can reference daemons that are momentarily missing from the
  // registry list during a poll).
  const hostnameFor = useCallback(
    (daemonId: string) => {
      const d = daemons.find((x) => x.daemonId === daemonId);
      return d?.hostname || `daemon ${daemonId.slice(0, 8)}`;
    },
    [daemons],
  );

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
        // Refresh the cross-daemon view so the newly-cloned row appears
        // without waiting for the staleTime to expire. Other rows on the
        // same project (different daemons) come back unchanged, so this
        // is essentially "make the badge flip immediately".
        await queryClient.invalidateQueries({ queryKey: PROJECT_DAEMONS_ALL_KEY });
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
      queryClient,
      selectProject,
    ],
  );

  // Modal "Clone repo" → user picks a fresh repo to install on the
  // currently-selected daemon. canCloneToSelected already gates the button
  // that opens this modal so we trust selectedDaemonId is a valid target.
  const handleRepoSelectedFromModal = async (repo: GitRepo) => {
    setIsCloneModalOpen(false);
    if (!selectedDaemonId) {
      toast.error("No daemon selected");
      return;
    }
    const projectName = repoNameFromUrl(repo.cloneUrl) || repo.fullName;
    const destinationPath = cloudPathForRepo(repo);
    const loadingToast = toast.loading(`Cloning "${projectName}"...`);
    try {
      await cloneAndOpen(repo, destinationPath, projectName, selectedDaemonId);
      toast.success(`Cloned "${projectName}"`);
    } catch (err) {
      console.error("Clone-from-modal failed:", err);
      toast.error(err instanceof Error ? err.message : "Failed to clone repository");
      setCloneStatus(null);
    } finally {
      toast.dismiss(loadingToast);
    }
  };

  // Per-row "Clone <name> to <hostname>?" — user confirmed cloning an
  // existing project (which has remote_url) onto a specific cloud daemon.
  // The chosen target was captured in cloneConfirm by the per-row menu.
  const handleConfirmRowClone = async () => {
    const confirm = cloneConfirm;
    setCloneConfirm(null);
    if (!confirm || !confirm.project.remote_url) return;
    const { project, daemonId } = confirm;
    const repo = {
      cloneUrl: project.remote_url!,
      defaultBranch: project.default_branch || "main",
      fullName: project.name,
    };
    const destinationPath = cloudPathForRepo({
      cloneUrl: project.remote_url!,
      fullName: project.name,
    } as GitRepo);
    const loadingToast = toast.loading(
      `Cloning "${project.name}" to ${hostnameFor(daemonId)}...`,
    );
    try {
      await cloneAndOpen(repo, destinationPath, project.name, daemonId);
      toast.success(`Cloned "${project.name}"`);
    } catch (err) {
      console.error("Clone-from-row failed:", err);
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
              <div className="flex items-center gap-4 mb-8">
                <BrandMark className="w-16 h-16" />
                <h1 className="text-4xl font-bold text-foreground">Reliant</h1>
              </div>
              {showConnectionInstructions ? (
                <NoActiveDaemonState />
              ) : (
                <>
                  {/* Daemon switcher — explicit "which daemon's projects am
                      I looking at" control. Hidden when the user has zero
                      or one daemon (the implicit case isn't ambiguous). */}
                  {daemons.length > 1 && (
                    <div
                      ref={daemonSwitcherRef}
                      className="relative mb-3 inline-block"
                    >
                      <button
                        type="button"
                        onClick={() => setIsDaemonSwitcherOpen((v) => !v)}
                        className="flex items-center gap-2 px-3 py-1.5 rounded-md border border-border/60 bg-card/80 hover:bg-card text-xs font-mono text-foreground/80 hover:text-foreground transition-colors"
                      >
                        <Cloud className="w-3.5 h-3.5 text-muted-foreground" />
                        <span>
                          Viewing:{" "}
                          <span className="text-foreground font-medium">
                            {selectedDaemon?.hostname ||
                              (selectedDaemonId
                                ? hostnameFor(selectedDaemonId)
                                : "select a daemon")}
                          </span>
                        </span>
                        <ChevronDown className="w-3.5 h-3.5 text-muted-foreground" />
                      </button>
                      {isDaemonSwitcherOpen && (
                        <div className="absolute z-20 mt-1 min-w-[16rem] rounded-md border border-border/60 bg-card shadow-lg overflow-hidden">
                          {daemons.map((d) => {
                            const isSelected = d.daemonId === selectedDaemonId;
                            const isCloud = isCloudDaemon(d.daemonType);
                            const isActive = d.status === DaemonStatus.ACTIVE;
                            return (
                              <button
                                key={d.daemonId}
                                type="button"
                                onClick={() => {
                                  setSelectedDaemonId(d.daemonId);
                                  setIsDaemonSwitcherOpen(false);
                                }}
                                className={`w-full flex items-center justify-between gap-3 px-3 py-2 text-left text-xs font-mono hover:bg-primary/10 transition-colors ${
                                  isSelected ? "bg-primary/5" : ""
                                }`}
                              >
                                <span className="flex items-center gap-2 min-w-0">
                                  <Cloud className="w-3.5 h-3.5 flex-shrink-0 text-muted-foreground" />
                                  <span className="truncate">
                                    {d.hostname || hostnameFor(d.daemonId)}
                                  </span>
                                </span>
                                <span className="flex items-center gap-2 flex-shrink-0 text-muted-foreground">
                                  <span className="uppercase tracking-wide text-[10px]">
                                    {isCloud ? "cloud" : "local"}
                                  </span>
                                  <span
                                    className={`uppercase tracking-wide text-[10px] ${
                                      isActive
                                        ? "text-emerald-500"
                                        : "text-muted-foreground/70"
                                    }`}
                                  >
                                    {isActive ? "active" : "offline"}
                                  </span>
                                  {isSelected && (
                                    <Check className="w-3.5 h-3.5 text-primary" />
                                  )}
                                </span>
                              </button>
                            );
                          })}
                        </div>
                      )}
                    </div>
                  )}
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
                              Pull a GitHub repo onto{" "}
                              {selectedDaemon?.hostname || "this daemon"}
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
                      // Row state is computed against selectedDaemonId
                      // (the daemon whose perspective the user is viewing).
                      // When no daemon is selected (electron local-only),
                      // we treat all rows as "installed here" and just open.
                      const isInstalledHere =
                        !selectedDaemonId || installedProjectIds.has(project.id);
                      // Names of OTHER daemons that have a clone — used to
                      // turn "Only on another daemon" into "Only on <name>".
                      const otherHostNames = (hostsByProject.get(project.id) ?? [])
                        .filter((id) => id !== selectedDaemonId)
                        .map(hostnameFor);
                      // canReclone: project has a remote_url AND we have at
                      // least one cloud daemon to clone into (any cloud
                      // daemon; the per-row menu shows the choices).
                      const canReclone =
                        !isInstalledHere && !!project.remote_url && cloneTargets.length > 0;
                      // localOnlyOnOther: not on the selected daemon and no
                      // remote to re-clone from — lives only on the other
                      // daemon's filesystem.
                      const isLocalOnlyOnOther =
                        !isInstalledHere && !project.remote_url;
                      const isDisabled = isLocalOnlyOnOther;
                      const selectedHost =
                        selectedDaemon?.hostname ||
                        (selectedDaemonId ? hostnameFor(selectedDaemonId) : "this daemon");
                      const cloneMenuOpen = openCloneMenuFor === project.id;
                      return (
                        <div key={project.id} className="relative">
                          <button
                            onClick={() => handleProjectClick(project)}
                            onMouseEnter={() => setHoveredProjectId(project.id)}
                            onMouseLeave={() => setHoveredProjectId(null)}
                            disabled={isDisabled}
                            title={
                              isLocalOnlyOnOther
                                ? `This project's only clone is on ${
                                    otherHostNames.length
                                      ? otherHostNames.join(", ")
                                      : "another daemon"
                                  } and has no git remote — open it from there.`
                                : undefined
                            }
                            className={`group w-full px-2.5 py-2 rounded-md transition-all duration-150 font-mono text-left text-xs bg-transparent active:scale-[0.99] ${
                              isInstalledHere
                                ? "text-foreground/80 hover:text-foreground"
                                : "text-foreground/50 hover:text-foreground/70"
                            } ${isDisabled ? "cursor-not-allowed" : ""}`}
                            style={{
                              backgroundColor:
                                isHovered && !isDisabled
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
                                {canReclone && (
                                  <span className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] font-mono uppercase tracking-wide bg-muted/60 text-muted-foreground flex-shrink-0">
                                    Not on {selectedHost}
                                  </span>
                                )}
                                {isLocalOnlyOnOther && (
                                  <span className="inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[10px] font-mono uppercase tracking-wide bg-muted/60 text-muted-foreground flex-shrink-0">
                                    {otherHostNames.length > 0
                                      ? `Only on ${otherHostNames.join(", ")}`
                                      : "Only on another daemon"}
                                  </span>
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

                          {/* Per-row "Clone to <hostname>" menu. Opens when
                              user clicks a row that's recloneable. Lists
                              every ACTIVE cloud daemon as a target so the
                              user can pick the install location explicitly
                              instead of relying on an implicit "active
                              daemon" pointer. */}
                          {cloneMenuOpen && canReclone && (
                            <div
                              ref={cloneMenuRef}
                              className="absolute z-20 left-2 top-full mt-1 min-w-[14rem] rounded-md border border-border/60 bg-card shadow-lg overflow-hidden"
                            >
                              <div className="px-3 py-2 border-b border-border/40 text-[10px] font-mono uppercase tracking-wide text-muted-foreground">
                                Clone to
                              </div>
                              {cloneTargets.map((d) => (
                                <button
                                  key={d.daemonId}
                                  type="button"
                                  onClick={() => {
                                    setOpenCloneMenuFor(null);
                                    setCloneConfirm({
                                      project,
                                      daemonId: d.daemonId,
                                    });
                                  }}
                                  className="w-full flex items-center gap-2 px-3 py-2 text-left text-xs font-mono hover:bg-primary/10 transition-colors"
                                >
                                  <Cloud className="w-3.5 h-3.5 text-muted-foreground" />
                                  <span className="truncate">
                                    {d.hostname || hostnameFor(d.daemonId)}
                                  </span>
                                </button>
                              ))}
                            </div>
                          )}
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

      {/* Confirm re-clone for a project that exists but isn't on the
          chosen target daemon. The target is captured in cloneConfirm by
          the per-row "Clone to" menu so this modal names it explicitly
          instead of pointing at an implicit "active daemon". */}
      <Modal
        isOpen={!!cloneConfirm}
        onClose={() => setCloneConfirm(null)}
        title={`Clone ${cloneConfirm?.project.name ?? "project"}?`}
        size="md"
      >
        <div className="space-y-4">
          <p className="text-sm text-muted-foreground">
            This project isn&apos;t cloned on{" "}
            <span className="text-foreground font-medium">
              {cloneConfirm ? hostnameFor(cloneConfirm.daemonId) : "this daemon"}
            </span>{" "}
            yet. Reliant will clone it from{" "}
            <code className="font-mono text-xs">
              {cloneConfirm?.project.remote_url}
            </code>{" "}
            and open it here.
          </p>
          <div className="flex gap-3">
            <button
              type="button"
              onClick={() => setCloneConfirm(null)}
              className="flex-1 px-4 py-2.5 bg-muted hover:bg-muted/80 border border-border rounded-lg text-sm font-medium"
            >
              Cancel
            </button>
            <button
              type="button"
              onClick={() => void handleConfirmRowClone()}
              className="flex-1 px-4 py-2.5 bg-primary text-primary-foreground hover:bg-primary/90 rounded-lg text-sm font-semibold"
            >
              Clone and open
            </button>
          </div>
        </div>
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