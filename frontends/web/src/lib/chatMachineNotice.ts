// Copyright (c) 2025 Reliant Labs

/**
 * What the transcript footer says about the chat's machine while the chat has
 * work waiting on it.
 *
 * Prod, 2026-10-10 (chat 66a045ce): the user's machine was asleep and being
 * resumed — the control plane said "Starting a machine for your workspace" —
 * while the chat showed two "continue" messages and "Processing •••". Every
 * machine surface keyed on the RUN: the footer said "Queued — will send when
 * your machine connects" only once the run reached WAITING_FOR_DAEMON, and the
 * composer's line only during a send or that same wait. The run never got
 * there (its replay was failing), so neither said a word about the machine.
 *
 * So the machine is read from the machine, not from the run: the chat's
 * pinned machine, or the one an unpinned chat resolves to
 * (`defaultMachineDaemon`), in the registry. Whenever it is coming up,
 * reconnecting, asleep or failed, and the caller has established that work is
 * waiting (an unread message, or a live run), this says so — whatever the run
 * is doing.
 *
 * Pure: no React, no RPCs.
 */

import { DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { defaultMachineDaemon } from "./chatMachine";
import {
  MACHINE_ASLEEP,
  MACHINE_ASLEEP_STARTING,
  MACHINE_FAILED_TO_START,
  MACHINE_NOUN,
  MACHINE_SLEPT_DURING_RUN_WAIT,
  MACHINE_SLEPT_DURING_USAGE_LIMIT,
  MACHINE_WAKES_ON_RESUME,
  RECONNECTING_MACHINE_FOR_MESSAGE,
  RECONNECTING_MACHINE_FOR_RUN,
  WAKING_MACHINE_FOR_MESSAGE,
  WAKING_MACHINE_FOR_RUN,
} from "./daemon-wait";
import { WAKE_SUSPENDED_GRACE_MS } from "./machineWake";

export type ChatMachineNoticeKind = "waking" | "reconnecting" | "asleep" | "failed";

export interface ChatMachineNotice {
  kind: ChatMachineNoticeKind;
  daemonId: string;
  /** The line itself. */
  title: string;
  /**
   * The control plane's own words when it gave some: cold-start progress
   * ("Starting a machine for your workspace…") or why it failed.
   */
  detail: string | null;
  /** When the wait began, for the elapsed time; unset when nothing is in flight. */
  since?: number;
  /** Nothing is waking the machine: offer to start it. */
  offerStart: boolean;
  /** It failed to start: offer Try again (a resume rebuilds it). */
  offerRetry: boolean;
}

/**
 * The machine a chat runs on: its pinned daemon, or for an unpinned chat (one
 * whose run has not started yet) the machine the server will pin it to
 * (`installedOn`: the project's installs, see defaultMachineDaemon). Undefined while the registry has
 * not answered, and for a pinned machine that no longer exists (the composer
 * says that one).
 */
export function resolveChatMachine(
  daemons: ReadonlyArray<DaemonInfo> | undefined,
  pinnedDaemonId: string | undefined,
  installedOn?: ReadonlySet<string>,
): DaemonInfo | undefined {
  if (!daemons) return undefined;
  if (pinnedDaemonId) return daemons.find((d) => d.daemonId === pinnedDaemonId);
  return defaultMachineDaemon(daemons, installedOn);
}

export interface ChatMachineNoticeInput {
  daemon: DaemonInfo | undefined;
  /**
   * The chat's latest turn is the user's (lib/awaitingReply): the message is
   * what waits for the machine. Otherwise the run is past it, mid-turn.
   */
  messageUnanswered: boolean;
  /** When a wake of this machine was recorded (lib/machineWake), if one was. */
  wakeStartedAt?: number;
  /** A send for this chat is in flight; the server wakes the machine inside it. */
  sending?: boolean;
  /**
   * The run is held for its machine (ChatActivity.WAITING_FOR_DAEMON). Its
   * preflight is the run's wake point: it resumed the machine before parking.
   */
  runWaitingForMachine?: boolean;
  /**
   * A live run is waiting off the machine: held until a provider's usage
   * limit resets ("usage_limit"), or otherwise working without it — an LLM
   * call, a sub-agent, a timer ("run"). The idle policy may suspend the
   * machine meanwhile, and the run wakes it itself when it next needs it
   * (the server's resume wake), so an asleep machine is news, not a problem.
   */
  runHeld?: "usage_limit" | "run";
  /** When the unanswered message was sent, epoch ms. */
  messageSentAt?: number;
  now?: number;
}

function controlPlaneSays(daemon: DaemonInfo): string | null {
  const message = daemon.lastStatusMessage?.trim();
  return message ? message : null;
}

/**
 * The notice for the chat's machine, or null when the machine is up (or
 * unknown) and there is nothing to say about it.
 */
export function chatMachineNotice({
  daemon,
  messageUnanswered,
  wakeStartedAt,
  sending = false,
  runWaitingForMachine = false,
  runHeld,
  messageSentAt,
  now = Date.now(),
}: ChatMachineNoticeInput): ChatMachineNotice | null {
  if (!daemon) return null;
  const base = { daemonId: daemon.daemonId, offerStart: false, offerRetry: false };
  // The wait began when the machine was woken, else when the message that
  // waits on it was sent.
  const since = wakeStartedAt ?? (messageUnanswered ? messageSentAt : undefined);

  switch (daemon.status) {
    case DaemonStatus.FAILED:
      return {
        ...base,
        kind: "failed",
        title: MACHINE_FAILED_TO_START,
        detail:
          controlPlaneSays(daemon) ??
          `It stopped before it finished starting up. Try again, and ${
            messageUnanswered ? "your message will send" : "the run continues"
          } once it connects.`,
        offerRetry: true,
      };
    case DaemonStatus.PENDING:
      return {
        ...base,
        kind: "waking",
        title: messageUnanswered ? WAKING_MACHINE_FOR_MESSAGE : WAKING_MACHINE_FOR_RUN,
        detail: controlPlaneSays(daemon),
        since,
      };
    case DaemonStatus.DISCONNECTED:
      return {
        ...base,
        kind: "reconnecting",
        title: messageUnanswered ? RECONNECTING_MACHINE_FOR_MESSAGE : RECONNECTING_MACHINE_FOR_RUN,
        detail: controlPlaneSays(daemon),
        since,
      };
    case DaemonStatus.SUSPENDED: {
      // Right after a wake the registry can still read SUSPENDED; past a
      // short grace a machine that stayed asleep was not woken.
      const beingWoken =
        sending ||
        runWaitingForMachine ||
        (wakeStartedAt !== undefined && now - wakeStartedAt <= WAKE_SUSPENDED_GRACE_MS);
      const promise = messageUnanswered
        ? `Your message will send when it connects.`
        : `The run continues when it connects.`;
      if (beingWoken) {
        return { ...base, kind: "asleep", title: MACHINE_ASLEEP_STARTING, detail: promise, since };
      }
      // A live run that is waiting elsewhere wakes its machine itself when
      // it resumes: say what happened and that nothing needs doing. A run
      // that has not yet read the user's latest message is not known to be
      // getting anywhere (chat 66a045ce was failing its replay), so that
      // one keeps the offer to start the machine — unless it is held on a
      // usage limit, which it will wait out and then wake the machine.
      if (runHeld === "usage_limit" || (runHeld === "run" && !messageUnanswered)) {
        return {
          ...base,
          kind: "asleep",
          title: runHeld === "usage_limit" ? MACHINE_SLEPT_DURING_USAGE_LIMIT : MACHINE_SLEPT_DURING_RUN_WAIT,
          detail: MACHINE_WAKES_ON_RESUME,
        };
      }
      return {
        ...base,
        kind: "asleep",
        title: MACHINE_ASLEEP,
        detail: `Start your ${MACHINE_NOUN} and ${promise.charAt(0).toLowerCase()}${promise.slice(1)}`,
        offerStart: true,
      };
    }
    default:
      // ACTIVE / IDLE: up. UNSPECIFIED: nothing known worth saying.
      return null;
  }
}

/** "42s", "3m 05s", "1h 02m": how long the wait has run. */
export function formatWaitElapsed(ms: number): string {
  const totalSeconds = Math.max(0, Math.floor(ms / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (hours > 0) return `${hours}h ${String(minutes).padStart(2, "0")}m`;
  if (minutes > 0) return `${minutes}m ${String(seconds).padStart(2, "0")}s`;
  return `${seconds}s`;
}
