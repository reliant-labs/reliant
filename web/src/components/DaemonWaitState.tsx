/**
 * The one way the app says "your machine isn't ready".
 *
 * Renders a `DaemonWaitState` at one of three densities so the same words and
 * the same escalation reach a full panel, an inline strip, and a terminal
 * overlay. Surfaces choose the density; they don't choose the wording, because
 * that divergence is exactly what made five surfaces describe one state five
 * different ways.
 */

import { AlertTriangle, Loader2, RefreshCw, Server } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";

import { cn } from "../lib/utils";
import type { DaemonWaitState as WaitState } from "../lib/daemon-wait";

export interface DaemonWaitStateProps {
  state: WaitState;
  /**
   * `panel`  — fills an empty region (file tree, editor body, search results)
   * `inline` — a strip inside existing chrome (chat composer, banners)
   * `overlay`— floats over live content (terminal)
   */
  variant?: "panel" | "inline" | "overlay";
  /** Manual retry. Rendered only when the state asks for it. */
  onRetry?: () => void;
  className?: string;
}

/**
 * A spinner means "something is happening". A warning triangle means "this
 * needs you". Getting that wrong is how a suspended machine ends up looking
 * like it's booting.
 */
function WaitIcon({ tone, className }: { tone: WaitState["tone"]; className?: string }) {
  if (tone === "failed") {
    return <AlertTriangle className={cn("text-warning", className)} aria-hidden="true" />;
  }
  return (
    <Loader2
      className={cn("animate-spin", tone === "slow" ? "text-warning" : "text-muted-foreground", className)}
      aria-hidden="true"
    />
  );
}

export function DaemonWaitState({
  state,
  variant = "panel",
  onRetry,
  className,
}: DaemonWaitStateProps) {
  const navigate = useNavigate();

  const goToMachines = () =>
    navigate({ to: "/settings/$section", params: { section: "environments" } });

  // Actions are shared across densities; only the layout around them changes.
  const actions = (state.showRetry && onRetry) || state.showManage ? (
    <div className="flex flex-wrap items-center gap-2">
      {state.showRetry && onRetry && (
        <button
          type="button"
          onClick={onRetry}
          className="inline-flex items-center gap-1.5 rounded-md border border-border/60 bg-background px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted"
        >
          <RefreshCw className="h-3 w-3" aria-hidden="true" />
          Try again
        </button>
      )}
      {state.showManage && (
        <button
          type="button"
          onClick={goToMachines}
          className="inline-flex items-center gap-1.5 rounded-md px-2.5 py-1 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground"
        >
          <Server className="h-3 w-3" aria-hidden="true" />
          Manage machines
        </button>
      )}
    </div>
  ) : null;

  // The backend's own words, kept visually distinct from ours. It's the only
  // part of this that is ground truth, and it's often the only actionable part.
  const reason = state.reason ? (
    <p className="max-w-md break-words rounded border border-border/50 bg-muted/40 px-2 py-1 font-mono text-[11px] leading-relaxed text-muted-foreground">
      {state.reason}
    </p>
  ) : null;

  if (variant === "overlay") {
    return (
      <div
        className={cn(
          "absolute left-1/2 top-1/2 flex -translate-x-1/2 -translate-y-1/2 flex-col items-center gap-2 rounded-lg border border-border bg-card px-4 py-3 shadow-lg",
          className,
        )}
        role="status"
        aria-live="polite"
      >
        <div className="flex items-center gap-2 text-xs font-medium text-foreground">
          <WaitIcon tone={state.tone} className="h-3.5 w-3.5" />
          {state.title}
        </div>
        {state.detail && (
          <p className="max-w-xs text-center text-xs text-muted-foreground">{state.detail}</p>
        )}
        {reason}
        {actions}
      </div>
    );
  }

  if (variant === "inline") {
    return (
      <div
        className={cn(
          "flex flex-wrap items-center gap-x-2 gap-y-1 border-t border-border/60 bg-muted/30 px-4 py-2 text-sm",
          className,
        )}
        role="status"
        aria-live="polite"
      >
        <WaitIcon tone={state.tone} className="h-3.5 w-3.5 shrink-0" />
        <span className="font-medium text-foreground">{state.title}</span>
        {state.detail && (
          <span className="text-xs text-muted-foreground">{state.detail}</span>
        )}
        {state.reason && (
          <span className="font-mono text-[11px] text-muted-foreground">{state.reason}</span>
        )}
        <span className="ml-auto">{actions}</span>
      </div>
    );
  }

  return (
    <div
      className={cn("flex h-full items-center justify-center p-6", className)}
      role="status"
      aria-live="polite"
    >
      <div className="flex max-w-sm flex-col items-center gap-3 text-center">
        <WaitIcon tone={state.tone} className="h-7 w-7" />
        <div className="space-y-1">
          <p className="text-sm font-medium text-foreground">{state.title}</p>
          {state.detail && (
            <p className="text-xs leading-relaxed text-muted-foreground">{state.detail}</p>
          )}
        </div>
        {reason}
        {actions}
      </div>
    </div>
  );
}
