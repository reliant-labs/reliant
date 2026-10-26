import { useCallback, useState } from "react";
import {
  Check,
  CheckCircle2,
  Copy,
  ExternalLink,
  Github,
  Loader2,
  XCircle,
} from "lucide-react";
import { cn } from "../lib/utils";
import type { UseCopilotOAuthReturn } from "../hooks";

export interface CopilotDevicePanelProps {
  oauth: UseCopilotOAuthReturn;
  onStart: () => void;
  /** Draw the panel's own inset frame and title. False when a host already frames and titles it. */
  framed?: boolean;
}

/**
 * Device-flow login panel for GitHub Copilot. Unlike the redirect-based
 * `OAuthHelperPanel`, this shows the GitHub-issued user code, a link to
 * github.com/login/device, and a polling spinner while awaiting authorization.
 *
 * Shared across the API key setup modal, settings, and onboarding so Copilot
 * presents identically everywhere it can be connected.
 */
export function CopilotDevicePanel({
  oauth,
  onStart,
  framed = true,
}: CopilotDevicePanelProps) {
  const [copied, setCopied] = useState(false);
  const { phase, userCode, verificationUri, message } = oauth;

  const handleCopy = useCallback(async () => {
    if (!userCode) return;
    try {
      await navigator.clipboard.writeText(userCode);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch {
      // Clipboard may be unavailable (permissions/insecure context); the code
      // remains selectable, so silently ignore.
    }
  }, [userCode]);

  const showCode = (phase === "awaiting_user" || phase === "polling") && !!userCode;

  // GitHub always issues github.com/login/device, but fall back defensively so
  // the user is never left without a URL to open if the field is ever empty.
  const verificationUrl = verificationUri || "https://github.com/login/device";

  return (
    <div
      className={cn(
        "space-y-4",
        // An inset, never bg-muted (it lifts in dark mode and recesses in light).
        framed && "rounded-lg border border-border/60 bg-background p-4",
      )}
    >
      {framed && (
        <div className="flex items-center gap-2">
          <Github className="h-4 w-4 text-foreground" />
          <p className="text-sm font-medium text-foreground">
            Sign in with GitHub Copilot
          </p>
        </div>
      )}

      {/* Device-code display + polling status */}
      {showCode && (
        <div className="space-y-3">
          <p className="text-sm text-muted-foreground">
            Open GitHub and enter this code to authorize Reliant:
          </p>
          <div className="flex items-center gap-2">
            <code
              className="flex-1 select-all rounded-md border border-border/40 bg-background px-3 py-2 text-center font-mono text-lg font-semibold tracking-[0.3em]"
              aria-label={`Device code ${userCode}`}
            >
              {userCode}
            </code>
            <button
              type="button"
              onClick={handleCopy}
              className="inline-flex items-center justify-center rounded-lg border border-border bg-background p-2.5 transition-colors hover:bg-foreground/5"
              aria-label={copied ? "Code copied" : "Copy code to clipboard"}
            >
              {copied ? (
                <Check className="h-4 w-4 text-success-ink" />
              ) : (
                <Copy className="h-4 w-4" />
              )}
            </button>
          </div>

          <a
            href={verificationUrl}
            target="_blank"
            rel="noreferrer"
            className="no-color inline-flex w-full items-center justify-center gap-2 rounded-lg bg-primary px-4 py-2.5 text-sm font-semibold text-primary-foreground transition-colors hover:bg-primary/90"
          >
            Open GitHub to authorize <ExternalLink className="h-4 w-4" />
          </a>

          <p className="text-center text-xs text-muted-foreground">
            or go to{" "}
            <a
              href={verificationUrl}
              target="_blank"
              rel="noreferrer"
              className="font-medium text-primary underline underline-offset-2 hover:text-primary/80"
            >
              {verificationUrl}
            </a>
          </p>

          <div
            className="flex items-center gap-2 text-sm text-muted-foreground"
            aria-live="polite"
          >
            <Loader2 className="h-4 w-4 animate-spin" />
            Completing sign-in…
          </div>

          <button
            type="button"
            onClick={() => oauth.cancel()}
            className="text-sm text-muted-foreground hover:text-foreground"
          >
            Cancel
          </button>
        </div>
      )}

      {/* Start / retry button (idle & error states) */}
      {(phase === "idle" || phase === "starting" || phase === "error") && (
        <button
          type="button"
          onClick={onStart}
          disabled={phase === "starting"}
          className={cn(
            "inline-flex w-full items-center justify-center gap-2 rounded-lg bg-primary px-4 py-2.5 text-sm font-semibold text-primary-foreground transition-colors hover:bg-primary/90",
            phase === "starting" && "cursor-not-allowed opacity-60"
          )}
        >
          {phase === "starting" && <Loader2 className="h-4 w-4 animate-spin" />}
          {phase === "error"
            ? "Try again"
            : phase === "starting"
              ? "Starting…"
              : "Continue with GitHub"}
        </button>
      )}

      {/* Terminal status message */}
      {message && (phase === "success" || phase === "error") && (
        <div
          role="status"
          aria-live="polite"
          className={cn(
            "flex items-center gap-2 rounded-lg p-3 text-sm",
            phase === "success"
              ? "border border-success/20 bg-success/10 text-success-ink"
              : "border border-destructive/20 bg-destructive/10 text-destructive-ink"
          )}
        >
          {phase === "success" ? (
            <CheckCircle2 className="h-4 w-4" />
          ) : (
            <XCircle className="h-4 w-4" />
          )}
          {message}
        </div>
      )}
    </div>
  );
}
