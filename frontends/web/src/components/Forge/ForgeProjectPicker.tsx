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
 * The list is the project selector's panel (Projects/ProjectSelector, chat
 * mode: projects only), inline in the card, so it searches and scales like
 * every other picker.
 *
 * Selecting writes through projectStore.selectProject, the same call every
 * other project-scoped surface uses, so the choice is not forge-local: it
 * updates the app's current project and is persisted for the next restore.
 */


import { useProjectStore, type Project } from "@/store/projectStore";
import { useProjects } from "@/hooks/project-queries";
import { ProjectSelectorPanel } from "@/components/Projects/ProjectSelector";

export interface ForgeProjectPickerProps {
  /** Called after the store selection settles, so the layout can sync the URL. */
  onSelected?: (project: Project) => void;
}

const noop = () => {};

export function ForgeProjectPicker({ onSelected }: ForgeProjectPickerProps) {
  // The list read fetches on mount when the layout never did (a cold load
  // straight onto a /forge URL), so the picker never shows its empty state
  // to a user who does have projects.
  const { projects } = useProjects();
  const selectProject = useProjectStore((state) => state.selectProject);

  return (
    <div
      data-testid="forge-project-picker"
      className="mx-auto max-w-md overflow-hidden rounded-xl border border-border bg-surface"
    >
      <div className="p-4 pb-2">
        <h2 className="text-base font-semibold text-ink">Choose a project</h2>
        <p className="mt-1 text-xs text-ink-muted">
          The forge screens report on one project at a time. Pick the one you want to
          look at.
        </p>
      </div>

      {projects.length === 0 ? (
        <p className="px-4 pb-4 text-sm text-ink-muted">
          No projects yet. Create one from the main screen and it will show up here.
        </p>
      ) : (
        <ProjectSelectorPanel
          mode="chat"
          footer={false}
          onClose={noop}
          onSelect={(selection) => {
            if (selection.kind !== "project") return;
            // The row's project, as the store holds it (the list's entry).
            const project = projects.find((p) => p.id === selection.row.key) ?? selection.row.project;
            void (async () => {
              await selectProject(project);
              onSelected?.(project);
            })();
          }}
        />
      )}
    </div>
  );
}
