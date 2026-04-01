import { useState, memo } from 'react';
import { GitCommit, GitPullRequest, Upload, X } from 'lucide-react';
import { worktreeGrpc } from '../../api/worktree-grpc';
import { cn } from '../../lib/utils';
import { parseAPIError, formatErrorForDisplay } from '../../lib/error-utils';
import { ErrorAlert } from '../ui/ErrorAlert';
import { Button } from '../ui/Button';

interface GitOperationsProps {
  worktreeId: string;
  branch: string;
  defaultBranch?: string;
  onOperationComplete?: () => void;
  className?: string;
}

function GitOperationsComponent({
  worktreeId,
  branch,
  defaultBranch,
  onOperationComplete,
  className = ""
}: GitOperationsProps) {
  const [isCommitModalOpen, setIsCommitModalOpen] = useState(false);
  const [isPRModalOpen, setIsPRModalOpen] = useState(false);
  const [commitMessage, setCommitMessage] = useState('');
  const [prTitle, setPrTitle] = useState('');
  const [prBody, setPrBody] = useState('');
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const isDefaultBranch = !!defaultBranch && branch === defaultBranch;

  const handleCommit = async () => {
    if (!commitMessage.trim()) {
      setError('Commit message is required');
      return;
    }

    setIsLoading(true);
    setError(null);
    try {
      await worktreeGrpc.commit(worktreeId, commitMessage);
      setCommitMessage('');
      setIsCommitModalOpen(false);
      onOperationComplete?.();
    } catch (err) {
      const errorMessage = await parseAPIError(err);
      setError(formatErrorForDisplay(errorMessage));
    } finally {
      setIsLoading(false);
    }
  };

  const handlePush = async () => {
    setIsLoading(true);
    setError(null);
    try {
      await worktreeGrpc.push(worktreeId);
      onOperationComplete?.();
    } catch (err) {
      const errorMessage = await parseAPIError(err);
      setError(formatErrorForDisplay(errorMessage));
    } finally {
      setIsLoading(false);
    }
  };

  const handleCreatePR = async () => {
    if (isDefaultBranch) {
      setError('Cannot create PR from the default branch');
      return;
    }

    if (!prTitle.trim()) {
      setError('Pull request title is required');
      return;
    }

    setIsLoading(true);
    setError(null);
    try {
      await worktreeGrpc.createPR(worktreeId, prTitle, prBody || undefined);
      setPrTitle('');
      setPrBody('');
      setIsPRModalOpen(false);
      onOperationComplete?.();
    } catch (err) {
      const errorMessage = await parseAPIError(err);
      setError(formatErrorForDisplay(errorMessage));
    } finally {
      setIsLoading(false);
    }
  };

  return (
    <div className={cn("space-y-2", className)}>
      <div className="flex gap-2">
        <Button
          onClick={() => {
            setError(null);
            setIsCommitModalOpen(true);
          }}
          leftIcon={<GitCommit className="w-3 h-3" />}
          variant="primary"
          size="xs"
        >
          Commit
        </Button>

        <Button
          onClick={handlePush}
          disabled={isLoading}
          loading={isLoading}
          leftIcon={<Upload className="w-3 h-3" />}
          variant="accent"
          size="xs"
        >
          Push
        </Button>

        <Button
          onClick={isDefaultBranch ? undefined : () => {
            setError(null);
            setIsPRModalOpen(true);
          }}
          leftIcon={<GitPullRequest className="w-3 h-3" />}
          variant="outline"
          size="xs"
          disabled={isLoading || isDefaultBranch}
          title={isDefaultBranch ? "Cannot create PR from the default branch" : "Create pull request"}
        >
          Create PR
        </Button>
      </div>

      {error && !isCommitModalOpen && !isPRModalOpen && (
        <ErrorAlert error={error} onDismiss={() => setError(null)} />
      )}

      {/* Commit Modal */}
      {isCommitModalOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="bg-background border border-border rounded-lg p-4 w-full max-w-md" style={{ backgroundColor: 'hsl(var(--background))' }}>
            <div className="flex items-center justify-between mb-4">
              <h3 className="text-sm font-mono font-semibold">git_commit()</h3>
              <button
                onClick={() => setIsCommitModalOpen(false)}
                className="p-1 hover:bg-accent rounded transition-colors"
              >
                <X className="w-4 h-4" />
              </button>
            </div>

            {error && (
              <ErrorAlert
                error={error}
                onDismiss={() => setError(null)}
                variant="modal"
                className="mb-3"
              />
            )}

            <div className="space-y-3">
              <div>
                <label className="block text-xs font-mono mb-1">
                  commit.message <span className="text-destructive">*</span>
                </label>
                <textarea
                  value={commitMessage}
                  onChange={(e) => setCommitMessage(e.target.value)}
                  className="w-full px-3 py-2 bg-background border border-border rounded text-sm font-mono focus:outline-none focus:ring-1 focus:ring-primary h-24 resize-none"
                  placeholder="Add feature X..."
                  autoFocus
                />
              </div>

              <div className="text-xs font-mono text-muted-foreground">
                Branch: <span className="text-foreground">{branch}</span>
              </div>

              <div className="flex gap-2">
                <Button
                  onClick={() => setIsCommitModalOpen(false)}
                  variant="ghost"
                  size="sm"
                  className="flex-1"
                >
                  Cancel
                </Button>
                <Button
                  onClick={handleCommit}
                  disabled={isLoading || !commitMessage.trim()}
                  loading={isLoading}
                  variant="primary"
                  size="sm"
                  className="flex-1"
                >
                  {isLoading ? 'Committing...' : 'Commit'}
                </Button>
              </div>
            </div>
          </div>
        </div>
      )}

      {/* Pull Request Modal */}
      {isPRModalOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
          <div className="bg-background border border-border rounded-lg p-4 w-full max-w-md" style={{ backgroundColor: 'hsl(var(--background))' }}>
            <div className="flex items-center justify-between mb-4">
              <h3 className="text-sm font-mono font-semibold">create_pull_request()</h3>
              <button
                onClick={() => setIsPRModalOpen(false)}
                className="p-1 hover:bg-accent rounded transition-colors"
              >
                <X className="w-4 h-4" />
              </button>
            </div>

            {error && (
              <ErrorAlert
                error={error}
                onDismiss={() => setError(null)}
                variant="modal"
                className="mb-3"
              />
            )}

            <div className="space-y-3">
              <div>
                <label className="block text-xs font-mono mb-1">
                  pr.title <span className="text-destructive">*</span>
                </label>
                <input
                  type="text"
                  value={prTitle}
                  onChange={(e) => setPrTitle(e.target.value)}
                  className="w-full px-3 py-2 bg-background border border-border rounded text-sm font-mono focus:outline-none focus:ring-1 focus:ring-primary"
                  placeholder="Add feature X"
                  autoFocus
                />
              </div>

              <div>
                <label className="block text-xs font-mono mb-1">
                  pr.body
                </label>
                <textarea
                  value={prBody}
                  onChange={(e) => setPrBody(e.target.value)}
                  className="w-full px-3 py-2 bg-background border border-border rounded text-sm font-mono focus:outline-none focus:ring-1 focus:ring-primary h-24 resize-none"
                  placeholder="## Summary\n\n- Added feature X\n- Fixed bug Y"
                />
              </div>

              <div className="text-xs font-mono text-muted-foreground">
                Branch: <span className="text-foreground">{branch}</span> → main
              </div>

              <div className="flex gap-2">
                <Button
                  onClick={() => setIsPRModalOpen(false)}
                  variant="ghost"
                  size="sm"
                  className="flex-1"
                >
                  Cancel
                </Button>
                <Button
                  onClick={handleCreatePR}
                  disabled={isLoading || !prTitle.trim()}
                  loading={isLoading}
                  variant="primary"
                  size="sm"
                  className="flex-1"
                >
                  {isLoading ? 'Creating...' : 'Create PR'}
                </Button>
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

export const GitOperations = memo(GitOperationsComponent);