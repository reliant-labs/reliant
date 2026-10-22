import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate } from "@tanstack/react-router";
import { AlertCircle, FolderPlus, Search } from "lucide-react";

import EmptyState from "@/components/forge-ui/empty_state";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { Button } from "@/components/ui/Button";
import { useNavigateToProjectPicker } from "@/hooks/useNavigateToProjectPicker";
import { logger } from "@/lib/logger";
import { toast } from "@/lib/toast-manager";
import { useProjectStore, type Project } from "@/store/projectStore";

import { RemoveProjectModal } from "../Projects/RemoveProjectModal";
import { SettingsPageHeader } from "./SettingsPageHeader";
import { CurrentProjectDetails } from "./projects/CurrentProjectDetails";
import { ProjectsTable } from "./projects/ProjectsTable";
import { projectMatchesQuery, sortProjectsForSettings } from "./projects/projectSource";

/** Below this many projects a filter box is clutter; above it, scanning is slow. */
const FILTER_THRESHOLD = 8;

/**
 * Settings → Projects: the list of every folder/repo Reliant can work in, and
 * the place to add or forget one. It is about the user's PROJECTS — plural —
 * with the current one summarised underneath, not a dashboard of the current
 * project (which is what this page used to be, and why a user with five
 * projects could only ever see one of them here).
 *
 * The project picker is the primary place to add a project; this is the
 * second. "Add project" deselects the current project, which is how the picker
 * is reached (see useNavigateToProjectPicker).
 */
