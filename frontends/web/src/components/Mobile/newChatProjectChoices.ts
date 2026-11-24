// Copyright (c) 2026 Reliant Labs

/**
 * The phone's new-chat Project picker (owner, 2026-11-24): which projects a
 * new chat can start in, and what picking each one does.
 *
 *   ready        the project is on one of the person's connected machines
 *                (connected, starting, or asleep: a send wakes it), or is
 *                landing there; picking it starts the chat in it
 *   unrecorded   no machine has a record of it (a folder a daemon found, or
 *                one older than install tracking): the server accepts any
 *                machine for it, so it is offered, after the ready ones
 *   set-up       on no connected machine, but a repository: picking it clones
 *                it onto `setUpTarget` first (reliant#3420's set-up, with the
 *                folder setUpPathOn picks), then starts in it
 *   unavailable  neither: listed, disabled, with the reason
 *
 * The context project is always first, whatever its state: it is the
 * default, and the screen already runs in it. A project the person can only
 * read (`viewer_access`) is left out: a chat cannot start in it.
 *
 * One primary project only. Attaching more projects is the desktop's
 * (Chat/NewChatView); the phone picks where the chat runs.
 *
 * Pure: no React, no RPCs.
 */

import type { DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import type { Project } from "@/hooks/project-queries";
import { isUsableMachineForChat } from "@/lib/chatMachine";
import { canChoose, canSetUpElsewhere, projectPlacement, type InstallRow } from "@/lib/projectPlacement";
import { daemonLabel } from "../Automations/daemonChoices";

type Machine = Pick<DaemonInfo, "daemonId" | "hostname" | "status"> & Partial<Pick<DaemonInfo, "name" | "daemonType">>;

export type ProjectChoiceKind = "ready" | "unrecorded" | "set-up" | "unavailable";

export interface ProjectChoice {
  project: Project;
  kind: ProjectChoiceKind;
  /** Where it is, or what picking it does, in words ("On laptop", "Set up on laptop"). */
  description: string;
  /** The first connected machine it is on (ready), for the row under the picker. */
  machineId?: string;
}

export interface ProjectChoicesInput {
  projects: ReadonlyArray<Project>;
  installs: ReadonlyArray<InstallRow>;
  daemons: ReadonlyArray<Machine>;
  currentProjectId: string | undefined;
  /** The machine a set-up clones onto: where the chat would run. None, and set-up is unavailable. */
  setUpTarget?: { daemonId: string; name: string };
}

const placesOf = (names: string[]) => (names.length > 2 ? `${names[0]} and ${names.length - 1} more` : names.join(" and "));

function choiceFor(project: Project, { installs, daemons, setUpTarget }: ProjectChoicesInput): ProjectChoice {
  const placement = projectPlacement(installs, project.id);
  if (!placement.known) return { project, kind: "unrecorded", description: "No machine on record" };
  const connected = daemons.filter(isUsableMachineForChat);
  const ready = connected.filter((d) => placement.stateOn(d.daemonId) === "ready");
  const landing = connected.filter((d) => canChoose(placement, d.daemonId) && placement.stateOn(d.daemonId) !== "ready");
  if (ready.length > 0) {
    return { project, kind: "ready", description: `On ${placesOf(ready.map((d) => daemonLabel(d, d.daemonId)))}`, machineId: ready[0].daemonId };
  }
  if (landing.length > 0) {
    return { project, kind: "ready", description: `Setting up on ${daemonLabel(landing[0], landing[0].daemonId)}…`, machineId: landing[0].daemonId };
  }
  // On a machine that is offline: name it, so "not on a machine" never misleads.
  const offline = daemons.filter((d) => placement.stateOn(d.daemonId) === "ready");
  const where = offline.length > 0 ? `On ${placesOf(offline.map((d) => daemonLabel(d, d.daemonId)))}, not connected` : "Not on a connected machine";
  if (!canSetUpElsewhere(project)) return { project, kind: "unavailable", description: `${where} · a folder, so it can't be set up elsewhere` };
  if (!setUpTarget) return { project, kind: "unavailable", description: `${where} · no machine to set it up on` };
  return { project, kind: "set-up", description: `${where} · Set up on ${setUpTarget.name}` };
}

const RANK: Record<ProjectChoiceKind, number> = { ready: 0, unrecorded: 1, "set-up": 2, unavailable: 3 };

export function newChatProjectChoices(input: ProjectChoicesInput): ProjectChoice[] {
  const current = input.projects.find((p) => p.id === input.currentProjectId);
  const others = input.projects
    .filter((p) => p.id !== input.currentProjectId && !p.viewer_access)
    .map((p) => choiceFor(p, input))
    // Stable: within a kind, the list's own order (most recent first).
    .sort((a, b) => RANK[a.kind] - RANK[b.kind]);
  return current ? [choiceFor(current, input), ...others] : others;
}
