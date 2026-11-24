import Button from "@/components/forge-ui/button";
import type { Project } from "@/store/projectStore";

/**
 * The danger zone that moves a project to another organization
 * (MoveProjectOrgDialog). Shared by Settings → Projects and a project page's
 * Settings tab; the caller opens the dialog and shows this only when the
 * person has a second org to move to.
 */
export function ProjectMoveSection({ project, onMove }: { project: Project; onMove: () => void }) {
  return (
    <section
      aria-labelledby="settings-projects-danger"
      className="flex flex-col gap-3 rounded-lg border border-danger-border px-4 py-3"
      data-testid="settings-projects-danger-zone"
    >
      <h2 id="settings-projects-danger" className="text-sm font-semibold text-foreground">
        Danger zone
      </h2>
      <div className="flex flex-col items-start gap-3 sm:flex-row sm:items-center sm:justify-between">
        <div className="min-w-0">
          <p className="text-sm font-medium text-foreground">Move {project.name} to another organization</p>
          <p className="text-xs text-muted-foreground">
            Its chats, runs and automations go with it, and its next run bills the new organization.
          </p>
        </div>
        <Button variant="danger" size="sm" onClick={onMove}>
          Move project…
        </Button>
      </div>
    </section>
  );
}
