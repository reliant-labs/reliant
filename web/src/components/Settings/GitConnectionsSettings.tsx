import { useCallback, useEffect, useState } from "react";
import { Github, Loader2, Plus, Trash2, X } from "lucide-react";
import { Button } from "../ui/Button";
import {
  deleteGitCredential,
  listGitCredentials,
  saveGitCredential,
} from "../../api/controlplane-client";
import type { GitAccount } from "../../api/controlplane-client";
import { useAuthStore } from "../../store/authStore";

export function GitConnectionsSettings() {
  const linkGithubAccount = useAuthStore((s) => s.linkGithubAccount);

  const [accounts, setAccounts] = useState<GitAccount[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busyAccount, setBusyAccount] = useState<string | null>(null);

  const [adding, setAdding] = useState(false);
  const [pat, setPat] = useState("");
  const [submittingPat, setSubmittingPat] = useState(false);

  const refresh = useCallback(async () => {
    setError(null);
    try {
      const res = await listGitCredentials("github");
      setAccounts(res.accounts ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to load accounts");
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  const handleConnectOAuth = async () => {
    setError(null);
    try {
      await linkGithubAccount();
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "OAuth link failed");
    }
  };

  const handleAddPat = async () => {
    const token = pat.trim();
    if (!token) return;
    setSubmittingPat(true);
    setError(null);
    try {
      await saveGitCredential("github", token, "repo");
      setPat("");
      setAdding(false);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to add token");
    } finally {
      setSubmittingPat(false);
    }
  };

  const handleDisconnect = async (account: GitAccount) => {
    if (!window.confirm(`Disconnect @${account.account_login}? Reliant will lose access to repos on this account.`)) {
      return;
    }
    setBusyAccount(account.account_login);
    setError(null);
    try {
      await deleteGitCredential(account.provider, account.account_login);
      await refresh();
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to disconnect");
    } finally {
      setBusyAccount(null);
    }
  };

  return (
    <div className="space-y-6">
      <div>
        <h2 className="mb-2 text-lg font-semibold">GitHub connections</h2>
        <p className="text-sm text-muted-foreground">
          Connect one or more GitHub accounts so Reliant can clone private
          repos and push changes. When cloning, Reliant uses the connected
          account whose login matches the repo owner.
        </p>
      </div>

      {error && (
        <div className="rounded-lg border border-red-200 bg-red-50 p-3 dark:border-red-800 dark:bg-red-950/20">
          <p className="text-sm text-red-800 dark:text-red-200">{error}</p>
        </div>
      )}

      <div className="space-y-3 rounded-lg border border-border p-4">
        <h3 className="font-medium">Connected accounts</h3>
        {loading ? (
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="h-4 w-4 animate-spin" />
            Loading...
          </div>
        ) : accounts.length === 0 ? (
          <p className="text-sm text-muted-foreground">No GitHub accounts connected.</p>
        ) : (
          <div className="space-y-2">
            {accounts.map((acct) => (
              <div
                key={acct.account_login}
                className="flex items-center justify-between rounded-lg border border-border px-3 py-2"
              >
                <div className="flex items-center gap-3">
                  <Github className="h-4 w-4 text-muted-foreground" />
                  <div>
                    <p className="text-sm font-medium">
                      {acct.account_login || <span className="italic text-muted-foreground">unverified</span>}
                    </p>
                    <p className="text-xs text-muted-foreground">
                      Scopes: {acct.scopes || "(none)"}
                    </p>
                  </div>
                </div>
                <Button
                  variant="ghost"
                  size="xs"
                  onClick={() => handleDisconnect(acct)}
                  disabled={busyAccount === acct.account_login}
                >
                  {busyAccount === acct.account_login ? (
                    <Loader2 className="h-4 w-4 animate-spin" />
                  ) : (
                    <Trash2 className="h-4 w-4 text-muted-foreground" />
                  )}
                </Button>
              </div>
            ))}
          </div>
        )}
      </div>

      <div className="space-y-3 rounded-lg border border-border p-4">
        <h3 className="font-medium">Add another account</h3>
        <p className="text-xs text-muted-foreground">
          Two ways to connect: sign in with GitHub OAuth (best for your
          primary account) or paste a personal access token. Tokens are
          handy for adding a second account (e.g. work + personal) without
          a sign-in dance.
        </p>

        <div className="flex flex-wrap gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={handleConnectOAuth}
            leftIcon={<Github className="h-4 w-4" />}
          >
            Connect with GitHub
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
              with the <code>repo</code> scope. Reliant calls{" "}
              <code>/user</code> to verify and store the account login.
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
                  "Add account"
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
    </div>
  );
}
