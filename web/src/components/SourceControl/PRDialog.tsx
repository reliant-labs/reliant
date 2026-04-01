import { useState, useEffect, useCallback } from "react";
import { Modal } from "../ui/Modal";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { Textarea } from "../ui/Textarea";
import { GitPullRequest, Loader2, ExternalLink } from "lucide-react";
import * as gitApi from "../../api/git";
import { logger } from "../../lib/logger";

interface PRDialogProps {
  isOpen: boolean;
  onClose: () => void;
  onPRCreated?: () => void;
  worktreeId: string;
  defaultBranch?: string;
  currentBranch?: string;
}

export function PRDialog({
  isOpen,
  onClose,
  onPRCreated,
  worktreeId,
  defaultBranch,
  currentBranch,
}: PRDialogProps) {
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [isCreating, setIsCreating] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [prUrl, setPrUrl] = useState<string | null>(null);

  const isDefaultBranch = !!defaultBranch && !!currentBranch && defaultBranch === currentBranch;

  useEffect(() => {
    if (!isOpen) {
      // Reset form when modal closes
      setTitle("");
      setBody("");
      setError(null);
      setPrUrl(null);
    }
  }, [isOpen]);

  const handleCreate = useCallback(async () => {
    if (isDefaultBranch) {
      setError("Cannot create PR from the default branch");
      return;
    }

    if (!title.trim()) {
      setError("PR title is required");
      return;
    }

    setIsCreating(true);
    setError(null);

    try {
      const response = await gitApi.createPullRequest(
        worktreeId,
        title.trim(),
        body.trim()
      );
      
      logger.info("Pull request created successfully", { url: response.pr_url });
      setPrUrl(response.pr_url || null);
      
      // Notify parent that PR was created
      onPRCreated?.();
      
      // Close after a brief delay to show success
      setTimeout(() => {
        onClose();
      }, 2000);
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : "Failed to create pull request";
      setError(errorMessage);
      logger.error("Failed to create pull request", err);
    } finally {
      setIsCreating(false);
    }
  }, [title, body, worktreeId, onPRCreated, onClose, isDefaultBranch]);

  // Global keyboard shortcut handler for Cmd/Ctrl + Enter
  const canCreate = title.trim().length > 0 && !isCreating && !prUrl && !isDefaultBranch;
  
  useEffect(() => {
    if (!isOpen) return;

    const handleGlobalKeyDown = (e: KeyboardEvent) => {
      // Ctrl + Enter to create PR (only if title is set and not already creating)
      // Note: Cmd+Enter is reserved for approving tool requests globally
      if (e.ctrlKey && !e.metaKey && e.key === "Enter" && canCreate) {
        e.preventDefault();
        handleCreate();
      }
    };

    document.addEventListener("keydown", handleGlobalKeyDown);
    return () => document.removeEventListener("keydown", handleGlobalKeyDown);
  }, [isOpen, handleCreate, canCreate]);

  const handleKeyDown = (e: React.KeyboardEvent) => {
    // Ctrl + Enter to create PR
    // Note: Cmd+Enter is reserved for approving tool requests globally
    if (e.ctrlKey && !e.metaKey && e.key === "Enter") {
      e.preventDefault();
      handleCreate();
    }
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={onClose}
      title="Create Pull Request"
      size="md"
    >
      <div className="space-y-6 p-6">
        {/* Success State */}
        {prUrl && (
          <div className="p-4 bg-green-500/10 border border-green-500/20 rounded-lg">
            <div className="flex items-start gap-3">
              <GitPullRequest className="w-5 h-5 text-green-500 mt-0.5" />
              <div className="flex-1">
                <p className="text-sm font-medium text-green-500">
                  Pull request created successfully!
                </p>
                {prUrl && (
                  <a
                    href={prUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="text-sm text-green-500/80 hover:text-green-500 underline flex items-center gap-1 mt-1"
                  >
                    View pull request
                    <ExternalLink className="w-3 h-3" />
                  </a>
                )}
              </div>
            </div>
          </div>
        )}

        {/* Error State */}
        {error && (
          <div className="p-3 bg-destructive/10 border border-destructive/20 rounded-lg">
            <p className="text-sm text-destructive">{error}</p>
          </div>
        )}

        {isDefaultBranch && !error && (
          <div className="p-3 bg-destructive/10 border border-destructive/20 rounded-lg">
            <p className="text-sm text-destructive">Cannot create PR from the default branch.</p>
          </div>
        )}

        {/* Form */}
        <div className="space-y-4">
          <div className="space-y-2">
            <label htmlFor="pr-title" className="text-sm font-medium text-foreground">
              Title <span className="text-destructive">*</span>
            </label>
            <Input
              id="pr-title"
              placeholder="Brief description of your changes"
              value={title}
              onChange={(e) => {
                setTitle(e.target.value);
                setError(null);
              }}
              onKeyDown={handleKeyDown}
              disabled={isCreating || !!prUrl}
              className="text-sm"
            />
          </div>

          <div className="space-y-2">
            <label htmlFor="pr-body" className="text-sm font-medium text-foreground">
              Description
            </label>
            <Textarea
              id="pr-body"
              placeholder="Detailed description of your changes (optional)"
              value={body}
              onChange={(e) => setBody(e.target.value)}
              onKeyDown={handleKeyDown}
              disabled={isCreating || !!prUrl}
              className="min-h-[120px] resize-none text-sm"
            />
          </div>

          <div className="text-xs text-muted-foreground">
            <p>
              Base branch: <code className="px-1.5 py-0.5 bg-muted rounded font-mono">{defaultBranch || "(detecting...)"}</code>
            </p>
          </div>
        </div>

        {/* Actions */}
        <div className="flex items-center justify-between pt-4 border-t border-border">
          <div className="text-xs text-muted-foreground">
            <kbd className="px-1.5 py-0.5 rounded bg-muted font-mono">Ctrl</kbd> +{" "}
            <kbd className="px-1.5 py-0.5 rounded bg-muted font-mono">Enter</kbd> to create
          </div>
          
          <div className="flex items-center gap-2">
            <Button
              variant="outline"
              onClick={onClose}
              disabled={isCreating}
              size="sm"
            >
              Cancel
            </Button>
            <Button
              onClick={handleCreate}
              disabled={!canCreate}
              size="sm"
            >
              {isCreating ? (
                <Loader2 className="w-4 h-4 animate-spin" />
              ) : (
                "Create"
              )}
            </Button>
          </div>
        </div>
      </div>
    </Modal>
  );
}
