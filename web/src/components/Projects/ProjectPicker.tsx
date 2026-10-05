import { useState, useEffect, memo, useMemo, useCallback, type ReactNode } from "react";
import { ArrowLeft, FolderOpen, FolderPlus, GitFork, Loader2, Search, X } from "lucide-react";
import { ConnectError, Code } from "@connectrpc/connect";

import { useProjectStore } from "../../store/projectStore";
import type { Project as StoreProject } from "../../store/projectStore";
import { useApiKeySetupStore } from "../../store/apiKeySetupStore";
import { cn } from "../../lib/utils";
import { basename } from "../../lib/pathUtils";
import { toast } from "../../lib/toast-manager";
import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { useGitHubCredential } from "../../hooks/useGitHubCredential";
import { capabilities } from "../../services/controlPlane/capabilities";
import type { GitRepo } from "../../services/controlPlane/git";
import { projectGrpc } from "../../api/project-grpc";
import { cloudPathForRepo, repoNameFromUrl } from "../../lib/cloudProjectPath";
import { settingsSync, SETTINGS_KEYS } from "../../services/settingsSync";
import { Modal } from "../ui/Modal";
import { Tooltip } from "../ui/Tooltip";

import { ProjectPickerModal } from "./ProjectPickerModal";
import { RemoveProjectsModal } from "./RemoveProjectsModal";
import { DirectoryPicker } from "./DirectoryPicker";
import { RepoSelector } from "./RepoSelector";
import { CloneTargetPicker } from "./CloneTargetPicker";
import { addProjectLead } from "./addProjectActions";
import { cloneAvailability, pickCloneTarget, cloneDescription } from "./cloneTargets";
import {
  SORT_DEFAULT_DIR,
  isCloudDaemon,
  projectMatchesQuery,
  sortProjects,
  type SortDir,
  type SortMode,
} from "./picker/format";
import { pickerButton } from "./picker/buttonStyles";
import type { AddProjectAction } from "./picker/addProjectActionModel";
import { HeaderActions } from "./picker/HeaderActions";
import { StartOptions } from "./picker/StartOptions";
import { ProjectTable } from "./picker/ProjectTable";
import {
  MachineStatusPending,
  MachineStatusStrip,
  NoActiveMachinePanel,
} from "./picker/MachineStatus";

/**
 * The project picker — the full page shown when no project is selected
 * (ModernApp renders it under the app Header, which owns the Electron title
 * bar and drag region, so this page needs none of its own).
 *
 * Layout, top to bottom, in forge's vocabulary:
 *
 *   header        "Projects" + one line, add-project actions on the right
 *   machine       one status strip — or, with no active machine, a panel
 *                 with the actions each machine supports
 *   projects      toolbar (search, count, select) above a sortable table;
 *                 with zero projects, a start panel offering each way in
 *
 * The page is a fixed-height column: the table is the only thing that
 * scrolls when the list is long, so the header and toolbar never leave view.
 */

// Use the store's Project type so refs from useProjectStore.getState().projects
// flow through without lossy widening.
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

