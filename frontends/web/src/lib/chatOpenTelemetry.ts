/**
 * The chat open's browser half, as one RUM event: `chat.snapshot_applied`.
 *
 * The server's StreamChatUpdates spans say when the snapshot left; nothing
 * said when the transcript was on screen, so a slow open could not be split
 * into transfer, decode and render (the 2026-11-04 chat-open breakdown, rows
 * 8-9). The event carries the server trace id from the stream's
 * `traceresponse` header, so it joins that open's server spans, and these
 * offsets from the moment the stream was opened:
 *
 *   received_ms  the snapshot frame arrived and was decoded
 *   applied_ms   the store holds it
 *   visible_ms   the frame after that commit was painted (two animation
 *                frames); left out when the page is hidden, where no frame
 *                paints
 *
 * Sent through the web runtime's `track()` whenever the build ships browser
 * telemetry, like the runtime's own fetch and long-task spans. It carries ids
 * and timings only. Best-effort: telemetry must never break the stream.
 */

import { track } from "@reliantlabs/forge-web-runtime";
import { clickstackEnabled } from "./telemetryEndpoint";

export const CHAT_SNAPSHOT_APPLIED_EVENT = "chat.snapshot_applied";

/** The trace id of a W3C `traceresponse` (`00-<trace>-<span>-<flags>`), or "". */
export function traceIdFromTraceresponse(header: string | null): string {
  const parts = (header ?? "").trim().split("-");
  if (parts.length !== 4 || !/^[0-9a-f]{32}$/.test(parts[1])) return "";
  return parts[1];
}

export interface ChatSnapshotApplied {
  chatId: string;
  traceId: string;
  openedAtMs: number;
  receivedAtMs: number;
  appliedAtMs: number;
  /** The first sync of a chat the user just opened, not a resync of an open one. */
  initialOpen: boolean;
  messages: number;
  otherUpdates: number;
}

export function chatSnapshotAppliedProps(
  s: ChatSnapshotApplied,
  visibleAtMs?: number,
): Record<string, string | number | boolean> {
  const props: Record<string, string | number | boolean> = {
    chat_id: s.chatId,
    trace_id: s.traceId,
    initial_open: s.initialOpen,
    received_ms: Math.round(s.receivedAtMs - s.openedAtMs),
    applied_ms: Math.round(s.appliedAtMs - s.openedAtMs),
    messages: s.messages,
    other_updates: s.otherUpdates,
  };
  if (visibleAtMs !== undefined) props.visible_ms = Math.round(visibleAtMs - s.openedAtMs);
  return props;
}

export function reportChatSnapshotApplied(s: ChatSnapshotApplied): void {
  try {
    if (!clickstackEnabled() || s.openedAtMs <= 0) return;
    const send = (visibleAtMs?: number) => {
      try {
        track(CHAT_SNAPSHOT_APPLIED_EVENT, chatSnapshotAppliedProps(s, visibleAtMs));
      } catch {
        // best-effort
      }
    };
    if (typeof document === "undefined" || document.visibilityState !== "visible" || typeof requestAnimationFrame !== "function") {
      send();
      return;
    }
    requestAnimationFrame(() => requestAnimationFrame(() => send(performance.now())));
  } catch {
    // best-effort
  }
}
