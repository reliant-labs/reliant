/**
 * Mobile-native "GitHub connection" panel — connection status, connect via
 * OAuth, and disconnect. Reuses the exact same data layer as desktop
 * `GitConnectionsSettings`: `useGitHubCredential` for status,
 * `gitService.getOAuthURL()` + the Supabase session token to kick off the
 * same `/auth/github/authorize` redirect, and `gitService.deleteCredential`
 * to disconnect. A divergent write path here would be a real bug, not a UI
 * difference — see that hook's module comment.
 *
 * The desktop component's personal-access-token fallback (for debugging org
 * SSO/authorization issues) is intentionally omitted here: it is a
 * debugging escape hatch, not a path a phone user should reach for, and
 * pasting a token on a phone keyboard is exactly the friction that path
 * exists to avoid on desktop. OAuth remains the only mobile connect path;
 * reconnecting via OAuth is also how a user recovers from an insufficient
 * scope grant.
 */

import { useEffect, useState } from "react";
import { AlertTriangle, Github, Loader2 } from "lucide-react";
import { gitService } from "../../services/controlPlane/git";
import { supabase } from "../../lib/supabase";
import { useGitHubCredential } from "../../hooks/useGitHubCredential";
import { cn } from "../../lib/utils";
import { MOBILE_PRIMARY_ACTION } from "./MobileChrome";

export function MobileGitHubPanel() {
  const { hasToken, scopes, isLoading, refresh } = useGitHubCredential();
  const [error, setError] = useState<string | null>(null);
  const [connecting, setConnecting] = useState(false);
  const [disconnecting, setDisconnecting] = useState(false);
  const [confirmingDisconnect, setConfirmingDisconnect] = useState(false);

  // Mirrors desktop GitConnectionsSettings: the OAuth callback lands back on
  // this route with these params, which must be consumed and stripped so a
  // refresh doesn't replay them.
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    if (params.get("github_connected") === "true") {
      window.history.replaceState({}, "", window.location.pathname);
      void refresh();
    }
    if (params.get("github_error")) {
      setError(
        params.get("github_error_msg") || params.get("github_error") || "GitHub connection failed",
      );
      window.history.replaceState({}, "", window.location.pathname);
    }
  }, [refresh]);

  const handleConnect = async () => {
    setConnecting(true);
    setError(null);
    try {
      const oauthURL = gitService.getOAuthURL();
      if (!oauthURL) throw new Error("Control plane URL not configured");
      const {
        data: { session },
      } = await supabase.auth.getSession();
      if (!session) throw new Error("No active session");
      const returnTo = `${window.location.pathname}${window.location.search}`;
      const params = new URLSearchParams({ token: session.access_token, returnTo });
      window.location.href = `${oauthURL}?${params.toString()}`;
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to start OAuth flow");
      setConnecting(false);
    }
  };

  const handleDisconnect = async () => {
    setDisconnecting(true);
    setError(null);
    try {
      await gitService.deleteCredential("github");
      await refresh();
      setConfirmingDisconnect(false);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to disconnect");
    } finally {
      setDisconnecting(false);
    }
  };

  const scopeParts = scopes
    .split(/[,\s]+/)
    .map((scope) => scope.trim())
    .filter(Boolean);
  const hasRepoScope = scopeParts.includes("repo");

  if (isLoading) {
    return (
      <div className="flex items-center gap-2 px-4 py-6 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
        Loading…
      </div>
    );
  }

  return (
    <div className="divide-y divide-border">
      {error && (
        <div className="mx-4 mt-4 rounded-lg border border-destructive/30 bg-destructive/10 p-3">
          <p className="text-sm text-destructive">{error}</p>
        </div>
      )}

      <div className="flex min-h-16 items-center gap-3 px-4 py-3">
        <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
          <Github className="h-4 w-4" />
        </span>
        <div className="min-w-0 flex-1">
          <p className="text-sm font-medium text-foreground">
            {hasToken ? "GitHub connected" : "Not connected"}
          </p>
          <p className="mt-0.5 text-xs text-muted-foreground">
            {hasToken
              ? `Scopes: ${scopes || "(none)"}`
              : "Connect GitHub so Reliant can clone private repos onto your machines."}
          </p>
        </div>
      </div>

      {hasToken && !hasRepoScope && (
        <div className="mx-4 my-3 flex items-start gap-2 rounded-lg border border-amber-500/30 bg-amber-500/10 p-3">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400" />
          <p className="text-xs text-amber-900 dark:text-amber-200">
            This token has no <code>repo</code> scope, so Reliant can only see public
            repositories. Reauthorize to grant full access.
          </p>
        </div>
      )}

      <div className="p-4">
        {hasToken ? (
          confirmingDisconnect ? (
            <div className="flex flex-col gap-2">
              <p className="text-sm text-muted-foreground">
                Disconnect GitHub? Reliant will lose access to your repos.
              </p>
              <div className="flex gap-2">
                <button
                  type="button"
                  onClick={() => setConfirmingDisconnect(false)}
                  disabled={disconnecting}
                  className="flex min-h-[44px] flex-1 items-center justify-center rounded-lg border border-border text-sm font-medium text-foreground active:bg-foreground/5 disabled:opacity-60"
                >
                  Cancel
                </button>
                <button
                  type="button"
                  onClick={() => void handleDisconnect()}
                  disabled={disconnecting}
                  className="flex min-h-[44px] flex-1 items-center justify-center rounded-lg bg-destructive text-sm font-medium text-destructive-foreground active:opacity-80 disabled:opacity-60"
                >
                  {disconnecting ? "Disconnecting…" : "Disconnect"}
                </button>
              </div>
            </div>
          ) : (
            <div className="flex flex-col gap-2">
              {!hasRepoScope && (
                <button
                  type="button"
                  onClick={() => void handleConnect()}
                  disabled={connecting}
                  className={cn(MOBILE_PRIMARY_ACTION, "w-full")}
                >
                  {connecting ? (
                    <Loader2 className="h-4 w-4 animate-spin" />
                  ) : (
                    <Github className="h-4 w-4" />
                  )}
                  {connecting ? "Connecting…" : "Reauthorize GitHub"}
                </button>
              )}
              <button
                type="button"
                onClick={() => setConfirmingDisconnect(true)}
                className="flex min-h-[44px] w-full items-center justify-center rounded-lg border border-border text-sm font-medium text-destructive active:bg-foreground/5"
              >
                Disconnect
              </button>
            </div>
          )
        ) : (
          <button
            type="button"
            onClick={() => void handleConnect()}
            disabled={connecting}
            className={cn(MOBILE_PRIMARY_ACTION, "w-full")}
          >
            {connecting ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : (
              <Github className="h-4 w-4" />
            )}
            {connecting ? "Connecting…" : "Connect with GitHub"}
          </button>
        )}
      </div>
    </div>
  );
}
