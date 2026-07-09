import { useEffect, useState } from "react";
import { Check, Loader2, Terminal } from "lucide-react";
import { cn } from "@/lib/utils";
import { authServeCommand, HOMEBREW_CASK_INSTALL } from "@/lib/cli-commands";

export interface OAuthHelperPanelProps {
  /** Display name of the OAuth provider (e.g. "Claude Code", "Codex"). */
  providerName: string;
  /** Whether the localhost OAuth helper is reachable. */
  available: boolean;
  /** True while an availability check is in flight. */
  loading: boolean;
  /** Re-check helper availability on demand. */
  onRetry: () => void;
  /** Start the OAuth flow for this provider. */
  onConnect: () => void;
  /** True while the OAuth flow is in progress. */
  connecting: boolean;
  /** Override the login button label. Defaults to `Login with ${providerName}`. */
  connectLabel?: string;
  /** Layout for the action button row. */
  buttonAlign?: "stretch" | "end";
  /** Visual treatment for the login button. */
  buttonVariant?: "primary" | "subtle";
  /** Text density. `compact` is suitable for tight onboarding layouts. */
  size?: "default" | "compact";
}

/**
 * Renders the "Authenticate via X" panel shared across the API key setup modal,
 * settings, and onboarding. Handles the "OAuth helper not running" state with a
 * code block + retry button, and the available state with a login button.
 *
 * When the helper isn't running we also surface how to install the `reliant`
 * CLI (the command runs through it), reusing the same install command as the
 * self-hosted-daemon flow. Inside the desktop app we can detect whether the
 * CLI is already on PATH and offer a one-click install when it isn't.
 */
