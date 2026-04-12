import { useState, useEffect, memo, useMemo, useCallback } from "react";
import {
  FolderOpen,
  GitBranch,
  Check,
  Copy,
} from "lucide-react";
import { ConnectError, Code } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { useProjectStore } from "../../store/projectStore";
import { useApiKeySetupStore } from "../../store/apiKeySetupStore";
import { ProjectPickerModal } from "./ProjectPickerModal";
import { DirectoryPicker } from "./DirectoryPicker";

import { grpcClient } from "../../api/grpc-client";
import { toast } from "../../lib/toast-manager";
import { DaemonStatus, ListDaemonsRequestSchema } from "../../gen/reliant/v1/tools_daemon_pb";
import type { DaemonInfo } from "../../gen/reliant/v1/tools_daemon_pb";

import { settingsSync, SETTINGS_KEYS } from "../../services/settingsSync";
import { GradientBackground } from "../GradientBackground";

interface Project {
  id: string;
  name: string;
  path: string;
  worktree_count?: number;
  description?: string;
  is_git_repo?: boolean;
  created_at?: string;
  updated_at?: string;
}

interface ProjectPickerProps {
  onProjectSelected: (project: Project) => void;
}

function CopyableCommand({ command }: { command: string }) {
  const [copied, setCopied] = useState(false);

  const handleCopy = useCallback(async () => {
    try {
      await navigator.clipboard.writeText(command);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Fallback for non-secure contexts
      const textArea = document.createElement("textarea");
      textArea.value = command;
      document.body.appendChild(textArea);
      textArea.select();
      document.execCommand("copy");
      document.body.removeChild(textArea);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    }
  }, [command]);

  return (
    <button
      onClick={handleCopy}
      className="group/copy w-full flex items-center justify-between gap-3 px-4 py-3 rounded-lg bg-background/80 border border-border/50 hover:border-primary/30 transition-colors cursor-pointer"
    >
      <code className="text-sm font-mono text-foreground">{command}</code>
      {copied ? (
        <Check className="w-4 h-4 text-green-500 flex-shrink-0" />
      ) : (
        <Copy className="w-4 h-4 text-muted-foreground group-hover/copy:text-foreground flex-shrink-0 transition-colors" />
      )}
    </button>
  );
}

function useDaemonStatus() {
  const [daemons, setDaemons] = useState<DaemonInfo[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    let intervalId: ReturnType<typeof setInterval> | null = null;

    const check = async () => {
      try {
        const resp = await grpcClient.daemonRegistry().listDaemons(create(ListDaemonsRequestSchema));
        if (!cancelled) {
          setDaemons(resp.daemons);
          setLoading(false);
        }
      } catch {
        if (!cancelled) {
          setDaemons([]);
          setLoading(false);
        }
      }
    };

    const startPolling = () => {
      if (!intervalId) {
        intervalId = setInterval(check, 5000);
      }
    };

    const stopPolling = () => {
      if (intervalId) {
        clearInterval(intervalId);
        intervalId = null;
      }
    };

    const handleVisibility = () => {
      if (document.visibilityState === "visible") {
        check();
        startPolling();
      } else {
        stopPolling();
      }
    };

    check();
    startPolling();
    document.addEventListener("visibilitychange", handleVisibility);

    return () => {
      cancelled = true;
      stopPolling();
      document.removeEventListener("visibilitychange", handleVisibility);
    };
  }, []);

  const activeDaemon = daemons.find((d) => d.status === DaemonStatus.ACTIVE);
  return { daemons, activeDaemon, loading };
}

function DaemonConnectionInstructions() {
  return (
    <div className="relative backdrop-blur-2xl bg-card/90 border border-border/50 rounded-2xl mb-6 overflow-hidden p-6">
      <div className="flex items-center justify-between mb-1">
        <h3 className="text-lg font-semibold text-foreground">
          Connect a Project
        </h3>
      </div>
      <p className="text-sm text-muted-foreground mb-5">
        To continue, you'll need to connect our tools daemon to your code so Reliant can access your files and run commands.
      </p>

      <div className="space-y-4">
        {/* Step 1: Install */}
        <div className="flex items-start gap-3">
          <div className="flex-shrink-0 w-6 h-6 rounded-full bg-primary/15 flex items-center justify-center mt-0.5">
            <span className="text-xs font-bold text-primary">1</span>
          </div>
          <div className="flex-1 min-w-0">
            <p className="text-sm font-medium text-foreground mb-1.5">Install Reliant</p>
            <CopyableCommand command="brew install --cask reliant-labs/reliant/reliant" />
            <p className="text-xs text-muted-foreground mt-1.5">
              Or{" "}
              <a
                href="https://docs.reliantlabs.io/getting-started/installation#direct-download"
                target="_blank"
                rel="noopener noreferrer"
                className="text-primary hover:underline"
              >
                download directly
              </a>
            </p>
          </div>
        </div>

        {/* Step 2: Open project */}
        <div className="flex items-start gap-3">
          <div className="flex-shrink-0 w-6 h-6 rounded-full bg-primary/15 flex items-center justify-center mt-0.5">
            <span className="text-xs font-bold text-primary">2</span>
          </div>
          <div className="flex-1 min-w-0">
            <p className="text-sm font-medium text-foreground mb-1.5">
              Open your project
            </p>
            <CopyableCommand command="cd /your/project && reliant open ." />
            <p className="text-xs text-muted-foreground mt-1.5">
              This will sign you in, start the daemon, and connect your project.
            </p>
          </div>
        </div>
      </div>
    </div>
  );
}



