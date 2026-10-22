/**
 * Which add-project action leads on the picker.
 *
 * The picker presented "Open Project" as the primary action unconditionally —
 * full primary tint, first in the card — with "Clone repo" underneath it as a
 * quiet secondary. That ordering is right for a local install and wrong for a
 * cloud account: a cloud user has no local checkout to browse to, and the
 * directory picker they are being steered toward reads the wrong filesystem
 * entirely (the browser's host, not the machine their code lives on). Their
 * real first step is always "clone from GitHub".
 *
 * So the lead action follows the machine the user actually works on rather
 * than being fixed. This is a pure module so the decision can be tested per
 * daemon shape instead of inferred from rendered markup.
 */

/** Daemon types that mean "this machine's filesystem IS the user's". */
const LOCAL_DAEMON_TYPES = new Set(["local", "self_hosted", "self-hosted", "selfhosted"]);

export function isLocalDaemonType(daemonType: string | undefined): boolean {
  return LOCAL_DAEMON_TYPES.has((daemonType ?? "").toLowerCase());
}

export type AddProjectLead = "clone" | "open";

/**
 * The action to present as primary.
 *
 * - No cloud daemons on the account at all (an OSS / local-only install):
 *   "open". There is no clone affordance to lead with.
 * - The machine the user is on is LOCAL: "open". Its filesystem is theirs, so
 *   browsing to a directory is a real and usually faster route.
 * - Otherwise — a cloud account, whether or not a machine has attached yet:
 *   "clone". Browsing a directory cannot reach a cloud machine's disk, so
 *   leading with it sends the user somewhere that cannot work.
 *
 * Note the no-active-daemon case resolves to "clone" rather than to "open":
 * during onboarding, or while a machine is still booting, cloning is exactly
 * what the user came to do and the clone is durably queued anyway.
 */
export function addProjectLead({
  hasCloudDaemons,
  activeDaemonType,
}: {
  hasCloudDaemons: boolean;
  activeDaemonType: string | undefined;
}): AddProjectLead {
  if (!hasCloudDaemons) return "open";
  if (isLocalDaemonType(activeDaemonType)) return "open";
  return "clone";
}
