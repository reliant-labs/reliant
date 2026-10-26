import { useState } from "react";
import { FolderOpen, Plus, Trash2 } from "lucide-react";

import EmptyState from "../forge-ui/empty_state";
import Button from "@/components/forge-ui/button";
import Tooltip from "../forge-ui/tooltip";
import { useProjectStore } from "../../store/projectStore";
import { logger } from "../../lib/logger";
import { toast } from "../../lib/toast-manager";
import { CurrentProjectDetails } from "../Settings/projects/CurrentProjectDetails";
import { RemoveProjectModal } from "./RemoveProjectModal";
import { openAddProject } from "../../store/addProjectDialogStore";
import { ADD_PROJECT_LABEL, NO_PROJECTS_COPY, REMOVE_PROJECT_HINT } from "./projectCopy";

interface ProjectPanelProps {
  onNavigateToChats?: () => void;
}

/**
 * The Projects viewer tab (TabbedViewerPanel): the current project, in the
 * same compact detail card Settings → Projects shows under its project list,
 * plus the two actions that make sense from inside a project — add another
 * one, or remove this one.
 *
 * The full list of projects lives in Settings → Projects (switching is the
 * sidebar's project menu); this tab stays about the project you are in.
 */
export function ProjectPanel({ onNavigateToChats }: ProjectPanelProps) {
  const currentProject = useProjectStore((state) => state.currentProject);
  const deleteProject = useProjectStore((state) => state.deleteProject);
  const [showRemoveModal, setShowRemoveModal] = useState(false);
  const [isRemoving, setIsRemoving] = useState(false);

  const handleRemoveProject = async () => {
    if (!currentProject) return;
    setIsRemoving(true);
    try {
      // deleteProject moves the context to the most recent other project.
      await deleteProject(currentProject.id);
      setShowRemoveModal(false);
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
            title="No project yet"
            description={NO_PROJECTS_COPY}
            actionLabel={ADD_PROJECT_LABEL}
            onAction={() => openAddProject()}
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
              <Button
                variant="secondary"
                size="sm"
                leftIcon={<Plus className="h-3.5 w-3.5" />}
                onClick={() => openAddProject()}
                data-testid="project-panel-add-project"
              >
                {ADD_PROJECT_LABEL}
              </Button>
              <Tooltip content={REMOVE_PROJECT_HINT} side="left" delay={300}>
                <button
                  type="button"
                  onClick={() => setShowRemoveModal(true)}
                  aria-label={`Remove ${currentProject.name} from Reliant`}
                  data-testid="project-panel-remove"
                  className="inline-flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-destructive/10 hover:text-destructive-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
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
