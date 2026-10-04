import {
  DaemonStatus,
  type DaemonInfo as CloudDaemon,
} from "../../gen/reliant/v1/daemon_registry_pb";

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
  DaemonStatus.ACTIVE,
  DaemonStatus.PENDING,
  DaemonStatus.SUSPENDED,
  DaemonStatus.DISCONNECTED,
  // IDLE is in the registry enum but is never emitted by the list handler.
  // Listing it would imply a state that cannot occur.
];

export function isCloneableDaemon(daemon: CloudDaemon): boolean {
  return CLONEABLE_STATUSES.includes(daemon.status);
}

export function isFailedDaemon(daemon: CloudDaemon): boolean {
  return daemon.status === DaemonStatus.FAILED;
}

/**
 * When this machine was last in use, as epoch seconds.
 *
 * `connectedAt` is the last time the daemon attached to the gateway, which is
 * the closest thing the control plane records to "when the user was last
 * working here". It falls back to `createdAt` for a machine that has never
 * connected (a PENDING one, still booting), and to 0 when neither is set so
 * an unknown machine sorts last rather than jumping the queue.
 */
function lastUsedSeconds(daemon: CloudDaemon): number {
  const stamp = daemon.connectedAt ?? daemon.createdAt;
  return stamp ? Number(stamp.seconds) : 0;
}

/** Most recently used first. */
function byRecency(a: CloudDaemon, b: CloudDaemon): number {
  return lastUsedSeconds(b) - lastUsedSeconds(a);
}

/**
 * Running machines first, then most recently used. This is the same ordering
 * `pickCloneTarget` applies, so the default target is always the first option
 * in the list the user sees — a default that did not match the top of the list
 * would read as a bug.
 */
function byReadinessThenRecency(a: CloudDaemon, b: CloudDaemon): number {
  const aActive = a.status === DaemonStatus.ACTIVE ? 0 : 1;
  const bActive = b.status === DaemonStatus.ACTIVE ? 0 : 1;
  return aActive - bActive || byRecency(a, b);
}

/**
 * The machine a clone will be queued for by default, or null when none
 * qualifies.
 *
 * Two rules, in order. An already-running machine clones soonest, so ACTIVE
 * beats everything else regardless of age — queueing behind a machine that
 * still has to boot is slower even if the user touched it more recently.
 * WITHIN a tier the most recently used machine wins, because with several
 * machines the one the user was last working on is the one they mean, and
 * silently picking whichever the server listed first drops a checkout
 * somewhere they did not ask for.
 */
export function pickCloneTarget(daemons: CloudDaemon[]): CloudDaemon | null {
  const active = daemons.filter((d) => d.status === DaemonStatus.ACTIVE);
  if (active.length > 0) return [...active].sort(byRecency)[0];

  const cloneable = daemons.filter(isCloneableDaemon);
  if (cloneable.length > 0) return [...cloneable].sort(byRecency)[0];

  return null;
}

/** One machine the user may choose as a clone target. */
export interface CloneTargetOption {
  daemon: CloudDaemon;
  /** True when the clone starts now rather than waiting for the machine. */
  immediate: boolean;
}

/**
 * Every machine that can take a clone, most recently used first — the list
 * behind the target picker.
 *
 * FAILED machines are omitted rather than disabled: nothing will ever drain
 * their queue, so offering one as a choice can only waste the user's click.
 * The blocked-with-a-reason copy for "all my machines failed" is
 * `cloneAvailability`'s job, and it stays on the button itself.
 */
export function cloneTargetOptions(daemons: CloudDaemon[]): CloneTargetOption[] {
  return daemons
    .filter(isCloneableDaemon)
    .sort(byReadinessThenRecency)
    .map((daemon) => ({
      daemon,
      immediate: daemon.status === DaemonStatus.ACTIVE,
    }));
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
      immediate: target.status === DaemonStatus.ACTIVE,
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
    cloneState.target.hostname || fallbackHost || "your machine";
  return cloneState.immediate
    ? `Pull a GitHub repo onto ${name}`
    : `Queue a GitHub repo — it'll clone when ${name} is ready`;
}

/**
 * The human-readable reason a machine failed, when the orchestrator supplied
 * one. `lastStatusMessage` is populated from gateway-side events (a storage
 * quota rejection, an image pull failure); it is frequently empty, and a
 * missing reason must not render as a blank line pretending to be one.
 *
 * The control-plane guarantees this string is user-safe — it translates the
 * underlying error and never passes raw Kubernetes text through — so callers
 * render it verbatim. Settings → Machines has its own `daemonFailureReason`
 * which additionally decides WHEN to show it (it is handed daemons of every
 * status); this one is only ever called on a row already known to be FAILED,
 * so it just answers whether there is a message worth printing.
 */
export function failureReason(daemon: CloudDaemon): string | null {
  const message = daemon.lastStatusMessage?.trim();
  return message ? message : null;
}
