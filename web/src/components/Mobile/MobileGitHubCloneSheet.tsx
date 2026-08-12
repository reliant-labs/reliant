/**
 * Bottom sheet for cloning a picked repo onto a chosen machine.
 *
 * `CloneRepo` is a unary RPC that only returns once the daemon finishes the
 * clone — commonly 10-60s, longer for a large repo (see `CloneRepo` in
 * `api/transport.ts`'s `LONG_TIMEOUT_METHODS`, and the daemon-side
 * `handleGitClone` in `internal/toolexec/daemonruntime/cmd_git.go`, which
 * runs a real `git clone` subprocess with no incremental progress channel
 * back to the caller). A backgrounded phone can have its network request
 * killed by the OS mid-clone, and the request context is threaded straight
 * through server-side (see `gitcredential.svc.Clone`'s `ctx` argument), so
 * the work aborts rather than completing server-side. There is no
 * cancellation-safe or resumable path today — this sheet's only mitigation
 * is telling the user not to background the app and giving them an honest
 * failure + retry when it happens.
 *
 * A future fix would give `CloneRepo` a completion event (a NATS status
 * subject or a poll-by-request-id endpoint) so the client could disconnect
 * and re-attach; that is out of scope here and tracked separately per the
 * task brief — this sheet is written to make swapping the unary await for a
 * status subscription a local change when that lands.
 */

import { useState } from "react";
import { AlertTriangle, Loader2, Server, X } from "lucide-react";
import { DaemonStatus } from "@/gen/controlplane/v1/public/shared_pb";
import type { Daemon } from "@/services/controlPlane/daemon";
import type { GitRepo } from "@/services/controlPlane/git/types";
import { gitService } from "@/services/controlPlane/git";
import { useDaemonList } from "@/hooks/useOnboardingQueries";
import { cn } from "@/lib/utils";
import { MOBILE_PRIMARY_ACTION } from "./MobileChrome";

/**
 * Only ACTIVE daemons are offered as a clone target. Suspended/Disconnected
 * need a Resume first (a separate write this surface already gates behind
 * `daemonResume`), Pending is still starting, and Failed needs desktop's
 * recreate path — offering any of those here would produce a request that
 * either fails outright or silently queues via the control-plane's
 * JetStream pending-command fallback (see `gitcredential.svc.Clone`), which
 * is a materially different, unverifiable outcome that must not be offered
 * as if it were an immediate clone.
 */
function isCloneTarget(daemon: Daemon): boolean {
  return daemon.status === DaemonStatus.ACTIVE;
}

type CloneState = "picking" | "cloning" | "success" | "error";

