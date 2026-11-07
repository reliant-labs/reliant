// Copyright (c) 2025 Reliant Labs

/**
 * The chat list's machine filter: every machine (the default), or any subset
 * of the user's machines.
 *
 * A chat belongs to one machine: the daemon it is pinned to
 * (Chat.active_daemon_id); else, for a chat in a branch workspace that has not
 * been pinned yet, the machine holding that workspace's checkout
 * (lib/workspaceMachine); else the machine the server would pin it to, the
 * user's default (lib/chatMachine defaultMachineDaemon). A chat with no machine
 * by design (Chat.no_machine) belongs to none, and is its own choice:
 * NO_MACHINE, offered only while such chats exist.
 *
 * The persisted selection is a list of ids, or null for All machines. It is
 * read through {@link effectiveMachineFilter}, which drops machines the user
 * no longer has and falls back to All machines when nothing is left of it, or
 * when the user has one machine (the filter is then not shown at all).
 *
 * Pure: no React, no RPCs, so the rules are testable on their own. The
 * desktop sidebar and the navigation store (keyboard next/prev) both filter
 * through it, so the order the keys walk is the list on screen.
 */

import type { DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { defaultMachineDaemon, NO_MACHINE } from "./chatMachine";

export { NO_MACHINE };

/** The persisted selection: machine ids (and maybe NO_MACHINE), or null for All machines. */
export type MachineFilter = readonly string[] | null;

/** The fields of a chat the filter reads. */
export interface MachineFilterableChat {
  noMachine?: boolean;
  activeDaemonId?: string;
  worktreeId?: string;
}

/** The fields of a machine the filter reads. */
export type FilterableMachine = Pick<DaemonInfo, "daemonId" | "status"> &
  Partial<Pick<DaemonInfo, "daemonType" | "isDefault">>;

/** What resolving a chat's machine needs, built once per list (see {@link machineResolver}). */
export interface ChatMachineContext {
  /** The machine an unpinned chat lands on; undefined when the user has none. */
  defaultMachineId: string | undefined;
  /** Branch workspaces' machines by workspace id, from whatever is cached. */
  workspaceMachines: ReadonlyMap<string, string>;
}

/**
 * The context for {@link chatMachineId}: the default machine, and each cached
 * branch workspace's machine.
 */
export function machineResolver(
  machines: ReadonlyArray<FilterableMachine>,
  workspaces: ReadonlyArray<{ id: string; is_main: boolean; daemon_id?: string }> = [],
): ChatMachineContext {
  const workspaceMachines = new Map<string, string>();
  for (const workspace of workspaces) {
    if (!workspace.is_main && workspace.daemon_id) workspaceMachines.set(workspace.id, workspace.daemon_id);
  }
  return { defaultMachineId: defaultMachineDaemon(machines)?.daemonId, workspaceMachines };
}

/**
 * The machine a chat belongs to: a daemon id, NO_MACHINE, or undefined when
 * the user has no machine to place it on. A chat pinned to a machine the user
 * has since removed keeps that id: it matches no machine's filter, only All
 * machines.
 */
export function chatMachineId(chat: MachineFilterableChat, context: ChatMachineContext): string | undefined {
  if (chat.noMachine) return NO_MACHINE;
  if (chat.activeDaemonId) return chat.activeDaemonId;
  const workspaceMachine = chat.worktreeId ? context.workspaceMachines.get(chat.worktreeId) : undefined;
  return workspaceMachine ?? context.defaultMachineId;
}

/**
 * The selection the list applies: null (every chat) when nothing is selected,
 * when the user has fewer than two machines, or when none of the selected
 * machines exists any more; otherwise the selection minus machines that are
 * gone. NO_MACHINE always survives — it names no machine that can go away.
 */
export function effectiveMachineFilter(
  persisted: MachineFilter | undefined,
  machines: ReadonlyArray<Pick<DaemonInfo, "daemonId">>,
): MachineFilter {
  // Persisted state is untrusted: anything but a list is All machines.
  if (!Array.isArray(persisted) || machines.length < 2) return null;
  const known = new Set(machines.map((m) => m.daemonId));
  const kept = persisted.filter((id) => id === NO_MACHINE || known.has(id));
  return kept.length > 0 ? kept : null;
}

/** Whether a chat is listed under the filter. */
export function chatMatchesMachineFilter(
  chat: MachineFilterableChat,
  filter: MachineFilter,
  context: ChatMachineContext,
): boolean {
  if (!filter) return true;
  const machineId = chatMachineId(chat, context);
  return machineId !== undefined && filter.includes(machineId);
}

/** The chats listed under the filter; the same array when nothing is filtered. */
export function filterChatsByMachine<T extends MachineFilterableChat>(
  chats: T[],
  filter: MachineFilter,
  context: ChatMachineContext,
): T[] {
  if (!filter) return chats;
  return chats.filter((chat) => chatMatchesMachineFilter(chat, filter, context));
}

/** Whether a choice is shown under the filter. */
export function isMachineShown(filter: MachineFilter, id: string): boolean {
  return !filter || filter.includes(id);
}

/**
 * Turn one choice on or off. `options` is every choice the menu offers.
 * Choosing every option is All machines (null), so a machine added later is
 * shown; turning off the last shown choice is refused (the selection comes
 * back unchanged), since a filter that shows nothing is not a view.
 */
export function toggleMachine(filter: MachineFilter, id: string, options: readonly string[]): MachineFilter {
  const shown = filter ?? options;
  const next = shown.includes(id) ? shown.filter((x) => x !== id) : [...shown, id];
  if (next.length === 0) return filter;
  return options.every((option) => next.includes(option)) ? null : next;
}

/** Show only this choice ("Only this"). With one option, that is all of them. */
export function onlyMachine(id: string, options: readonly string[]): MachineFilter {
  return options.length <= 1 ? null : [id];
}

/** Chats waiting on the user, per machine id (NO_MACHINE included). */
export function countByMachine<T extends MachineFilterableChat>(
  chats: readonly T[],
  context: ChatMachineContext,
  include: (chat: T) => boolean,
): Map<string, number> {
  const counts = new Map<string, number>();
  for (const chat of chats) {
    if (!include(chat)) continue;
    const machineId = chatMachineId(chat, context);
    if (machineId === undefined) continue;
    counts.set(machineId, (counts.get(machineId) ?? 0) + 1);
  }
  return counts;
}
