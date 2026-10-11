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

import {
  DaemonLifecyclePhase,
  DaemonStatus,
  type DaemonInfo as Daemon,
} from "@/gen/reliant/v1/daemon_registry_pb";
import { DAEMON_LIST_QUERY_KEY, fetchDaemonList } from "./daemonListQuery";
import { capabilities } from "@/services/controlPlane/capabilities";
import { resumeDaemon } from "@/services/controlPlane/daemon";
import { isAlreadyResumedError } from "@/lib/daemon-resume";
import {
  classifyDaemonWait,
  DAEMON_WAIT_POLL_MS,
  DAEMON_WAIT_SLOW_MS,
  type DaemonWaitState,
} from "@/lib/daemon-wait";
import {
  clearWaking,
  machineWsStatus,
  presentMachineStatus,
  useWakingMachines,
} from "@/lib/machineWake";

/** How often the elapsed clock re-renders the copy while waiting. */
const TICK_MS = 1_000;

/**
 * Backstop poll of the machine's status while a wait is in progress.
 *
 * NOT the freshness mechanism: the list is the shared, push-invalidated
 * DAEMON_LIST_QUERY_KEY, and the gateway announces every attach, detach and
 * lifecycle transition (provisioning, cloning, suspend, failure) on the user
 * stream. This used to poll its own key every 2s for as long as anything
 * waited — including a terminal parked on a SUSPENDED machine, which waits
 * until the user acts. In prod that was ~1,700 of 2,750 ListDaemons a day
 * from one open tab (2026-10-08 api-server log: 30/min, exactly 2s apart).
 */
export const DAEMON_WAIT_STATUS_POLL_MS = 15_000;

/**
 * Caller retry cadence once the wait is past DAEMON_WAIT_SLOW_MS. A machine
 * that has not come up in 20s is a cold boot (minutes, see
 * DaemonConnectingGate); the moment it attaches, the pushed daemon list flips
 * to ACTIVE and the caller is retried at once, so a slower cadence costs no
 * latency.
 */
export const DAEMON_WAIT_SLOW_RETRY_MS = 5_000;

/**
 * When the current wait on the machine began — ONE clock for every surface.
 *
 * The escalation is about how long the user has been waiting on the machine,
 * not on a particular panel. Per-surface clocks made those disagree: open a
 * new chat five minutes into a boot and its composer said "This usually takes
 * about a minute", with no buttons, while the file tree beside it had long
 * since escalated. That mattered once only one surface speaks in full (see
 * useDaemonWaitSpeaker) — the speaker could be the newest surface, and the
 * exits would vanish.
 *
 * The clock starts with the first waiting surface and resets when the last
 * one stops waiting, so the next outage starts from zero.
 */
