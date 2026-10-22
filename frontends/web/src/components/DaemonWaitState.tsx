/**
 * The one way the app says "your machine isn't ready".
 *
 * Renders a `DaemonWaitState` at one of three densities so the same words and
 * the same escalation reach a full panel, an inline strip, and a terminal
 * overlay. Surfaces choose the density; they don't choose the wording, because
 * that divergence is exactly what made five surfaces describe one state five
 * different ways.
 *
 * Several surfaces are usually blocked at once, so only one of them says it in
 * full; the others echo the headline. See `useDaemonWaitSpeaker` for how that
 * one is chosen.
 */

import { AlertTriangle, Loader2, RefreshCw, Server } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";

import { cn } from "../lib/utils";
import type { DaemonWaitState as WaitState } from "../lib/daemon-wait";
import { useDaemonWaitSpeaker } from "../hooks/useDaemonWaitSpeaker";

export interface DaemonWaitStateProps {
  state: WaitState;
  /**
   * `panel`  — fills an empty region (file tree, editor body, search results)
   * `inline` — a strip inside existing chrome (chat composer, banners)
   * `overlay`— a card for the host to float over live content (terminal); the
   *            host owns positioning, because it alone knows what it's
   *            floating over and how to stay on top of it
   */
  variant?: "panel" | "inline" | "overlay";
  /**
   * An ancillary surface (file tree, editor, terminal). When another visible
   * surface is already explaining the wait, this one shrinks to a one-line
   * echo of the headline instead of repeating the whole message. Leave unset
   * for surfaces whose region exists to carry the message — the chat
   * composer, the connect modal, the onboarding gate.
   */
  secondary?: boolean;
  /** Manual retry. Rendered only when the state asks for it. */
  onRetry?: () => void;
  className?: string;
}

/** Primary surfaces outrank any secondary; among secondaries a panel has room to say more than an overlay. */
function speakerRank(variant: NonNullable<DaemonWaitStateProps["variant"]>, secondary: boolean): number {
  if (!secondary) return 2;
  return variant === "overlay" ? 0 : 1;
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
  secondary = false,
  onRetry,
  className,
}: DaemonWaitStateProps) {
  const navigate = useNavigate();
  const speaker = useDaemonWaitSpeaker({
    rank: speakerRank(variant, secondary),
    secondary,
    onRetry,
  });

  const goToMachines = () =>
    navigate({ to: "/settings/$section", params: { section: "environments" } });

  // Only the speaker is a live region. Three echoes of one state announced
  // three times bury the phase change that is the thing worth hearing.
  const liveRegion = speaker.speaks
    ? ({ role: "status", "aria-live": "polite" } as const)
    : {};

  // What a deferring surface shows: enough to explain why it's empty, nothing
  // that repeats the speaker.
  const echo = (
    <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
      <WaitIcon tone={state.tone} className="h-3 w-3 shrink-0" />
      {state.title}
    </span>
  );

  // Actions are shared across densities; only the layout around them changes.
  const actions = (state.showRetry && onRetry) || state.showManage ? (
    <div className="flex flex-wrap items-center justify-center gap-2">
      {state.showRetry && onRetry && (
        <button
          type="button"
          onClick={speaker.retry}
          className="inline-flex items-center gap-1.5 whitespace-nowrap rounded-md border border-border/60 bg-background px-2.5 py-1 text-xs font-medium text-foreground transition-colors hover:bg-muted"
        >
          <RefreshCw className="h-3 w-3" aria-hidden="true" />
          Try again
        </button>
      )}
      {state.showManage && (
        <button
          type="button"
          onClick={goToMachines}
          className="inline-flex items-center gap-1.5 whitespace-nowrap rounded-md px-2.5 py-1 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground"
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
    <p className="max-w-md break-words rounded border border-border/50 bg-muted/40 px-2 py-1 font-mono text-xs leading-relaxed text-muted-foreground">
      {state.reason}
    </p>
  ) : null;

  if (variant === "overlay") {
    return (
      <div
        ref={speaker.ref}
        className={cn(
          "flex max-w-sm flex-col items-center gap-2 rounded-lg border border-border bg-card px-4 py-3 shadow-lg",
          className,
        )}
        {...liveRegion}
      >
        {speaker.speaks ? (
          <>
            <div className="flex items-center gap-2 text-xs font-medium text-foreground">
              <WaitIcon tone={state.tone} className="h-3.5 w-3.5 shrink-0" />
              {state.title}
            </div>
            {state.detail && (
              <p className="max-w-xs text-center text-xs text-muted-foreground">{state.detail}</p>
            )}
            {reason}
            {actions}
          </>
        ) : (
          echo
        )}
      </div>
    );
  }

  if (variant === "inline") {
    return (
      <div
        ref={speaker.ref}
        className={cn(
          "flex flex-wrap items-center gap-x-2 gap-y-1 border-t border-border/60 bg-muted/30 px-4 py-2 text-sm",
          className,
        )}
        {...liveRegion}
      >
        {speaker.speaks ? (
          <>
            <WaitIcon tone={state.tone} className="h-3.5 w-3.5 shrink-0" />
            <span className="font-medium text-foreground">{state.title}</span>
            {state.detail && (
              <span className="text-xs text-muted-foreground">{state.detail}</span>
            )}
            {state.reason && (
              <span className="font-mono text-xs text-muted-foreground">{state.reason}</span>
            )}
            <span className="ml-auto">{actions}</span>
          </>
        ) : (
          echo
        )}
      </div>
    );
  }

  return (
    <div
      ref={speaker.ref}
      className={cn("flex h-full items-center justify-center p-6", className)}
      {...liveRegion}
    >
      {speaker.speaks ? (
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
      ) : (
        echo
      )}
    </div>
  );
}
