// Copyright (c) 2025 Reliant Labs

/**
 * chatMarkers
 *
 * TypeScript mirror of `reliant/internal/chatmarkers/markers.go`. Centralizes
 * the cross-process "chat-error marker" contract used to ferry structured
 * signals from Go workflow / driver code to the chat-error UI across the
 * Temporal serialization boundary (which flattens typed errors to plain
 * strings).
 *
 * Wire format:
 *
 *   "<human-readable message> [<KIND>:<payload>]"
 *
 * - <KIND>    is one of the `ChatMarkerKind` literals below.
 * - <payload> is opaque to this module (URL, integer count, …).
 *
 * Adding a new marker:
 *   1. Add the literal to the `ChatMarkerKind` union AND to `CHAT_MARKER_KINDS`.
 *   2. Mirror it in `reliant/internal/chatmarkers/markers.go`.
 *   3. Extend the drift-guard tests on both sides.
 *
 * DO NOT diverge the kind strings, the regex, or the wrap format from the Go
 * mirror — both sides must agree byte-for-byte on the wire.
 */

/**
 * All chat marker kinds. Add new entries to BOTH this object and the
 * `ChatMarkerKind` union below.
 *
 * Exported as a `const` object (and a derived union) so consumers can switch
 * on `kind` with exhaustive narrowing and so callers can iterate the kinds
 * (e.g. in the drift-guard test).
 */
export const CHAT_MARKER_KINDS = {
  ReliantManagedQuotaExhausted: "RELIANT_MANAGED_QUOTA_EXHAUSTED",
  DaemonOfflineHalt: "RELIANT_DAEMON_OFFLINE_HALT",
} as const;

/**
 * Union of all valid marker kind strings on the wire.
 */
export type ChatMarkerKind =
  (typeof CHAT_MARKER_KINDS)[keyof typeof CHAT_MARKER_KINDS];

/**
 * Bracketed-tail matcher. Mirrors `markerRegex` in markers.go:
 *   - optional leading whitespace (so `Strip` removes the separator)
 *   - `[KIND:payload]`
 *   - KIND restricted to upper-case ASCII + digits + underscore
 *   - payload may be empty; payload cannot contain `]`
 *
 * Kept narrow so unrelated bracketed text in user prompts won't false-positive.
 */
const MARKER_REGEX = /\s*\[([A-Z0-9_]+):([^\]]*)\]/;

/**
 * Set of recognized kinds, for fast membership checks in `extractChatMarker`.
 * Keeps Extract from emitting "found-but-unknown" results on bracketed
 * SCREAMING_SNAKE_CASE text that happens to match the regex shape.
 */
const KNOWN_KINDS: ReadonlySet<string> = new Set(
  Object.values(CHAT_MARKER_KINDS),
);

/**
 * `extractChatMarker` scans `message` for the first known chat-marker tail
 * and returns the structured `{ kind, payload }` pair. Returns `null` when
 * no recognized marker is present.
 *
 * Mirrors `chatmarkers.Extract` in markers.go: single-marker semantics, first
 * match wins. Returns `null` rather than throwing — callers route on it.
 */
export function extractChatMarker(
  message: string,
): { kind: ChatMarkerKind; payload: string } | null {
  if (!message) return null;
  const match = MARKER_REGEX.exec(message);
  if (!match) return null;
  const kind = match[1];
  if (!KNOWN_KINDS.has(kind)) {
    // Bracketed text that matches the shape but isn't one of OUR kinds.
    // Treat as no marker so unrelated text doesn't get routed.
    return null;
  }
  return { kind: kind as ChatMarkerKind, payload: match[2] };
}

/**
 * `stripChatMarker` removes the first chat-marker tail (and any whitespace
 * immediately preceding it) from `message`, then trims the result. Returns
 * `message` unchanged (apart from `.trim()`) when no marker is present.
 *
 * Mirrors `chatmarkers.Strip` in markers.go. The cleanup is idempotent, so
 * it's safe to call on both live error events and snapshot replays.
 */
export function stripChatMarker(message: string): string {
  if (!message) return "";
  return message.replace(MARKER_REGEX, "").trim();
}
