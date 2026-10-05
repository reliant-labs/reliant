import { useState } from "react";
import { FolderOpen, Plus, Trash2 } from "lucide-react";

import EmptyState from "../forge-ui/empty_state";
import { Button } from "../ui/Button";
import { Tooltip } from "../ui/Tooltip";
import { useProjectStore } from "../../store/projectStore";
import { logger } from "../../lib/logger";
import { toast } from "../../lib/toast-manager";
import { CurrentProjectDetails } from "../Settings/projects/CurrentProjectDetails";
import { RemoveProjectModal } from "./RemoveProjectModal";

interface ProjectPanelProps {
  onNavigateToProjectPicker?: () => void;
  onNavigateToChats?: () => void;
}

/**
 * The Projects viewer tab (TabbedViewerPanel): the current project, in the
 * same compact detail card Settings → Projects shows under its project list,
 * plus the two actions that make sense from inside a project — go add/switch
 * to another one, or forget this one.
 *
 * The full list of projects lives in Settings → Projects and the picker; this
 * tab stays about the project you are in.
 */
export function ProjectPanel({ onNavigateToProjectPicker, onNavigateToChats }: ProjectPanelProps) {
  const currentProject = useProjectStore((state) => state.currentProject);
  const deleteProject = useProjectStore((state) => state.deleteProject);
  const [showRemoveModal, setShowRemoveModal] = useState(false);
  const [isRemoving, setIsRemoving] = useState(false);

  const handleRemoveProject = async () => {
    if (!currentProject) return;
    setIsRemoving(true);
    try {
      await deleteProject(currentProject.id);
      setShowRemoveModal(false);
      onNavigateToProjectPicker?.();
    } catch (error) {
      logger.error("Failed to remove project:", error);
      toast.error(`Could not remove ${currentProject.name}`);
    } finally {
      setIsRemoving(false);
    }
  };

  if (!currentProject) {
    return (
      <div className="forge-ui flex h-full items-center justify-center p-6">
        <div className="w-full max-w-md">
          <EmptyState
            icon={<FolderOpen className="h-6 w-6" />}
            title="No project selected"
            description="Choose a project, or add a folder or repository, to see its details here."
            actionLabel={onNavigateToProjectPicker ? "Select project" : undefined}
            onAction={onNavigateToProjectPicker}
          />
        </div>
      </div>
    );
  }

  return (
    <div className="forge-ui h-full overflow-y-auto bg-background">
      <div className="mx-auto w-full max-w-5xl p-6">
        <CurrentProjectDetails
          project={currentProject}
          onChatOpened={onNavigateToChats}
          actions={
            <>
              {/* "Add project" rather than "Change project": the picker does
                  both, but adding is the action users could not reach from
                  anywhere except the picker itself. */}
              {onNavigateToProjectPicker && (
                <Button
                  variant="secondary"
                  size="sm"
                  leftIcon={<Plus className="h-3.5 w-3.5" />}
                  onClick={onNavigateToProjectPicker}
                  data-testid="project-panel-add-project"
                >
                  Add project
                </Button>
              )}
              <Tooltip content="Remove from Reliant (files stay on disk)" placement="left" delay={300}>
                <button
                  type="button"
                  onClick={() => setShowRemoveModal(true)}
                  aria-label={`Remove ${currentProject.name} from Reliant`}
                  data-testid="project-panel-remove"
                  className="inline-flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  <Trash2 className="h-4 w-4" aria-hidden="true" />
                </button>
              </Tooltip>
            </>
          }
        />
      </div>

      <RemoveProjectModal
        isOpen={showRemoveModal}
        onClose={() => {
          if (!isRemoving) setShowRemoveModal(false);
        }}
        onConfirm={() => void handleRemoveProject()}
        project={currentProject}
        isRemoving={isRemoving}
      />
    </div>
  );
}
