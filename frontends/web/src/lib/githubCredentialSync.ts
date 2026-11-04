/**
 * githubCredentialSync
 *
 * Centralized, retrying, observable bridge between Supabase GitHub OAuth and
 * the control-plane `git_credentials` table.
 *
 * Supabase's GitHub provider may be backed by either of two GitHub apps, and
 * only one of them yields a token worth keeping:
 *
 *   - the GitHub App `reliant-labs`: `provider_token` is a user-to-server token
 *     (`ghu_…`) that expires, with a `provider_refresh_token`. Signing in IS
 *     connecting GitHub, so we persist both and the user consents once.
 *   - a classic OAuth app (`gho_…`, 0 scopes): sign-in only. Persisting it
 *     would store a token that cannot read a single private repo, so we don't.
 *     Repo access then comes from the dedicated connect flow.
 *
 * `syncProviderTokenFromSession` is the gate between the two.
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
 *     `GitHubSyncStatus`.
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
  refreshToken: string = "",
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
      await gitService.saveCredential("github", providerToken, scopes, refreshToken);
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

/** Prefix of a GitHub App user-to-server token. */
const GITHUB_APP_USER_TOKEN_PREFIX = "ghu_";

/**
 * Persist the GitHub credential a Supabase sign-in produced, when it is one
 * worth persisting: a GitHub App user token. Anything else — a classic
 * `gho_` token, another provider's token, no token — is left alone.
 */
async function syncProviderTokenFromSession(session: {
  provider_token?: string | null;
  provider_refresh_token?: string | null;
} | null): Promise<GitHubCredentialSyncResult | null> {
  const token = session?.provider_token;
  if (!token || !token.startsWith(GITHUB_APP_USER_TOKEN_PREFIX)) return null;
  // An App token's access is governed by its installations, not by an OAuth
  // scope string, so there is no scope to claim here.
  return sync(token, "", "signin", session?.provider_refresh_token ?? "");
}

export const githubCredentialSync = {
  sync,
  syncProviderTokenFromSession,
};
