import { useCallback, useEffect, useState } from "react";
import { Github, Loader2, Plus, RefreshCw, Trash2, X, Cloud } from "lucide-react";
import { Button } from "../ui/Button";
import { gitService } from "../../services/controlPlane/git";
import type {
  GitAppInstallation,
  GitCredentialHealth,
  GitCredentialKind,
  GitCredentialStatus,
} from "../../services/controlPlane/git/types";
import { capabilities } from "../../services/controlPlane/capabilities";
import { supabase } from "../../lib/supabase";
import { ManageGitHubAccess } from "../Projects/ManageGitHubAccess";

/** Plain-English name for the credential kind. The kind is what determines
 *  whether the token expires and whether its scopes mean anything, so it is
 *  worth saying out loud rather than leaving the user to infer it. */
function describeCredentialKind(kind: GitCredentialKind): string {
  switch (kind) {
    case "github_app":
      return "GitHub App";
    case "oauth_app":
      return "OAuth app";
    case "pat":
      return "Personal access token";
    default:
      return "GitHub";
  }
}

/** Whether the connection currently works — the one fact the old page never
 *  showed, and the first thing anyone debugging a failed clone wants. */
function TokenHealthLine({
  health,
  expiresAt,
}: {
  health: GitCredentialHealth;
  expiresAt?: string;
}) {
  if (health === "needsReconnect") {
    return (
      <p className="mt-1 text-xs font-medium text-destructive">
        Access expired — reconnect GitHub to keep cloning private repos.
      </p>
    );
  }
  if (health === "expired") {
    return (
      <p className="mt-1 text-xs font-medium text-destructive">
        Token expired and can&apos;t be renewed automatically. Reconnect GitHub.
      </p>
    );
  }
  if (health !== "valid") return null;
  return (
    <p className="mt-1 text-xs text-muted-foreground">
      Access is valid
      {/* An App token is renewed automatically, so its eight-hour expiry is
          not something the user must act on — say so, or the date reads as a
          deadline. */}
      {expiresAt ? " and renews automatically." : "."}
    </p>
  );
}

/** The GitHub App installations this credential can reach, each linking to
 *  where repository access is managed, plus the install flow itself.
 *
 *  Both facts matter and neither is the token: an empty list is why a private
 *  repo is invisible, and "selected repositories" is why a repo on an account
 *  that IS connected can still be missing. Reconnecting fixes neither.
 *
 *  The install link comes from the control plane (it knows the App slug);
 *  where it is absent we fall back to the user's own installations page,
 *  which at least gets them to the right settings screen. */
function InstallationsPanel({
  installations,
  installUrl,
  kind,
}: {
  installations: GitAppInstallation[];
  installUrl?: string;
  /** Which sort of token this is. Only a GitHub App token (`ghu_`) can
   *  enumerate installations; for every other kind the control plane returns
   *  an empty list that means "not applicable", not "none installed". */
  kind?: GitCredentialKind;
}) {
  // Believe an empty list only for the one credential kind that can actually
  // report installations. Otherwise the panel keeps the neutral wording — the
  // CTA is identical, so the only thing at stake is whether we assert
  // something false about the user's account.
  const installationsKnown = kind === "github_app";
  return (
    <ManageGitHubAccess
      installUrl={installUrl ?? "https://github.com/settings/installations"}
      installations={installations}
      installationsKnown={installationsKnown}
      variant={
        installations.length === 0 && installationsKnown ? "prominent" : "footer"
      }
    />
  );
}

