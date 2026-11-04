/**
 * GitHubSyncStatus
 *
 * Headless surface for the GitHub credential sync state managed by
 * `githubCredentialSync`. Renders nothing — it just bridges sync events to
 * sonner toasts so the user gets a consistent, top-of-app notification
 * regardless of which route they're on.
 *
 *  - status='syncing': loading toast pinned by stable id (no stacking).
 *  - status='succeeded': dismisses the loading toast.
 *  - status='failed': error toast with a Retry action that re-runs the
 *                     control-plane `/auth/github/authorize` flow, which
 *                     (re)issues a long-lived repo-scoped token and writes
 *                     it to `git_credentials`.
 *
 * Previously this was a `position: fixed` floating banner (`GitHubSyncBanner`)
 * which collided z-index-wise with the onboarding checklist and only mounted
 * inside `ModernApp` — so users on `/settings` or `/workflow/*` got no sync
 * feedback at all. Now mounted on the root route; works everywhere.
 */

import { toast } from "sonner";

import { connectGitHub } from "@/lib/github-connect";
import { trackEvent } from "@/lib/analytics";
import { logger } from "@/lib/logger";
import { useEvent } from "@/lib/event-context";

const SYNC_TOAST_ID = "github-sync";

async function retryGitHubOAuth() {
  trackEvent("github_credential_sync_retry_clicked");
  // Restart the control-plane connect flow, which re-issues a long-lived
  // credential and writes it to git_credentials, clearing the failed-sync
  // state on return.
  const outcome = await connectGitHub();
  if (outcome.kind === "error") {
    logger.warn("[GitHubSyncStatus] retry GitHub OAuth failed", outcome.message);
  }
}

export function GitHubSyncStatus() {
  useEvent("github-credential:syncing", () => {
    toast.loading("Syncing GitHub credentials…", { id: SYNC_TOAST_ID });
  });

  useEvent("github-credential:succeeded", () => {
    toast.dismiss(SYNC_TOAST_ID);
  });

  useEvent("github-credential:failed", () => {
    toast.error("GitHub credential sync failed", {
      id: SYNC_TOAST_ID,
      description:
        "We couldn't save your GitHub credential to Reliant Cloud. Git operations may not work until this is resolved.",
      duration: Infinity,
      action: {
        label: "Retry",
        onClick: () => {
          void retryGitHubOAuth();
        },
      },
    });
  });

  return null;
}
