/**
 * One vocabulary, one escalation policy, for "the machine isn't there yet".
 *
 * Every surface that talks to the daemon (file tree, editor, terminal, chat,
 * search, git) can fail the same way: the RPC comes back with `no daemon
 * connected` because the machine is still coming online, suspended, or dead.
 * Each surface used to invent its own answer — five different nouns, timeouts
 * of 60s / none / forever, and copy that ranged from "Connecting to your
 * environment…" to "Waiting for daemon to come online...". The user sees one
 * system, so it has to speak with one voice.
 *
 * Two things live here:
 *
 *   1. `classifyDaemonWait` — given what we know about the machine and how
 *      long we've been waiting, what should the user be told, and can they do
 *      anything about it?
 *   2. The escalation policy — the thing that stops a spinner from lying.
 *
 * ## Why waiting no longer "times out"
 *
 * The old rule was: spin for 60s, then declare "Couldn't connect to your
 * environment." That deadline was invented by the frontend. Nothing in the
 * control plane promises a machine is ready in 60s and nothing fails it at 60s
 * — a pod stuck pulling an image stays Pending indefinitely, and cloning a
 * large repo legitimately takes longer. So the 60s error was frequently a lie
 * in both directions: it reported failure for a machine that was fine, and it
 * reported the same failure for a machine that was genuinely never coming.
 *
 * The rule now is: only the BACKEND declares failure (a FAILED status, or a
 * status message explaining what broke). The frontend never invents one. What
 * the frontend does instead is escalate — it gets more forthcoming, and more
 * insistent about offering an exit, the longer the wait runs. The user is
 * never told the machine is broken when we don't know that, and is never left
 * without a way out.
 */

import {
  DaemonLifecyclePhase,
  DaemonStatus,
  type DaemonInfo as Daemon,
} from "@/gen/reliant/v1/daemon_registry_pb";
import { machineWsStatus, presentMachineStatus } from "./machineWake";

/** The user-facing noun for a daemon. Settings → Machines already uses it. */
export const MACHINE_NOUN = "machine";

/**
 * What a message the server accepted, but that no turn has read yet, says
 * while its run is held for the machine (ChatActivity.WAITING_FOR_DAEMON).
 * Nothing is lost and nothing is stuck: the run reads it as soon as the
 * machine connects. Saying so is the difference between a queue and a hang.
 */
export const QUEUED_FOR_MACHINE = `Queued — will send when your ${MACHINE_NOUN} connects`;

/** The same promise as the tail of a queued-strip entry ("queued 12s ago · …"). */
export const QUEUED_FOR_MACHINE_DETAIL = `will send when your ${MACHINE_NOUN} connects`;

/** The run is held for its machine and has no unread message to speak for. */
export const WAITING_FOR_MACHINE = `Waiting for your ${MACHINE_NOUN}`;

/**
 * What a message says once the run that held it has ended because its machine
 * never came up — it failed to start, was removed, or was still down after
 * hours (ChatActivity.QUEUED_FOR_MACHINE). Still nothing is lost: the server
 * sends it the moment the machine connects, with no resend.
 */
export const QUEUED_UNTIL_MACHINE_IS_BACK = `Queued — will send when your ${MACHINE_NOUN} is back`;

/**
 * The transcript footer while the chat's machine is down and the chat has work
 * waiting on it (lib/chatMachineNotice). Said whatever the run is doing —
 * held for the machine, retrying, resuming, still replaying — because what the
 * user is waiting on is the machine, and the run's state does not change that.
 */