export function ProjectsSection() {
  const navigate = useNavigate();
  const navigateToProjectPicker = useNavigateToProjectPicker();

  const projects = useProjectStore((state) => state.projects);
  const currentProject = useProjectStore((state) => state.currentProject);
  const isLoading = useProjectStore((state) => state.isLoading);
  const loadProjects = useProjectStore((state) => state.loadProjects);
  const selectProject = useProjectStore((state) => state.selectProject);
  const deleteProject = useProjectStore((state) => state.deleteProject);

  const [loadError, setLoadError] = useState<string | null>(null);
  const [hasLoaded, setHasLoaded] = useState(projects.length > 0);
  const [query, setQuery] = useState("");
  const [pendingRemoval, setPendingRemoval] = useState<Project | null>(null);
  const [isRemoving, setIsRemoving] = useState(false);
  const [openingProjectId, setOpeningProjectId] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoadError(null);
    try {
      await loadProjects();
    } catch (error) {
      setLoadError(error instanceof Error ? error.message : "Could not load projects");
    } finally {
      setHasLoaded(true);
    }
  }, [loadProjects]);

  useEffect(() => {
    void load();
  }, [load]);

  const sortedProjects = useMemo(
    () => sortProjectsForSettings(projects, currentProject?.id),
    [projects, currentProject?.id],
  );
  const visibleProjects = useMemo(
    () => sortedProjects.filter((project) => projectMatchesQuery(project, query)),
    [sortedProjects, query],
  );

  const goToProject = useCallback(
    (projectId: string) => {
      void navigate({ to: "/project/$projectId", params: { projectId }, search: {} });
    },
    [navigate],
  );

  const openProject = async (project: Project) => {
    setOpeningProjectId(project.id);
    try {
      // selectProject does not move the URL while the user is in Settings
      // (projectStore.syncProjectUrl), so leaving Settings is explicit here.
      if (project.id !== currentProject?.id) {
        await selectProject(project);
      }
      goToProject(project.id);
    } catch (error) {
      logger.error("[ProjectsSection] Failed to open project", error);
      toast.error(`Could not open ${project.name}`);
    } finally {
      setOpeningProjectId(null);
    }
  };

  const confirmRemoval = async () => {
    if (!pendingRemoval) return;
    setIsRemoving(true);
    try {
      await deleteProject(pendingRemoval.id);
      setPendingRemoval(null);
    } catch (error) {
      logger.error("[ProjectsSection] Failed to remove project", error);
      toast.error(`Could not remove ${pendingRemoval.name}`);
    } finally {
      setIsRemoving(false);
    }
  };

  const showSkeleton = projects.length === 0 && (!hasLoaded || isLoading) && !loadError;
  const showError = projects.length === 0 && !!loadError;
  const showEmpty = projects.length === 0 && hasLoaded && !isLoading && !loadError;
  const showFilter = projects.length > FILTER_THRESHOLD;

  return (
    <div className="forge-ui h-full overflow-auto bg-background">
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-6 px-6 py-8 lg:px-8">
        <SettingsPageHeader
          className="mb-0"
          title="Projects"
          description="Folders and repositories Reliant can work in. Removing a project forgets it and its project settings here — files on disk are never deleted."
          actions={
            <Button
              variant="primary"
              size="md"
              leftIcon={<FolderPlus className="h-4 w-4" />}
              onClick={navigateToProjectPicker}
              data-testid="settings-projects-add"
            >
              Add project
            </Button>
          }
        />

        {showSkeleton && (
          <div className="rounded-lg border border-border bg-card" data-testid="settings-projects-loading">
            <SkeletonLoader variant="table-row" count={3} />
          </div>
        )}

        {showError && (
          <div
            role="alert"
            className="flex flex-col items-start gap-3 rounded-lg border border-danger-border bg-danger-surface px-4 py-3 sm:flex-row sm:items-center sm:justify-between"
          >
            <div className="flex items-start gap-2.5">
              <AlertCircle className="mt-0.5 h-4 w-4 flex-shrink-0 text-danger" aria-hidden="true" />
              <div>
                <p className="text-sm font-medium text-danger-ink">Couldn’t load your projects</p>
                <p className="text-xs text-danger-ink/80">{loadError}</p>
              </div>
            </div>
            {/* secondary, not outline: inside .forge-ui `accent` is the primary
                colour, so outline's hover:bg-accent would flood on hover. */}
            <Button variant="secondary" size="sm" onClick={() => void load()}>
              Try again
            </Button>
          </div>
        )}

        {showEmpty && (
          <EmptyState
            icon={<FolderPlus className="h-6 w-6" />}
            title="No projects yet"
            description="Add a folder or clone a repository and Reliant can start working in it."
            actionLabel="Add project"
            onAction={navigateToProjectPicker}
          />
        )}

        {projects.length > 0 && (
          <section aria-labelledby="settings-projects-all" className="flex flex-col gap-3">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <h2 id="settings-projects-all" className="text-sm font-semibold text-foreground">
                All projects{" "}
                <span className="font-normal tabular-nums text-muted-foreground">{projects.length}</span>
              </h2>
              {showFilter && (
                <label className="relative block w-full sm:w-64">
                  <span className="sr-only">Filter projects</span>
                  <Search
                    className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground"
                    aria-hidden="true"
                  />
                  <input
                    type="search"
                    value={query}
                    onChange={(event) => setQuery(event.target.value)}
                    placeholder="Filter by name, path or remote"
                    className="h-8 w-full rounded-md border border-input bg-background pl-8 pr-2.5 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  />
                </label>
              )}
            </div>

            {visibleProjects.length === 0 ? (
              <p className="rounded-lg border border-dashed border-border px-4 py-6 text-center text-sm text-muted-foreground">
                No projects match “{query.trim()}”.
              </p>
            ) : (
              <ProjectsTable
                projects={visibleProjects}
                currentProjectId={currentProject?.id}
                onOpen={(project) => void openProject(project)}
                onRemove={setPendingRemoval}
                openingProjectId={openingProjectId}
              />
            )}
          </section>
        )}

        {currentProject && (
          <CurrentProjectDetails
            project={currentProject}
            onChatOpened={() => goToProject(currentProject.id)}
            onManageWorkspaces={() =>
              void navigate({ to: "/settings/$section", params: { section: "workspaces" } })
            }
          />
        )}
      </div>

      <RemoveProjectModal
        isOpen={!!pendingRemoval}
        project={pendingRemoval}
        isRemoving={isRemoving}
        onClose={() => {
          if (!isRemoving) setPendingRemoval(null);
        }}
        onConfirm={() => void confirmRemoval()}
      />
    </div>
  );
}
