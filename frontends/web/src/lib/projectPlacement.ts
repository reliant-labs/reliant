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
  /** Where the checkout is (or is landing) on that machine. */
  path?: string;
  /** Why the last clone failed, as the machine reported it. */
  install_error?: string;
  /**
   * The project's nested repositories on that machine, each cloned into its
   * folder after the root. Absent from a server older than nested clones.
   */
  nested_repos?: NestedRepoInstall[];
}

/** One nested repository of a checkout, and how its clone went. */
export interface NestedRepoInstall {
  name: string;
  /** Its folder inside the checkout ("forge", "tools/sdk"). */
  relative_path: string;
  install_state: ProjectInstallState;
  /** git's words, when its clone failed. */
  install_error?: string;
}

/** The wire shape (ProjectDaemonNestedRepo) as an install row carries it. */
export function nestedRepoInstalls(
  repos: ReadonlyArray<{ name: string; relativePath: string; installState: ProjectInstallState; installError: string }> | undefined,
): NestedRepoInstall[] {
  return (repos ?? []).map((repo) => ({
    name: repo.name,
    relative_path: repo.relativePath,
    install_state: repo.installState,
    install_error: repo.installError || undefined,
  }));
}

/**
 * A nested repository as the user knows it: its folder ("forge/"), and its
 * name too when the folder is called something else ("sdk (tools/sdk/)").
 */
export function nestedRepoLabel(repo: { name: string; relative_path: string }): string {
  const path = repo.relative_path.replace(/\/+$/, "");
  if (!path) return repo.name;
  const base = path.split("/").pop();
  return !repo.name || base === repo.name ? `${path}/` : `${repo.name} (${path}/)`;
}

/**
 * The machines a project would be moved off when it is set up on `targetId`:
 * every machine that has its checkout (INSTALLED) other than the target, an
 * online one first, so its uncommitted work can be checked.
 */
export function moveSourceDaemonIds(
  installs: ReadonlyArray<InstallRow>,
  projectId: string | undefined,
  targetId: string | undefined,
  isOnline: (daemonId: string) => boolean = () => false,
): string[] {
  if (!projectId) return [];
  const ids = installs
    .filter(
      (row) =>
        row.project_id === projectId &&
        row.daemon_id !== targetId &&
        row.install_state === ProjectInstallState.INSTALLED,
    )
    .map((row) => row.daemon_id);
  const unique = [...new Set(ids)];
  return [...unique.filter(isOnline), ...unique.filter((id) => !isOnline(id))];
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
  /** Why the clone onto this machine failed, when it did and the machine said. */
  failureOn(daemonId: string): string | undefined;
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
  const failures = new Map<string, string>();
  if (projectId) {
    for (const row of installs) {
      if (row.project_id !== projectId) continue;
      const state = STATE_OF_INSTALL[row.install_state] ?? "absent";
      // One row per (project, daemon) on the server; if a stale cache ever
      // holds two, the better state wins.
      const prior = byDaemon.get(row.daemon_id);
      if (!prior || STATE_RANK[state] < STATE_RANK[prior]) byDaemon.set(row.daemon_id, state);
      const reason = row.install_error?.trim();
      if (state === "failed" && reason) failures.set(row.daemon_id, reason);
    }
  }
  return {
    known: byDaemon.size > 0,
    stateOn: (daemonId) => byDaemon.get(daemonId) ?? "absent",
    failureOn: (daemonId) => (byDaemon.get(daemonId) === "failed" ? failures.get(daemonId) : undefined),
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

/**
 * The folder to clone the project into on `daemonId`, or undefined to let
 * the server choose the machine's default projects directory.
 *
 * `path` (the project's path) is a folder on the machine the project was
 * added from. It is reused only on a machine of the same platform as one
 * that has the checkout there: a second Mac of the same user takes the same
 * /Users/… path, but a Linux machine cannot create /Users at all. On
 * 2026-11-23 setting up a project added from a Mac on a Linux build host
 * failed at once with "mkdir /Users: permission denied", and "Try again"
 * sent the same path again. A machine of the target's platform that has the
 * checkout elsewhere names the folder instead; no such machine, the server's
 * default.
 *
 * Nothing to judge by (no other machine has the checkout, the target's or the
 * holder's platform is not reported) is the server's default too: it is
 * always a folder that machine can create, and the path is not known to be.
 * That is also what makes "Try again" after a failed set-up work: the failed
 * row is never evidence for its own path.
 */
export function setUpPathOn({
  installs,
  projectId,
  path,
  daemonId,
  platformOf,
}: {
  installs: ReadonlyArray<InstallRow>;
  projectId: string;
  path: string | undefined;
  daemonId: string;
  platformOf: (daemonId: string) => string | undefined;
}): string | undefined {
  const wanted = path?.trim() || undefined;
  const platform = (id: string) => platformOf(id)?.trim().toLowerCase() || undefined;
  const target = platform(daemonId);
  const holders = installs.filter(
    (row) =>
      row.project_id === projectId &&
      row.daemon_id !== daemonId &&
      row.install_state === ProjectInstallState.INSTALLED,
  );
  if (!target) return undefined;
  // The project's path, where a machine like this one has it.
  if (wanted && holders.some((row) => (row.path ?? wanted) === wanted && platform(row.daemon_id) === target)) {
    return wanted;
  }
  // Else where a machine of this platform has it.
  const alike = holders.find((row) => row.path?.trim() && platform(row.daemon_id) === target);
  return alike?.path?.trim();
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
 *
 * `canClone` is whether the project can be added to another machine at all
 * (canSetUpElsewhere): a plain folder cannot, so its reason names the other
 * ways forward instead of an add it has no way to do.
 */
export function startBlockedReason({
  placement,
  daemonId,
  projectName,
  machineName,
  canClone = true,
}: {
  placement: ProjectPlacement;
  daemonId: string | undefined;
  projectName: string;
  machineName: string;
  canClone?: boolean;
}): string | null {
  if (!daemonId || !placement.known) return null;
  switch (placement.stateOn(daemonId)) {
    case "ready":
      return null;
    case "setting_up":
      return `Setting up ${projectName} on ${machineName}. You can send once it's there.`;
    case "failed": {
      // The machine's reason, when it gave one: "Try again" alone reads as
      // transient, and most clone failures (a path, a credential) are not.
      const reason = placement.failureOn(daemonId)?.replace(/[.\s]+$/, "");
      const why = reason ? `: ${reason}.` : ".";
      return `Setting up ${projectName} on ${machineName} failed${why} Try again, or pick a machine it's on.`;
    }
    case "absent":
      return canClone
        ? `${projectName} isn't on ${machineName} yet. Add it there, or pick a machine it's on.`
        : `${projectName} is a folder on another machine, so it can't be copied to ${machineName}. Pick a machine it's on, or choose a folder already on ${machineName}.`;
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
