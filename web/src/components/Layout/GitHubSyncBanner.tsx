/**
 * GitHubSyncBanner
 *
 * Non-blocking, top-level UI surface for the GitHub credential sync state
 * managed by `githubCredentialSync`. Renders nothing when idle.
 *
 *  - status='syncing': subtle indicator pinned to the bottom-right.
 *  - status='failed':  warning banner with a Retry button. Retry attempts
 *                       a fresh `linkGithubAccount()` OAuth handshake because
 *                       the original transient provider_token is no longer
 *                       in memory — we never keep it around.
 *
 * The banner is intentionally fire-and-forget: it never blocks login or any
 * other interaction. It exists to (a) tell the user what's happening when
 * sync is in flight and (b) recover them with one click when the original
 * save retries all failed.
 */

import { useState } from "react";
import { Loader2, AlertTriangle, X } from "lucide-react";

import { useAuthStore } from "@/store/authStore";
import { trackEvent } from "@/lib/analytics";
import { logger } from "@/lib/logger";
import { useEvent } from "@/lib/event-context";

type SyncStatus = "idle" | "syncing" | "failed";

export function GitHubSyncBanner() {
  const [status, setStatus] = useState<SyncStatus>("idle");
  const [dismissed, setDismissed] = useState(false);
  const [retryInFlight, setRetryInFlight] = useState(false);

  const linkGithubAccount = useAuthStore((s) => s.linkGithubAccount);

  useEvent("github-credential:syncing", () => {
    setStatus("syncing");
    setDismissed(false);
  });
  useEvent("github-credential:succeeded", () => {
    setStatus("idle");
  });
  useEvent("github-credential:failed", () => {
    setStatus("failed");
    setDismissed(false);
  });

  if (dismissed) return null;

  if (status === "syncing") {
    return (
      <div
        className="fixed bottom-4 right-4 z-50 flex items-center gap-2 rounded-md border border-border bg-background/95 px-3 py-2 text-xs text-muted-foreground shadow-md backdrop-blur"
        role="status"
        aria-live="polite"
      >
        <Loader2 className="h-3.5 w-3.5 animate-spin" />
        <span>Syncing GitHub credentials…</span>
      </div>
    );
  }

  if (status === "failed") {
    const onRetry = async () => {
      if (retryInFlight) return;
      setRetryInFlight(true);
      trackEvent("github_credential_sync_retry_clicked");
      try {
        // We don't have the original provider_token in memory anymore — the
        // only safe way to obtain a new one is to redo the Supabase OAuth
        // handshake. The callback handlers will pipe the new token through
        // githubCredentialSync, which will flip status back to 'idle' on
        // success.
        await linkGithubAccount();
      } catch (err) {
        logger.warn("[GitHubSyncBanner] retry linkGithubAccount failed", err);
      } finally {
        setRetryInFlight(false);
      }
    };

    return (
      <div
        className="fixed bottom-4 right-4 z-50 max-w-sm rounded-md border border-amber-500/40 bg-amber-500/10 p-3 shadow-md backdrop-blur"
        role="alert"
        aria-live="polite"
      >
        <div className="flex items-start gap-3">
          <AlertTriangle className="mt-0.5 h-4 w-4 flex-shrink-0 text-amber-600 dark:text-amber-400" />
          <div className="flex-1 min-w-0">
            <p className="text-sm font-medium text-foreground">
              GitHub credential sync failed
            </p>
            <p className="mt-1 text-xs text-muted-foreground">
              We couldn't save your GitHub credential to Reliant Cloud. Git
              operations may not work until this is resolved.
            </p>
            <div className="mt-2 flex items-center gap-2">
              <button
                type="button"
                onClick={onRetry}
                disabled={retryInFlight}
                className="rounded-md border border-border bg-background px-2.5 py-1 text-xs font-medium hover:bg-accent disabled:opacity-50"
              >
                {retryInFlight ? "Retrying…" : "Retry"}
              </button>
              <button
                type="button"
                onClick={() => setDismissed(true)}
                className="rounded-md px-2 py-1 text-xs text-muted-foreground hover:bg-accent"
              >
                Dismiss
              </button>
            </div>
          </div>
          <button
            type="button"
            onClick={() => setDismissed(true)}
            className="rounded p-0.5 text-muted-foreground hover:bg-accent"
            aria-label="Dismiss"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>
    );
  }

  return null;
}
