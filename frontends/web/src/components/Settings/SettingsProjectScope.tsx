import { useEffect, useId, useRef } from "react";
import { useNavigate } from "@tanstack/react-router";
import { FolderGit2 } from "lucide-react";
import { useProjects } from "../../hooks/project-queries";
import { useProjectStore } from "../../store/projectStore";

/**
 * The project a project-scoped settings page (Prompts, MCP servers, Branches)
 * applies to, shown in its header and switchable there.
 *
 * Those pages read the app's current project. Opened from a deep link or a
 * refresh, nothing had chosen one yet, so they sat inert — Prompts' New was
 * silently disabled and Branches said "Open a project" with no way to. This
 * picks the user's last project (else the most recent) when none is open,
 * and switching here opens that project, exactly as picking it in the
 * sidebar does: there is one current project, and these pages follow it.
 */
export function SettingsProjectScope({ hint }: { /** What the scope decides, for the select's description. */ hint: string }) {
  const id = useId();
  const navigate = useNavigate();
  const { projects, isLoading, error } = useProjects();
  const currentProject = useProjectStore((state) => state.currentProject);
  const selectProject = useProjectStore((state) => state.selectProject);
  const restoreLastProject = useProjectStore((state) => state.restoreLastProject);

  // Once per mount: a page opened with no project picks the last one.
  const restored = useRef(false);
  useEffect(() => {
    if (restored.current || currentProject || isLoading || projects.length === 0) return;
    restored.current = true;
    void restoreLastProject();
  }, [currentProject, isLoading, projects.length, restoreLastProject]);

  if (isLoading) {
    return <span className="text-xs text-muted-foreground">Loading projects…</span>;
  }

  if (error && projects.length === 0) {
    return <span className="text-xs text-destructive-ink">Couldn't load your projects.</span>;
  }

  if (projects.length === 0) {
    return (
      <span className="inline-flex items-center gap-2 text-xs text-muted-foreground">
        No projects yet.
        <button
          type="button"
          onClick={() => void navigate({ to: "/settings/$section", params: { section: "projects" } })}
          className="font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring rounded-sm"
        >
          Add a project
        </button>
      </span>
    );
  }

  return (
    <span className="inline-flex items-center gap-2">
      <FolderGit2 className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
      <label htmlFor={id} className="text-xs font-medium text-foreground">
        Project
      </label>
      <select
        id={id}
        value={currentProject?.id ?? ""}
        title={hint}
        onChange={(event) => {
          const project = projects.find((p) => p.id === event.target.value);
          if (project) void selectProject(project);
        }}
        className="h-8 max-w-[16rem] truncate rounded-md border border-border bg-background px-2 text-xs text-foreground focus:outline-none focus:ring-2 focus:ring-ring"
      >
        {!currentProject && (
          <option value="" disabled>
            Pick a project
          </option>
        )}
        {projects.map((project) => (
          <option key={project.id} value={project.id}>
            {project.name}
          </option>
        ))}
      </select>
    </span>
  );
}
