/**
 * When the timeline in view — a chat's main thread, or one of its side
 * (spawn) threads — is "catching up", and covers what it shows with a loading
 * overlay. Keyed by the viewed (chat, thread), from two signals
 * (store/chatStoreHooks.useTimelineSync):
 *
 *  - The chat's: opening a chat renders whatever is cached (possibly
 *    nothing), then the stream delivers the chat's state — a snapshot, or a
 *    replay from the cached cursor — and ends that initial sync with the
 *    server's chat_caught_up (chatStore.chatSyncPendingId, set when a
 *    subscription begins and cleared by chat_caught_up).
 *  - The side thread's: a spawn thread has no subscription of its own; its
 *    history is a thread-scoped read, and the chat's stream carries its live
 *    messages into that read's cache. Until the read lands, the timeline is
 *    still showing the previous thread.
 *
 * Nothing that happens once caught up reopens either: live tokens and tool
 * events arrive after chat_caught_up and only upsert into an already-loaded
 * thread cache, and a reconnect of a caught-up subscription (a network blip,
 * a gap resync) replays only what was missed onto content that is already
 * current, so it shows no overlay.
 */

import type { ConnectionStatus } from "../types/streaming";
import { isLocalOnlyMessageId } from "./pendingSends";

export interface TimelineCatchUpInput {
  /** The stream has not sent chat_caught_up for this chat's subscription. */
  chatSyncPending: boolean;
  /** The viewed side thread's own history read has not answered yet. */
  threadHistoryPending: boolean;
  /** The transcript as rendered: cached plus streaming. */
  messages: readonly { id: string }[];
  connectionStatus: ConnectionStatus | string;
}

export function isTimelineCatchingUp({
  chatSyncPending,
  threadHistoryPending,
  messages,
  connectionStatus,
}: TimelineCatchUpInput): boolean {
  if (!chatSyncPending && !threadHistoryPending) return false;
  // Only while the stream is working on it. A dead connection cannot catch
  // up, and an overlay that waited for it would lock the reader out of the
  // cached transcript for as long as they are offline; the connection status
  // says what is wrong instead.
  if (connectionStatus !== "connecting" && connectionStatus !== "connected") {
    return false;
  }
  // Whatever is on screen while a side thread loads is another thread's.
  if (threadHistoryPending) return true;
  // A chat whose transcript is only this client's own sends (one just started
  // from the composer) is already showing everything there is.
  if (messages.length > 0 && messages.every((m) => isLocalOnlyMessageId(m.id))) {
    return false;
  }
  return true;
}

/** Catch-up shorter than this shows nothing: a cached switch is instant. */
export const CATCH_UP_SHOW_DELAY_MS = 180;
/** Once shown, the overlay stays at least this long, so it never flickers. */
export const CATCH_UP_MIN_VISIBLE_MS = 300;