export const WAKING_MACHINE_FOR_MESSAGE = `Waking your ${MACHINE_NOUN} — your message will send when it connects`;
/** The same, for a run past the user's message: it carries on when the machine connects. */
export const WAKING_MACHINE_FOR_RUN = `Waking your ${MACHINE_NOUN} — the run continues when it connects`;
/** A machine that was up and dropped (DISCONNECTED). */
export const RECONNECTING_MACHINE_FOR_MESSAGE = `Reconnecting to your ${MACHINE_NOUN} — your message will send when it connects`;
export const RECONNECTING_MACHINE_FOR_RUN = `Reconnecting to your ${MACHINE_NOUN} — the run continues when it connects`;
/** FAILED: nothing brings it back by itself; Try again rebuilds it. */
export const MACHINE_FAILED_TO_START = `Your ${MACHINE_NOUN} failed to start`;
/** SUSPENDED, and something is waking it (a send, or Resume). */
export const MACHINE_ASLEEP_STARTING = `Your ${MACHINE_NOUN} is asleep — starting it`;
/** SUSPENDED, and nothing is waking it: the user is offered Start it. */
export const MACHINE_ASLEEP = `Your ${MACHINE_NOUN} is asleep`;

/**
 * What the thinking indicator says once a run has shown nothing for
 * STILL_WORKING_AFTER_MS: no streamed output, no new activity. "Processing"
 * cycling for minutes is the same picture as a run that has wedged, so past
 * the bound it stops pretending to narrate and offers a way to look or stop.
 */
export const STILL_WORKING = "Still working — no response yet";
export const STILL_WORKING_AFTER_MS = 60_000;

/**
 * How long a consumer should retry before it starts saying more than
 * "connecting". Not a deadline — nothing fails here, the copy just escalates.
 */
export const DAEMON_WAIT_SLOW_MS = 20_000;

/** When we start actively pointing at the escape hatches. */
export const DAEMON_WAIT_STUCK_MS = 60_000;

/** Poll cadence while actively waiting on a machine to come up. */
export const DAEMON_WAIT_POLL_MS = 2_000;

/**
 * How severe the wait is. Surfaces map this to their own visual weight — an
 * inline pill may only care about `tone`, a full-panel placeholder renders the
 * whole thing.
 */
export type DaemonWaitTone = "waiting" | "slow" | "failed";

export interface DaemonWaitState {
  tone: DaemonWaitTone;
  /** Short headline. Sentence case, no trailing period. */
  title: string;
  /** One line of context under the headline. May be null. */
  detail: string | null;
  /**
   * The backend's own explanation, when it gave one (image pull failed, quota
   * exceeded, …). Rendered verbatim and separately from our copy — it is the
   * only part of this that is ground truth rather than our narration.
   */
  reason: string | null;
  /** Whether the caller should keep retrying. False once genuinely failed. */
  shouldRetry: boolean;
  /** Whether to offer a manual retry / "check now" affordance. */
  showRetry: boolean;
  /** Whether to point the user at Settings → Machines to intervene. */
  showManage: boolean;
}

export interface DaemonWaitInput {
  /**
   * The control-plane record for the machine we're waiting on, when we have
   * one. Absent for self-hosted machines and before the first poll lands.
   */
  daemon?: Daemon | null;
  /** Milliseconds since this wait began. */
  elapsedMs: number;
  /**
   * Whether this deployment has cloud machines at all. Self-hosted waits are
   * on a human running a command, so the copy has to differ — no amount of
   * waiting provisions anything.
   */
  isCloud: boolean;
  /**
   * When a wake of `daemon` was recorded (`lib/machineWake`): the user
   * resumed it, or a request found it asleep and the server woke it. Absent
   * when nothing is waking it.
   */
  wakeStartedAt?: number;
  /** The clock `wakeStartedAt` is read against. Defaults to now. */
  now?: number;
}

/**
 * The backend's status message, when it carries information. Empty strings and
 * whitespace are treated as absent so callers can `?? fallback` cleanly.
 */
function statusMessage(daemon: Daemon | null | undefined): string | null {
  const raw = daemon?.lastStatusMessage?.trim();
  return raw ? raw : null;
}

/**
 * Decide what to tell the user about a machine that isn't serving requests.
 *
 * Ordered most-certain first: a status the backend reported beats anything we
 * infer from a stopwatch. We only fall back to elapsed-time heuristics when
 * the backend hasn't told us anything more specific.
 */