export function GitConnectionsSettings() {
  const [credential, setCredential] = useState<GitCredentialStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);
  const [connectingOAuth, setConnectingOAuth] = useState(false);

  const [adding, setAdding] = useState(false);
  const [pat, setPat] = useState("");
  const [submittingPat, setSubmittingPat] = useState(false);

  const hasToken = credential?.hasToken ?? false;
  const scopes = credential?.scopes ?? "";
  const kind = credential?.kind ?? "unknown";
  const health = credential?.health ?? "unknown";

  // A GitHub App's access comes from its per-repository INSTALLATIONS, not
  // from OAuth scopes — GitHub ignores the scope parameter for Apps entirely.
  // So the "no repo scope" warning is meaningful for an OAuth App token or a
  // PAT and actively misleading for an App, where the real question is which
  // orgs the App is installed on.
  const scopesAreMeaningful = kind === "oauth_app" || kind === "pat";
  const hasRepoScope = scopes
    .split(/[,\s]+/)
    .map((scope) => scope.trim())
    .filter(Boolean)
    .includes("repo");

  const refresh = useCallback(async () => {
    setError(null);
    try {
      setCredential(await gitService.getCredential("github"));
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load credential");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    if (!capabilities.gitConnections) {
      setLoading(false);
      return;
    }
    refresh();
  }, [refresh]);

  // Installing the App or changing its repo selection happens on github.com.
  // Returning to a page that still shows the old installation list reads as
  // "it didn't take", so re-read the status on the way back in.
  useEffect(() => {
    if (!capabilities.gitConnections) return;
    const refreshOnReturn = () => {
      if (document.visibilityState === "hidden") return;
      void refresh();
    };
    window.addEventListener("focus", refreshOnReturn);
    document.addEventListener("visibilitychange", refreshOnReturn);
    return () => {
      window.removeEventListener("focus", refreshOnReturn);
      document.removeEventListener("visibilitychange", refreshOnReturn);
    };
  }, [refresh]);

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    if (params.get("github_connected") === "true") {
      window.history.replaceState({}, "", window.location.pathname);
    }
    if (params.get("github_error")) {
      setError(params.get("github_error_msg") || params.get("github_error") || "GitHub connection failed");
      window.history.replaceState({}, "", window.location.pathname);
    }
  }, []);

  const handleConnectOAuth = async () => {
    setConnectingOAuth(true);
    setError(null);
    try {
      const oauthURL = gitService.getOAuthURL();
      if (!oauthURL) throw new Error("Control plane URL not configured");
      const { data: { session } } = await supabase.auth.getSession();
      if (!session) throw new Error("No active session");
      const returnTo = `${window.location.pathname}${window.location.search}`;
      const params = new URLSearchParams({ token: session.access_token, returnTo });
      window.location.href = `${oauthURL}?${params.toString()}`;
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to start OAuth flow");
      setConnectingOAuth(false);
    }
  };

  const handleAddPat = async () => {
    const token = pat.trim();
    if (!token) return;
    setSubmittingPat(true);
    setError(null);
    try {
      await gitService.saveCredential("github", token, "repo");
      setPat("");
      setAdding(false);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to add token");
    } finally {
      setSubmittingPat(false);
    }
  };

  const handleDisconnect = async () => {
    if (!window.confirm("Disconnect GitHub? Reliant will lose access to your repos.")) {
      return;
    }
    setDisconnecting(true);
    setError(null);
    try {
      await gitService.deleteCredential("github");
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to disconnect");
    } finally {
      setDisconnecting(false);
    }
  };

  if (!capabilities.gitConnections) {
    return (
      <div className="space-y-6">
        <div>
          <h2 className="mb-2 text-lg font-semibold">GitHub connection</h2>
          <p className="text-sm text-muted-foreground">
            Connect your GitHub account so Reliant can clone private repos and
            push changes.
          </p>
        </div>

        <div className="rounded-lg border border-border p-6 text-center space-y-3">
          <div className="mx-auto flex h-10 w-10 items-center justify-center rounded-full bg-muted">
            <Cloud className="h-5 w-5 text-muted-foreground" />
          </div>
          <h3 className="text-sm font-medium">Cloud feature</h3>
          <p className="mx-auto max-w-sm text-sm text-muted-foreground">
            Git connections let Reliant securely store GitHub credentials and
            clone private repositories on your behalf. This feature requires a
            Reliant Cloud account.
          </p>
        </div>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <div>
        <h2 className="mb-2 text-lg font-semibold">GitHub connection</h2>
        <p className="text-sm text-muted-foreground">
          Connect your GitHub account so Reliant can clone private repos and
          push changes.
        </p>
      </div>

      {error && (
        <div className="rounded-lg border border-red-200 bg-red-50 p-3 dark:border-red-800 dark:bg-red-950/20">
          <p className="text-sm text-red-800 dark:text-red-200">{error}</p>
        </div>
      )}

      <div className="space-y-3 rounded-lg border border-border p-4">
        <h3 className="font-medium">Connection status</h3>
        {loading ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" />
            Loading...
          </div>
        ) : hasToken ? (
          <div className="space-y-3">
          <div className="flex items-start justify-between gap-3 rounded-lg border border-border px-3 py-2">
            <div className="flex items-start gap-3 min-w-0">
              {credential?.accountAvatarUrl ? (
                <img
                  src={credential.accountAvatarUrl}
                  alt=""
                  className="mt-0.5 h-8 w-8 rounded-full border border-border"
                />
              ) : (
                <Github className="mt-1 h-4 w-4 text-muted-foreground" />
              )}
              <div className="min-w-0">
                <p className="text-sm font-medium">
                  {credential?.accountLogin
                    ? `Connected as ${credential.accountLogin}`
                    : "GitHub connected"}
                </p>
                <p className="text-xs text-muted-foreground">
                  {describeCredentialKind(kind)}
                  {scopesAreMeaningful && ` · Scopes: ${scopes || "(none)"}`}
                </p>
                <TokenHealthLine health={health} expiresAt={credential?.expiresAt} />
                <p className="mt-1 text-xs text-muted-foreground">
                  Private org repos can still require org OAuth approval or SSO authorization.
                </p>
                {/* Said here because the Reconnect button is right there, and
                    it is the button people reach for when a repo is missing —
                    which it cannot fix. */}
                <p className="mt-1 text-xs text-muted-foreground">
                  Reconnect signs you in to GitHub again. To change which
                  accounts or repositories Reliant can see, use the repository
                  access controls below.
                </p>
              </div>
            </div>
            <div className="flex flex-shrink-0 items-center gap-1">
              <Button
                variant="ghost"
                size="xs"
                onClick={handleConnectOAuth}
                disabled={connectingOAuth}
                leftIcon={
                  connectingOAuth ? (
                    <Loader2 className="h-4 w-4 animate-spin" />
                  ) : (
                    <RefreshCw className="h-4 w-4" />
                  )
                }
              >
                Reconnect
              </Button>
              <Button
                variant="ghost"
                size="xs"
                onClick={handleDisconnect}
                disabled={disconnecting}
              >
                {disconnecting ? (
                  <Loader2 className="h-4 w-4 animate-spin" />
                ) : (
                  <Trash2 className="h-4 w-4 text-muted-foreground" />
                )}
              </Button>
            </div>
          </div>

          {/* For a GitHub App, WHICH orgs it is installed on is the fact that
              determines whether a given private repo can be cloned at all —
              far more useful than the scope string it replaces. */}
          {kind === "github_app" && (
            <InstallationsPanel
              installations={credential?.installations ?? []}
              installUrl={credential?.installUrl}
              kind={credential?.kind}
            />
          )}

          {/* A credential whose kind we cannot determine gets no installations
              panel, because we cannot tell that it is an App — control-plane
              derives the kind from the token's prefix, so a token it cannot
              decrypt, or one written before the GitHub App existed, arrives
              as "unknown". Withholding the panel is right, but withholding it
              SILENTLY leaves this page with nothing to say about why
              repository access cannot be managed. Name the remedy instead. */}
          {kind === "unknown" && health !== "valid" && (
            <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-xs text-amber-900 dark:text-amber-200">
              Reconnect GitHub to enable repository access management. This
              connection was stored in a form Reliant can no longer read, so it
              can&apos;t tell which repositories it is allowed to see.
              <div className="mt-2">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={handleConnectOAuth}
                  disabled={connectingOAuth}
                  leftIcon={
                    connectingOAuth ? (
                      <Loader2 className="h-4 w-4 animate-spin" />
                    ) : (
                      <Github className="h-4 w-4" />
                    )
                  }
                >
                  {connectingOAuth ? "Connecting..." : "Reconnect GitHub"}
                </Button>
              </div>
            </div>
          )}

          {scopesAreMeaningful && !hasRepoScope && (
            <div className="rounded-lg border border-amber-500/30 bg-amber-500/10 p-3 text-xs text-amber-900 dark:text-amber-200">
              This GitHub token has no <code>repo</code> scope, so Reliant can only see public repositories.
              Reauthorize GitHub to grant the required repository access.
              <div className="mt-2 flex flex-wrap gap-2">
                <Button
                  variant="outline"
                  size="sm"
                  onClick={handleConnectOAuth}
                  disabled={connectingOAuth}
                  leftIcon={connectingOAuth ? <Loader2 className="h-4 w-4 animate-spin" /> : <Github className="h-4 w-4" />}
                >
                  {connectingOAuth ? "Connecting..." : "Reauthorize GitHub"}
                </Button>
                {!adding && (
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setAdding(true)}
                    leftIcon={<Plus className="h-4 w-4" />}
                  >
                    Use a personal access token
                  </Button>
                )}
              </div>
            </div>
          )}
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">No GitHub token configured.</p>
        )}
      </div>

      {!loading && (!hasToken || adding) && (
        <div className="space-y-3 rounded-lg border border-border p-4">
          <h3 className="font-medium">
            {hasToken ? "Personal access token" : "Connect GitHub"}
          </h3>
          <p className="text-xs text-muted-foreground">
            {hasToken
              ? "Paste a GitHub token to use instead of the OAuth connection above. This is a manual fallback for debugging an OAuth or org SSO authorization problem — it replaces the stored credential and, unlike the OAuth connection, is never renewed automatically."
              : "Sign in with GitHub via OAuth. Pasting a token by hand is a fallback for debugging authorization problems."}
          </p>

          {!hasToken && (
            <div className="flex flex-wrap gap-2">
              <Button
                variant="outline"
                size="sm"
                onClick={handleConnectOAuth}
                disabled={connectingOAuth}
                leftIcon={connectingOAuth ? <Loader2 className="h-4 w-4 animate-spin" /> : <Github className="h-4 w-4" />}
              >
                {connectingOAuth ? "Connecting..." : "Connect with GitHub"}
              </Button>
              {!adding && (
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => setAdding(true)}
                  leftIcon={<Plus className="h-4 w-4" />}
                >
                  Use a personal access token
                </Button>
              )}
            </div>
          )}

          {adding && (
            <div className="space-y-2 rounded-lg border border-border bg-muted/30 p-3">
              <label className="block text-xs font-medium">
                Personal access token (classic or fine-grained)
              </label>
              <input
                type="password"
                autoComplete="off"
                spellCheck={false}
                value={pat}
                onChange={(e) => setPat(e.target.value)}
                placeholder="ghp_..."
                className="w-full rounded border border-border bg-background px-3 py-2 font-mono text-xs"
              />
              <p className="text-xs text-muted-foreground">
                Generate at{" "}
                <a
                  href="https://github.com/settings/tokens/new?scopes=repo&description=Reliant"
                  target="_blank"
                  rel="noopener noreferrer"
                  className="underline hover:text-foreground"
                >
                  github.com/settings/tokens
                </a>{" "}
                with the <code>repo</code> scope.
              </p>
              <div className="flex gap-2">
                <Button
                  variant="primary"
                  size="sm"
                  onClick={handleAddPat}
                  disabled={!pat.trim() || submittingPat}
                >
                  {submittingPat ? (
                    <Loader2 className="h-4 w-4 animate-spin" />
                  ) : (
                    "Add token"
                  )}
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setAdding(false);
                    setPat("");
                  }}
                  leftIcon={<X className="h-4 w-4" />}
                >
                  Cancel
                </Button>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
