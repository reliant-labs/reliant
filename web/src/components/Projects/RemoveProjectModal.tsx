import { Modal } from "../ui/Modal";
import { Button } from "../ui/Button";
import { collapseHomePath } from "../../lib/pathUtils";
import type { Project } from "../../store/projectStore";

interface RemoveProjectModalProps {
  isOpen: boolean;
  onClose: () => void;
  onConfirm: () => void;
  project: Project | null;
  /** True while the removal request is in flight — disables both buttons. */
  isRemoving?: boolean;
}

/**
 * Confirm forgetting ONE project. The consequence the user is worried about is
 * "will this delete my code" — so the answer leads, in plain words, and the
 * project being removed is shown as an identifier (name + path) so a user with
 * two checkouts of the same repo can tell which one this is.
 */
export function RemoveProjectModal({
  isOpen,
  onClose,
  onConfirm,
  project,
  isRemoving = false,
}: RemoveProjectModalProps) {
  if (!project) return null;

  return (
    <Modal isOpen={isOpen} onClose={onClose} title={`Remove ${project.name}?`} size="sm">
      <div className="flex flex-col gap-4">
        <p className="text-sm leading-relaxed text-muted-foreground text-pretty">
          {/* Accurate to the schema: project_configs, plans and triggers
              cascade on project delete; nothing touches the filesystem. */}
          Reliant forgets this project, along with its project settings and automations.{" "}
          <span className="font-medium text-foreground">Nothing on disk is deleted</span> — your
          files and git history stay where they are, and you can add the folder back at any time.
        </p>

        <div className="rounded-md border border-border/60 bg-background px-3 py-2.5">
          <div className="truncate text-sm font-medium text-foreground">{project.name}</div>
          <div className="truncate font-mono text-xs text-muted-foreground" title={project.path}>
            {collapseHomePath(project.path)}
          </div>
        </div>

        <div className="flex justify-end gap-2">
          <Button variant="ghost" size="md" onClick={onClose} disabled={isRemoving}>
            Cancel
          </Button>
          <Button
            variant="destructive"
            size="md"
            onClick={onConfirm}
            loading={isRemoving}
            data-testid="remove-project-confirm"
          >
            Remove project
          </Button>
        </div>
      </div>
    </Modal>
  );
}
