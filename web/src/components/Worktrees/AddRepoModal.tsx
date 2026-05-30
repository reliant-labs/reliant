import { useState, useEffect, useCallback } from "react";
import { GitBranch, FolderOpen, Link, Loader2, CheckCircle2, Github } from "lucide-react";
import { Modal } from "../ui/Modal";
import { Button } from "../ui/Button";
import { gitService } from "../../services/controlPlane/git";
import type { GitAccount } from "../../services/controlPlane/git";
import { supabase } from "../../lib/supabase";

interface AddRepoModalProps {
  isOpen: boolean;
  onClose: () => void;
  daemonId: string;
}

type ModalState = "idle" | "loading" | "success" | "error";

export function AddRepoModal({ isOpen, onClose, daemonId }: AddRepoModalProps) {
  const [accounts, setAccounts] = useState<GitAccount[]>([]);
  const [checkingGitHub, setCheckingGitHub] = useState(false);
  const [selectedAccount, setSelectedAccount] = useState<string>("");

  const [repoUrl, setRepoUrl] = useState("");
  const [branch, setBranch] = useState("main");
  const [path, setPath] = useState("");
  const [modalState, setModalState] = useState<ModalState>("idle");
  const [error, setError] = useState<string | null>(null);
  const [clonedPath, setClonedPath] = useState<string | null>(null);

  const gitHubConnected = accounts.length > 0;

  // Extract repo name from URL and auto-populate path
  const extractRepoName = useCallback((url: string): string => {
    try {
      // Handle https://github.com/org/repo or https://github.com/org/repo.git
      const match = url.match(/\/([^/]+?)(?:\.git)?$/);
      return match?.[1] || "";
    } catch {
      return "";
    }
  }, []);

  useEffect(() => {
    if (repoUrl) {
      const repoName = extractRepoName(repoUrl);
      if (repoName) {
        setPath(`/home/workspace/projects/${repoName}`);
      }
    }
  }, [repoUrl, extractRepoName]);

  // Check GitHub credential status when modal opens
  useEffect(() => {
    if (!isOpen) return;

    // Reset state when modal opens
    setModalState("idle");
    setError(null);
    setClonedPath(null);

    const refreshAccounts = async () => {
      setCheckingGitHub(true);
      try {
        const res = await gitService.listCredentials("github");
        setAccounts(res.accounts ?? []);
        if (res.accounts?.length === 1) {
          setSelectedAccount(res.accounts[0].account_login);
        }
      } catch {
        setAccounts([]);
      } finally {
        setCheckingGitHub(false);
      }
    };

    refreshAccounts();
  }, [isOpen]);

  const handleConnectGitHub = async () => {
    try {
      // Kick off the control-plane custom OAuth flow. The Supabase GitHub
      // provider is sign-in only (0 scopes); the long-lived repo-scoped
      // token comes from /auth/github/authorize, which writes it to
      // git_credentials. The page will navigate away; on return, the
      // modal's open-effect re-runs and refreshes accounts.
      const oauthURL = gitService.getOAuthURL();
      if (!oauthURL) throw new Error("Control plane URL not configured");
      const { data: { session } } = await supabase.auth.getSession();
      if (!session?.access_token) throw new Error("Not signed in");
      const returnTo = `${window.location.pathname}${window.location.search}`;
      const params = new URLSearchParams({ token: session.access_token, returnTo });
      window.location.href = `${oauthURL}?${params.toString()}`;
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to connect GitHub");
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();

    if (!repoUrl) {
      setError("Repository URL is required");
      return;
    }

    setModalState("loading");
    setError(null);

    try {
      const result = await gitService.cloneRepo({
        daemonId,
        gitRepo: repoUrl,
        gitBranch: branch,
        path,
      });
      setClonedPath(result.clonedPath);
      setModalState("success");

      // Auto-close after brief delay on success
      setTimeout(() => {
        handleClose();
      }, 2000);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to clone repository");
      setModalState("error");
    }
  };

  const handleClose = () => {
    setRepoUrl("");
    setBranch("main");
    setPath("");
    setModalState("idle");
    setError(null);
    setClonedPath(null);
    onClose();
  };

  return (
    <Modal
      isOpen={isOpen}
      onClose={handleClose}
      title="Add Repository"
      size="lg"
    >
      {/* Loading GitHub status */}
      {checkingGitHub && (
        <div className="flex items-center justify-center py-12">
          <Loader2 className="w-6 h-6 text-muted-foreground animate-spin" />
        </div>
      )}

      {/* GitHub not connected */}
      {!checkingGitHub && !gitHubConnected && (
        <div className="space-y-6">
          <div className="flex flex-col items-center justify-center py-6 text-center">
            <div className="p-4 rounded-full bg-muted ring-1 ring-border mb-4">
              <Github className="w-8 h-8 text-muted-foreground" />
            </div>
            <h3 className="text-sm font-semibold text-foreground mb-2">
              Connect GitHub
            </h3>
            <p className="text-sm text-muted-foreground max-w-sm">
              Connect your GitHub account to clone private repositories.
              Public repos can still be cloned without connecting.
            </p>
          </div>

          <div className="flex gap-3 pt-4 border-t border-border">
            <Button
              onClick={handleClose}
              variant="secondary"
              className="flex-1"
            >
              Cancel
            </Button>
            <Button
              onClick={handleConnectGitHub}
              leftIcon={<Github className="w-4 h-4" />}
              variant="primary"
              className="flex-1"
            >
              Connect GitHub
            </Button>
          </div>
        </div>
      )}

      {/* Clone form */}
      {!checkingGitHub && gitHubConnected && (
        <form onSubmit={handleSubmit} className="space-y-6">
          {/* Account picker — only render when more than one account is connected */}
          {accounts.length > 1 && (
            <div className="space-y-2">
              <label className="block text-sm font-semibold text-foreground">
                GitHub account
              </label>
              <div className="flex flex-wrap gap-2">
                <button
                  type="button"
                  onClick={() => setSelectedAccount("")}
                  className={`rounded-lg border px-3 py-1.5 text-xs font-medium transition-colors ${
                    selectedAccount === ""
                      ? "border-primary bg-primary/10 text-primary"
                      : "border-border text-muted-foreground hover:border-primary/50"
                  }`}
                >
                  Auto (match repo owner)
                </button>
                {accounts.map((acct) => (
                  <button
                    key={acct.account_login}
                    type="button"
                    onClick={() => setSelectedAccount(acct.account_login)}
                    className={`rounded-lg border px-3 py-1.5 text-xs font-medium transition-colors ${
                      selectedAccount === acct.account_login
                        ? "border-primary bg-primary/10 text-primary"
                        : "border-border text-muted-foreground hover:border-primary/50"
                    }`}
                  >
                    @{acct.account_login}
                  </button>
                ))}
              </div>
              <p className="text-xs text-muted-foreground">
                Pick which connected account to authenticate with. "Auto"
                picks the account whose login matches the repo owner.
              </p>
            </div>
          )}
          {error && (
            <div className="p-4 bg-destructive/10 border border-destructive/30 text-destructive rounded-lg text-sm">
              <div className="flex items-start gap-2">
                <span className="text-destructive mt-0.5">⚠️</span>
                <span className="flex-1">{error}</span>
              </div>
            </div>
          )}

          {/* Success message */}
          {modalState === "success" && clonedPath && (
            <div className="p-4 bg-emerald-500/10 border border-emerald-500/30 rounded-lg">
              <div className="flex items-center gap-2 text-sm text-emerald-600 dark:text-emerald-400">
                <CheckCircle2 className="w-4 h-4 flex-shrink-0" />
                <span>Repository cloned to <code className="font-mono text-xs bg-muted px-1 py-0.5 rounded">{clonedPath}</code></span>
              </div>
            </div>
          )}

          <div className="space-y-5">
            {/* Repo URL */}
            <div className="space-y-2">
              <label className="block text-sm font-semibold text-foreground">
                Repository URL <span className="text-destructive">*</span>
              </label>
              <div className="relative">
                <Link className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
                <input
                  type="text"
                  value={repoUrl}
                  onChange={(e) => setRepoUrl(e.target.value)}
                  className="w-full pl-10 pr-4 py-3 elevation-0 border border-border/60 rounded-lg text-sm font-mono placeholder:text-muted-foreground/60 placeholder:font-normal placeholder:italic focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent transition-all"
                  placeholder="https://github.com/org/repo"
                  required
                  autoFocus
                  disabled={modalState === "loading" || modalState === "success"}
                />
              </div>
            </div>

            {/* Branch */}
            <div className="space-y-2">
              <label className="block text-sm font-semibold text-foreground">
                Branch
              </label>
              <div className="relative">
                <GitBranch className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
                <input
                  type="text"
                  value={branch}
                  onChange={(e) => setBranch(e.target.value)}
                  className="w-full pl-10 pr-4 py-3 elevation-0 border border-border/60 rounded-lg text-sm font-mono placeholder:text-muted-foreground/60 placeholder:font-normal placeholder:italic focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent transition-all"
                  placeholder="main"
                  disabled={modalState === "loading" || modalState === "success"}
                />
              </div>
              <p className="text-xs text-muted-foreground">
                Branch to check out after cloning
              </p>
            </div>

            {/* Path */}
            <div className="space-y-2">
              <label className="block text-sm font-semibold text-foreground">
                Clone Path
              </label>
              <div className="relative">
                <FolderOpen className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-muted-foreground" />
                <input
                  type="text"
                  value={path}
                  onChange={(e) => setPath(e.target.value)}
                  className="w-full pl-10 pr-4 py-3 elevation-0 border border-border/60 rounded-lg text-sm font-mono placeholder:text-muted-foreground/60 placeholder:font-normal placeholder:italic focus:outline-none focus:ring-2 focus:ring-primary focus:border-transparent transition-all"
                  placeholder="/home/workspace/projects/repo-name"
                  disabled={modalState === "loading" || modalState === "success"}
                />
              </div>
              <p className="text-xs text-muted-foreground">
                Destination path on the daemon (auto-populated from repo URL)
              </p>
            </div>
          </div>

          <div className="flex gap-3 pt-6 border-t border-border">
            <button
              type="button"
              onClick={handleClose}
              className="flex-1 px-5 py-3 bg-muted hover:bg-muted/80 border border-border rounded-lg text-sm font-medium transition-all focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background"
              disabled={modalState === "loading"}
            >
              Cancel
            </button>
            <button
              type="submit"
              className="flex-1 px-5 py-3 bg-primary text-primary-foreground hover:bg-primary/90 rounded-lg text-sm font-semibold shadow-sm transition-all focus:outline-none focus:ring-2 focus:ring-primary focus:ring-offset-2 focus:ring-offset-background disabled:opacity-50 disabled:cursor-not-allowed"
              disabled={modalState === "loading" || modalState === "success"}
            >
              {modalState === "loading" ? "Cloning..." : modalState === "success" ? "Cloned!" : "Clone Repository"}
            </button>
          </div>
        </form>
      )}
    </Modal>
  );
}