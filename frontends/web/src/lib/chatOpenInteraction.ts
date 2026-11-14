/**
 * A chat open as one `chat.open` interaction span: from the click
 * (chatStore.selectChat) to the frame after the timeline stopped catching up
 * (ChatPresenter). The RPCs made while it is open, the StreamChatUpdates
 * fetch among them, become its children, and through their traceparent so do
 * the server's chat_stream.* / chat_snapshot.* spans.
 *
 * Phases are span events marked from the chat stream's hooks
 * (api/chat-streaming-grpc.ts): `subscribe`, then `snapshot_received` or
 * `replay_started`, then `caught_up`. The span ends with
 *
 *   interaction.outcome  ok | error | superseded (another open, or the chat
 *                        was left) | timeout
 *   chat.sync            snapshot | replay | none (the stream was already live)
 *   chat.overlay_shown   whether the catch-up overlay came up
 *   chat.tool_bodies     stub | full (lib/toolBodies.ts), to compare open times
 *
 * One open at a time, keyed by its chat: nothing about another chat ever
 * marks or ends it. Best-effort throughout: the runtime never throws, and
 * with telemetry off it records nothing.
 */

import { useEffect, useSyncExternalStore } from "react";
import {
  interaction,
  useInteractionEnd,
  type Interaction,
  type InteractionAttributes,
} from "@reliantlabs/forge-web-runtime";
import type { ChatSyncKind } from "@reliantlabs/sdk/client";
import { TRANSCRIPT_TOOL_BODIES } from "./toolBodies";

export const CHAT_OPEN_INTERACTION = "chat.open";

type ChatOpenSync = ChatSyncKind | "none";

interface ChatOpen {
  chatId: string;
  op: Interaction;
  /**
   * selectChat has finished: the stream subscription it asserts is in place,
   * so the timeline's readiness now speaks for this open. Until then a render
   * of the new chat may predate its sync-pending signal.
   */
  armed: boolean;
}

let current: ChatOpen | null = null;
const listeners = new Set<() => void>();

function publish(next: ChatOpen | null): void {
  current = next;
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function openFor(chatId: string | null | undefined): Interaction | undefined {
  if (!chatId || current?.chatId !== chatId || current.op.ended) return undefined;
  return current.op;
}

/**
 * Start the open of `chatId` (the user's intent). Ends whatever open was in
 * flight as superseded: the runtime holds one foreground interaction.
 */
export function beginChatOpen(
  chatId: string,
  attributes: InteractionAttributes = {},
): Interaction {
  const op = interaction(CHAT_OPEN_INTERACTION, {
    "chat.id": chatId,
    "chat.sync": "none",
    "chat.overlay_shown": false,
    // stub | full: whether the transcript opened without finished tool bodies.
    "chat.tool_bodies": TRANSCRIPT_TOOL_BODIES,
    ...attributes,
  });
  publish({ chatId, op, armed: false });
  return op;
}

/** selectChat is done setting the open up; see ChatOpen.armed. */
export function armChatOpen(chatId: string): void {
  if (!current || current.chatId !== chatId || current.armed) return;
  publish({ ...current, armed: true });
}

/** A phase of the open of `chatId`, if that open is still in flight. */
export function markChatOpen(
  chatId: string,
  phase: string,
  attributes?: InteractionAttributes,
): void {
  openFor(chatId)?.mark(phase, attributes);
}

/** How the stream brought `chatId` up to date. */
export function noteChatOpenSync(chatId: string, sync: ChatOpenSync): void {
  openFor(chatId)?.setAttributes({ "chat.sync": sync });
}

/** The open of `chatId` failed: the stream errored or refused the chat. */
export function failChatOpen(chatId: string | null | undefined, reason: string): void {
  openFor(chatId)?.end({ outcome: "error", "chat.open.error": reason });
}

/**
 * The user left `chatId` for no other chat (the new-chat view): its open
 * ends as superseded, as it would have had they opened another one.
 */
export function leaveChatOpen(chatId: string): void {
  openFor(chatId)?.end({ outcome: "superseded" });
}

/** The open in flight for `chatId` once selectChat armed it, reactively. */
export function useChatOpen(chatId: string | null | undefined): Interaction | undefined {
  const open = useSyncExternalStore(subscribe, () => current);
  if (!chatId || !open || open.chatId !== chatId || !open.armed) return undefined;
  return open.op;
}

export interface ChatOpenReadiness {
  /** The timeline is still catching up (lib/timelineCatchUp). */
  catchingUp: boolean;
  /** The catch-up overlay is on screen. */
  overlayVisible: boolean;
  /**
   * The chat's sync never completed and cannot right now: the stream is
   * neither connecting nor connected, so the timeline stopped waiting on it.
   */
  syncAbandoned: boolean;
}

/**
 * End the open of `chatId` after the paint of the first commit in which its
 * timeline is usable: caught up, and the overlay (if it came up) lifted.
 */
export function useChatOpenEnd(
  chatId: string | null | undefined,
  { catchingUp, overlayVisible, syncAbandoned }: ChatOpenReadiness,
): void {
  const op = useChatOpen(chatId);
  useEffect(() => {
    if (op && overlayVisible) op.setAttributes({ "chat.overlay_shown": true });
  }, [op, overlayVisible]);
  useInteractionEnd(
    op,
    !catchingUp && !overlayVisible,
    syncAbandoned ? { outcome: "error", "chat.open.error": "disconnected" } : { outcome: "ok" },
  );
}

/** Forget the open in flight. For tests. */
export function resetChatOpen(): void {
  current?.op.end({ outcome: "reset" });
  publish(null);
}
