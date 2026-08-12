import { AlertCircle, Loader2 } from "lucide-react";

import { Modal } from "../ui/Modal";
import type { Project } from "../../store/projectStore";

interface RemoveProjectsModalProps {
  /** Projects staged for removal. Empty closes the modal. */
  projects: Project[];
  isRemoving: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}

// Above this we summarise instead of listing every row — a confirm dialog that
// scrolls defeats the point of a confirm dialog.
const MAX_LISTED = 8;

/**
 * Confirmation for removing one or many projects from Reliant.
 *
 * Removal deletes the project *record*, never the checkout on disk — that's
 * what makes offering a bulk version reasonable, and why the copy leads with
 * it. Handles both the single-row and bulk cases so there's one place where
 * that promise is worded.
 */
export function RemoveProjectsModal({
  projects,
  isRemoving,
  onCancel,
  onConfirm,
}: RemoveProjectsModalProps) {
  const count = projects.length;
  if (count === 0) return null;

  const listed = projects.slice(0, MAX_LISTED);
  const remainder = count - listed.length;

  return (
    <Modal
      isOpen
      onClose={onCancel}
      title={count === 1 ? "Remove project" : `Remove ${count} projects`}
      size="md"
    >
      <div className="space-y-4">
        <div className="flex items-start gap-3 rounded-lg border border-warning/20 bg-warning/10 p-4">
          <AlertCircle className="mt-0.5 h-5 w-5 shrink-0 text-warning" />
          <div className="flex-1">
            <p className="text-sm font-medium text-foreground">
              {count === 1
                ? "This removes the project from Reliant"
                : "This removes these projects from Reliant"}
            </p>
            <p className="mt-1 text-sm text-muted-foreground">
              Your repositories and all files stay exactly as they are on disk.
              You can add {count === 1 ? "it" : "them"} back later.
            </p>
          </div>
        </div>

        <div className="space-y-1 rounded-lg bg-muted/30 p-3">
          {listed.map((project) => (
            <div key={project.id} className="min-w-0">
              <div className="truncate text-sm font-medium text-foreground">
                {project.name}
              </div>
              <div className="truncate font-mono text-xs text-muted-foreground">
                {project.path}
              </div>
            </div>
          ))}
          {remainder > 0 && (
            <div className="pt-1 text-xs text-muted-foreground">
              and {remainder} more…
            </div>
          )}
        </div>

        <div className="flex justify-end gap-3 pt-2">
          <button
            type="button"
            onClick={onCancel}
            disabled={isRemoving}
            className="rounded-lg px-4 py-2 text-sm font-medium text-foreground transition-colors hover:bg-muted disabled:opacity-60"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={onConfirm}
            disabled={isRemoving}
            className="inline-flex items-center gap-2 rounded-lg bg-destructive px-4 py-2 text-sm font-medium text-destructive-foreground transition-colors hover:bg-destructive/90 disabled:opacity-60"
            data-testid="confirm-remove-projects"
          >
            {isRemoving && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
            {count === 1 ? "Remove project" : `Remove ${count} projects`}
          </button>
        </div>
      </div>
    </Modal>
  );
}
