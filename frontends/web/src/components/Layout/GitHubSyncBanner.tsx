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

import { gitService } from "@/services/controlPlane/git";
import { supabase } from "@/lib/supabase";
import { trackEvent } from "@/lib/analytics";
import { logger } from "@/lib/logger";
import { useEvent } from "@/lib/event-context";

const SYNC_TOAST_ID = "github-sync";

async function retryGitHubOAuth() {
  trackEvent("github_credential_sync_retry_clicked");
  try {
    // Restart the control-plane custom OAuth flow. The backend
    // /auth/github/authorize endpoint will exchange the code for a
    // long-lived repo-scoped token and write it to git_credentials,
    // which clears the failed-sync state on return.
    const oauthURL = gitService.getOAuthURL();
    if (!oauthURL) throw new Error("Control plane URL not configured");
    const { data: { session } } = await supabase.auth.getSession();
    if (!session?.access_token) throw new Error("Not signed in");
    const returnTo = `${window.location.pathname}${window.location.search}`;
    const params = new URLSearchParams({
      token: session.access_token,
      returnTo,
    });
    window.location.href = `${oauthURL}?${params.toString()}`;
  } catch (err) {
    logger.warn("[GitHubSyncStatus] retry GitHub OAuth failed", err);
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
