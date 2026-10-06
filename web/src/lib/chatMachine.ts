// Copyright (c) 2025 Reliant Labs

/**
 * Where a new chat runs: on one of the user's machines, or with no machine at
 * all (research/NO_MACHINE_CHATS.md §2).
 *
 * A chat with no machine runs on Reliant's servers only. It can use the web and
 * the user's integrations, but it cannot read or change files in the project,
 * run a shell, or reach a local MCP server. The server enforces that from the
 * chat row (`Chat.no_machine`); this module only decides what the picker
 * offers and preselects.
 *
 * Pure: no React, no RPCs, so the rules are testable on their own.
 */

import { DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { daemonLabel, daemonStatusLabel, NO_MACHINE } from "@/components/Automations/daemonChoices";

export { NO_MACHINE };

/**
 * The picker value that sends no daemon at all and lets the server pick the
 * user's machine — what every chat did before there was a choice. Distinct
 * from NO_MACHINE, which is a chat with no machine by design.
 */
export const DEFAULT_MACHINE = "";

/** A picker value: DEFAULT_MACHINE, NO_MACHINE, or a daemon id. */
export type ChatMachineChoice = string;

/**
 * Whether a chat can run on this machine now or after a wake.
 *
 *   - ACTIVE / IDLE: connected.
 *   - SUSPENDED: asleep. A send wakes it ("Waking…"), so it still counts:
 *     rule §2.1 keeps an asleep machine as the default rather than falling
 *     back to no machine.
 *
 * Deliberately narrower than onboarding's hasUsableDaemonForOnboarding
 * (ComputeStep.tsx), which also counts PENDING. That predicate answers "has
 * the user already chosen where their machine lives", and a machine still
 * being provisioned has. This one answers "can the next chat run somewhere",
 * and a machine still provisioning cannot: §2.1 names it as a case that falls
 * back to no machine. DISCONNECTED and FAILED cannot be woken from here.
 */
export function isUsableMachineForChat(daemon: Pick<DaemonInfo, "status">): boolean {
  return (
    daemon.status === DaemonStatus.ACTIVE ||
    daemon.status === DaemonStatus.IDLE ||
    daemon.status === DaemonStatus.SUSPENDED
  );
}

export function hasUsableMachineForChat(daemons: ReadonlyArray<Pick<DaemonInfo, "status">>): boolean {
  return daemons.some(isUsableMachineForChat);
}

/** Connected right now: a send needs no wake. */
export function isAwakeMachine(daemon: Pick<DaemonInfo, "status">): boolean {
  return daemon.status === DaemonStatus.ACTIVE || daemon.status === DaemonStatus.IDLE;
}

export interface DefaultChatMachineInput {
  daemons: ReadonlyArray<Pick<DaemonInfo, "status">>;
  /** The daemon list has not loaded yet. */
  loading: boolean;
  /**
   * The desktop app's bundled daemon has not registered yet
   * (useBundledDaemonPending): an empty list is not yet "no machine".
   */
  awaitingBundledDaemon?: boolean;
  surface?: "desktop" | "mobile";
}

/**
 * The machine a new chat starts on when the user has not picked one, or
 * undefined while that cannot be known yet (the list is loading, or the
 * desktop app's own daemon is still registering).
 *
 * Desktop (§2.1): the user's machine whenever they have a usable one, and no
 * machine only when they have none at all. Mobile (§2.5): no machine unless
 * one of their machines is awake right now, because waking one from a phone
 * is an explicit choice; the picker still offers an asleep machine.
 */
export function defaultChatMachine({
  daemons,
  loading,
  awaitingBundledDaemon = false,
  surface = "desktop",
}: DefaultChatMachineInput): ChatMachineChoice | undefined {
  if (loading || awaitingBundledDaemon) return undefined;
  if (surface === "mobile") {
    return daemons.some(isAwakeMachine) ? DEFAULT_MACHINE : NO_MACHINE;
  }
  return hasUsableMachineForChat(daemons) ? DEFAULT_MACHINE : NO_MACHINE;
}

export interface ChatMachineOption {
  value: ChatMachineChoice;
  label: string;
  /** Status in words, e.g. "online", "suspended". Empty for No machine. */
  statusLabel: string;
  /** The machine can run a chat now or after a wake. */
  usable: boolean;
}

const STATUS_RANK: Partial<Record<DaemonStatus, number>> = {
  [DaemonStatus.ACTIVE]: 0,
  [DaemonStatus.IDLE]: 1,
  [DaemonStatus.SUSPENDED]: 2,
  [DaemonStatus.PENDING]: 3,
  [DaemonStatus.DISCONNECTED]: 4,
  [DaemonStatus.FAILED]: 5,
};

/** The user's machines, usable first and online before asleep, by name. */
export function chatMachineOptions(
  daemons: ReadonlyArray<Pick<DaemonInfo, "daemonId" | "hostname" | "status">>,
): ChatMachineOption[] {
  return [...daemons]
    .sort((a, b) => {
      const byStatus = (STATUS_RANK[a.status] ?? 6) - (STATUS_RANK[b.status] ?? 6);
      if (byStatus !== 0) return byStatus;
      return daemonLabel(a, a.daemonId).localeCompare(daemonLabel(b, b.daemonId));
    })
    .map((daemon) => ({
      value: daemon.daemonId,
      label: daemonLabel(daemon, daemon.daemonId),
      statusLabel: daemonStatusLabel(daemon.status),
      usable: isUsableMachineForChat(daemon),
    }));
}

/** What StartChat sends for a choice. */
export function startOptionsForMachine(choice: ChatMachineChoice): {
  daemon_id?: string;
  no_machine?: boolean;
} {
  if (choice === NO_MACHINE) return { no_machine: true };
  if (choice === DEFAULT_MACHINE) return {};
  return { daemon_id: choice };
}

/** Copy, kept in one place so the pill, header and hints agree. */
export const NO_MACHINE_PILL = "No machine · web & integrations";
export const NO_MACHINE_EXPLAINER = "No machine: can't read or change files in this project";
export const NO_MACHINE_COMPOSER_HINT =
  "No machine — this chat can use the web and your integrations, not the files in this project.";

/**
 * Where "Continue without machine" branches from: the chat's latest message on
 * its main thread that the server has stored. An optimistic message (a send
 * still in flight) has no row yet, and a sub-agent's message would branch that
 * sub-agent's thread rather than the conversation.
 */
export function continueBranchPoint(
  messages: ReadonlyArray<{ id: string; thread?: string }>,
  mainThreadId: string | undefined,
): string | undefined {
  for (let i = messages.length - 1; i >= 0; i--) {
    const message = messages[i]!;
    if (message.id.startsWith("optimistic-")) continue;
    if (mainThreadId && message.thread && message.thread !== mainThreadId) continue;
    return message.id;
  }
  return undefined;
}
