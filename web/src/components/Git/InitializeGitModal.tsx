import { useState } from "react";
import { GitBranch, CheckCircle2, Loader2 } from "lucide-react";
import { Modal } from "../ui/Modal";
import { api } from "../../api/client";
import { logger } from "../../lib/logger";
import { toast } from "../../lib/toast-manager";

interface InitializeGitModalProps {
  isOpen: boolean;
  onClose: () => void;
  onSuccess: () => void | Promise<void>;
  projectId: string;
  projectName: string;
}

const DEFAULT_GITIGNORE_PATTERNS = [
  "# Reliant",
  ".reliant/",
  "",
  "# OS",
  ".DS_Store",
  "Thumbs.db",
  "",
  "# Editor",
  ".vscode/",
  ".idea/",
  "*.swp",
  "*.swo",
  "*~",
  "",
  "# Dependencies",
  "node_modules/",
  "vendor/",
  "",
  "# Build outputs",
  "dist/",
  "build/",
  "*.o",
  "*.so",
  "*.exe",
  "",
  "# Environment",
  ".env",
  ".env.local",
  ".env.*.local",
];

export function InitializeGitModal({
  isOpen,
  onClose,
  onSuccess,
  projectId,
  projectName,
}: InitializeGitModalProps) {
  const [initialBranch, setInitialBranch] = useState("main");
  const [createInitialCommit, setCreateInitialCommit] = useState(true);
  const [customGitignore, setCustomGitignore] = useState(
    DEFAULT_GITIGNORE_PATTERNS.join("\n")
  );
  const [isInitializing, setIsInitializing] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const handleInitialize = async () => {
    setIsInitializing(true);
    setError(null);

    try {
      const gitignorePatterns = customGitignore
        .split("\n")
        .map((line) => line.trim())
        .filter((line) => line.length > 0);

      // Trim the branch name — stray whitespace (e.g. "main ") is an
      // invalid git branch name and would fail server-side validation.
      const branch = initialBranch.trim() || "main";

      await api.git.initGitRepository(projectId, {
        initial_branch: branch,
        gitignore_patterns: gitignorePatterns,
        initial_commit: createInitialCommit,
      });

      toast.success(`Git repository initialized for ${projectName}`);
      logger.info("[InitializeGitModal] Git initialized successfully", {
        projectId,
        projectName,
      });

      await onSuccess();
      onClose();
    } catch (err: any) {
      let errorMessage = "Failed to initialize git repository";
      
      // Try to extract error message from ky HTTPError
      if (err.response) {
        try {
          const errorData = await err.response.json();
          // Backend returns { "error": "message" }
          errorMessage = errorData.error || errorData.message || errorMessage;
        } catch (e) {
          // If JSON parsing fails, use the error message if available
          errorMessage = err.message || errorMessage;
        }
      } else if (err.message) {
        errorMessage = err.message;
      } else if (typeof err === 'string') {
        errorMessage = err;
      }
      
      logger.error("[InitializeGitModal] Failed to initialize git", {
        error: err,
        errorMessage,
        projectId,
      });
      setError(errorMessage);
      toast.error(errorMessage);
    } finally {
      setIsInitializing(false);
    }
  };

  return (
    <Modal isOpen={isOpen} onClose={onClose} title="Initialize Git Repository" size="md">
      <div className="space-y-4">
        <div className="flex items-start gap-3">
          <div className="p-2 rounded-lg bg-primary/10 ring-1 ring-primary/20">
            <GitBranch className="w-5 h-5 text-primary" />
          </div>
          <div className="flex-1">
            <p className="text-sm text-foreground mb-2">
              Initialize git for{" "}
              <span className="font-semibold text-primary">{projectName}</span>
            </p>
            <p className="text-xs text-muted-foreground">
              This will create a <code className="bg-muted px-1.5 py-0.5 rounded text-xs font-mono">.git</code>{" "}
              directory and configure version control.
            </p>
          </div>
        </div>

        {error && (
          <div className="p-4 bg-destructive/10 text-destructive rounded-lg text-sm">
            {error}
          </div>
        )}

        <div className="space-y-3">
          <div className="space-y-1.5">
            <label className="block text-xs font-semibold text-foreground">
              Initial Branch Name
            </label>
            <div className="relative">
              <GitBranch className="absolute left-2.5 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-muted-foreground" />
              <input
                type="text"
                value={initialBranch}
                onChange={(e) => setInitialBranch(e.target.value)}
                className="w-full pl-9 pr-3 py-2 elevation-0 border border-border/60 rounded-lg text-xs font-mono focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent transition-all"
                placeholder="main"
              />
            </div>
          </div>

          <div className="space-y-1.5">
            <label className="block text-xs font-semibold text-foreground">
              .gitignore Patterns
            </label>
            <textarea
              value={customGitignore}
              onChange={(e) => setCustomGitignore(e.target.value)}
              rows={6}
              className="w-full px-3 py-2 elevation-0 border border-border/60 rounded-lg text-xs font-mono focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent transition-all resize-none"
              placeholder="Enter gitignore patterns..."
            />
          </div>

          <div className="flex items-center gap-2 p-3 elevation-1 border border-border rounded-lg">
            <input
              type="checkbox"
              id="initial-commit"
              checked={createInitialCommit}
              onChange={(e) => setCreateInitialCommit(e.target.checked)}
              className="w-3.5 h-3.5 rounded border-border text-primary focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background"
            />
            <label
              htmlFor="initial-commit"
              className="text-xs font-medium text-foreground cursor-pointer"
            >
              Create initial commit
            </label>
          </div>
        </div>

        <div className="p-3 bg-primary/5 rounded-lg border border-primary/20">
          <p className="text-xs font-semibold text-foreground uppercase tracking-wide mb-2">
            This will create:
          </p>
          <ul className="text-xs text-muted-foreground space-y-1">
            <li className="flex items-start gap-1.5">
              <CheckCircle2 className="w-3.5 h-3.5 text-primary mt-0.5 flex-shrink-0" />
              <span>
                <code className="bg-muted px-1 py-0.5 rounded text-xs font-mono">.git/</code> directory
              </span>
            </li>
            <li className="flex items-start gap-1.5">
              <CheckCircle2 className="w-3.5 h-3.5 text-primary mt-0.5 flex-shrink-0" />
              <span>
                <code className="bg-muted px-1 py-0.5 rounded text-xs font-mono">.gitignore</code> file
              </span>
            </li>
            <li className="flex items-start gap-1.5">
              <CheckCircle2 className="w-3.5 h-3.5 text-primary mt-0.5 flex-shrink-0" />
              <span>Branch "{initialBranch.trim() || "main"}"</span>
            </li>
            {createInitialCommit && (
              <li className="flex items-start gap-1.5">
                <CheckCircle2 className="w-3.5 h-3.5 text-primary mt-0.5 flex-shrink-0" />
                <span>Initial commit</span>
              </li>
            )}
          </ul>
        </div>

        <div className="flex justify-end gap-2 pt-3 border-t border-border">
          <button
            onClick={onClose}
            disabled={isInitializing}
            className="px-4 py-2 text-xs font-medium text-foreground bg-muted hover:bg-muted/80 border-2 border-border hover:border-border/80 rounded-lg transition-all focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            onClick={handleInitialize}
            disabled={isInitializing}
            className="flex items-center gap-2 px-4 py-2 bg-primary hover:bg-primary/90 text-primary-foreground border-2 border-primary hover:border-primary/90 rounded-lg text-xs font-semibold shadow-sm transition-all focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background disabled:opacity-50 disabled:cursor-not-allowed"
          >
            {isInitializing && <Loader2 className="w-3.5 h-3.5 animate-spin" />}
            {isInitializing ? "Initializing..." : "Initialize Git"}
          </button>
        </div>
      </div>
    </Modal>
  );
}
