// Copyright (c) 2025 Reliant Labs

/**
 * Which daemons an automation may run on, in which order, and which one the
 * form should pick for the user.
 *
 * The rule mirrors the server's validateTriggerDaemon
 * (internal/grpc/services/trigger.go) so the form never offers a choice the
 * server will reject:
 *
 *   - the daemon must be one of the caller's (the registry list is already
 *     scoped to the caller), and
 *   - if the project has ANY project_daemons row, the daemon must have the
 *     project INSTALLED there. A project with no rows at all (a local path a
 *     daemon discovered, or one predating install tracking) accepts any daemon.
 *
 * Pure: no React, no RPCs. That is what makes the rule testable.
 */

import { DaemonStatus, type DaemonInfo } from "@/gen/reliant/v1/daemon_registry_pb";
import { ProjectInstallState, type ProjectDaemonInfo } from "@/api/project-grpc";

export interface DaemonChoice {
  daemonId: string;
  label: string;
  statusLabel: string;
  /** The project is installed on this daemon. */
  installed: boolean;
  /** The server would accept this daemon for this project. */
  eligible: boolean;
  /** Why it is not eligible, for the option text. */
  ineligibleReason?: string;
}

export function daemonLabel(daemon: Pick<DaemonInfo, "daemonId" | "hostname"> | undefined, daemonId: string): string {
  return daemon?.hostname || `daemon ${daemonId.slice(0, 8)}`;
}

/** Registry status in words. Online daemons are the ones a run can start on immediately. */
export function daemonStatusLabel(status: DaemonStatus): string {
  switch (status) {
    case DaemonStatus.ACTIVE:
      return "online";
    case DaemonStatus.IDLE:
      return "idle";
    case DaemonStatus.PENDING:
      return "starting";
    case DaemonStatus.SUSPENDED:
      return "suspended";
    case DaemonStatus.DISCONNECTED:
      return "offline";
    case DaemonStatus.FAILED:
      return "failed";
    default:
      return "unknown";
  }
}

const STATUS_RANK: Partial<Record<DaemonStatus, number>> = {
  [DaemonStatus.ACTIVE]: 0,
  [DaemonStatus.IDLE]: 1,
  [DaemonStatus.PENDING]: 2,
  [DaemonStatus.SUSPENDED]: 3,
  [DaemonStatus.DISCONNECTED]: 4,
  [DaemonStatus.FAILED]: 5,
};

function statusRank(status: DaemonStatus): number {
  return STATUS_RANK[status] ?? 6;
}

/**
 * The picker's options for a project: eligible first, then installed, then
 * by status (online first), then by name.
 */
export function buildDaemonChoices(
  daemons: DaemonInfo[],
  projectDaemons: ProjectDaemonInfo[],
  projectId: string,
): DaemonChoice[] {
  const rows = projectDaemons.filter((row) => row.project_id === projectId);
  const installedOn = new Set(
    rows.filter((row) => row.install_state === ProjectInstallState.INSTALLED).map((row) => row.daemon_id),
  );
  // Any row — even a failed or in-flight clone — means install tracking exists
  // for this project, and the server then requires an INSTALLED one.
  const tracked = rows.length > 0;

  return daemons
    .map((daemon): DaemonChoice => {
      const installed = installedOn.has(daemon.daemonId);
      const eligible = !tracked || installed;
      return {
        daemonId: daemon.daemonId,
        label: daemonLabel(daemon, daemon.daemonId),
        statusLabel: daemonStatusLabel(daemon.status),
        installed,
        eligible,
        ineligibleReason: eligible ? undefined : "project not installed",
      };
    })
    .sort((a, b) => {
      if (a.eligible !== b.eligible) return a.eligible ? -1 : 1;
      if (a.installed !== b.installed) return a.installed ? -1 : 1;
      const byStatus =
        statusRank(daemons.find((d) => d.daemonId === a.daemonId)!.status) -
        statusRank(daemons.find((d) => d.daemonId === b.daemonId)!.status);
      if (byStatus !== 0) return byStatus;
      return a.label.localeCompare(b.label);
    });
}

/**
 * The daemon to preselect: the single obvious one, or nothing.
 *
 * Obvious means exactly one eligible daemon with the project installed, or —
 * when nothing is installed anywhere — exactly one eligible daemon at all.
 * With several equally valid choices the user picks; guessing which machine
 * an unattended run lands on is not a default worth having.
 */
export function defaultDaemonId(choices: DaemonChoice[]): string | undefined {
  const installed = choices.filter((c) => c.eligible && c.installed);
  if (installed.length === 1) return installed[0]!.daemonId;
  if (installed.length > 1) return undefined;
  const eligible = choices.filter((c) => c.eligible);
  return eligible.length === 1 ? eligible[0]!.daemonId : undefined;
}
