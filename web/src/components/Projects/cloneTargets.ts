import {
  DAEMON_STATUS_ACTIVE,
  DAEMON_STATUS_FAILED,
  DAEMON_STATUS_PENDING,
  DAEMON_STATUS_SUSPENDED,
  DAEMON_STATUS_DISCONNECTED,
  type Daemon as CloudDaemon,
} from "../../services/controlPlane/daemon";

/**
 * Which cloud daemons can accept a clone, and what to tell the user when none
 * can.
 *
 * This is deliberately a pure module: the picker previously HID "Clone repo"
 * unless an ACTIVE daemon existed, so a user whose only machine had failed saw
 * no way to add a project at all — the dead end this fixes. Keeping the rule
 * out of the component lets the decision be tested directly, per daemon-set,
 * rather than inferred from rendered markup.
 *
 * A clone does not need a running machine. CloneRepo durably enqueues the
 * command onto the pending-command stream and the daemon drains it when it
 * connects, so a PENDING machine that is still starting is a perfectly good
 * target. FAILED is the one status that cannot be: nothing will ever drain the
 * queue for it.
 */

/** Statuses whose machine will eventually drain a queued clone. */
const CLONEABLE_STATUSES = [
  DAEMON_STATUS_ACTIVE,
  DAEMON_STATUS_PENDING,
  DAEMON_STATUS_SUSPENDED,
  DAEMON_STATUS_DISCONNECTED,
];

export function isCloneableDaemon(daemon: CloudDaemon): boolean {
  return CLONEABLE_STATUSES.includes(daemon.status);
}

export function isFailedDaemon(daemon: CloudDaemon): boolean {
  return daemon.status === DAEMON_STATUS_FAILED;
}

/** The machine a clone will be queued for, or null when none qualifies. */
export function pickCloneTarget(daemons: CloudDaemon[]): CloudDaemon | null {
  // An already-running machine clones soonest, so prefer one when present
  // rather than queueing behind a machine that still has to boot.
  return (
    daemons.find((d) => d.status === DAEMON_STATUS_ACTIVE) ??
    daemons.find(isCloneableDaemon) ??
    null
  );
}

export type CloneAvailability =
  | { kind: "ready"; target: CloudDaemon; immediate: boolean }
  | { kind: "blocked"; reason: string };

/**
 * Whether "Clone repo" can be actioned, and if not, why — as copy meant for
 * the user rather than a status code. The button stays VISIBLE in the blocked
 * case and carries the reason; hiding it is what stranded people.
 */
export function cloneAvailability(daemons: CloudDaemon[]): CloneAvailability {
  const target = pickCloneTarget(daemons);
  if (target) {
    return {
      kind: "ready",
      target,
      immediate: target.status === DAEMON_STATUS_ACTIVE,
    };
  }
  if (daemons.length === 0) {
    return { kind: "blocked", reason: "Create a machine first to clone a repository." };
  }
  if (daemons.every(isFailedDaemon)) {
    return {
      kind: "blocked",
      reason:
        daemons.length === 1
          ? "Your machine failed to start, so there's nowhere to clone to. Delete it and create a new one."
          : "All your machines failed to start, so there's nowhere to clone to. Delete them and create a new one.",
    };
  }
  return { kind: "blocked", reason: "No machine is available to clone onto." };
}

/**
 * The sub-label under "Clone repo". It states the honest outcome up front:
 * cloning onto a machine that is not running yet is QUEUED, not done, and
 * saying "cloned" there is the false success this replaces.
 */
export function cloneDescription({
  cloneState,
  hasGitHubCredential,
  fallbackHost,
}: {
  cloneState: CloneAvailability;
  hasGitHubCredential: boolean;
  fallbackHost?: string;
}): string {
  if (cloneState.kind === "blocked") return cloneState.reason;
  if (!hasGitHubCredential) return "Connect GitHub to clone a repository";

  const name =
    cloneState.target.name || cloneState.target.hostname || fallbackHost || "your machine";
  return cloneState.immediate
    ? `Pull a GitHub repo onto ${name}`
    : `Queue a GitHub repo — it'll clone when ${name} is ready`;
}

/**
 * The human-readable reason a machine failed, when the orchestrator supplied
 * one. `lastStatusMessage` is populated from gateway-side events (a storage
 * quota rejection, an image pull failure); it is frequently empty, and a
 * missing reason must not render as a blank line pretending to be one.
 */
export function failureReason(daemon: CloudDaemon): string | null {
  const message = daemon.lastStatusMessage?.trim();
  return message ? message : null;
}
