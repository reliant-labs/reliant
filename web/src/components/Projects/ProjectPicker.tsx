import { useState, useEffect, memo, useMemo, useCallback } from "react";
import {
  FolderOpen,
  GitBranch,
  GitFork,
  Cloud,
  Loader2,
  ArrowLeft,
  Search,
  X,
  Pencil,
  Trash2,
  ChevronUp,
  ChevronDown,
  ChevronsUpDown,
} from "lucide-react";
import { ConnectError, Code } from "@connectrpc/connect";
import { useQuery } from "@tanstack/react-query";
import { useProjectStore } from "../../store/projectStore";
import type { Project as StoreProject } from "../../store/projectStore";
import { useApiKeySetupStore } from "../../store/apiKeySetupStore";
import { cn } from "../../lib/utils";
import { ProjectPickerModal } from "./ProjectPickerModal";
import { RemoveProjectsModal } from "./RemoveProjectsModal";
import { DirectoryPicker } from "./DirectoryPicker";
import { Modal } from "../ui/Modal";
import { RepoSelector } from "./RepoSelector";

import { toast } from "../../lib/toast-manager";
import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { useResumeDaemon } from "../../hooks/useOnboardingQueries";
import { ConnectDaemonModal } from "./ConnectDaemonModal";
// Aliased deliberately. There are TWO unrelated `DaemonStatus` enums in this
// app and their numeric values COLLIDE: registry ACTIVE=1 is control-plane
// PENDING=1, registry IDLE=2 is control-plane ACTIVE=2. This file reads both
// kinds of daemon, so importing either under the bare name `DaemonStatus` is
// how a control-plane row silently gets labelled with a registry name.
import { DaemonStatus as RegistryDaemonStatus } from "../../gen/reliant/v1/daemon_registry_pb";
import { useGitHubCredential } from "../../hooks/useGitHubCredential";
import { capabilities } from "../../services/controlPlane/capabilities";
import {
  listDaemons as listCloudDaemons,
  DAEMON_STATUS_ACTIVE,
  DAEMON_STATUS_SUSPENDED,
  type Daemon as CloudDaemon,
} from "../../services/controlPlane/daemon";
import { cloudDaemonStatusLabel } from "./cloudDaemonStatusLabel";
import { gitService } from "../../services/controlPlane/git";
import type { GitRepo } from "../../services/controlPlane/git";
import { projectGrpc } from "../../api/project-grpc";
import { cloudPathForRepo, repoNameFromUrl } from "../../lib/cloudProjectPath";

import { settingsSync, SETTINGS_KEYS } from "../../services/settingsSync";
import { GradientBackground } from "../GradientBackground";
import { BrandMark } from "../icons/BrandMark";

// Use the store's Project type so refs from useProjectStore.getState().projects
// flow through without lossy widening. ProjectPicker accepts Partial fields in
// a few code paths (existing project shape from the legacy callbacks) so we
// alias rather than redefine.
type Project = StoreProject;

type CloneTarget = {
  daemonId: string;
  hostname: string;
};

interface ProjectPickerProps {
  onProjectSelected: (project: Project) => void;
}

// Search only earns its space once the list is long enough to be worth
// filtering; below this it's pure noise. (Sorting lives in the column
// headers, which cost nothing extra.)
const LIST_CONTROLS_MIN_PROJECTS = 5;

type SortMode = "recent" | "name" | "path";
type SortDir = "asc" | "desc";

// Each sortable column's default direction. Recency wants newest-first, but
// text columns want A→Z, so the first click on a header shouldn't be a
// uniform "ascending".
const SORT_DEFAULT_DIR: Record<SortMode, SortDir> = {
  recent: "desc",
  name: "asc",
  path: "asc",
};

// Relative "last active" stamp. Deliberately coarse — in a project picker the
// useful signal is "today vs. last week", not a precise timestamp.
function formatLastActive(iso: string): string {
  const then = new Date(iso).getTime();
  if (!Number.isFinite(then)) return "—";
  const mins = Math.floor((Date.now() - then) / 60000);
  if (mins < 1) return "just now";
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  if (days < 7) return `${days}d ago`;
  const weeks = Math.floor(days / 7);
  if (weeks < 5) return `${weeks}w ago`;
  const months = Math.floor(days / 30);
  if (months < 12) return `${months}mo ago`;
  return `${Math.floor(days / 365)}y ago`;
}

