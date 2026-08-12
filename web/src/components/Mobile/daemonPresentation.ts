/**
 * Presentation rules for daemons on the mobile surface.
 *
 * Kept out of the components because the list row and the detail screen must
 * agree: a daemon that reads "Suspended" in the list and offers Resume there
 * has to read the same in detail, or tapping through looks like the state
 * changed under you. One table, two renderers.
 *
 * `resumable` is the load-bearing piece. `daemonResume` is true on mobile but
 * `daemonManage` is false, so Resume is the ONLY write this surface performs —
 * and it is only meaningful for a daemon that is stopped but recoverable.
 * Resuming an ACTIVE daemon is a no-op the control plane would reject, and
 * PENDING is already on its way up, so offering the button there invites a
 * double-start.
 */

import { DaemonStatus } from "@/gen/controlplane/v1/public/shared_pb";
import { DaemonSize } from "@/gen/controlplane/v1/public/shared_pb";
import type { Daemon } from "@/services/controlPlane/daemon";
import { timestampDate } from "@bufbuild/protobuf/wkt";

/** How a status renders, and whether Resume applies to it. */
export interface DaemonPresentation {
  label: string;
  /**
   * Semantic token class pair for the status pill. Tokens rather than raw
   * colors so both themes track — see the web styling contract.
   */
  pillClassName: string;
  /** Whether the mobile surface may offer Resume for a daemon in this state. */
  resumable: boolean;
}

const UNKNOWN: DaemonPresentation = {
  label: "Unknown",
  pillClassName: "bg-muted text-muted-foreground ring-border",
  resumable: false,
};

const BY_STATUS: Record<number, DaemonPresentation> = {
  [DaemonStatus.ACTIVE]: {
    label: "Active",
    pillClassName: "bg-success/10 text-success ring-success/25",
    resumable: false,
  },
  [DaemonStatus.PENDING]: {
    label: "Starting",
    pillClassName: "bg-info/10 text-info ring-info/25",
    // Already coming up. A Resume here races the start it would duplicate.
    resumable: false,
  },
  [DaemonStatus.SUSPENDED]: {
    label: "Suspended",
    pillClassName: "bg-warning/10 text-warning ring-warning/25",
    resumable: true,
  },
  [DaemonStatus.DISCONNECTED]: {
    label: "Disconnected",
    pillClassName: "bg-destructive/10 text-destructive ring-destructive/25",
    resumable: true,
  },
  [DaemonStatus.FAILED]: {
    label: "Failed",
    pillClassName: "bg-destructive/10 text-destructive ring-destructive/25",
    // A failed daemon needs the desktop's diagnostics + recreate path, which
    // is `daemonManage` — out of scope here. Resume would just fail again.
    resumable: false,
  },
};

export function presentDaemon(daemon: Daemon): DaemonPresentation {
  return BY_STATUS[daemon.status] ?? UNKNOWN;
}

/** Whether Resume — the mobile surface's only daemon write — applies. */
export function canResume(daemon: Daemon): boolean {
  return presentDaemon(daemon).resumable;
}

const SIZE_LABELS: Record<number, string> = {
  [DaemonSize.SMALL]: "Small",
  [DaemonSize.MEDIUM]: "Medium",
  [DaemonSize.LARGE]: "Large",
  [DaemonSize.XL]: "XL",
};

/** Size badge text, or "" when the control plane didn't report a tier. */
export function sizeLabel(daemon: Daemon): string {
  return SIZE_LABELS[daemon.size] ?? "";
}

/** Last heartbeat as epoch ms, or null when the daemon has never checked in. */
export function heartbeatMs(daemon: Daemon): number | null {
  if (!daemon.lastHeartbeat) return null;
  try {
    return timestampDate(daemon.lastHeartbeat).getTime();
  } catch {
    // A malformed timestamp should degrade to "no heartbeat", not crash the
    // list — this data crosses a network boundary from the daemon gateway.
    return null;
  }
}
