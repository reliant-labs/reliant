/**
 * Daemon type literals.
 *
 * These string values are the authoritative `daemon_type` set by the Go
 * daemon registry at registration time (see daemon_registry.proto). They
 * MUST stay in sync with the backend — do not change them here.
 *
 * Use these constants and helpers instead of literal strings so call sites
 * are easy to audit and refactor.
 */

export const DAEMON_TYPE_MANAGED = "managed" as const;
export const DAEMON_TYPE_SELF_HOSTED = "self_hosted" as const;

export type DaemonTypeLiteral =
  | typeof DAEMON_TYPE_MANAGED
  | typeof DAEMON_TYPE_SELF_HOSTED;

export function isManagedDaemon(
  daemonType: string | undefined | null,
): boolean {
  return daemonType === DAEMON_TYPE_MANAGED;
}

export function isSelfHostedDaemon(
  daemonType: string | undefined | null,
): boolean {
  return daemonType === DAEMON_TYPE_SELF_HOSTED;
}
