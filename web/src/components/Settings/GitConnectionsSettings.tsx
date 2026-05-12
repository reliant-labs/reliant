import { useCallback, useEffect, useState } from "react";
import { Github, Loader2, Plus, Trash2, X, Cloud } from "lucide-react";
import { Button } from "../ui/Button";
import { gitService } from "../../services/controlPlane/git";
import { capabilities } from "../../services/controlPlane/capabilities";
import { supabase } from "../../lib/supabase";

export function GitConnectionsSettings() {
  const [hasToken, setHasToken] = useState(false);
  const [scopes, setScopes] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [disconnecting, setDisconnecting] = useState(false);
  const [connectingOAuth, setConnectingOAuth] = useState(false);

  const [adding, setAdding] = useState(false);
  const [pat, setPat] = useState("");
  const [submittingPat, setSubmittingPat] = useState(false);

  const refresh = useCallback(async () => {
    setError(null);
    try {
      const res = await gitService.getCredential("github");
      setHasToken(res.hasToken);
      setScopes(res.scopes || "");
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
      window.location.href = `${oauthURL}?token=${session.access_token}`;
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
          <div className="flex items-center justify-between rounded-lg border border-border px-3 py-2">
            <div className="flex items-center gap-3">
              <Github className="h-4 w-4 text-muted-foreground" />
              <div>
                <p className="text-sm font-medium">GitHub connected</p>
                <p className="text-xs text-muted-foreground">
                  Scopes: {scopes || "(none)"}
                </p>
              </div>
            </div>
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
        ) : (
          <p className="text-sm text-muted-foreground">No GitHub token configured.</p>
        )}
      </div>

      {!hasToken && !loading && (
        <div className="space-y-3 rounded-lg border border-border p-4">
          <h3 className="font-medium">Connect GitHub</h3>
          <p className="text-xs text-muted-foreground">
            Sign in with GitHub via OAuth, or paste a personal access token.
          </p>

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
                Paste a token
              </Button>
            )}
          </div>

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
              <p className="text-[11px] text-muted-foreground">
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
