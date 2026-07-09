/**
 * Post-onboarding "Connecting your daemon..." gate.
 *
 * Polls the control-plane's listDaemons every 2s for up to 60s after the user
 * finishes onboarding, and surfaces one of four states:
 *
 *   1. Connecting — status is PENDING or DISCONNECTED inside the 60s window.
 *      Spinner + elapsed seconds.
 *   2. Connected — status flipped to ACTIVE. Green check + Continue button.
 *   3. Failed — status is FAILED, or 60s elapsed without ACTIVE. Red icon,
 *      headline, optional reason from `last_status_message`, plus Retry,
 *      View logs (admin app), and Skip and continue CTAs.
 *   4. Stuck disconnected — same UI as Failed.
 *
 * The gate is rendered by terminal onboarding steps (ProjectPickerStep and
 * GitHubConnectStep) AFTER completeOnboarding succeeds but BEFORE navigating
 * to the chat view, so the user never lands on a silent "No daemon connected"
 * banner without context.
 */
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import {
  AlertCircle,
  CheckCircle2,
  ExternalLink,
  Loader2,
  RotateCw,
} from "lucide-react";
import { cn } from "@/lib/utils";
import {
  DAEMON_STATUS_ACTIVE,
  DAEMON_STATUS_FAILED,
  DAEMON_STATUS_PENDING,
  getDaemonStatusMessage,
  listDaemons,
  type Daemon,
} from "@/services/controlPlane/daemon";

/**
 * 60 seconds at 2-second intervals is enough to catch the median provisioning
 * window for a cloud daemon (image pull + NATS handshake) without keeping the
 * user staring at a spinner indefinitely. After the window we fail fast and
 * give the user real CTAs (Retry / View logs / Skip).
 */
export const POLL_INTERVAL_MS = 2_000;
export const POLL_TIMEOUT_MS = 60_000;

const FAILED_FALLBACK_MESSAGE =
  "We couldn't establish a connection. This usually means a networking or configuration issue.";

export type DaemonConnectingPhase = "connecting" | "connected" | "failed";

interface DaemonConnectingGateProps {
  /**
   * Called when the user dismisses the gate — on success, on Skip, or after
   * acknowledging a Connected state. Parent should run any post-onboarding
   * navigation here.
   */
  onContinue: () => void;
  /**
   * UUID of the daemon we're waiting on. When omitted we just look at the
   * first daemon in the list — fine during onboarding since we only create
   * one. */
  daemonRef?: string;
}

interface DaemonListEnvelope {
  daemons: Daemon[];
}

function pickDaemon(daemons: Daemon[], ref?: string): Daemon | undefined {
  if (!daemons.length) return undefined;
  if (ref) {
    const exact = daemons.find((d) => d.id === ref);
    if (exact) return exact;
  }
  // Prefer ACTIVE / PENDING when present so a stale FAILED row from a prior
  // attempt doesn't poison the gate.
  return (
    daemons.find((d) => d.status === DAEMON_STATUS_ACTIVE) ||
    daemons.find((d) => d.status === DAEMON_STATUS_PENDING) ||
    daemons[0]
  );
}

/**
 * Pure state derivation — exported so tests can cover each phase without
 * having to drive timers + the network mock at the same time.
 */
export function derivePhase(
  daemon: Daemon | undefined,
  elapsedMs: number,
): DaemonConnectingPhase {
  if (daemon?.status === DAEMON_STATUS_ACTIVE) return "connected";
  if (daemon?.status === DAEMON_STATUS_FAILED) return "failed";
  if (elapsedMs >= POLL_TIMEOUT_MS) return "failed";
  return "connecting";
}