// Collapse the user's home directory to "~". Matches both macOS (/Users/x)
// and Linux (/home/x) layouts, which is all the daemon ever reports.
function displayPath(path: string): string {
  return path.replace(/^\/(?:Users|home)\/[^/]+/, "~");
}

/**
 * Split a path for middle-ellipsis rendering.
 *
 * Long project paths used to overflow the row and push the name out of view.
 * CSS alone can only truncate at the end, which hides the leaf directory —
 * exactly the part that distinguishes two checkouts of the same repo. So we
 * hand the head to a `truncate` span and pin the last segment beside it,
 * giving "~/src/very/long/…/my-project".
 */
function splitPathForDisplay(path: string): { head: string; tail: string } {
  const shown = displayPath(path);
  const lastSlash = shown.lastIndexOf("/");
  if (lastSlash <= 0) return { head: "", tail: shown };
  return { head: shown.slice(0, lastSlash + 1), tail: shown.slice(lastSlash + 1) };
}

// Case-insensitive substring match over the fields a user would search by.
// Deliberately not fuzzy: with a handful of projects, substring matching is
// predictable and never surprises you with a "close" hit.
function projectMatchesQuery(project: Project, needle: string): boolean {
  if (!needle) return true;
  const haystack = `${project.name} ${displayPath(project.path)} ${project.path}`;
  return haystack.toLowerCase().includes(needle);
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

// A daemon's status rendered as a dot + sentence-case word, rather than the
// shouty uppercase monospace it used to be. Colour carries the meaning; the
// text is there to name it.
const DAEMON_STATUS_DOT: Record<string, string> = {
  active: "bg-emerald-500",
  starting: "bg-amber-500",
  resuming: "bg-amber-500",
  suspended: "bg-muted-foreground/50",
  failed: "bg-destructive",
  disconnected: "bg-destructive/70",
};

function DaemonStatusBadge({ label }: { label: string }) {
  const dot = DAEMON_STATUS_DOT[label] ?? "bg-muted-foreground/50";
  return (
    <span className="inline-flex items-center gap-1.5 rounded-full border border-border/60 bg-muted/40 px-2 py-0.5 text-xs font-medium capitalize text-muted-foreground">
      <span className={`h-1.5 w-1.5 shrink-0 rounded-full ${dot}`} aria-hidden="true" />
      {label}
    </span>
  );
}

// A sortable column header. Clicking the active column flips direction;
// clicking another switches to it. aria-sort keeps that legible to screen
// readers, which a styled <div> row never would.
function SortableHeader({
  mode,
  label,
  activeMode,
  dir,
  onSort,
}: {
  mode: SortMode;
  label: string;
  activeMode: SortMode;
  dir: SortDir;
  onSort: (mode: SortMode) => void;
}) {
  const isActive = mode === activeMode;
  return (
    <th
      scope="col"
      aria-sort={isActive ? (dir === "asc" ? "ascending" : "descending") : "none"}
      className="px-3 py-2 font-medium"
    >
      <button
        type="button"
        onClick={() => onSort(mode)}
        className={cn(
          "group inline-flex items-center gap-1 text-xs uppercase tracking-wide transition-colors",
          isActive
            ? "text-foreground"
            : "text-muted-foreground hover:text-foreground",
        )}
        data-testid={`project-sort-${mode}`}
      >
        {label}
        {isActive ? (
          dir === "asc" ? (
            <ChevronUp className="h-3 w-3" aria-hidden="true" />
          ) : (
            <ChevronDown className="h-3 w-3" aria-hidden="true" />
          )
        ) : (
          // Reserve the caret's space on inactive headers so the labels don't
          // shift horizontally when the sort column changes.
          <ChevronsUpDown
            className="h-3 w-3 opacity-0 transition-opacity group-hover:opacity-60"
            aria-hidden="true"
          />
        )}
      </button>
    </th>
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
                  <DaemonStatusBadge label={statusLabel} />
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
  const updateProject = useProjectStore((state) => state.updateProject);
  const deleteProject = useProjectStore((state) => state.deleteProject);
  const ensureApiKeyOrShowModal = useApiKeySetupStore((state) => state.ensureApiKeyOrShowModal);
  const [isCreateModalOpen, setIsCreateModalOpen] = useState(false);
  const [isDirectoryPickerOpen, setIsDirectoryPickerOpen] = useState(false);
  const [isCloneModalOpen, setIsCloneModalOpen] = useState(false);
  const [cloneStatus, setCloneStatus] = useState<string | null>(null);
  const [isOpenButtonHovered, setIsOpenButtonHovered] = useState(false);
  const [isCloneButtonHovered, setIsCloneButtonHovered] = useState(false);
  const [query, setQuery] = useState("");
  const [sortMode, setSortMode] = useState<SortMode>("recent");
  const [sortDir, setSortDir] = useState<SortDir>(SORT_DEFAULT_DIR.recent);
  // Management mode. Off by default so the picker stays a one-click "open a
  // project" surface; turning it on swaps row clicks from "open" to "select"
  // and reveals the rename / remove affordances.
  const [isManaging, setIsManaging] = useState(false);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [renameDraft, setRenameDraft] = useState("");
  // Projects staged for removal, pending confirmation. Empty means no prompt.
  const [pendingRemoval, setPendingRemoval] = useState<Project[]>([]);
  const [isRemoving, setIsRemoving] = useState(false);

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

  // Leaving management mode drops any selection and in-flight rename, so the
  // picker can't come back with stale checkboxes ticked.
  const exitManageMode = useCallback(() => {
    setIsManaging(false);
    setSelectedIds(new Set());
    setRenamingId(null);
  }, []);

  const toggleSelected = useCallback((projectId: string) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(projectId)) next.delete(projectId);
      else next.add(projectId);
      return next;
    });
  }, []);

  const beginRename = useCallback((project: Project) => {
    setRenamingId(project.id);
    setRenameDraft(project.name);
  }, []);

  const commitRename = useCallback(
    async (project: Project) => {
      const name = renameDraft.trim();
      setRenamingId(null);
      // A no-op or empty rename is a cancel, not an error — don't round-trip
      // to the server to set a project's name to what it already is.
      if (!name || name === project.name) return;
      try {
        await updateProject(project.id, { name });
      } catch (err) {
        console.error("Rename failed:", err);
        // updateProject surfaces its own error toast; reloading keeps the
        // list honest if the write partially applied.
        await loadProjects();
      }
    },
    [renameDraft, updateProject, loadProjects],
  );

  // Remove projects from Reliant. This deletes the project *record* only —
  // the checkout on disk is untouched (see RemoveProjectsModal's copy), which
  // is why a bulk remove is a safe thing to offer.
  const confirmRemoval = useCallback(async () => {
    if (pendingRemoval.length === 0) return;
    setIsRemoving(true);
    const failures: string[] = [];
    // Sequential rather than Promise.all: each delete mutates the shared
    // store slice, and a partial failure should still leave the successful
    // ones removed.
    for (const project of pendingRemoval) {
      try {
        await deleteProject(project.id);
      } catch (err) {
        console.error(`Failed to remove ${project.name}:`, err);
        failures.push(project.name);
      }
    }
    setIsRemoving(false);
    setPendingRemoval([]);
    setSelectedIds(new Set());
    if (failures.length > 0) {
      toast.error(`Could not remove: ${failures.join(", ")}`);
    }
    await loadProjects();
  }, [pendingRemoval, deleteProject, loadProjects]);

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
    // Registry rows here (from useDaemonStatus), so the registry enum is the
    // right one — see the aliased import above.
    const activeCloud = cloudDaemons.find(
      (d) => d.status === RegistryDaemonStatus.ACTIVE,
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

  // Sort projects by the active column. "recent" is the default because the
  // picker's job is usually "get me back where I was". Each comparator is
  // written ascending and flipped for "desc", so direction is one rule
  // rather than three.
  const sortedProjects = useMemo(() => {
    const ascending: Record<SortMode, (a: Project, b: Project) => number> = {
      recent: (a, b) =>
        new Date(a.last_active).getTime() - new Date(b.last_active).getTime(),
      name: (a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: "base" }),
      path: (a, b) =>
        displayPath(a.path).localeCompare(displayPath(b.path), undefined, {
          sensitivity: "base",
        }),
    };
    const compare = ascending[sortMode];
    const sign = sortDir === "asc" ? 1 : -1;
    return [...projects].sort((a, b) => sign * compare(a, b));
  }, [projects, sortMode, sortDir]);

  // Clicking the active column flips direction; clicking a new one switches
  // to it at that column's natural default.
  const handleSort = useCallback(
    (mode: SortMode) => {
      if (mode === sortMode) {
        setSortDir((d) => (d === "asc" ? "desc" : "asc"));
      } else {
        setSortMode(mode);
        setSortDir(SORT_DEFAULT_DIR[mode]);
      }
    },
    [sortMode],
  );

  const normalizedQuery = query.trim().toLowerCase();
  const isSearching = normalizedQuery.length > 0;

  // The list is never truncated: it scrolls instead. Hiding projects behind a
  // "View all" toggle made them unreachable and meant search had to special-
  // case the cap; showing everything in a scroll container is simpler and
  // strictly more useful.
  const displayedProjects = useMemo(
    () => sortedProjects.filter((p) => projectMatchesQuery(p, normalizedQuery)),
    [sortedProjects, normalizedQuery],
  );

  // The most recently active project — the "main app / workspace" the user
  // would expect to return to. Used by the "Back to <project>" affordance;
  // when there are no projects (genuine first-run) it's undefined and the
  // affordance hides. Computed independently of sortMode/query so changing
  // the list's sort or typing a search never retargets "Back to …".
  const mostRecentProject = useMemo(() => {
    let best: Project | undefined;
    for (const p of projects) {
      if (!best || new Date(p.last_active).getTime() > new Date(best.last_active).getTime()) {
        best = p;
      }
    }
    return best;
  }, [projects]);

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

      {/* Content. The scroll container is this element, not the page: the
          picker is mounted inside a fixed-height flex shell, so an
          unconstrained child would overflow off-screen with no way to reach
          it. `justify-center` (not `items-center`) keeps the card group
          optically centered when it fits, and lets it grow past the fold —
          scrolling instead of clipping — when it doesn't. */}
      <div className="relative z-10 h-full min-h-0 overflow-y-auto overscroll-contain">
        <div className="min-h-full flex flex-col justify-center px-6 py-12">
          <div className="w-full max-w-3xl mx-auto">
              {/* Header. "Back" sits above the brand and hard-left, where a
                  back affordance belongs — reading order puts the escape
                  hatch before the content it escapes from. It used to be
                  right-aligned on the brand row, which read as a primary
                  action and collided with the logo on narrow widths. */}
              <div className="mb-8">
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
                    className="-ml-2 mb-4 inline-flex items-center gap-1.5 rounded-md px-2 py-1.5 text-sm font-medium text-muted-foreground transition-colors hover:bg-muted/60 hover:text-foreground"
                    data-testid="project-picker-back-to-app"
                  >
                    <ArrowLeft className="w-4 h-4" />
                    Back to {mostRecentProject.name}
                  </button>
                )}
                <div className="flex items-center gap-4">
                  <BrandMark className="w-16 h-16" />
                  <h1 className="text-4xl font-bold text-foreground">Reliant</h1>
                </div>
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

              {/* Projects list — searchable, sortable, and independently
                  scrollable so a large project count can never push the rest
                  of the picker off-screen. */}
              {projects.length > 0 && (
                <div className="relative backdrop-blur-2xl bg-card/90 border border-border/50 rounded-2xl p-6">
                  <div className="flex flex-wrap items-center justify-between gap-3 mb-4">
                    <h2 className="text-base font-semibold text-foreground">
                      Projects
                      <span className="ml-2 text-sm font-normal text-muted-foreground">
                        {isSearching
                          ? `${displayedProjects.length} of ${projects.length}`
                          : projects.length}
                      </span>
                    </h2>
                    <button
                      type="button"
                      onClick={() => (isManaging ? exitManageMode() : setIsManaging(true))}
                      className="text-sm text-muted-foreground transition-colors hover:text-primary"
                      data-testid="project-manage-toggle"
                    >
                      {isManaging ? "Done" : "Manage"}
                    </button>
                  </div>

                  {/* Search + sort. Shown once the list is long enough to be
                      worth filtering; below that they'd be pure noise. */}
                  {projects.length >= LIST_CONTROLS_MIN_PROJECTS && (
                    <div className="flex flex-wrap items-center gap-2 mb-3">
                      <div className="relative min-w-0 flex-1">
                        <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground/70" />
                        <input
                          type="text"
                          value={query}
                          onChange={(e) => setQuery(e.target.value)}
                          placeholder="Search projects by name or path"
                          aria-label="Search projects"
                          className="w-full rounded-lg border border-border/60 bg-background/80 py-2 pl-9 pr-9 text-sm text-foreground placeholder:text-muted-foreground/70 transition-colors focus:border-primary focus:outline-none focus:ring-2 focus:ring-primary/30"
                          data-testid="project-search"
                        />
                        {query && (
                          <button
                            type="button"
                            onClick={() => setQuery("")}
                            aria-label="Clear search"
                            className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-1 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                          >
                            <X className="h-3.5 w-3.5" />
                          </button>
                        )}
                      </div>
                    </div>
                  )}

                  {/* Bulk action bar. Appears only once something is selected
                      — select-all itself lives in the table's header column,
                      so an always-present toolbar would just be chrome. */}
                  {isManaging && selectedIds.size > 0 && (
                    <div className="mb-3 flex flex-wrap items-center justify-between gap-3 rounded-lg border border-border/60 bg-muted/40 px-3 py-2">
                      <span className="text-sm text-muted-foreground">
                        {selectedIds.size} selected
                      </span>
                      <button
                        type="button"
                        onClick={() =>
                          setPendingRemoval(
                            projects.filter((p) => selectedIds.has(p.id)),
                          )
                        }
                        className="inline-flex items-center gap-1.5 rounded-md border border-destructive/40 px-2.5 py-1.5 text-sm font-medium text-destructive transition-colors hover:bg-destructive/10"
                        data-testid="project-bulk-remove"
                      >
                        <Trash2 className="h-3.5 w-3.5" />
                        Remove
                      </button>
                    </div>
                  )}

                  {displayedProjects.length === 0 ? (
                    <p className="py-6 text-center text-sm text-muted-foreground">
                      No projects match “{query.trim()}”.
                    </p>
                  ) : (
                    /* A real table: aligned columns, sortable headers, one row
                       per project. max-h caps it at roughly nine rows and the
                       header stays stuck to the top while the body scrolls, so
                       the column meanings never scroll away. */
                    <div className="max-h-[22rem] overflow-y-auto overscroll-contain rounded-lg border border-border/50">
                      <table className="w-full table-fixed border-collapse text-sm">
                        <colgroup>
                          {isManaging && <col className="w-10" />}
                          <col className="w-[38%]" />
                          <col />
                          <col className="w-28" />
                          {isManaging && <col className="w-20" />}
                        </colgroup>
                        <thead className="sticky top-0 z-10 bg-card">
                          <tr className="border-b border-border/60 text-left">
                            {isManaging && (
                              <th scope="col" className="px-3 py-2">
                                <input
                                  type="checkbox"
                                  className="h-4 w-4 align-middle accent-[hsl(var(--primary))]"
                                  aria-label="Select all projects"
                                  checked={
                                    displayedProjects.length > 0 &&
                                    displayedProjects.every((p) => selectedIds.has(p.id))
                                  }
                                  onChange={(e) => {
                                    // Select-all applies to what's currently
                                    // visible, so it composes with search
                                    // instead of silently selecting
                                    // filtered-out projects.
                                    const visible = displayedProjects.map((p) => p.id);
                                    setSelectedIds((prev) => {
                                      const next = new Set(prev);
                                      if (e.target.checked)
                                        visible.forEach((id) => next.add(id));
                                      else visible.forEach((id) => next.delete(id));
                                      return next;
                                    });
                                  }}
                                  data-testid="project-select-all"
                                />
                              </th>
                            )}
                            <SortableHeader
                              mode="name"
                              label="Name"
                              activeMode={sortMode}
                              dir={sortDir}
                              onSort={handleSort}
                            />
                            <SortableHeader
                              mode="path"
                              label="Location"
                              activeMode={sortMode}
                              dir={sortDir}
                              onSort={handleSort}
                            />
                            <SortableHeader
                              mode="recent"
                              label="Last used"
                              activeMode={sortMode}
                              dir={sortDir}
                              onSort={handleSort}
                            />
                            {isManaging && (
                              <th scope="col" className="px-3 py-2">
                                <span className="sr-only">Actions</span>
                              </th>
                            )}
                          </tr>
                        </thead>
                        <tbody>
                          {displayedProjects.map((project) => {
                            const isSelected = selectedIds.has(project.id);
                            const isRenaming = renamingId === project.id;
                            const { head, tail } = splitPathForDisplay(project.path);

                            return (
                              <tr
                                key={project.id}
                                className={cn(
                                  "border-b border-border/30 last:border-0 transition-colors",
                                  isSelected ? "bg-primary/10" : "hover:bg-primary/[0.06]",
                                  // Outside management mode the whole row opens
                                  // the project; inside it, it toggles
                                  // selection so a mis-click during cleanup
                                  // can't yank you into a workspace.
                                  !isRenaming && "cursor-pointer",
                                )}
                                onClick={() => {
                                  if (isRenaming) return;
                                  if (isManaging) toggleSelected(project.id);
                                  else handleProjectClick(project);
                                }}
                                data-testid="project-item"
                              >
                                {isManaging && (
                                  <td className="px-3 py-2 align-middle">
                                    <input
                                      type="checkbox"
                                      checked={isSelected}
                                      onChange={() => toggleSelected(project.id)}
                                      onClick={(e) => e.stopPropagation()}
                                      aria-label={`Select ${project.name}`}
                                      className="h-4 w-4 align-middle accent-[hsl(var(--primary))]"
                                      data-testid="project-select"
                                    />
                                  </td>
                                )}

                                <td className="px-3 py-2 align-middle">
                                  {isRenaming ? (
                                    <input
                                      autoFocus
                                      value={renameDraft}
                                      onChange={(e) => setRenameDraft(e.target.value)}
                                      onClick={(e) => e.stopPropagation()}
                                      onBlur={() => void commitRename(project)}
                                      onKeyDown={(e) => {
                                        if (e.key === "Enter") void commitRename(project);
                                        if (e.key === "Escape") setRenamingId(null);
                                      }}
                                      aria-label={`Rename ${project.name}`}
                                      className="w-full rounded border border-primary/60 bg-background px-2 py-1 text-sm text-foreground focus:outline-none focus:ring-2 focus:ring-primary/30"
                                      data-testid="project-rename-input"
                                    />
                                  ) : (
                                    <div className="flex items-center gap-2">
                                      <span
                                        className="truncate font-medium text-foreground"
                                        title={project.name}
                                      >
                                        {project.name}
                                      </span>
                                      {project.is_git_repo && (
                                        <GitBranch
                                          className="h-3 w-3 shrink-0 text-muted-foreground/50"
                                          aria-label="Git repository"
                                        />
                                      )}
                                    </div>
                                  )}
                                </td>

                                {/* Path, middle-truncated: the head collapses
                                    under pressure while the leaf directory is
                                    pinned, so two checkouts of the same repo
                                    stay distinguishable. */}
                                <td
                                  className="px-3 py-2 align-middle"
                                  title={displayPath(project.path)}
                                >
                                  <div className="flex min-w-0 items-baseline font-mono text-xs text-muted-foreground/80">
                                    <span className="truncate">{head}</span>
                                    <span className="shrink-0">{tail}</span>
                                  </div>
                                </td>

                                <td className="px-3 py-2 align-middle text-xs tabular-nums text-muted-foreground">
                                  {formatLastActive(project.last_active)}
                                </td>

                                {isManaging && (
                                  <td className="px-3 py-2 align-middle">
                                    <div className="flex items-center justify-end gap-0.5">
                                      <button
                                        type="button"
                                        onClick={(e) => {
                                          e.stopPropagation();
                                          beginRename(project);
                                        }}
                                        aria-label={`Rename ${project.name}`}
                                        className="rounded p-1.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                                        data-testid="project-rename"
                                      >
                                        <Pencil className="h-3.5 w-3.5" />
                                      </button>
                                      <button
                                        type="button"
                                        onClick={(e) => {
                                          e.stopPropagation();
                                          setPendingRemoval([project]);
                                        }}
                                        aria-label={`Remove ${project.name}`}
                                        className="rounded p-1.5 text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive"
                                        data-testid="project-remove"
                                      >
                                        <Trash2 className="h-3.5 w-3.5" />
                                      </button>
                                    </div>
                                  </td>
                                )}
                              </tr>
                            );
                          })}
                        </tbody>
                      </table>
                    </div>
                  )}
                </div>
              )}
            </div>
        </div>
      </div>

      {/* Remove one or many projects (record only — files stay on disk) */}
      <RemoveProjectsModal
        projects={pendingRemoval}
        isRemoving={isRemoving}
        onCancel={() => setPendingRemoval([])}
        onConfirm={() => void confirmRemoval()}
      />

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