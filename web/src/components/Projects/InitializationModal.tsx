import { useState } from "react";
import { FolderPlus, GitBranch, CheckCircle2 } from "lucide-react";
import { Modal } from "../ui/Modal";

interface InitializationModalProps {
  isOpen: boolean;
  projectName: string;
  isGitRepo: boolean;
  onConfirm: (options: { initGit: boolean }) => void;
  onCancel: () => void;
}

export function InitializationModal({
  isOpen,
  projectName,
  isGitRepo,
  onConfirm,
  onCancel,
}: InitializationModalProps) {
  const [initGit, setInitGit] = useState(!isGitRepo);

  const handleConfirm = () => {
    onConfirm({ initGit });
  };

  return (
    <Modal 
      isOpen={isOpen} 
      onClose={onCancel} 
      title="Initialize Project"
      size="md"
    >
      <div className="space-y-6">
        <div className="flex items-start gap-4">
          <div className="p-3 rounded-xl bg-primary/10 ring-1 ring-primary/20">
            <FolderPlus className="w-6 h-6 text-primary" />
          </div>
          <div className="flex-1">
            <p className="text-sm text-foreground mb-3">
              The project <span className="font-semibold text-primary">{projectName}</span> 
              {' '}has not been initialized for Reliant AI.
            </p>
            <p className="text-sm text-muted-foreground leading-relaxed">
              Initializing will create a <code className="bg-muted px-1.5 py-0.5 rounded text-xs font-mono">.reliant</code> directory 
              in your project to store configuration and cache files.
            </p>
          </div>
        </div>

        {!isGitRepo && (
          <div className="p-4 bg-muted/30 rounded-lg border border-border space-y-3">
            <label className="flex items-start gap-3 cursor-pointer group">
              <input
                type="checkbox"
                checked={initGit}
                onChange={(e) => setInitGit(e.target.checked)}
                className="mt-0.5 w-4 h-4 text-primary border-border rounded focus:ring-primary focus:ring-offset-2 focus:ring-offset-background"
              />
              <div className="flex-1">
                <div className="flex items-center gap-2 mb-2">
                  <GitBranch className="w-4 h-4 text-muted-foreground group-hover:text-foreground transition-colors" />
                  <span className="text-sm font-medium text-foreground">Initialize Git repository</span>
                </div>
                <p className="text-xs text-muted-foreground leading-relaxed">
                  Creates a git repository and adds standard .gitignore entries
                </p>
              </div>
            </label>
          </div>
        )}

        <div className="p-4 bg-primary/5 rounded-lg border border-primary/20">
          <p className="text-xs font-semibold text-foreground uppercase tracking-wide mb-3">
            This will create:
          </p>
          <ul className="text-sm text-muted-foreground space-y-2">
            <li className="flex items-start gap-2">
              <CheckCircle2 className="w-4 h-4 text-primary mt-0.5 flex-shrink-0" />
              <span><code className="bg-muted px-1 py-0.5 rounded text-xs font-mono">.reliant/</code> directory</span>
            </li>
            <li className="flex items-start gap-2">
              <CheckCircle2 className="w-4 h-4 text-primary mt-0.5 flex-shrink-0" />
              <span>Project configuration files</span>
            </li>
            {initGit && (
              <>
                <li className="flex items-start gap-2">
                  <CheckCircle2 className="w-4 h-4 text-primary mt-0.5 flex-shrink-0" />
                  <span><code className="bg-muted px-1 py-0.5 rounded text-xs font-mono">.git/</code> repository</span>
                </li>
                <li className="flex items-start gap-2">
                  <CheckCircle2 className="w-4 h-4 text-primary mt-0.5 flex-shrink-0" />
                  <span><code className="bg-muted px-1 py-0.5 rounded text-xs font-mono">.gitignore</code> file</span>
                </li>
              </>
            )}
          </ul>
        </div>

        <div className="flex justify-end gap-3 pt-4 border-t border-border">
          <button
            onClick={onCancel}
            className="px-5 py-2.5 text-sm font-medium text-foreground bg-muted hover:bg-muted/80 border border-border rounded-lg transition-all focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background"
          >
            Cancel
          </button>
          <button
            onClick={handleConfirm}
            className="px-5 py-2.5 bg-primary text-primary-foreground hover:bg-primary/90 rounded-lg text-sm font-semibold shadow-sm transition-all focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background"
          >
            Initialize Project
          </button>
        </div>
      </div>
    </Modal>
  );
}