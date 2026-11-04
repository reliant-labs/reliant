// Copyright (c) 2025 Reliant Labs

/**
 * The build a machine's daemon reported, as one short label for the machine
 * list.
 *
 * The registry keeps the build on the attachment lease, so it exists only
 * while the daemon is attached. A machine that is not attached says nothing
 * (null): "unknown build" there would read as a fault in a machine that is
 * merely asleep. An attached daemon that reported no build — one that predates
 * the report, or an unstamped binary — is "unknown build".
 */

import { DaemonStatus } from "@/gen/reliant/v1/daemon_registry_pb";

export const UNKNOWN_BUILD_LABEL = "unknown build";

export interface BuildReporting {
  status: DaemonStatus;
  version?: string;
  commit?: string;
}

const SHORT_COMMIT_LEN = 8;

export function machineBuildLabel(d: BuildReporting): string | null {
  if (d.status !== DaemonStatus.ACTIVE) return null;
  if (d.version) return d.version;
  if (d.commit) return d.commit.slice(0, SHORT_COMMIT_LEN);
  return UNKNOWN_BUILD_LABEL;
}