export function OAuthHelperPanel({
  providerName,
  available,
  loading,
  onRetry,
  onConnect,
  connecting,
  connectLabel,
  buttonAlign = "end",
  buttonVariant = "primary",
  size = "default",
}: OAuthHelperPanelProps) {
  const compact = size === "compact";
  const bodyText = compact ? "text-xs leading-relaxed" : "text-sm";
  const codeText = compact ? "text-sm" : "text-sm";

  // ──────────────────────────────────────────────────────────────────────────
  // CLI install state (Electron only). The desktop app auto-installs `reliant`
  // to PATH at first launch (see electron/src/cli-installer.js). We poll the
  // status so we can hide the manual install command when the CLI is already
  // available, and offer a one-click install when it is not. Outside Electron
  // (plain browser) we always show the manual install command.
  // Mirrors the pattern in SelfHostedDaemonConnect.tsx.
  // ──────────────────────────────────────────────────────────────────────────
  const isElectron = typeof window !== "undefined" && !!window.electronAPI;
  const [cliInstalled, setCliInstalled] = useState<boolean | null>(null);
  const [cliPath, setCliPath] = useState<string | null>(null);
  const [installingCli, setInstallingCli] = useState(false);
  const [installError, setInstallError] = useState<string | null>(null);

  useEffect(() => {
    if (!isElectron || !window.electronAPI?.getCliStatus) return;
    let cancelled = false;
    window.electronAPI
      .getCliStatus()
      .then((status) => {
        if (cancelled) return;
        setCliInstalled(status.installed);
        setCliPath(status.path);
      })
      .catch(() => {
        if (cancelled) return;
        setCliInstalled(false);
      });
    return () => {
      cancelled = true;
    };
  }, [isElectron]);

  const handleInstallCli = async () => {
    if (!window.electronAPI?.installCLI) return;
    setInstallingCli(true);
    setInstallError(null);
    try {
      const result = await window.electronAPI.installCLI();
      if (result.success) {
        if (window.electronAPI.getCliStatus) {
          const status = await window.electronAPI.getCliStatus();
          setCliInstalled(status.installed);
          setCliPath(status.path);
        } else {
          setCliInstalled(true);
        }
      } else {
        setInstallError(result.error || "Failed to install CLI");
      }
    } catch (err) {
      setInstallError(
        err instanceof Error ? err.message : "Failed to install CLI",
      );
    } finally {
      setInstallingCli(false);
    }
  };

  const loginButtonClass =
    buttonVariant === "subtle"
      ? "border border-primary/40 bg-primary/10 text-primary hover:bg-primary/20"
      : "bg-primary text-primary-foreground hover:bg-primary/90";

  const loginButton = (
    <button
      type="button"
      onClick={onConnect}
      disabled={connecting || loading || !available}
      className={cn(
        "inline-flex items-center justify-center gap-2 rounded-lg px-4 py-2.5 text-sm font-semibold transition-colors disabled:cursor-not-allowed disabled:bg-muted disabled:text-muted-foreground",
        buttonAlign === "stretch" && "w-full",
        loginButtonClass,
      )}
    >
      {connecting && <Loader2 className="h-4 w-4 animate-spin" />}
      {connectLabel ?? `Login with ${providerName}`}
    </button>
  );

  const retryButton = (
    <button
      type="button"
      onClick={onRetry}
      disabled={loading}
      className={cn(
        "rounded-lg border border-border/40 bg-background px-4 py-2 text-sm transition-colors hover:bg-muted disabled:opacity-50",
        buttonAlign === "stretch" && "w-full",
      )}
    >
      {loading ? "Checking…" : "Retry"}
    </button>
  );

  const codeBlockClass = cn(
    "block select-all rounded-md border border-border/40 bg-background px-3 py-2 font-mono break-all",
    codeText,
  );

  // Whether to show the manual install command. In Electron we hide it once the
  // CLI is confirmed installed (we show a confirmation line instead); everywhere
  // else we always show it.
  const showManualInstall = !isElectron || cliInstalled !== true;

  return (
    <div className="space-y-3 rounded-lg border border-border/40 bg-muted/30 p-4">
      <div className="space-y-2">
        <p className="text-sm font-medium text-foreground">
          Authenticate via {providerName}
        </p>
        <p className={cn(bodyText, "text-muted-foreground")}>
          {available
            ? `Sign in with ${providerName} to connect your account.`
            : "The local OAuth helper is not running. We need this to intercept your credentials:"}
        </p>
        {!available && !loading && (
          <div className="space-y-3">
            {/* Install the reliant CLI (the helper command runs through it). */}
            <div className="space-y-1.5">
              <p className={cn(bodyText, "text-muted-foreground")}>
                Don&apos;t have the Reliant CLI? Install it:
              </p>

              {isElectron && cliInstalled === false && (
                <button
                  type="button"
                  onClick={handleInstallCli}
                  disabled={installingCli}
                  className={cn(
                    "inline-flex w-full items-center justify-center gap-2 rounded-md px-3 py-2 text-sm font-medium transition-colors",
                    installingCli
                      ? "cursor-not-allowed bg-muted text-muted-foreground"
                      : "bg-zinc-950 text-white hover:bg-zinc-800 dark:bg-white dark:text-zinc-950 dark:hover:bg-zinc-200",
                  )}
                >
                  {installingCli ? (
                    <Loader2 className="h-4 w-4 animate-spin" />
                  ) : (
                    <Terminal className="h-4 w-4" />
                  )}
                  {installingCli ? "Installing…" : "Install reliant CLI"}
                </button>
              )}

              {isElectron && cliInstalled === true && (
                <p className="flex items-center gap-1.5 text-xs text-emerald-600 dark:text-emerald-400">
                  <Check className="h-3.5 w-3.5" />
                  <span>
                    <code className="font-mono">reliant</code> CLI installed
                    {cliPath && (
                      <>
                        {" "}
                        at <code className="font-mono">{cliPath}</code>
                      </>
                    )}
                  </span>
                </p>
              )}

              {showManualInstall && (
                <code className={codeBlockClass}>{HOMEBREW_CASK_INSTALL}</code>
              )}

              {installError && (
                <p className="text-xs text-destructive">{installError}</p>
              )}
            </div>

            {/* Then run the OAuth helper. */}
            <div className="space-y-1.5">
              <p className={cn(bodyText, "text-muted-foreground")}>Then run:</p>
              <code className={codeBlockClass}>{authServeCommand()}</code>
            </div>
          </div>
        )}
      </div>
      <div
        className={cn(
          "flex pt-1",
          buttonAlign === "stretch" ? "justify-stretch" : "justify-end",
        )}
      >
        {available ? loginButton : retryButton}
      </div>
    </div>
  );
}