export function DaemonConnectingGate({
  onContinue,
  daemonRef,
}: DaemonConnectingGateProps) {
  const navigate = useNavigate();
  // `attemptStartedAt` resets on Retry, which both resets the elapsed clock
  // and triggers a fresh refetch.
  const [attemptStartedAt, setAttemptStartedAt] = useState(() => Date.now());
  const [now, setNow] = useState(() => Date.now());
  const tickRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const elapsedMs = now - attemptStartedAt;
  const elapsedSeconds = Math.min(
    Math.floor(elapsedMs / 1000),
    Math.floor(POLL_TIMEOUT_MS / 1000),
  );

  // Pre-compute whether we've timed out so the query can stop polling.
  const timedOut = elapsedMs >= POLL_TIMEOUT_MS;

  const queryKey = useMemo(
    () => ["onboarding", "daemons", "gate", attemptStartedAt] as const,
    [attemptStartedAt],
  );

  const { data, refetch } = useQuery<DaemonListEnvelope>({
    queryKey,
    queryFn: () => listDaemons(),
    refetchInterval: (query) => {
      const daemons = query.state.data?.daemons ?? [];
      const target = pickDaemon(daemons, daemonRef);
      // Stop polling once we've reached a terminal state — ACTIVE / FAILED —
      // or the user has timed out. TanStack passes the latest result via
      // `query.state.data`, so we don't rely on the closure.
      if (target?.status === DAEMON_STATUS_ACTIVE) return false;
      if (target?.status === DAEMON_STATUS_FAILED) return false;
      if (Date.now() - attemptStartedAt >= POLL_TIMEOUT_MS) return false;
      return POLL_INTERVAL_MS;
    },
    refetchOnWindowFocus: false,
    staleTime: 0,
  });

  // Tick the elapsed clock once a second so the user sees a live counter.
  // Stops as soon as we reach a terminal phase or time out.
  useEffect(() => {
    const target = pickDaemon(data?.daemons ?? [], daemonRef);
    const phase = derivePhase(target, Date.now() - attemptStartedAt);
    if (phase !== "connecting") {
      if (tickRef.current) {
        clearInterval(tickRef.current);
        tickRef.current = null;
      }
      return;
    }
    if (!tickRef.current) {
      tickRef.current = setInterval(() => setNow(Date.now()), 1_000);
    }
    return () => {
      if (tickRef.current) {
        clearInterval(tickRef.current);
        tickRef.current = null;
      }
    };
  }, [data, daemonRef, attemptStartedAt]);

  const daemon = pickDaemon(data?.daemons ?? [], daemonRef);
  const phase = derivePhase(daemon, elapsedMs);
  const reason = getDaemonStatusMessage(daemon);

  const handleRetry = useCallback(() => {
    setAttemptStartedAt(Date.now());
    setNow(Date.now());
    // Don't await — the query will refetch via the new key, and we want the
    // UI to flip back to "connecting" immediately.
    void refetch();
  }, [refetch]);

  // ── Render ─────────────────────────────────────────────────────────────
  if (phase === "connected") {
    return (
      <div
        className="space-y-5 text-center"
        role="status"
        aria-live="polite"
        data-testid="daemon-gate-connected"
      >
        <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-emerald-500/15 text-emerald-500">
          <CheckCircle2 className="h-7 w-7" />
        </div>
        <div className="space-y-1">
          <h2 className="text-xl font-semibold tracking-tight text-foreground">
            Connected
          </h2>
          <p className="text-sm text-muted-foreground">
            Your environment is ready. You can start chatting now.
          </p>
        </div>
        <button
          type="button"
          onClick={onContinue}
          className="w-full rounded-lg bg-primary py-3 text-sm font-semibold text-primary-foreground transition-colors hover:bg-primary/90"
        >
          Continue
        </button>
      </div>
    );
  }

  if (phase === "failed") {
    // In-app: route to the Environments settings section, deep-linking to the
    // failing daemon's detail view when we know its id (the section reads the
    // `daemon` search param via useSearch({ strict: false })).
    const goToEnvironments = () =>
      navigate({
        to: "/settings/$section",
        params: { section: "environments" },
        search: daemon?.id ? { daemon: daemon.id } : {},
      });

    return (
      <div
        className="space-y-5 text-center"
        role="alert"
        aria-live="assertive"
        data-testid="daemon-gate-failed"
      >
        <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-destructive/15 text-destructive">
          <AlertCircle className="h-7 w-7" />
        </div>
        <div className="space-y-1">
          <h2 className="text-xl font-semibold tracking-tight text-foreground">
            Couldn't connect
          </h2>
          <p className="text-sm text-muted-foreground">
            {reason || FAILED_FALLBACK_MESSAGE}
          </p>
        </div>
        <div className="space-y-2">
          <button
            type="button"
            onClick={handleRetry}
            className="flex w-full items-center justify-center gap-2 rounded-lg bg-primary py-3 text-sm font-semibold text-primary-foreground transition-colors hover:bg-primary/90"
          >
            <RotateCw className="h-4 w-4" />
            Retry
          </button>
          <button
            type="button"
            onClick={goToEnvironments}
            className={cn(
              "flex w-full items-center justify-center gap-2 rounded-lg border border-border/40 bg-background py-3 text-sm font-medium text-foreground transition-colors hover:bg-muted",
            )}
          >
            <ExternalLink className="h-4 w-4" />
            View machine
          </button>
          <button
            type="button"
            onClick={onContinue}
            className="w-full py-2 text-xs text-muted-foreground transition-colors hover:text-foreground"
          >
            Skip and continue
          </button>
        </div>
      </div>
    );
  }

  // phase === "connecting"
  return (
    <div
      className="space-y-5 text-center"
      role="status"
      aria-live="polite"
      data-testid="daemon-gate-connecting"
    >
      <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-primary/15 text-primary">
        <Loader2 className="h-7 w-7 animate-spin" />
      </div>
      <div className="space-y-1">
        <h2 className="text-xl font-semibold tracking-tight text-foreground">
          Connecting your environment...
        </h2>
        <p className="text-sm text-muted-foreground">
          Hang tight — your hosted workspace is coming online.
        </p>
      </div>
      <p className="text-xs text-muted-foreground">
        Elapsed: {elapsedSeconds}s {timedOut ? "" : `/ ${Math.floor(POLL_TIMEOUT_MS / 1000)}s`}
      </p>
      <button
        type="button"
        onClick={onContinue}
        className="w-full py-2 text-xs text-muted-foreground transition-colors hover:text-foreground"
      >
        Skip and continue
      </button>
    </div>
  );
}