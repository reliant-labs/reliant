import { Modal } from "../ui/Modal";
import { AlertCircle } from "lucide-react";
import type { Project } from "../../store/projectStore";

interface RemoveProjectModalProps {
  isOpen: boolean;
  onClose: () => void;
  onConfirm: () => void;
  project: Project | null;
}

export function RemoveProjectModal({
  isOpen,
  onClose,
  onConfirm,
  project,
}: RemoveProjectModalProps) {
  if (!project) return null;

  return (
    <Modal isOpen={isOpen} onClose={onClose} title="Remove Project" size="md">
      <div className="space-y-4">
        <div className="flex items-start gap-3 p-4 bg-warning/10 border border-warning/20 rounded-lg">
          <AlertCircle className="w-5 h-5 text-warning mt-0.5 flex-shrink-0" />
          <div className="flex-1">
            <p className="text-sm font-medium text-foreground">
              This will remove the project from Reliant
            </p>
            <p className="text-sm text-muted-foreground mt-1">
              Your repository and all files will remain intact on disk. You can add this project back later if needed.
            </p>
          </div>
        </div>

        <div className="space-y-2">
          <div className="text-sm font-medium text-foreground">Project Details:</div>
          <div className="p-3 bg-muted/30 rounded-lg space-y-1">
            <div className="text-sm font-mono font-semibold">{project.name}</div>
            <div className="text-xs text-muted-foreground font-mono">{project.path}</div>
          </div>
        </div>

        <div className="text-sm text-muted-foreground">
          Are you sure you want to remove this project from Reliant?
        </div>

        <div className="flex justify-end gap-3 pt-4">
          <button
            onClick={onClose}
            className="px-4 py-2 text-sm font-medium text-foreground hover:bg-muted rounded-lg transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={onConfirm}
            className="px-4 py-2 text-sm font-medium bg-destructive text-destructive-foreground hover:bg-destructive/90 rounded-lg transition-colors"
          >
            Remove Project
          </button>
        </div>
      </div>
    </Modal>
  );
}
