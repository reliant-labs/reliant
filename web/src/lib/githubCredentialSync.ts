/**
 * githubCredentialSync
 *
 * Centralized, retrying, observable bridge between Supabase GitHub OAuth and
 * the control-plane `git_credentials` table. Whenever Supabase hands us a
 * transient `provider_token` (a one-shot GitHub PAT) we have to persist it
 * to the control plane so subsequent git operations can use it.
 *
 * Historically each callsite did a fire-and-forget `gitService.saveCredential`
 * with a console.warn on failure. When that save failed the user would land
 * in the app with a Supabase GitHub identity but no row in `git_credentials`,
 * be greeted by the "Reconnect GitHub" prompt, and have to OAuth a second
 * time. This module makes the bridge bulletproof:
 *
 *   - Retries with exponential backoff (500ms, 2s, 8s).
 *   - Broadcasts sync state via the typed EventBus
 *     (`github-credential:syncing|succeeded|failed`) for UI consumers like
 *     `GitHubSyncBanner`.
 *   - Invalidates the React Query cache for the `useGitHubCredential` hook
 *     so the UI updates immediately on success.
 *   - Treats local-only deployments (no control plane configured) as a
 *     no-op so this module is safe to call unconditionally.
 *   - Emits analytics for observability.
 */

import { gitService } from "@/services/controlPlane/git";
import { hasControlPlane } from "@/services/controlPlane/config";
import { queryClient } from "@/lib/query-client";
import { logger } from "@/lib/logger";
import { trackEvent } from "@/lib/analytics";
import { getEventBus } from "@/lib/events";
import { GITHUB_CREDENTIAL_QUERY_KEY } from "@/hooks/useGitHubCredential";

export type GitHubCredentialSyncTrigger =
  | "signin"
  | "link"
  | "session_refresh"
  | "retry";

export interface GitHubCredentialSyncResult {
  ok: boolean;
  error?: string;
}

const RETRY_DELAYS_MS = [500, 2000, 8000];

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function describeError(err: unknown): string {
  if (err instanceof Error) return err.message;
  if (typeof err === "string") return err;
  try {
    return JSON.stringify(err);
  } catch {
    return "unknown_error";
  }
}

async function sync(
  providerToken: string,
  scopes: string = "repo",
  trigger: GitHubCredentialSyncTrigger = "signin",
): Promise<GitHubCredentialSyncResult> {
  // Local-only deploys have no control plane; treat as success no-op.
  if (!hasControlPlane) {
    return { ok: true };
  }

  if (!providerToken) {
    return { ok: false, error: "missing_provider_token" };
  }

  trackEvent("github_credential_sync_started", { trigger });
  try {
    getEventBus().emit("github-credential:syncing");
  } catch {
    /* bus not ready */
  }

  let lastError: unknown = null;
  for (let attempt = 1; attempt <= RETRY_DELAYS_MS.length; attempt += 1) {
    try {
      await gitService.saveCredential("github", providerToken, scopes);
      try {
        const now = new Date().toISOString();
        queryClient.setQueryData(GITHUB_CREDENTIAL_QUERY_KEY, {
          available: true,
          hasToken: true,
          provider: "github",
          scopes,
          createdAt: now,
          updatedAt: now,
        });
        await queryClient.invalidateQueries({
          queryKey: GITHUB_CREDENTIAL_QUERY_KEY,
        });
      } catch (cacheErr) {
        logger.debug(
          "[githubCredentialSync] cache invalidation failed",
          cacheErr,
        );
      }
      try {
        getEventBus().emit("github-credential:succeeded", { trigger, attempt });
      } catch {
        /* bus not ready */
      }
      trackEvent("github_credential_sync_succeeded", { trigger, attempt });
      return { ok: true };
    } catch (err) {
      lastError = err;
      logger.warn(
        `[githubCredentialSync] saveCredential failed (attempt ${attempt}/${RETRY_DELAYS_MS.length})`,
        err,
      );
      if (attempt < RETRY_DELAYS_MS.length) {
        await sleep(RETRY_DELAYS_MS[attempt - 1]);
      }
    }
  }

  const errorMessage = describeError(lastError);
  try {
    getEventBus().emit("github-credential:failed", {
      trigger,
      attempts: RETRY_DELAYS_MS.length,
      error: errorMessage,
    });
  } catch {
    /* bus not ready */
  }
  trackEvent("github_credential_sync_failed", {
    trigger,
    attempts: RETRY_DELAYS_MS.length,
    error: errorMessage,
  });
  return { ok: false, error: errorMessage };
}

export const githubCredentialSync = {
  sync,
};