export function classifyDaemonWait(input: DaemonWaitInput): DaemonWaitState {
  const { daemon, elapsedMs, isCloud, wakeStartedAt, now = Date.now() } = input;
  const reason = statusMessage(daemon);

  // ── Terminal, and the backend said so ────────────────────────────────────
  // The only path that reports failure. Note we surface `reason` rather than
  // paraphrasing it: "image pull failed: manifest unknown" tells the user
  // something actionable that no generic copy of ours could.
  if (daemon?.status === DaemonStatus.FAILED) {
    return {
      tone: "failed",
      title: MACHINE_FAILED_TO_START,
      detail: reason
        ? null
        : "It stopped before it finished starting up. Retrying may work; if it doesn't, recreate it.",
      reason,
      shouldRetry: false,
      showRetry: true,
      showManage: true,
    };
  }

  // ── Asleep, and being woken ──────────────────────────────────────────────
  // Something woke this machine: the user pressed Resume, or a request found
  // it asleep and the server woke it. Said as Settings → Machines says it, and
  // retried. It comes BEFORE the suspended branch on purpose: right after a
  // wake the registry can still read SUSPENDED, and treating that as "nothing
  // is in flight" would stop the retry that completes the user's request.
  // `presentMachineStatus` decides when the wake is over (up, failed, or still
  // asleep past its grace period), and then the branches below apply as usual.
  if (daemon && wakeStartedAt !== undefined) {
    const wake = presentMachineStatus(
      machineWsStatus(daemon.status),
      daemon.lastStatusMessage,
      wakeStartedAt,
      now,
    );
    if (wake.label) {
      return {
        tone: elapsedMs >= DAEMON_WAIT_SLOW_MS ? "slow" : "waiting",
        title: wake.label,
        detail:
          wake.progress ??
          `Your ${MACHINE_NOUN} was asleep. It usually takes about a minute to wake, and this will load on its own.`,
        reason: null,
        shouldRetry: true,
        showRetry: elapsedMs >= DAEMON_WAIT_SLOW_MS,
        showManage: elapsedMs >= DAEMON_WAIT_STUCK_MS,
      };
    }
  }

  // ── Stopped, but recoverable ─────────────────────────────────────────────
  // Distinct from "starting": nothing is in flight, so spinning would be a
  // lie. Something has to wake it, which means the user needs a button, not
  // a spinner.
  if (daemon?.status === DaemonStatus.SUSPENDED) {
    return {
      tone: "failed",
      title: `Your ${MACHINE_NOUN} is suspended`,
      detail: "Resume it to pick up where you left off.",
      reason,
      shouldRetry: false,
      showRetry: true,
      showManage: true,
    };
  }

  // A machine that was up and dropped. It may come back on its own (pod
  // restart), so we keep retrying — but we say what happened rather than
  // implying it's still booting for the first time.
  if (daemon?.status === DaemonStatus.DISCONNECTED) {
    return {
      tone: elapsedMs >= DAEMON_WAIT_STUCK_MS ? "failed" : "slow",
      title: `Your ${MACHINE_NOUN} disconnected`,
      detail: reason ? null : "Trying to reach it again.",
      reason,
      shouldRetry: true,
      showRetry: true,
      showManage: elapsedMs >= DAEMON_WAIT_STUCK_MS,
    };
  }

  // ── Self-hosted: waiting on a person, not on infrastructure ──────────────
  // No provisioning is happening, so "this only takes a moment" would be
  // false. The user has to run something.
  if (!isCloud) {
    return {
      tone: elapsedMs >= DAEMON_WAIT_SLOW_MS ? "slow" : "waiting",
      title: `Waiting for your ${MACHINE_NOUN}`,
      detail:
        elapsedMs >= DAEMON_WAIT_SLOW_MS
          ? "Still nothing. Check that `reliant daemon start` is running and hasn't exited."
          : "Start it and this will connect on its own.",
      reason,
      shouldRetry: true,
      showRetry: elapsedMs >= DAEMON_WAIT_SLOW_MS,
      showManage: false,
    };
  }

  // ── Cloud, still coming up ───────────────────────────────────────────────
  // The common case, and the one the escalation exists for. We never call it
  // failed — we get more candid, and we surface the exits.
  const step = startupStep(daemon?.lifecyclePhase);

  if (elapsedMs >= DAEMON_WAIT_STUCK_MS) {
    return {
      tone: "slow",
      title: step.title,
      detail: `${step.stillDetail} It should still connect on its own — you can keep working elsewhere, or check on it.`,
      reason,
      shouldRetry: true,
      showRetry: true,
      showManage: true,
    };
  }

  if (elapsedMs >= DAEMON_WAIT_SLOW_MS) {
    return {
      tone: "slow",
      title: step.title,
      detail: step.slowDetail,
      reason,
      shouldRetry: true,
      showRetry: false,
      showManage: false,
    };
  }

  return {
    tone: "waiting",
    title: step.title,
    detail: step.detail,
    reason,
    shouldRetry: true,
    showRetry: false,
    showManage: false,
  };
}

