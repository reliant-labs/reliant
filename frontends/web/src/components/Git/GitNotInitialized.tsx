import { useState } from "react";
import { ExternalLink, FolderGit2 } from "lucide-react";
import { Button } from "../ui/Button";
import { InitializeGitModal } from "./InitializeGitModal";
import { useProjectStore } from "../../store/projectStore";
import { cn } from "../../lib/utils";

interface GitNotInitializedProps {
  projectId: string;
  projectName: string;
  onInitialized?: () => void;
  className?: string;
}

/**
 * Empty state for a project folder that isn't a Git repository yet. The one
 * action is to initialize one; everything source-control related (changes,
 * commits, workspaces, PRs) needs it.
 */
export function GitNotInitialized({
  projectId,
  projectName,
  onInitialized,
  className = "",
}: GitNotInitializedProps) {
  const [showInitModal, setShowInitModal] = useState(false);
  const refreshCurrentProject = useProjectStore((state) => state.refreshCurrentProject);

  const handleInitSuccess = async () => {
    await refreshCurrentProject();
    setShowInitModal(false);
    onInitialized?.();
  };

  return (
    <div className={cn("flex flex-col items-center justify-center p-4", className)}>
      <div className="flex w-full max-w-xs flex-col items-center gap-3 rounded-lg border border-dashed border-border px-4 py-6 text-center">
        <FolderGit2 className="h-6 w-6 text-muted-foreground" aria-hidden="true" />
        <div className="flex flex-col gap-1">
          <h3 className="text-balance text-sm font-semibold text-foreground">Not a Git repository</h3>
          <p className="text-pretty text-xs text-muted-foreground">
            Initialize Git in <span className="font-medium text-foreground">{projectName}</span> to track changes,
            commit, and work in parallel workspaces.
          </p>
        </div>
        <Button variant="primary" size="sm" onClick={() => setShowInitModal(true)} className="w-full">
          Initialize repository
        </Button>
        <a
          href="https://docs.reliant.dev/source-control"
          target="_blank"
          rel="noopener noreferrer"
          className="inline-flex items-center gap-1 text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
        >
          How source control works in Reliant
          <ExternalLink className="h-3 w-3" aria-hidden="true" />
        </a>
      </div>

      <InitializeGitModal
        isOpen={showInitModal}
        onClose={() => setShowInitModal(false)}
        onSuccess={handleInitSuccess}
        projectId={projectId}
        projectName={projectName}
      />
    </div>
  );
}
