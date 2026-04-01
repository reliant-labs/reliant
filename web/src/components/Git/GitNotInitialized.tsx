import { useState } from "react";
import { FolderGit2, Github, ExternalLink } from "lucide-react";
import { Button } from "../ui/Button";
import { InitializeGitModal } from "./InitializeGitModal";
import { useProjectStore } from "../../store/projectStore";

interface GitNotInitializedProps {
  projectId: string;
  projectName: string;
  onInitialized?: () => void;
  className?: string;
}

/**
 * VS Code-style prompt shown when the current project doesn't have a Git repository.
 * Provides options to:
 * - Initialize a new git repository
 * - Publish to GitHub (future)
 * - Learn more about git
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
    <div className={`flex flex-col items-center justify-center p-6 text-center ${className}`}>
      <div className="max-w-sm space-y-6">
        {/* Main message */}
        <div className="space-y-3">
          <p className="text-sm text-foreground leading-relaxed">
            The folder currently open doesn't have a Git repository. You can initialize a repository which will enable source control features powered by Git.
          </p>
        </div>

        {/* Initialize Repository button */}
        <Button
          onClick={() => setShowInitModal(true)}
          leftIcon={<FolderGit2 className="w-4 h-4" />}
          variant="outline"
          size="md"
          className="w-full justify-center border-primary/40 hover:border-primary hover:bg-primary/5"
        >
          Initialize Repository
        </Button>

        {/* Learn more link */}
        <p className="text-sm text-muted-foreground">
          To learn more about how to use Git and source control in Reliant,{" "}
          <a
            href="https://docs.reliant.dev/source-control"
            target="_blank"
            rel="noopener noreferrer"
            className="text-primary hover:underline inline-flex items-center gap-1"
          >
            read our docs
            <ExternalLink className="w-3 h-3" />
          </a>
          .
        </p>

        {/* Publish to GitHub section */}
        <div className="pt-4 border-t border-border space-y-3">
          <p className="text-sm text-muted-foreground leading-relaxed">
            You can directly publish this folder to a GitHub repository. Once published, you'll have access to source control features powered by Git and GitHub.
          </p>

          <Button
            onClick={() => {
              // TODO: Implement GitHub publishing
              alert("GitHub publishing coming soon!");
            }}
            leftIcon={<Github className="w-4 h-4" />}
            variant="outline"
            size="md"
            className="w-full justify-center"
            disabled
          >
            Publish to GitHub
          </Button>
        </div>
      </div>

      {/* Initialize Git Modal */}
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