function isAlreadyExistsError(error: unknown): boolean {
  return (
    (error instanceof ConnectError && error.code === Code.AlreadyExists) ||
    (error instanceof Error &&
      (error.message.includes("already exists") || error.message.includes("409")))
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
  const [query, setQuery] = useState("");
  const [sortMode, setSortMode] = useState<SortMode>("recent");
  const [sortDir, setSortDir] = useState<SortDir>(SORT_DEFAULT_DIR.recent);
  // Selection mode, for bulk remove. Off by default so the picker stays a
  // one-click "open a project" surface; turning it on swaps row clicks from
  // "open" to "select". Single-row rename / remove don't need it — they sit on
  // each row.
  const [isSelecting, setIsSelecting] = useState(false);
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

  // Leaving selection mode drops any selection and in-flight rename, so the
  // picker can't come back with stale checkboxes ticked.
  const exitSelectMode = useCallback(() => {
    setIsSelecting(false);
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

  const setSelected = useCallback((projectIds: string[], selected: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      for (const id of projectIds) {
        if (selected) next.add(id);
        else next.delete(id);
      }
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
  // Cloud machines are the only valid clone targets — cloning requires a
  // managed machine the control plane can reach.
  const cloudDaemons = useMemo(
    () => daemons.filter((d) => isCloudDaemon(d.daemonType)),
    [daemons],
  );
  // Hostname lookup for naming machines in the clone status toast. Falls back
  // to a short id slice when the row hasn't loaded yet.
  const hostnameFor = useCallback(
    (daemonId: string) => {
      const d = daemons.find((x) => x.daemonId === daemonId);
      return d?.hostname || `daemon ${daemonId.slice(0, 8)}`;
    },
    [daemons],
  );

  // The cloud machine a clone installs onto by default: the most recently
  // used one (pickCloneTarget). CloneTargetPicker lets the user change it.
  const selectedCloneDaemon = useMemo<CloneTarget | null>(() => {
    const preferred = pickCloneTarget(cloudDaemons);
    if (!preferred) return null;
    return {
      daemonId: preferred.daemonId,
      hostname: preferred.hostname || hostnameFor(preferred.daemonId),
    };
  }, [cloudDaemons, hostnameFor]);

  // The machine the NEXT clone will use. Null means "whatever the default
  // resolves to"; a string means the user chose explicitly in the modal, and
  // that choice must win over the default for as long as the modal is open.
  const [chosenCloneDaemonId, setChosenCloneDaemonId] = useState<string | null>(null);

  // "Clone" is VISIBLE whenever the account can have cloud machines at all,
  // and merely DISABLED (carrying the reason) when none can take a clone right
  // now. Hiding it is what produced the reported dead end: with every machine
  // FAILED there was no add-project entry point anywhere in the app.
  const cloneState = useMemo(() => cloneAvailability(cloudDaemons), [cloudDaemons]);
  const showCloneAction = capabilities.cloudDaemons;

  // Which add-project action leads. A cloud user's code is never on the
  // browser host's filesystem, so leading them at the directory picker sends
  // them somewhere that cannot work; a local-machine user's filesystem IS
  // theirs. See addProjectActions.ts.
  const cloneLeads =
    showCloneAction &&
    addProjectLead({
      hasCloudDaemons: cloudDaemons.length > 0,
      activeDaemonType: activeDaemon?.daemonType,
    }) === "clone";

  // Which machine the next clone actually lands on: the user's explicit
  // choice when they made one, else the recency default, else any machine
  // that will eventually drain the queue.
  const effectiveCloneDaemonId =
    chosenCloneDaemonId ??
    selectedCloneDaemon?.daemonId ??
    (cloneState.kind === "ready" ? cloneState.target.daemonId : null);

  // Add a repo as a project on a target machine, in ONE server call.
  // CreateProjectFromRepo owns clone + create + install server-side, so this
  // function's whole job is to ask, report, and open.
  //
  // It returns as soon as the clone is QUEUED: the machine may still be
  // asleep, so the checkout does not exist yet and the copy must not pretend
  // it does. The real outcome arrives over the updates stream.
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
      setCloneStatus(`Queueing ${projectName} for ${targetHost}...`);

      const result = await projectGrpc.createProjectFromRepo({
        cloneUrl: repo.cloneUrl,
        daemonId: targetDaemonId,
        name: projectName,
        branch,
        path: destinationPath,
      });

      if (result.queued) {
        const machine = result.daemonName || targetHost;
        toast.info(`Queued — ${projectName} will clone when ${machine} is ready`);
      }

      setCloneStatus(`Opening ${projectName}...`);
      await loadProjects();
      if (result.project) {
        // Re-read from the store so the opened project is the full
        // StoreProject shape (last_active, remote_url), not the narrower
        // one the RPC returns.
        const opened =
          useProjectStore.getState().projects.find((p) => p.id === result.project!.id);
        if (opened) {
          await selectProject(opened);
          onProjectSelected(opened);
        }
      }
      setCloneStatus(null);
    },
    [hostnameFor, loadProjects, onProjectSelected, selectProject],
  );

  const handleRepoSelectedFromModal = async (repo: GitRepo) => {
    setIsCloneModalOpen(false);
    // The machine the user chose in the modal, or the recency default. A
    // clone onto a still-starting machine is valid — it just lands later.
    const targetDaemonId = effectiveCloneDaemonId;
    if (!targetDaemonId) {
      toast.error(
        cloneState.kind === "blocked" ? cloneState.reason : "No machine available to clone onto",
      );
      return;
    }
    const projectName = repoNameFromUrl(repo.cloneUrl) || repo.fullName;
    const destinationPath = cloudPathForRepo(repo);
    const loadingToast = toast.loading(`Queueing "${projectName}"...`);
    try {
      await cloneAndOpen(repo, destinationPath, projectName, targetDaemonId);
    } catch (err) {
      console.error("Clone-from-modal failed:", err);
      toast.error(err instanceof Error ? err.message : "Failed to clone repository");
      setCloneStatus(null);
    } finally {
      toast.dismiss(loadingToast);
    }
  };

  // Create (or reopen) a project for a folder the user picked, by either the
  // native Electron dialog or the in-app DirectoryPicker.
  const openFolderAsProject = async (selectedPath: string) => {
    const projectName = basename(selectedPath) || selectedPath || "Untitled Project";
    const projectData = {
      name: projectName,
      path: selectedPath,
      description: "",
      is_git_repo: false, // Determined by the backend
      default_branch: "main",
    };

    const loadingToast = toast.loading(`Opening project "${projectName}"...`);
    try {
      const createdProject = await createProject(projectData);
      toast.dismiss(loadingToast);
      // Reload to pick up initialization status
      await loadProjects();
      if (createdProject) {
        handleProjectClick(createdProject);
      }
    } catch (error) {
      toast.dismiss(loadingToast);
      // A project already registered at this path: open it instead.
      if (isAlreadyExistsError(error)) {
        const existing = projects.find((p) => p.path === selectedPath);
        if (existing) {
          toast.success(`Opening existing project "${existing.name}"`);
          handleProjectClick(existing);
          return;
        }
        // Might not be in our loaded list yet; reload and try again
        await loadProjects();
        const found = useProjectStore.getState().projects.find((p) => p.path === selectedPath);
        if (found) {
          toast.success(`Opening existing project "${found.name}"`);
          handleProjectClick(found);
          return;
        }
      }
      console.error("Failed to create project:", error);
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
    if (selectedPath) await openFolderAsProject(selectedPath);
  };

  // The add-project actions, in lead order. "Open folder" and "New project"
  // are dropped only when there is no filesystem to reach at all (web mode
  // with no attached machine); "Clone" stays whenever the account can have
  // cloud machines, disabled with a reason when none can take a clone.
  // Recomputed every render rather than memoized: it is a handful of object
  // literals, and memoizing would freeze handleOpenExistingProject's closure
  // over a stale project list.
  const addProjectActions: AddProjectAction[] = (() => {
    const clone: AddProjectAction | null = showCloneAction
      ? {
          kind: "clone",
          label: cloneLeads ? "Clone from GitHub" : "Clone repository",
          description: cloneDescription({
            cloneState,
            hasGitHubCredential,
            fallbackHost: selectedCloneDaemon?.hostname,
          }),
          icon: GitFork,
          onClick: () => setIsCloneModalOpen(true),
          disabled: cloneState.kind === "blocked",
          lead: cloneLeads,
          testId: "project-picker-clone-repo",
        }
      : null;
    const machineReachable = !showConnectionInstructions;
    const open: AddProjectAction | null = machineReachable
      ? {
          kind: "open",
          label: "Open folder",
          description: isElectron
            ? "Choose a folder on this computer and open it as a project."
            : "Browse to a folder on your machine and open it as a project.",
          icon: FolderOpen,
          onClick: () => void handleOpenExistingProject(),
          disabled: false,
          lead: !cloneLeads,
          testId: "project-picker-open-folder",
        }
      : null;
    const create: AddProjectAction | null = machineReachable
      ? {
          kind: "new",
          label: "New project",
          description: "Add a folder by its full path, with a name and default branch.",
          icon: FolderPlus,
          onClick: () => setIsCreateModalOpen(true),
          disabled: false,
          lead: false,
          testId: "project-picker-new-project",
        }
      : null;
    const ordered = cloneLeads ? [clone, open, create] : [open, clone, create];
    return ordered.filter((a): a is AddProjectAction => a !== null);
  })();

  const sortedProjects = useMemo(
    () => sortProjects(projects, sortMode, sortDir),
    [projects, sortMode, sortDir],
  );

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

  // The list is never truncated: it scrolls instead.
  const displayedProjects = useMemo(
    () => sortedProjects.filter((p) => projectMatchesQuery(p, normalizedQuery)),
    [sortedProjects, normalizedQuery],
  );

  // The most recently active project — the workspace the user would expect
  // to return to. Computed independently of sort/search so neither retargets
  // "Back to …". Undefined on a genuine first run, which hides the affordance.
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
  // workspace/chat shell, through the same path as a row click.
  const handleBackToApp = useCallback(() => {
    if (mostRecentProject) handleProjectClick(mostRecentProject);
    // handleProjectClick is a stable-enough closure over store setters; the
    // picker re-renders on project changes so we intentionally key only on
    // the target project.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mostRecentProject]);

  const hasProjects = projects.length > 0;

  let machineSection: ReactNode;
  if (showConnectionInstructions) {
    machineSection = <NoActiveMachinePanel />;
  } else if (activeDaemon) {
    machineSection = (
      <MachineStatusStrip hostname={activeDaemon.hostname} daemonType={activeDaemon.daemonType} />
    );
  } else if (daemonLoading) {
    machineSection = <MachineStatusPending message="Checking your machine…" />;
  } else {
    // Electron with its bundled machine not attached yet — it starts with the
    // app, so this is a wait, not an action.
    machineSection = (
      <MachineStatusPending message="Waiting for the machine on this computer to connect…" />
    );
  }

  return (
    <div className="forge-ui h-full bg-background" data-testid="project-picker">
      {/* The page scrolls only when the window is too short for the column's
          minimum height; otherwise the table is the one scrolling region. */}
      <div className="h-full overflow-y-auto overscroll-contain">
        <div className="mx-auto flex h-full min-h-[34rem] w-full max-w-5xl flex-col gap-6 px-6 py-8">
          <header className="flex shrink-0 flex-col gap-3">
            {/* Back to the active workspace. The picker is reached by
                deselecting the current project; re-selecting the most
                recently active one restores the workspace shell. */}
            {mostRecentProject && (
              <button
                type="button"
                onClick={handleBackToApp}
                className={pickerButton("ghost", "sm", "-ml-2.5 self-start")}
                data-testid="project-picker-back-to-app"
              >
                <ArrowLeft className="h-3.5 w-3.5" aria-hidden="true" />
                Back to {mostRecentProject.name}
              </button>
            )}
            <div className="flex flex-wrap items-end justify-between gap-x-6 gap-y-3">
              <div className="min-w-0">
                <h1 className="text-balance text-2xl font-semibold tracking-tight text-ink">
                  Projects
                </h1>
                <p className="mt-1 text-pretty text-sm text-ink-muted">
                  {hasProjects
                    ? "Repositories and folders Reliant works in. Open one to pick up where you left off."
                    : "Add a repository or folder for Reliant to work in."}
                </p>
              </div>
              {/* With no projects the start panel below carries these same
                  actions; a second copy up here would only compete with it. */}
              {hasProjects && addProjectActions.length > 0 && (
                <HeaderActions actions={addProjectActions} />
              )}
            </div>
          </header>

          <div className="shrink-0">{machineSection}</div>

          {hasProjects ? (
            <section
              className="flex min-h-0 flex-1 flex-col gap-3"
              aria-labelledby="project-picker-list-title"
            >
              <h2 id="project-picker-list-title" className="sr-only">
                Your projects
              </h2>
              <div className="flex shrink-0 flex-wrap items-center gap-2">
                {/* Search is shown once the list is long enough to be worth
                    filtering. The spacer keeps the right cluster right-aligned
                    when it's absent. */}
                {projects.length >= LIST_CONTROLS_MIN_PROJECTS ? (
                  <div className="relative min-w-[12rem] max-w-sm flex-1">
                    <Search
                      className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-subtle"
                      aria-hidden="true"
                    />
                    <input
                      type="search"
                      value={query}
                      onChange={(e) => setQuery(e.target.value)}
                      placeholder="Search by name or path"
                      aria-label="Search projects"
                      className="h-8 w-full rounded-md border border-border-strong bg-background pl-8 pr-8 text-sm text-ink placeholder:text-ink-subtle focus:border-accent focus:outline-none focus:ring-2 focus:ring-accent/30 [&::-webkit-search-cancel-button]:hidden"
                      data-testid="project-search"
                    />
                    {query && (
                      <Tooltip
                        content="Clear search"
                        placement="top"
                        delay={300}
                        wrapperClassName="absolute right-1 top-1/2 -translate-y-1/2"
                      >
                        <button
                          type="button"
                          onClick={() => setQuery("")}
                          aria-label="Clear search"
                          className="inline-flex h-6 w-6 items-center justify-center rounded text-ink-muted transition-colors hover:bg-muted hover:text-ink"
                        >
                          <X className="h-3.5 w-3.5" aria-hidden="true" />
                        </button>
                      </Tooltip>
                    )}
                  </div>
                ) : null}
                <div className="ml-auto flex items-center gap-2">
                  {isSelecting ? (
                    selectedIds.size > 0 ? (
                      <>
                        <span className="text-xs tabular-nums text-ink-muted">
                          {selectedIds.size} selected
                        </span>
                        <button
                          type="button"
                          onClick={() =>
                            setPendingRemoval(projects.filter((p) => selectedIds.has(p.id)))
                          }
                          className={pickerButton("danger", "sm")}
                          data-testid="project-bulk-remove"
                        >
                          Remove
                        </button>
                      </>
                    ) : (
                      <span className="text-xs text-ink-muted">Select projects to remove</span>
                    )
                  ) : (
                    <span className="text-xs tabular-nums text-ink-muted">
                      {isSearching
                        ? `${displayedProjects.length} of ${projects.length}`
                        : `${projects.length} ${projects.length === 1 ? "project" : "projects"}`}
                    </span>
                  )}
                  <button
                    type="button"
                    onClick={() => (isSelecting ? exitSelectMode() : setIsSelecting(true))}
                    aria-pressed={isSelecting}
                    className={pickerButton(isSelecting ? "secondary" : "ghost", "sm")}
                    data-testid="project-manage-toggle"
                  >
                    {isSelecting ? "Done" : "Select"}
                  </button>
                </div>
              </div>

              {displayedProjects.length === 0 ? (
                <div className="rounded-lg border border-dashed border-border-strong px-6 py-10 text-center">
                  <p className="text-sm text-ink">No projects match “{query.trim()}”.</p>
                  <button
                    type="button"
                    onClick={() => setQuery("")}
                    className={pickerButton("ghost", "sm", "mt-2")}
                  >
                    Clear search
                  </button>
                </div>
              ) : (
                // Natural height up to the space left in the column, then it
                // scrolls (min-h-0 lets the flex item shrink below content).
                <ProjectTable
                  projects={displayedProjects}
                  sortMode={sortMode}
                  sortDir={sortDir}
                  onSort={handleSort}
                  onOpen={handleProjectClick}
                  isSelecting={isSelecting}
                  selectedIds={selectedIds}
                  onToggleSelected={toggleSelected}
                  onSetSelected={setSelected}
                  renamingId={renamingId}
                  renameDraft={renameDraft}
                  onRenameDraftChange={setRenameDraft}
                  onBeginRename={beginRename}
                  onCommitRename={(project) => void commitRename(project)}
                  onCancelRename={() => setRenamingId(null)}
                  onRemove={(project) => setPendingRemoval([project])}
                />
              )}
            </section>
          ) : (
            <StartOptions actions={addProjectActions} />
          )}
        </div>
      </div>

      {/* Remove one or many projects (record only — files stay on disk) */}
      <RemoveProjectsModal
        projects={pendingRemoval}
        isRemoving={isRemoving}
        onCancel={() => setPendingRemoval([])}
        onConfirm={() => void confirmRemoval()}
      />

      <ProjectPickerModal
        isOpen={isCreateModalOpen}
        onClose={() => setIsCreateModalOpen(false)}
        onProjectCreated={handleProjectCreated}
      />

      {/* Directory Picker (browser mode) */}
      <DirectoryPicker
        isOpen={isDirectoryPickerOpen}
        onClose={() => setIsDirectoryPickerOpen(false)}
        onSelect={(path) => void openFolderAsProject(path)}
      />

      {/* Clone a GitHub repo onto a cloud machine */}
      <Modal
        isOpen={isCloneModalOpen}
        onClose={() => setIsCloneModalOpen(false)}
        title="Clone a repository"
        size="lg"
      >
        {/* The target comes FIRST: which machine the checkout lands on is a
            decision about the clone, and discovering it after picking a repo
            is the wrong order. Renders nothing when there is only one
            candidate. */}
        <CloneTargetPicker
          daemons={cloudDaemons}
          selectedDaemonId={effectiveCloneDaemonId}
          onSelect={setChosenCloneDaemonId}
        />
        <RepoSelector
          onSelect={(repo) => {
            void handleRepoSelectedFromModal(repo);
          }}
          analyticsPhase="project_picker"
        />
      </Modal>

      {cloneStatus && (
        <div
          role="status"
          className={cn(
            "fixed bottom-4 left-1/2 z-50 flex -translate-x-1/2 items-center gap-2",
            "rounded-lg border border-border bg-card px-3 py-2 text-sm text-ink shadow-lg",
          )}
        >
          <Loader2 className="h-3.5 w-3.5 animate-spin" aria-hidden="true" />
          {cloneStatus}
        </div>
      )}
    </div>
  );
}

export const ProjectPicker = memo(ProjectPickerComponent);
