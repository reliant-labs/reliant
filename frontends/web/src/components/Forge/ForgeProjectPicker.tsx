// Copyright (c) 2025 Reliant Labs

/**
 * The project picker the forge screens fall back to when no project could be
 * resolved.
 *
 * WHY THIS EXISTS. The three forge screens used to render the sentence "Select
 * a project to see its forge release topology." and nothing else — no picker,
 * no link, no way back. On a hard refresh of /forge/topology that sentence was
 * the entire page, and it instructed the user to perform an action the page
 * offered no way to perform. ForgeLayout now exhausts every automatic
 * resolution path (see its comment) before rendering this, so reaching it means
 * the user genuinely has to choose — and here they can.
 *
 * Selecting writes through projectStore.selectProject, the same call every
 * other project-scoped surface uses, so the choice is not forge-local: it
 * updates the app's current project and is persisted for the next restore.
 */

import { useEffect } from "react";

import { useProjectStore, type Project } from "@/store/projectStore";

export interface ForgeProjectPickerProps {
  /** Called after the store selection settles, so the layout can sync the URL. */
  onSelected?: (project: Project) => void;
}

export function ForgeProjectPicker({ onSelected }: ForgeProjectPickerProps) {
  const projects = useProjectStore((state) => state.projects);
  const loadProjects = useProjectStore((state) => state.loadProjects);
  const selectProject = useProjectStore((state) => state.selectProject);

  // The layout only renders this once resolution has failed, which can mean the
  // list was never fetched (a cold load straight onto a /forge URL). Asking
  // again is cheap — loadProjects dedupes concurrent calls behind a shared
  // promise — and without it the picker could render its own empty state while
  // the user does have projects.
  useEffect(() => {
    if (projects.length === 0) void loadProjects();
  }, [projects.length, loadProjects]);

  return (
    <div
      data-testid="forge-project-picker"
      className="mx-auto max-w-md rounded-lg border border-border bg-card p-6"
    >
      <h2 className="text-base font-semibold text-foreground">Choose a project</h2>
      <p className="mt-1 text-xs text-muted-foreground">
        The forge screens report on one project at a time. Pick the one you want to
        look at.
      </p>

      {projects.length === 0 ? (
        <p className="mt-4 text-sm text-muted-foreground">
          No projects yet. Create one from the main screen and it will show up here.
        </p>
      ) : (
        <ul className="mt-4 space-y-1">
          {projects.map((project) => (
            <li key={project.id}>
              <button
                type="button"
                data-testid={`forge-project-option-${project.id}`}
                onClick={() => {
                  void (async () => {
                    await selectProject(project);
                    onSelected?.(project);
                  })();
                }}
                className="flex w-full flex-col items-start gap-0.5 rounded-md border border-border/60 bg-background px-3 py-2 text-left transition-colors hover:bg-muted/70"
              >
                <span className="text-sm font-medium text-foreground">{project.name}</span>
                {/* The path is an identifier, so it renders mono. */}
                <span className="font-mono text-xs text-muted-foreground">
                  {project.path}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