/** Copy for one step of a cold start, at each level of escalation. */
interface StartupStep {
  title: string;
  detail: string;
  /** Used once the wait passes the "slow" threshold. */
  slowDetail: string;
  /** Used once the wait is long enough that we start offering exits. */
  stillDetail: string;
}

/**
 * Turn the reported phase into what the user is actually waiting on.
 *
 * This is the whole point of plumbing phase through. Without it every cold
 * start reads as one undifferentiated "Starting your machine", so a 90-second
 * clone of a large repo is indistinguishable from a machine wedged on a bad
 * image pull — and the user has no way to tell "this is working" from "this is
 * broken". With it, the wait narrates itself, and a long clone reads as
 * progress rather than as a hang.
 *
 * UNSPECIFIED is the honest default and must stay generic: self-hosted
 * machines never report a phase, and neither do rows created before the
 * control plane started recording it.
 */
function startupStep(phase: DaemonLifecyclePhase | undefined): StartupStep {
  switch (phase) {
    case DaemonLifecyclePhase.PROVISIONING:
      return {
        title: `Preparing your ${MACHINE_NOUN}`,
        detail: "Getting compute and downloading the image.",
        slowDetail:
          "Still getting compute. A first run on a new image takes the longest.",
        stillDetail: "Your compute is taking a while to come up.",
      };
    case DaemonLifecyclePhase.CLONING:
      return {
        title: "Cloning your repository",
        detail: "Your code is being copied onto the machine.",
        // Worth saying plainly: on a big repo this is legitimately slow, and
        // without that context a long clone looks like a stall.
        slowDetail: "Large repositories can take a few minutes to clone.",
        stillDetail: "This repository is taking a while to clone.",
      };
    case DaemonLifecyclePhase.SUSPENDING:
      return {
        title: `Your ${MACHINE_NOUN} is going to sleep`,
        detail: "It will be ready again in a moment.",
        slowDetail: "Still shutting down.",
        stillDetail: "This is taking longer than a suspend usually does.",
      };
    default:
      // No phase reported: self-hosted, pre-migration rows, or the gap before
      // the first status poll lands.
      return {
        title: `Starting your ${MACHINE_NOUN}`,
        detail: "This usually takes about a minute.",
        slowDetail:
          "Taking a little longer than usual — pulling the image or cloning your repo.",
        stillDetail: "This is longer than usual.",
      };
  }
}

/**
 * Compact one-liner for surfaces with no room for a headline + detail (a
 * terminal overlay, a status pill, a toast).
 */
export function daemonWaitSummary(state: DaemonWaitState): string {
  return state.reason ? `${state.title} — ${state.reason}` : state.title;
}
