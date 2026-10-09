import { useState, useEffect, useMemo } from "react";
import { AlertCircle, FolderGit2, Loader2, Lock, Trash2 } from "lucide-react";
import { Modal } from "../ui/Modal";
import { Button } from "../ui/Button";
import { CardInset } from "../forge-ui/card";
import { useWorktreeStore } from "../../store/worktreeStore";
import type { DiscoveredWorktree } from "../../api/worktree-grpc";
import {
  adoptConfirmation,
  groupByRepo,
  isAdoptBlocked,
  lockedMoveExplanation,
} from "./discoverWorktreesUtils";

interface DiscoverWorktreesModalProps {
  isOpen: boolean;
  onClose: () => void;
  onWorktreesImported: (importedWorktreeIds?: string[]) => void | Promise<void>;
  projectId: string;
}

export function DiscoverWorktreesModal({
  isOpen,
  onClose,
  onWorktreesImported,
  projectId,
}: DiscoverWorktreesModalProps) {
  const discovered = useWorktreeStore((s) => s.discoveredWorktrees);
  const stale = useWorktreeStore((s) => s.staleWorktrees);
  const workspacesRoot = useWorktreeStore((s) => s.workspacesRoot);
  const isDiscovering = useWorktreeStore((s) => s.isDiscovering);
  const error = useWorktreeStore((s) => s.error);
  const discoverWorktrees = useWorktreeStore((s) => s.discoverWorktrees);
  const importWorktree = useWorktreeStore((s) => s.importWorktree);
  const pruneWorktrees = useWorktreeStore((s) => s.pruneWorktrees);

  const [pending, setPending] = useState<DiscoveredWorktree | null>(null);
  const [name, setName] = useState("");
  const [adopting, setAdopting] = useState(false);
  const [itemErrors, setItemErrors] = useState<Record<string, string>>({});
  const [pruning, setPruning] = useState(false);
  const [pruneError, setPruneError] = useState<string | null>(null);
  const [prunedPaths, setPrunedPaths] = useState<string[] | null>(null);

  useEffect(() => {
    if (isOpen && projectId) {
      void discoverWorktrees(projectId);
      setPending(null);
      setItemErrors({});
      setPruneError(null);
      setPrunedPaths(null);
    }
  }, [isOpen, projectId, discoverWorktrees]);

  const groups = useMemo(() => groupByRepo(discovered), [discovered]);

  const startAdopt = (entry: DiscoveredWorktree) => {
    setPending(entry);
    setName(entry.name);
  };

  const confirmAdopt = async () => {
    if (!pending) return;
    const entry = pending;
    setAdopting(true);
    try {
      const imported = await importWorktree({
        path: entry.path,
        name: name.trim() || entry.name,
        project_id: projectId,
        repo_id: entry.repo_id,
        confirm_move: entry.moves_on_import,
      });
      setItemErrors((prev) => {
        const { [entry.path]: _gone, ...rest } = prev;
        return rest;
      });
      setPending(null);
      await onWorktreesImported([imported.id]);
    } catch (err) {
      const msg = err instanceof Error ? err.message : "Failed to adopt";
      setItemErrors((prev) => ({ ...prev, [entry.path]: msg }));
    } finally {
      setAdopting(false);
    }
  };

  const handlePrune = async () => {
    setPruning(true);
    setPruneError(null);
    try {
      const pruned = await pruneWorktrees(projectId);
      setPrunedPaths(pruned.map((w) => w.path));
    } catch (err) {
      setPruneError(err instanceof Error ? err.message : "Failed to clean up");
    } finally {
      setPruning(false);
    }
  };

  const pendingBlocked = pending ? isAdoptBlocked(pending) : false;
  const pendingError = pending ? itemErrors[pending.path] : undefined;

  return (
    <>
      <Modal isOpen={isOpen} onClose={onClose} title="Worktrees made outside Reliant" size="xl">
        <div className="space-y-4">
          {error && (
            <div
              role="alert"
              className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive-ink"
            >
              <AlertCircle className="mt-0.5 h-4 w-4 flex-shrink-0" aria-hidden="true" />
              <span className="flex-1">{error}</span>
            </div>
          )}

          {isDiscovering ? (
            <div className="flex items-center justify-center gap-2 py-8 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" aria-hidden="true" /> Looking for worktrees…
            </div>
          ) : (
            <>
              {groups.length === 0 && !error && (
                <p className="py-4 text-center text-sm text-muted-foreground">
                  No git worktrees outside Reliant were found for this project.
                </p>
              )}

              {groups.map((group) => (
                <section key={group.repoId} aria-label={group.repoName} className="space-y-2">
                  <h3 className="flex items-center gap-2 text-sm font-medium text-foreground">
                    <FolderGit2 className="h-4 w-4 text-muted-foreground" aria-hidden="true" />
                    {group.repoName}
                  </h3>
                  <ul className="space-y-2">
                    {group.items.map((entry) => (
                      <li key={entry.path}>
                        <CardInset className="flex flex-col gap-2">
                          <div className="flex items-center gap-3">
                            <div className="min-w-0 flex-1">
                              <p className="truncate text-sm text-foreground">
                                {entry.branch || "detached"}
                                {entry.locked && (
                                  <Lock className="ml-1.5 inline h-3 w-3 text-muted-foreground" aria-label="locked" />
                                )}
                              </p>
                              <p className="truncate font-mono text-xs text-muted-foreground" title={entry.path}>
                                {entry.path}
                              </p>
                            </div>
                            <Button variant="outline" size="sm" onClick={() => startAdopt(entry)}>
                              Adopt
                            </Button>
                          </div>
                          {itemErrors[entry.path] && !pending && (
                            <p role="alert" className="text-xs text-destructive-ink">
                              {itemErrors[entry.path]}
                            </p>
                          )}
                        </CardInset>
                      </li>
                    ))}
                  </ul>
                </section>
              ))}

              <CardInset className="space-y-2">
                <div className="flex items-center justify-between gap-3">
                  <p className="text-sm text-foreground">
                    {stale.length} stale git {stale.length === 1 ? "record" : "records"} (their
                    directories are gone)
                  </p>
                  <Button
                    variant="outline"
                    size="sm"
                    leftIcon={<Trash2 className="h-3.5 w-3.5" />}
                    disabled={stale.length === 0}
                    loading={pruning}
                    onClick={() => void handlePrune()}
                  >
                    Clean up
                  </Button>
                </div>
                {stale.length > 0 && (
                  <ul className="space-y-0.5">
                    {stale.map((w) => (
                      <li key={`${w.repo_id}:${w.path}`} className="truncate font-mono text-xs text-muted-foreground" title={w.reason}>
                        {w.path}
                      </li>
                    ))}
                  </ul>
                )}
                {pruneError && (
                  <p role="alert" className="text-xs text-destructive-ink">
                    {pruneError}
                  </p>
                )}
                {prunedPaths && (
                  <div className="text-xs text-muted-foreground" data-testid="prune-result">
                    <p>
                      Removed {prunedPaths.length} stale {prunedPaths.length === 1 ? "record" : "records"}
                      {prunedPaths.length > 0 ? ":" : "."}
                    </p>
                    <ul>
                      {prunedPaths.map((p) => (
                        <li key={p} className="truncate font-mono">
                          {p}
                        </li>
                      ))}
                    </ul>
                  </div>
                )}
              </CardInset>
            </>
          )}
        </div>
      </Modal>

      {pending && (
        <Modal isOpen onClose={() => !adopting && setPending(null)} title="Adopt this worktree?" size="md">
          <div className="space-y-3">
            <p className="text-sm text-foreground">{adoptConfirmation(pending, workspacesRoot, name.trim() || pending.name)}</p>
            {pendingBlocked && (
              <p role="alert" className="text-sm text-destructive-ink">
                {lockedMoveExplanation(pending)}
              </p>
            )}
            <label className="block space-y-1 text-sm text-foreground">
              <span>Workspace name</span>
              <input
                type="text"
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={pending.name}
                className="h-9 w-full rounded-md border border-border bg-background px-3 text-sm focus:outline-none focus:ring-2 focus:ring-primary/30"
              />
            </label>
            {pendingError && (
              <p role="alert" className="text-sm text-destructive-ink">
                {pendingError}
              </p>
            )}
            <div className="flex justify-end gap-2">
              <Button variant="outline" size="sm" disabled={adopting} onClick={() => setPending(null)}>
                Cancel
              </Button>
              <Button
                variant="primary"
                size="sm"
                disabled={pendingBlocked}
                loading={adopting}
                onClick={() => void confirmAdopt()}
              >
                Adopt
              </Button>
            </div>
          </div>
        </Modal>
      )}
    </>
  );
}
