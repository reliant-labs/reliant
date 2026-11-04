// Copyright (c) 2025 Reliant Labs

/**
 * Which machines a project is set up on, as the new-chat pickers show it.
 *
 * A project is "on" a machine when that machine has its checkout: an
 * INSTALLED project_daemons row. StartChat refuses any other machine for a
 * project installed anywhere (internal/daemonchoice.Validate: "project is not
 * installed on that daemon"), and a chat that got past that would open on a
 * file tree saying "<project> isn't on <machine>". So the pickers ask this
 * module first and never offer a pair that fails: a machine without the
 * project is listed as "Not set up", with the action that sets it up there
 * (CreateProjectFromRepo, the same clone ProjectCheckoutMissingPanel runs).
 *
 * A project with no install rows at all has no recorded checkout anywhere,
 * and the server accepts any of the user's machines for it (Validate's
 * "no rows" case). `known` is false then, and the pickers constrain nothing —
 * they cannot claim a machine lacks a project nobody has a record of.
 *
 * Pure: no React, no RPCs, so the rules are testable on their own.
 */

import { ProjectInstallState } from "@/gen/reliant/v1/project_pb";

export interface InstallRow {
  project_id: string;
  daemon_id: string;
  install_state: ProjectInstallState;
}

/**
 * The project on one machine:
 *   ready       the checkout is there; a chat can start
 *   setting_up  a clone is queued or running; a chat can start once it lands
 *   failed      the last clone failed; it can be tried again
 *   absent      no record of the project on this machine
 */
export type PlacementState = "ready" | "setting_up" | "failed" | "absent";

export interface ProjectPlacement {
  /** The project has an install record somewhere, so absence means something. */
  known: boolean;
  stateOn(daemonId: string): PlacementState;
}

const STATE_OF_INSTALL: Record<ProjectInstallState, PlacementState> = {
  [ProjectInstallState.UNSPECIFIED]: "absent",
  [ProjectInstallState.INSTALLED]: "ready",
  [ProjectInstallState.INSTALLING]: "setting_up",
  [ProjectInstallState.FAILED]: "failed",
};

export function projectPlacement(
  installs: ReadonlyArray<InstallRow>,
  projectId: string | undefined,
): ProjectPlacement {
  const byDaemon = new Map<string, PlacementState>();
  if (projectId) {
    for (const row of installs) {
      if (row.project_id !== projectId) continue;
      const state = STATE_OF_INSTALL[row.install_state] ?? "absent";
      // One row per (project, daemon) on the server; if a stale cache ever
      // holds two, the better state wins.
      const prior = byDaemon.get(row.daemon_id);
      if (!prior || STATE_RANK[state] < STATE_RANK[prior]) byDaemon.set(row.daemon_id, state);
    }
  }
  return {
    known: byDaemon.size > 0,
    stateOn: (daemonId) => byDaemon.get(daemonId) ?? "absent",
  };
}

const STATE_RANK: Record<PlacementState, number> = { ready: 0, setting_up: 1, failed: 2, absent: 3 };

/** A chat can be started on this machine for this project. */
export function canStartOn(placement: ProjectPlacement, daemonId: string): boolean {
  return !placement.known || placement.stateOn(daemonId) === "ready";
}

/**
 * The machine can be chosen for the project now: ready, or setting up (the
 * composer then waits for the clone). A machine without it is set up first.
 */
export function canChoose(placement: ProjectPlacement, daemonId: string): boolean {
  if (!placement.known) return true;
  const state = placement.stateOn(daemonId);
  return state === "ready" || state === "setting_up";
}

/** Whether the project can be put on another machine: only a repository can be cloned. */
export function canSetUpElsewhere(project: { remote_url?: string } | null | undefined): boolean {
  return Boolean(project?.remote_url);
}

export interface PlacementSections<T> {
  /** Has the project: ready ones first, then those still setting up. */
  ready: T[];
  /** Does not have it (absent, or its clone failed). */
  notSetUp: T[];
}

/**
 * Split machines for a picker: the project's own machines first, then the
 * rest. Order inside each section is the caller's (status, then name).
 */
export function sectionMachines<T>(
  machines: ReadonlyArray<T>,
  daemonIdOf: (machine: T) => string,
  placement: ProjectPlacement,
): PlacementSections<T> {
  const ready: T[] = [];
  const settingUp: T[] = [];
  const notSetUp: T[] = [];
  for (const machine of machines) {
    const state = placement.stateOn(daemonIdOf(machine));
    if (state === "ready") ready.push(machine);
    else if (state === "setting_up") settingUp.push(machine);
    else notSetUp.push(machine);
  }
  return { ready: [...ready, ...settingUp], notSetUp };
}

/**
 * Why a new chat cannot be sent with this project and machine, or null when
 * it can. `daemonId` is the machine the chat will run on (the explicit pick,
 * or what the default resolves to); undefined when unknown, which the server
 * resolves itself.
 */
export function startBlockedReason({
  placement,
  daemonId,
  projectName,
  machineName,
}: {
  placement: ProjectPlacement;
  daemonId: string | undefined;
  projectName: string;
  machineName: string;
}): string | null {
  if (!daemonId || !placement.known) return null;
  switch (placement.stateOn(daemonId)) {
    case "ready":
      return null;
    case "setting_up":
      return `Setting up ${projectName} on ${machineName}. You can send once it's there.`;
    case "failed":
      return `Setting up ${projectName} on ${machineName} failed. Try again, or pick a machine it's on.`;
    case "absent":
      return `${projectName} isn't set up on ${machineName}. Set it up there, or pick a machine it's on.`;
  }
}

/**
 * The machines a new chat for this project may default to: the ones it is
 * installed on, else (none is yet) the ones it is being set up on, so the
 * picker names the machine the clone is landing on rather than one that will
 * never have it. Undefined when neither: nothing to narrow to.
 */
export function preferredDaemonIds(
  installs: ReadonlyArray<InstallRow>,
  projectId: string | undefined,
): ReadonlySet<string> | undefined {
  if (!projectId) return undefined;
  const rows = installs.filter((row) => row.project_id === projectId);
  for (const state of [ProjectInstallState.INSTALLED, ProjectInstallState.INSTALLING]) {
    const ids = rows.filter((row) => row.install_state === state).map((row) => row.daemon_id);
    if (ids.length > 0) return new Set(ids);
  }
  return undefined;
}

/** Some machine is still cloning this project, so its state will change soon. */
export function isSettingUpAnywhere(installs: ReadonlyArray<InstallRow>, projectId: string | undefined): boolean {
  return Boolean(projectId) && installs.some(
    (row) => row.project_id === projectId && row.install_state === ProjectInstallState.INSTALLING,
  );
}
