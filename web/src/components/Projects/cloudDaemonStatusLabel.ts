/**
 * Status label for a CONTROL-PLANE machine row.
 *
 * Extracted from ProjectPicker so it can be tested directly, because the bug
 * it encodes was invisible by inspection: there are two `DaemonStatus` enums
 * in this app and their numeric values collide.
 *
 *   value | control-plane   | reliant registry
 *   ------+-----------------+------------------
 *     1   | PENDING         | ACTIVE
 *     2   | ACTIVE          | IDLE
 *     3   | SUSPENDED       | DISCONNECTED
 *     4   | DISCONNECTED    | —
 *     5   | FAILED          | —
 *
 * The previous implementation fell through to `DaemonStatus[daemon.status]`
 * using the REGISTRY enum on a CONTROL-PLANE row, so a machine that was still
 * starting (PENDING=1) rendered as "active" with a green dot, and a
 * DISCONNECTED (4) machine ran off the end of the registry enum and rendered
 * "unknown". Both of those are exactly the states a user is watching for.
 *
 * Every branch here reads the control-plane enum explicitly. There is no
 * fallthrough that could pick up the wrong one.
 */

import {
  DAEMON_STATUS_ACTIVE,
  DAEMON_STATUS_DISCONNECTED,
  DAEMON_STATUS_FAILED,
  DAEMON_STATUS_PENDING,
  DAEMON_STATUS_SUSPENDED,
  type Daemon as CloudDaemon,
} from "../../services/controlPlane/daemon";

export function cloudDaemonStatusLabel(
  daemon: CloudDaemon,
  isResuming: boolean,
): string {
  if (isResuming) return "resuming";
  switch (daemon.status) {
    case DAEMON_STATUS_ACTIVE:
      return "active";
    case DAEMON_STATUS_SUSPENDED:
      return "suspended";
    case DAEMON_STATUS_PENDING:
      return "starting";
    case DAEMON_STATUS_DISCONNECTED:
      return "disconnected";
    case DAEMON_STATUS_FAILED:
      return "failed";
    default:
      return "unknown";
  }
}