const sharedWait = { startedAt: null as number | null, waiters: 0 };

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
  const [elapsedMs, setElapsedMs] = useState(0);

  // Keep the callback in a ref so the retry interval doesn't tear down and
  // restart every time the parent re-renders with a fresh closure.
  const onRetryRef = useRef(onRetry);
  useEffect(() => {
    onRetryRef.current = onRetry;
  }, [onRetry]);

  // Join the shared clock while waiting, and tick it so the copy can
  // escalate. Only runs while waiting.
  useEffect(() => {
    if (!waiting) {
      setElapsedMs(0);
      return;
    }
    if (sharedWait.startedAt === null) sharedWait.startedAt = Date.now();
    sharedWait.waiters += 1;
    const tick = () => setElapsedMs(Date.now() - (sharedWait.startedAt ?? Date.now()));
    tick();
    const id = setInterval(tick, TICK_MS);
    return () => {
      clearInterval(id);
      sharedWait.waiters -= 1;
      if (sharedWait.waiters === 0) sharedWait.startedAt = null;
    };
  }, [waiting]);

  // Wakes in flight (lib/machineWake). A request that found its machine
  // asleep comes back naming the machine the server woke, and the transport
  // records it there.
  const waking = useWakingMachines();
  const isWaking = useCallback(
    (d: Daemon) => {
      const started = waking.get(d.daemonId);
      return (
        started !== undefined &&
        !presentMachineStatus(machineWsStatus(d.status), d.lastStatusMessage, started).wakeFinished
      );
    },
    [waking],
  );

  // Which machine are we actually waiting on?
  //
  // Ordered by what best explains the wait, because picking wrong means
  // narrating the wrong machine: a user with one booting machine and one
  // long-suspended machine should see "cloning your repository", not
  // "suspended". A machine being woken for this user wins — the server named
  // it as the one a request needed; then one reporting an in-flight startup
  // phase; then one the backend has said something about; then anything.
  const pickDaemon = useCallback(
    (list: readonly Daemon[]): Daemon | null =>
      list.find(isWaking) ??
      list.find((d) => STARTUP_PHASES.has(d.lifecyclePhase)) ??
      list.find((d) => d.lastStatusMessage?.trim()) ??
      list[0] ??
      null,
    [isWaking],
  );

  // The machine's real status, from the shared push-invalidated list. Read
  // once when a wait begins (staleTime 0), then kept current by push. The
  // backstop poll runs only while the machine is on its way somewhere — a
  // SUSPENDED or FAILED one will not change until someone acts, and the push
  // says when they do (see DAEMON_WAIT_STATUS_POLL_MS). Decided from the data
  // each fetch returns, so it stops the moment the answer is "suspended".
  // Cloud-only: without a control plane there is no record to read, and the
  // copy falls back to the self-hosted branch which needs no status.
  const { data: daemons, refetch } = useQuery<Daemon[]>({
    queryKey: DAEMON_LIST_QUERY_KEY,
    queryFn: fetchDaemonList,
    enabled: waiting && capabilities.cloudDaemons,
    refetchInterval: (query) => {
      if (!waiting) return false;
      const d = pickDaemon(query.state.data ?? []);
      const next = classifyDaemonWait({
        daemon: d,
        elapsedMs: 0,
        isCloud: capabilities.cloudDaemons,
        wakeStartedAt: d ? waking.get(d.daemonId) : undefined,
      });
      return next.shouldRetry ? DAEMON_WAIT_STATUS_POLL_MS : false;
    },
    refetchIntervalInBackground: false,
    staleTime: 0,
  });

  // Forget wakes that are over (the machine came up, failed, or never left
  // sleep), so a later wait on the same machine is not mislabelled as a wake.
  useEffect(() => {
    for (const d of daemons ?? []) {
      if (waking.has(d.daemonId) && !isWaking(d)) clearWaking(d.daemonId);
    }
  }, [daemons, waking, isWaking]);

  const daemon = useMemo<Daemon | null>(() => pickDaemon(daemons ?? []), [daemons, pickDaemon]);

  // Drive the caller's retry on the poll cadence, but only while the state
  // says retrying is still worthwhile — a FAILED or SUSPENDED machine will
  // not start serving because we asked a fourth time.
  const state = useMemo<DaemonWaitState | null>(() => {
    if (!waiting) return null;
    return classifyDaemonWait({
      daemon,
      elapsedMs,
      isCloud: capabilities.cloudDaemons,
      wakeStartedAt: daemon ? waking.get(daemon.daemonId) : undefined,
    });
  }, [waiting, daemon, elapsedMs, waking]);

  const shouldRetry = state?.shouldRetry ?? false;

  const slow = elapsedMs >= DAEMON_WAIT_SLOW_MS;
  useEffect(() => {
    if (!waiting || !shouldRetry) return;
    const id = setInterval(
      () => onRetryRef.current?.(),
      slow ? DAEMON_WAIT_SLOW_RETRY_MS : DAEMON_WAIT_POLL_MS,
    );
    return () => clearInterval(id);
  }, [waiting, shouldRetry, slow]);

  // The machine just attached (the gateway's push refetched the list): retry
  // now rather than on the next tick.
  const daemonActive = daemon?.status === DaemonStatus.ACTIVE;
  useEffect(() => {
    if (waiting && daemonActive) onRetryRef.current?.();
  }, [waiting, daemonActive]);

  // Restarts the shared clock, so every surface's escalation starts over
  // together; the others pick it up on their next tick.
  //
  // On a FAILED machine "try again" must actually try again: refetching only
  // re-reads the same FAILED row. The control plane treats ResumeDaemon on a
  // Failed machine as a retry and rebuilds its pod on the current image, which
  // is what unsticks a machine whose pod crash-looped on a bad release.
  const failedDaemonId =
    capabilities.cloudDaemons && daemon?.status === DaemonStatus.FAILED ? daemon.daemonId : null;
  const retryNow = useCallback(() => {
    if (sharedWait.waiters > 0) sharedWait.startedAt = Date.now();
    setElapsedMs(0);
    if (failedDaemonId) {
      void resumeDaemon(failedDaemonId)
        .catch((err: unknown) => {
          if (!isAlreadyResumedError(err)) console.warn("retrying failed machine:", err);
        })
        .finally(() => {
          void refetch();
          onRetryRef.current?.();
        });
      return;
    }
    void refetch();
    onRetryRef.current?.();
  }, [refetch, failedDaemonId]);

  return { state, elapsedMs, daemon, retryNow };
}
