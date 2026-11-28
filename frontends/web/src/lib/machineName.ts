/**
 * What to call a machine, everywhere a machine is named.
 *
 * A machine has two candidate labels and they mean different things:
 *
 *   - `name` — what its owner CALLED it ("default", "gpu-box"). The control
 *     plane owns it and mirrors it into the registry.
 *   - `hostname` — where it RUNS. For a self-hosted machine that is the
 *     computer's own name ("Seans-MacBook-Pro.local"), which is a fine label.
 *     For a managed machine it is the pod's hostname, "ws-" + its workspace id
 *     ("ws-ws-2aab1465"): an implementation detail no user chose, and the
 *     doubled prefix the owner saw on 2026-10-07 in place of the "default"
 *     they had typed.
 *
 * So: the name when there is one; a self-hosted machine's hostname when it is
 * a real one; and otherwise a short id-derived tag. A managed machine's
 * hostname is never shown as its name. Every surface that labels a machine
 * goes through here, so one machine cannot be called two things on two
 * screens.
 */

export interface NameableMachine {
  daemonId: string;
  name?: string;
  hostname?: string;
  daemonType?: string;
}

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** True for the registry's managed-machine type. */
export function isManagedMachine(d: Pick<NameableMachine, "daemonType">): boolean {
  return d.daemonType === "managed";
}

function shortId(daemonId: string): string {
  return (daemonId || "").slice(0, 8);
}

/**
 * A name or hostname that names nothing a person would recognize: empty, the
 * daemon's own id, or a bare UUID. A self-hosted daemon that connected with
 * neither a name nor a hostname is named by its id in the control plane — a
 * placeholder, not a name: shown, it read "reliant-labs isn't on
 * 46e3682b-33cd-…".
 */
function isPlaceholderLabel(label: string, daemonId: string): boolean {
  return !label || label.toLowerCase() === (daemonId || "").toLowerCase() || UUID_RE.test(label);
}

export function machineDisplayName(d: NameableMachine): string {
  const name = d.name?.trim() ?? "";
  if (!isPlaceholderLabel(name, d.daemonId)) return name;

  const id = shortId(d.daemonId);
  if (isManagedMachine(d)) {
    return id ? `Sandbox (${id})` : "Sandbox";
  }
  const hostname = d.hostname?.trim() ?? "";
  if (!isPlaceholderLabel(hostname, d.daemonId)) return hostname;
  return id ? `Self-hosted machine (${id})` : "Self-hosted machine";
}

/**
 * The label for a machine that may not be loaded yet — a clone target or a
 * pinned daemon known only by id. Falls back to `fallback` rather than a
 * fabricated tag, because "your machine" reads better in a sentence than a
 * stranger's id.
 */
export function machineDisplayNameOr(
  d: NameableMachine | null | undefined,
  fallback: string,
): string {
  return d ? machineDisplayName(d) : fallback;
}
