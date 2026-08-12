/**
 * The waiting half of `useDaemonStatus`.
 *
 * `useDaemonStatus` answers "is a machine connected right now". This hook
 * answers the follow-up every daemon-backed surface then had to work out for
 * itself: we're not connected, so what do I show, how long have we been like
 * this, and should I try again?
 *
 * Previously each surface kept its own stopwatch (FileTree's
 * `connectingSinceRef`, DaemonConnectingGate's `attemptStartedAt`, Terminal's
 * reconnect refs) and its own retry loop, which is why their behavior drifted.
 * The stopwatch and the escalation policy live here once; surfaces render.
 *
 * The elapsed clock ticks only while waiting, and stops the moment a machine
 * connects — so a surface that mounts against a healthy machine does no timer
 * work at all.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";

import { DaemonLifecyclePhase } from "@/gen/controlplane/v1/public/shared_pb";
import { capabilities } from "@/services/controlPlane/capabilities";
import { listDaemons, type Daemon } from "@/services/controlPlane/daemon";
import {
  classifyDaemonWait,
  DAEMON_WAIT_POLL_MS,
  type DaemonWaitState,
} from "@/lib/daemon-wait";

/** How often the elapsed clock re-renders the copy while waiting. */
const TICK_MS = 1_000;

/**
 * Phases that mean "this machine is on its way up right now". A machine in one
 * of these is the one the user is waiting on, and the one whose phase should
 * drive the copy.
 */
const STARTUP_PHASES: ReadonlySet<DaemonLifecyclePhase> = new Set([
  DaemonLifecyclePhase.PROVISIONING,
  DaemonLifecyclePhase.CLONING,
]);

export interface UseDaemonWaitOptions {
  /**
   * Whether we are currently waiting. Surfaces pass their own notion of
   * "blocked on the daemon" (an RPC that failed with `no daemon connected`, a
   * websocket that won't open). When false the hook idles: no polling, no
   * timers, no re-renders.
   */
  waiting: boolean;
  /**
   * Called when the caller should retry whatever failed. Invoked on the poll
   * cadence while waiting, and by `retryNow`.
   */
  onRetry?: () => void;
}

export interface UseDaemonWaitResult {
  /** What to render. Null when not waiting. */
  state: DaemonWaitState | null;
  /** Milliseconds spent in the current wait. */
  elapsedMs: number;
  /** The machine we're waiting on, when the control plane knows about one. */
  daemon: Daemon | null;
  /** Force an immediate retry + status refetch. */
  retryNow: () => void;
}

/**
 * Track a wait on the daemon and produce the copy for it.
 *
 * While `waiting` is true this polls the control plane for the machine's real
 * status — that poll is what turns a generic spinner into "your machine failed
 * to start: image pull failed". Without it we'd only know that *our* RPC
 * failed, not why.
 */
export function useDaemonWait({
  waiting,
  onRetry,
}: UseDaemonWaitOptions): UseDaemonWaitResult {
  const [startedAt, setStartedAt] = useState<number | null>(null);
  const [elapsedMs, setElapsedMs] = useState(0);

  // Keep the callback in a ref so the retry interval doesn't tear down and
  // restart every time the parent re-renders with a fresh closure.
  const onRetryRef = useRef(onRetry);
  useEffect(() => {
    onRetryRef.current = onRetry;
  }, [onRetry]);

  // Start/stop the stopwatch on wait transitions.
  useEffect(() => {
    if (!waiting) {
      setStartedAt(null);
      setElapsedMs(0);
      return;
    }
    setStartedAt((prev) => prev ?? Date.now());
  }, [waiting]);

  // Tick the elapsed clock so the copy can escalate. Only runs while waiting.
  useEffect(() => {
    if (!waiting || startedAt === null) return;
    setElapsedMs(Date.now() - startedAt);
    const id = setInterval(() => setElapsedMs(Date.now() - startedAt), TICK_MS);
    return () => clearInterval(id);
  }, [waiting, startedAt]);

  // Poll the machine's real status while waiting. Cloud-only: without a
  // control plane there is no record to read, and the copy falls back to the
  // self-hosted branch which needs no status.
  const { data: daemons, refetch } = useQuery<Daemon[]>({
    queryKey: ["daemonWait", "daemons"],
    queryFn: async () => (await listDaemons()).daemons,
    enabled: waiting && capabilities.cloudDaemons,
    refetchInterval: waiting ? DAEMON_WAIT_POLL_MS : false,
    refetchIntervalInBackground: false,
    staleTime: 0,
  });

  // Which machine are we actually waiting on?
  //
  // Ordered by what best explains the wait, because picking wrong means
  // narrating the wrong machine: a user with one booting machine and one
  // long-suspended machine should see "cloning your repository", not
  // "suspended". A machine reporting an in-flight startup phase wins; then one
  // the backend has said something about; then anything.
  const daemon = useMemo<Daemon | null>(() => {
    const list = daemons ?? [];
    if (list.length === 0) return null;
    return (
      list.find((d) => STARTUP_PHASES.has(d.lifecyclePhase)) ??
      list.find((d) => d.lastStatusMessage?.trim()) ??
      list[0] ??
      null
    );
  }, [daemons]);

  // Drive the caller's retry on the poll cadence, but only while the state
  // says retrying is still worthwhile — a FAILED or SUSPENDED machine will
  // not start serving because we asked a fourth time.
  const state = useMemo<DaemonWaitState | null>(() => {
    if (!waiting) return null;
    return classifyDaemonWait({
      daemon,
      elapsedMs,
      isCloud: capabilities.cloudDaemons,
    });
  }, [waiting, daemon, elapsedMs]);

  const shouldRetry = state?.shouldRetry ?? false;
  useEffect(() => {
    if (!waiting || !shouldRetry) return;
    const id = setInterval(() => onRetryRef.current?.(), DAEMON_WAIT_POLL_MS);
    return () => clearInterval(id);
  }, [waiting, shouldRetry]);

  const retryNow = useCallback(() => {
    setStartedAt(Date.now());
    setElapsedMs(0);
    void refetch();
    onRetryRef.current?.();
  }, [refetch]);

  return { state, elapsedMs, daemon, retryNow };
}
