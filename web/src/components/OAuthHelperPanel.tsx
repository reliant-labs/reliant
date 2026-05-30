import { Loader2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { authServeCommand } from "@/lib/cli-commands";

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

  return (
    <div className="space-y-3 rounded-lg border border-border/40 bg-muted/30 p-4">
      <div className="space-y-2">
        <p className="text-sm font-medium text-foreground">
          Authenticate via {providerName}
        </p>
        <p className={cn(bodyText, "text-muted-foreground")}>
          {available
            ? `Sign in with ${providerName} to connect your account.`
            : "The local OAuth helper is not running. Start it in your terminal to enable login:"}
        </p>
        {!available && !loading && (
          <code
            className={cn(
              "block select-all rounded-md border border-border/40 bg-background px-3 py-2 font-mono break-all",
              codeText,
            )}
          >
            {authServeCommand()}
          </code>
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
