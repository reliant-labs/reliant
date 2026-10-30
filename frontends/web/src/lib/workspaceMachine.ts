// Copyright (c) 2025 Reliant Labs

/**
 * Which machine a new chat's workspace pins it to.
 *
 * A branch workspace (a worktree) is a checkout on ONE machine's disk —
 * `daemon_id` on its row. A chat in it can only run there: on any other
 * machine its tools, Files tab and terminal point at a folder that does not
 * exist (the owner saw "Its workspace was archived or removed from the
 * machine"). The main checkout is on every machine with the project, so it
 * pins nothing.
 *
 * The new-chat composer follows two rules, and the server enforces the same
 * (StartChat refuses a daemon_id that is not the workspace's machine):
 *
 *   - selecting a workspace selects its machine (the picker shows it, with a
 *     note saying why);
 *   - choosing a machine offers only that machine's workspaces (plus the main
 *     checkout and "New workspace", which is made on that machine).
 *
 * Pure: no React, no RPCs.
 */

import { DEFAULT_MACHINE, NO_MACHINE, type ChatMachineChoice } from "./chatMachine";

export interface MachineOwnedWorkspace {
  id: string;
  is_main: boolean;
  /** The machine holding the checkout; absent for the main checkout and old rows. */
  daemon_id?: string;
}

/**
 * The machine a workspace lives on, or undefined when it pins none: the main
 * checkout, a row that records no machine, or a machine the user no longer
 * has (it can run nowhere, and the server says so on send).
 */
export function workspaceMachine(
  workspace: MachineOwnedWorkspace | undefined,
  machineIds: ReadonlySet<string>,
): string | undefined {
  if (!workspace || workspace.is_main || !workspace.daemon_id) return undefined;
  return machineIds.has(workspace.daemon_id) ? workspace.daemon_id : undefined;
}

export interface ResolvedChatMachine {
  /** What the picker shows as selected: a daemon id, DEFAULT_MACHINE or NO_MACHINE. */
  value: ChatMachineChoice;
  /** Set when the selected workspace decided the machine. */
  lockedBy?: string;
}

/**
 * The machine a new chat runs on: No machine when the user chose it (the chat
 * then runs in the main checkout and ignores the workspace); else the selected
 * workspace's machine; else the user's choice; else the default.
 */
export function resolveChatMachine({
  choice,
  fallback,
  workspaceDaemonId,
}: {
  choice: ChatMachineChoice | null;
  fallback: ChatMachineChoice;
  workspaceDaemonId: string | undefined;
}): ResolvedChatMachine {
  if (choice === NO_MACHINE) return { value: NO_MACHINE };
  if (workspaceDaemonId) return { value: workspaceDaemonId, lockedBy: workspaceDaemonId };
  return { value: choice ?? fallback };
}

/**
 * What StartChat sends for a resolved machine. A machine the workspace decided
 * is left to the server, which pins the chat to its workspace's machine
 * (daemonplacement.PinTarget): naming it again adds nothing and would be
 * refused on a project whose installs were never recorded there.
 */
export function startChoice(resolved: ResolvedChatMachine): ChatMachineChoice {
  return resolved.lockedBy ? DEFAULT_MACHINE : resolved.value;
}

/**
 * The branch workspaces to offer for `machineId`: the ones on it, and the ones
 * whose machine is unknown (old rows, where the server cannot refuse either).
 * Undefined machineId (the default machine, nothing chosen) offers every one,
 * and selecting one on another machine moves the chat there.
 */
export function workspacesOnMachine<T extends MachineOwnedWorkspace>(
  workspaces: ReadonlyArray<T>,
  machineId: string | undefined,
): T[] {
  return workspaces.filter((w) => !machineId || !w.daemon_id || w.daemon_id === machineId);
}

/**
 * The machine the workspace list is narrowed to: the workspace's machine when
 * one decided it, else a machine the user explicitly chose. Undefined when
 * nothing was chosen, so every workspace is offered, each saying where it is.
 */
export function workspaceFilterMachine(
  choice: ChatMachineChoice | null,
  resolved: ResolvedChatMachine,
): string | undefined {
  if (resolved.lockedBy) return resolved.lockedBy;
  if (choice && choice !== DEFAULT_MACHINE && choice !== NO_MACHINE) return choice;
  return undefined;
}

/**
 * Whether choosing `next` in the picker leaves the selected workspace behind:
 * a machine other than the one holding it. The composer then falls back to
 * the main checkout, which every machine has.
 */
export function choiceLeavesWorkspace(next: ChatMachineChoice, workspaceDaemonId: string | undefined): boolean {
  return !!workspaceDaemonId && next !== NO_MACHINE && next !== workspaceDaemonId;
}
