/**
 * Compact relative time for mobile rows.
 *
 * Phone rows have no space for a full timestamp, and every mobile list wants
 * the same shape ("now", "4m", "3h", "2d"). Lives here rather than inside one
 * screen because the chat list and the daemon list both render it — two
 * copies would drift the moment one of them grew a "just now" threshold.
 *
 * Takes epoch milliseconds so callers can feed it either an ISO string
 * (`Date.parse`) or a protobuf Timestamp (`timestampDate(ts).getTime()`)
 * without this module knowing about either representation.
 */
export function relativeTimeFromMs(ms: number | null | undefined): string {
  if (ms == null || Number.isNaN(ms)) return "";
  const seconds = Math.floor((Date.now() - ms) / 1000);
  // Clock skew between the daemon and this device can put a heartbeat a few
  // seconds in the future; "now" is a better answer than a negative age.
  if (seconds < 60) return "now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h`;
  if (seconds < 604800) return `${Math.floor(seconds / 86400)}d`;
  return `${Math.floor(seconds / 604800)}w`;
}

/** `relativeTimeFromMs` for an ISO-8601 string. */
export function relativeTime(iso?: string): string {
  if (!iso) return "";
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return "";
  return relativeTimeFromMs(then);
}