export function MobileGitHubCloneSheet({
  repo,
  onClose,
}: {
  repo: GitRepo;
  onClose: () => void;
}) {
  const { data: daemons, isLoading: daemonsLoading } = useDaemonList();
  const activeDaemons = (daemons ?? []).filter(isCloneTarget);

  const [selectedDaemonId, setSelectedDaemonId] = useState<string | null>(null);
  const [branch, setBranch] = useState(repo.defaultBranch || "main");
  const [state, setState] = useState<CloneState>("picking");
  const [error, setError] = useState("");
  const [clonedPath, setClonedPath] = useState("");

  const selectedDaemon = activeDaemons.find((d) => d.id === selectedDaemonId);

  const path = `/home/workspace/projects/${repo.fullName.split("/").pop() || "repo"}`;

  const handleClone = async () => {
    if (!selectedDaemonId) return;
    setState("cloning");
    setError("");
    try {
      const result = await gitService.cloneRepo({
        daemonId: selectedDaemonId,
        gitRepo: repo.cloneUrl,
        gitBranch: branch.trim() || repo.defaultBranch || "main",
        path,
      });
      setClonedPath(result.clonedPath);
      setState("success");
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to clone repository");
      setState("error");
    }
  };

  const closable = state !== "cloning";

  return (
    <div className="fixed inset-0 z-50 flex flex-col justify-end">
      <button
        type="button"
        aria-label="Dismiss"
        onClick={closable ? onClose : undefined}
        disabled={!closable}
        className="absolute inset-0 bg-black/40"
      />
      <div
        className="relative flex max-h-[85vh] flex-col rounded-t-2xl border-t border-border bg-background shadow-lg"
        style={{ paddingBottom: "env(safe-area-inset-bottom)" }}
      >
        <div className="flex items-center justify-between border-b border-border px-4 py-3">
          <span className="truncate text-sm font-semibold text-foreground">
            Clone {repo.fullName}
          </span>
          <button
            type="button"
            onClick={onClose}
            disabled={!closable}
            aria-label="Close"
            className="flex min-h-[44px] min-w-[44px] shrink-0 items-center justify-center rounded-md text-muted-foreground active:bg-muted disabled:opacity-40"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto p-4">
          {state === "success" ? (
            <div className="space-y-3 text-center">
              <p className="text-sm font-medium text-foreground">Cloned successfully</p>
              <p className="break-all text-xs text-muted-foreground">{clonedPath}</p>
              <p className="text-xs text-muted-foreground">
                on {selectedDaemon?.name || selectedDaemon?.hostname || "the machine"}
              </p>
            </div>
          ) : state === "cloning" ? (
            <div className="flex flex-col items-center gap-3 py-6 text-center">
              <Loader2 className="h-6 w-6 animate-spin text-primary" />
              <p className="text-sm font-medium text-foreground">Cloning…</p>
              <div className="flex items-start gap-2 rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-left">
                <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400" />
                <p className="text-xs text-amber-900 dark:text-amber-200">
                  This can take up to a couple of minutes for a large repo. Keep this
                  app in the foreground — backgrounding it can cancel the clone before
                  it finishes.
                </p>
              </div>
            </div>
          ) : (
            <div className="space-y-4">
              {state === "error" && (
                <div className="rounded-lg border border-destructive/30 bg-destructive/10 p-3">
                  <p className="text-sm text-destructive">{error}</p>
                </div>
              )}

              <div>
                <p className="mb-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                  Clone onto
                </p>
                {daemonsLoading ? (
                  <div className="flex items-center gap-2 py-3 text-sm text-muted-foreground">
                    <Loader2 className="h-4 w-4 animate-spin" />
                    Loading machines…
                  </div>
                ) : activeDaemons.length === 0 ? (
                  <p className="rounded-lg border border-border bg-muted/30 p-3 text-xs text-muted-foreground">
                    No active machines available. Start or resume a machine on
                    desktop, then come back here.
                  </p>
                ) : (
                  <div className="divide-y divide-border overflow-hidden rounded-lg border border-border">
                    {activeDaemons.map((daemon) => {
                      const selected = daemon.id === selectedDaemonId;
                      return (
                        <button
                          key={daemon.id}
                          type="button"
                          onClick={() => setSelectedDaemonId(daemon.id)}
                          className={cn(
                            "flex min-h-[44px] w-full items-center gap-3 px-3 py-3 text-left active:bg-foreground/5",
                            selected && "bg-primary/10",
                          )}
                        >
                          <Server className="h-4 w-4 shrink-0 text-muted-foreground" />
                          <span className="min-w-0 flex-1 truncate text-sm text-foreground">
                            {daemon.name || daemon.hostname || `daemon ${daemon.id.slice(0, 8)}`}
                          </span>
                          <span
                            className={cn(
                              "h-2.5 w-2.5 shrink-0 rounded-full border-2",
                              selected ? "border-primary bg-primary" : "border-muted-foreground/40",
                            )}
                          />
                        </button>
                      );
                    })}
                  </div>
                )}
              </div>

              <div>
                <label className="mb-1 block text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                  Branch
                </label>
                <input
                  type="text"
                  value={branch}
                  onChange={(e) => setBranch(e.target.value)}
                  placeholder={repo.defaultBranch || "main"}
                  className="w-full min-h-[44px] rounded-lg border border-border bg-background px-3 text-sm text-foreground placeholder:text-muted-foreground focus:outline-none focus:ring-2 focus:ring-ring/20"
                />
              </div>

              <p className="break-all text-xs text-muted-foreground">
                Will clone to <code className="text-foreground">{path}</code>
              </p>

              <button
                type="button"
                onClick={() => void handleClone()}
                disabled={!selectedDaemonId}
                className={cn(MOBILE_PRIMARY_ACTION, "w-full")}
              >
                {state === "error" ? "Retry clone" : "Clone repository"}
              </button>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