function ProjectPickerComponent({ onProjectSelected }: ProjectPickerProps) {
  const projects = useProjectStore((state) => state.projects);
  const loadProjects = useProjectStore((state) => state.loadProjects);
  const createProject = useProjectStore((state) => state.createProject);
  const ensureApiKeyOrShowModal = useApiKeySetupStore((state) => state.ensureApiKeyOrShowModal);
  const [isCreateModalOpen, setIsCreateModalOpen] = useState(false);
  const [isDirectoryPickerOpen, setIsDirectoryPickerOpen] = useState(false);
  const [hoveredProjectId, setHoveredProjectId] = useState<string | null>(null);
  const [isOpenButtonHovered, setIsOpenButtonHovered] = useState(false);
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
    onProjectSelected(project);
  };

  const handleProjectCreated = async (createdProject?: Project) => {
    await loadProjects();
    setIsCreateModalOpen(false);

    // If a project was created, trigger the initialization check
    if (createdProject) {
      handleProjectClick(createdProject);
    }
  };

  const isElectron = !!window.electronAPI?.selectDirectory;
  const isWebMode = !isElectron;
  const { activeDaemon, loading: daemonLoading } = useDaemonStatus();
  const showConnectionInstructions = isWebMode && !activeDaemon && !daemonLoading;

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
          // Project might not be in our loaded list yet — reload and try again
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
                {/* Logo with original colors - no glow or border */}
                <svg
                  className="w-16 h-auto"
                  viewBox="0 0 1187.07 682.09"
                  xmlns="http://www.w3.org/2000/svg"
                >
                  <path
                    className="fill-[#0d8cff]"
                    d="M551.54,73.53l-98.13,98.13c-33.34-22.45-72.48-34.48-113.5-34.48-54.46,0-105.63,21.22-144.18,59.72-31.87,31.92-51.86,72.48-57.76,116.47H0c6.4-80.67,40.88-155.62,98.77-213.51C163.21,35.49,248.81,0,339.9,0c77.78,0,151.59,25.88,211.63,73.53Z"
                  />
                  <path
                    className="fill-[#0d8cff]"
                    d="M822.2,341.05l-241.13,241.13c-9.47,9.47-19.34,18.25-29.59,26.29-61.73,48.98-136.68,73.44-211.59,73.44-87.34,0-174.64-33.24-241.13-99.73C40.88,524.33,6.4,449.38,0,368.71h138.01c5.85,43.95,25.84,84.55,57.71,116.47,38.55,38.5,89.72,59.68,144.18,59.72,41.02,0,80.16-12.03,113.54-34.48,10.88-7.32,21.13-15.73,30.64-25.24l241.13-241.13,5.76,5.72,91.23,91.27Z"
                  />
                  <path
                    className="fill-[#6e39ff]"
                    d="M1187.07,368.71c-6.36,80.67-40.88,155.62-98.82,213.46-64.34,64.43-150.04,99.87-241.08,99.92-77.78,0-151.59-25.88-211.63-73.58l98.18-98.13c79.16,53.14,187.63,44.72,257.59-25.2,31.92-31.92,51.95-72.53,57.85-116.47h137.92Z"
                  />
                  <path
                    className="fill-[#6e39ff]"
                    d="M1187.07,313.38h-137.92c-5.9-43.99-25.93-84.55-57.85-116.47-38.5-38.5-89.67-59.72-144.14-59.72-41.02,0-80.21,12.03-113.5,34.48-10.88,7.32-21.13,15.73-30.68,25.24l-109.43,109.47-34.66,34.66-96.99,96.99-5.76-5.76-91.27-91.23,91.27-91.23,40.42-40.42,96.99-97.04,12.48-12.48c9.42-9.42,19.21-18.2,29.5-26.34C695.58,25.88,769.39,0,847.17,0c91.05,0,176.74,35.49,241.17,99.87,57.85,57.89,92.37,132.84,98.73,213.51Z"
                  />
                </svg>
                <h1 className="text-4xl font-bold text-foreground">Reliant</h1>
              </div>
              {showConnectionInstructions ? (
                <DaemonConnectionInstructions />
              ) : (
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
                </div>
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
                      return (
                        <button
                          key={project.id}
                          onClick={() => handleProjectClick(project)}
                          onMouseEnter={() => setHoveredProjectId(project.id)}
                          onMouseLeave={() => setHoveredProjectId(null)}
                          className="group w-full px-2.5 py-2 rounded-md transition-all duration-150 font-mono text-left text-xs bg-transparent text-foreground/80 hover:text-foreground active:scale-[0.99]"
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
                              <span className="font-mono font-medium truncate group-hover:text-foreground transition-colors duration-200">
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
    </div>
  );
}

export const ProjectPicker = memo(ProjectPickerComponent);