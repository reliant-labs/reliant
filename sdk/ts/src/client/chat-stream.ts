// One chat's detail stream (StreamChatUpdates): its snapshot or replay, the
// caught_up marker, then live messages, approvals, tool calls and streaming
// deltas. One connection at a time, for the chat on screen.
//
// It is separate from the user-wide stream (user-stream.ts) on purpose: a
// chat switch cancels and reopens only this stream, so it never pays for a new
// user stream, its global replay, or the user-level events that would stall
// behind it. The two reconnect independently, and a chat this stream cannot
// serve never takes the user stream down.

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  StreamChatUpdatesRequestSchema,
  type ChatStreamEvent,
  type ChatUpdateData,
} from "#gen/reliant/v1/streaming_pb.js";
import { decodeChatUpdate, decodeUserUpdate, latestMainThreadContextUsage } from "./decode.js";
import type {
  ChatUpdate,
  ContextUsageInfo,
  MessagePaginationInfo,
  ProtoMessageUpdate,
  UserUpdate,
} from "./events.js";
import { ReconnectingStream, type StreamEnvironment } from "./reconnecting-stream.js";
import type { StreamingClient } from "./user-stream.js";

// Log prefix for filtering/debugging
const LOG_PREFIX_STREAM = "[📡 ChatStream]";

// Chat gap-resync pacing. Chat gaps are exact (per-chat sequences are
// contiguous and never filtered), so the interval only guards against
// reconnect loops.
const CHAT_GAP_RESYNC_MIN_INTERVAL_MS = 1_000;

/**
 * The server refused the chat: StreamChatUpdates answers NotFound for a chat
 * that no longer exists or belongs to someone else (both deliberately
 * indistinguishable), and for nothing else during setup.
 */
function isChatRefused(error: unknown): boolean {
  return error instanceof ConnectError && error.code === Code.NotFound;
}

/** The trace id of a W3C `traceresponse` (`00-<trace>-<span>-<flags>`), or "". */
export function traceIdFromTraceresponse(header: string | null): string {
  const parts = (header ?? "").trim().split("-");
  if (parts.length !== 4 || !/^[0-9a-f]{32}$/.test(parts[1] ?? "")) return "";
  return parts[1] ?? "";
}

// Callbacks of the per-chat stream. Every chat event names the chat it
// belongs to: the stream that delivered it, never a flag that may have moved
// on since.
export interface ChatStreamCallbacks {
  onChatUpdate: (chatId: string, updates: ChatUpdate[]) => void;
  onChatSnapshot: (chatId: string, updates: ChatUpdate[]) => void;
  onChatPaginationInfo?: (chatId: string, pagination: MessagePaginationInfo) => void;
  onChatContextUsage?: (chatId: string, contextUsage: ContextUsageInfo) => void;
  // User-level updates about the open chat (or a chat it started) that the
  // project-scoped user stream does not deliver because they belong to another
  // project. Live only: they never move the user stream's cursor.
  onUserUpdates?: (updates: UserUpdate[]) => void;
  onStatusChange?: (status: import("./events.js").ConnectionStatus) => void;
  onError?: (error: string) => void;
  // Per-chat resume. The stream is leaving `chatId`, whose cached state is
  // current as of chat update `sequence` (0n: no state it can vouch for).
  onChatCursorRelease?: (chatId: string, sequence: bigint) => void;
  // The stream is entering `chatId`: the sequence to resume from, or 0n to
  // request a full snapshot. Only a caller whose cached state for the chat is
  // exactly the server's state as of that sequence may return non-zero.
  resolveChatResumeSequence?: (chatId: string) => bigint;
  // The chat whose initial sync is still outstanding for the current
  // subscription (until the server's caught_up), or null.
  onChatSyncPending?: (chatId: string | null) => void;
  // The server refused to stream `chatId` (gone, or not this user's). The
  // service has already dropped it and gone idle; the user stream is untouched.
  onChatSubscriptionRejected?: (chatId: string) => void;
}

/**
 * What a snapshot cost to open, for hosts that report it. Timings are `performance.now()`.
 *
 * @deprecated The offsets are measured from the connection's open, not from
 * the user's intent, and only a snapshot reports them: a replay never does.
 * Time a chat open on the host's own clock from `onSubscribe`,
 * `onSnapshotReceived`, `onReplayStarted` and `onCaughtUp` instead. Kept, and
 * still reported, for existing consumers.
 */
export interface SnapshotTiming {
  chatId: string;
  traceId: string;
  openedAtMs: number;
  receivedAtMs: number;
  appliedAtMs: number;
  /** The first sync of a chat just opened, not a resync of an open one. */
  initialOpen: boolean;
  messages: number;
  otherUpdates: number;
}

/** How a connection brought the chat's state up to date before its caught_up. */
export type ChatSyncKind = "snapshot" | "replay";

/** A connection's caught_up: the chat's state is now the server's. */
export interface ChatCaughtUpInfo {
  chatId: string;
  /**
   * "snapshot" when the server sent one on this connection, otherwise
   * "replay" (from the connection's cursor; possibly a replay of nothing).
   */
  sync: ChatSyncKind;
  /** The first sync of a chat just subscribed to, not a resync of an open one. */
  initialSync: boolean;
  /** Persisted updates the connection delivered before caught_up. */
  replayedUpdates: number;
  latestSequence: number;
}

/** Observation points for hosts that profile the stream. All optional, all best-effort. */
export interface ChatStreamHooks {
  onSubscribe?: (chatId: string) => void;
  onSnapshotReceived?: (info: { chatId: string; messages: number; otherUpdates: number }) => void;
  /**
   * The server answered the connection's cursor with a replay: its first
   * update batch before caught_up, when no snapshot came first. Once per
   * connection. A replay of nothing sends no batch, so it reports only
   * `onCaughtUp`.
   */
  onReplayStarted?: (info: { chatId: string; sinceSequence: number; updates: number }) => void;
  /** The server's caught_up for the connection. Once per connection. */
  onCaughtUp?: (info: ChatCaughtUpInfo) => void;
  /**
   * @deprecated See {@link SnapshotTiming}: prefer `onSnapshotReceived` and
   * `onCaughtUp`. Still called after every snapshot.
   */
  onSnapshotApplied?: (timing: SnapshotTiming) => void;
}

export interface ChatStreamOptions extends StreamEnvironment {
  callbacks: ChatStreamCallbacks;
  /** Resolved on every connection attempt, so a client may follow a changing base URL. */
  client: () => StreamingClient;
  /** The project the user stream is scoped to. */
  projectId?: string;
  hooks?: ChatStreamHooks;
}

export class ChatStream extends ReconnectingStream<ChatStreamEvent> {
  private callbacks: ChatStreamCallbacks;
  private readonly client: () => StreamingClient;
  private readonly hooks: ChatStreamHooks;
  private chatId: string | undefined = undefined;
  private lastChatSequence: bigint = 0n;
  // The project the user stream is scoped to. The server forwards, on this
  // stream, the chat's user-level updates that scope filters out.
  private projectId: string | undefined = undefined;
  private lastChatGapResyncAt = 0;
  // The current connection's open time (performance.now) and the server
  // trace it is part of, from its traceresponse header: what the deprecated
  // onSnapshotApplied hook reports.
  private connectionOpenedAtMs = 0;
  private connectionTraceId = "";
  // How the current connection is bringing the chat up to date, for the
  // onReplayStarted / onCaughtUp hooks: null until its first snapshot or
  // update batch, and the persisted updates it delivered before caught_up.
  private connectionSync: ChatSyncKind | null = null;
  private connectionCaughtUp = false;
  private connectionReplayedUpdates = 0;
  // The chat whose initial sync has not arrived yet for the current
  // subscription, or null. Set when a subscription to a chat begins; cleared
  // by settleChatSync when the server sends caught_up for it. Survives
  // reconnects in between, but a reconnect of an already-synced subscription
  // (gap resync, network blip) does not re-open it.
  private chatSyncPendingFor: string | null = null;
  // Chats the server refused to stream (NotFound: deleted, or not this
  // user's). Remembered so the self-healing reconcile, which re-asserts the
  // rendered chat on every connection change, cannot turn one refusal into a
  // reconnect loop. Per service instance: a fresh service tries once more.
  private rejectedChatIds = new Set<string>();

  constructor(options: ChatStreamOptions) {
    super(
      LOG_PREFIX_STREAM,
      {
        onStatusChange: (status) => options.callbacks.onStatusChange?.(status),
        onError: (error) => options.callbacks.onError?.(error),
      },
      options,
    );
    this.callbacks = options.callbacks;
    this.client = options.client;
    this.hooks = options.hooks ?? {};
    this.projectId = options.projectId;
    // Idle until the first subscribeToChat: there is nothing to stream yet.
    this.isIntentionallyClosed = true;
  }

  /**
   * Stream a chat's detail events.
   *
   * Callers (`reconcileChatSubscription`'s self-healing invariant, in
   * particular) invoke this on every render of the active chat, and cannot
   * tell "genuinely need to (re)subscribe" from "already subscribed, just
   * confirming" without asking this service — so this method has to make
   * that distinction itself, explicitly, rather than always reconnecting:
   *
   *  - Different chatId (a real chat switch): always reconnect. The chat
   *    being left hands its cursor back via `onChatCursorRelease`, and the
   *    chat being entered resumes from `resolveChatResumeSequence` — the
   *    owner of its cached state answers with the sequence that state is
   *    current as of, or 0 when it holds none it can vouch for. A cursor
   *    asks the server for a replay of (cursor, latest]; 0 asks for a
   *    snapshot. The server may answer a cursor with a snapshot instead
   *    (gap too large, cursor ahead of the chat), which replaces state as
   *    it always has, so either reply is handled.
   *  - Same chatId, already connected to it: true no-op. Nothing changed.
   *  - Same chatId, but not yet confirmed connected (a connection attempt
   *    for this exact chat is already in flight — e.g. triggered a moment
   *    ago by a gap-resync): also a no-op. Firing a second reconnect here
   *    would supersede the in-flight one before it completes (wasted round
   *    trip, a logged stream error) AND would zero `lastChatSequence` and
   *    force a full snapshot even though the in-flight connection was about
   *    to deliver an incremental replay.
   *  - Same chatId, genuinely disconnected (no attempt in flight): reconnect
   *    IS needed to restore the subscription, but the cursor is preserved —
   *    it's the same chat, so the server can still replay
   *    (since_seq, latest] instead of sending a snapshot.
   *
   * Only this stream reconnects; the user stream is never touched.
   */
  subscribeToChat(chatId: string): void {
    if (this.rejectedChatIds.has(chatId)) {
      this.log.debug("Not subscribing to a chat the server refused",
        { chatId: chatId.slice(0, 8) },
      );
      return;
    }
    if (this.chatId === chatId) {
      if (this.isConnected_ || this.connectAttemptInFlight) {
        this.log.debug("Already subscribed (or subscribing) to this chat — ignoring redundant (re)assert",
          { chatId: chatId.slice(0, 8), connected: this.isConnected_ },
        );
        return;
      }
      // Same chat, but disconnected with nothing in flight — reconnect to
      // restore the subscription, preserving the cursor for a replay.
      this.log.info("Reasserting chat subscription after disconnect",
        { chatId: chatId.slice(0, 8) },
      );
      this.reconnect();
      return;
    }

    this.log.info("Subscribing to chat details", {
      chatId: chatId.slice(0, 8),
      previousChatId: this.chatId?.slice(0, 8),
    });

    this.hooks.onSubscribe?.(chatId);

    this.releaseChatCursor();
    const resumeFrom = this.callbacks.resolveChatResumeSequence?.(chatId) ?? 0n;
    this.chatId = chatId;
    this.lastChatSequence = resumeFrom > 0n ? resumeFrom : 0n;
    this.setChatSyncPendingFor(chatId);
    this.resetConnectionSync();
    if (this.lastChatSequence > 0n) {
      this.log.info("Resuming chat from cached cursor", {
        chatId: chatId.slice(0, 8),
        chatSinceSeq: Number(this.lastChatSequence),
      });
    }

    this.reconnect();
  }

  /**
   * Stop streaming the current chat. The connection goes idle until the next
   * subscribeToChat.
   */
  unsubscribe(): void {
    if (!this.chatId) return;

    this.log.info("Unsubscribing from chat details", {
      chatId: this.chatId.slice(0, 8),
    });

    this.releaseChatCursor();
    this.chatId = undefined;
    this.lastChatSequence = 0n;
    this.setChatSyncPendingFor(null);
    this.close(false);
  }

  /**
   * The user stream's project scope changed. The server picks the chat's
   * cross-project user updates by it, so an open chat stream reconnects —
   * from its cursor, a replay of nothing when the chat did not change.
   */
  setProjectScope(projectId: string | undefined): void {
    if (this.projectId === projectId) return;
    this.projectId = projectId;
    if (this.chatId && !this.isIntentionallyClosed) {
      this.reconnectNow("resubscribe");
    }
  }

  /** Tear the stream down for good (the shell disconnected). */
  stop(): void {
    this.log.info("Stopping");
    this.releaseChatCursor();
    this.setChatSyncPendingFor(null);
    this.close(true);
  }

  getChatId(): string | undefined {
    return this.chatId;
  }

  getLastChatSequence(): number {
    return Number(this.lastChatSequence);
  }

  /** (Re)open the stream for the current chat, keeping its cursor. */
  private reconnect(): void {
    if (this.isIntentionallyClosed) {
      this.open();
      return;
    }
    this.reconnectNow("resubscribe");
  }

  /**
   * Hand the cursor of the chat being left back to whoever owns that chat's
   * cached state. Every exit from a chat — switch, unsubscribe, stop — goes
   * through here, so the owner always holds the cursor matching its state.
   */
  private releaseChatCursor(): void {
    if (!this.chatId) return;
    this.callbacks.onChatCursorRelease?.(this.chatId, this.lastChatSequence);
  }

  /**
   * Forget a chat the server will not stream to this user. No cursor is
   * handed back: there is no state left on the server for one to describe.
   */
  private dropRefusedChat(chatId: string): void {
    this.log.warn("Server refused the chat subscription — continuing without it",
      { chatId: chatId.slice(0, 8) },
    );
    this.rejectedChatIds.add(chatId);
    this.chatId = undefined;
    this.lastChatSequence = 0n;
    this.setChatSyncPendingFor(null);
    this.close(false);
    this.callbacks.onChatSubscriptionRejected?.(chatId);
  }

  private resetConnectionSync(): void {
    this.connectionSync = null;
    this.connectionCaughtUp = false;
    this.connectionReplayedUpdates = 0;
  }

  private setChatSyncPendingFor(chatId: string | null): void {
    if (this.chatSyncPendingFor === chatId) return;
    this.chatSyncPendingFor = chatId;
    this.callbacks.onChatSyncPending?.(chatId);
  }

  /**
   * The current subscription's initial sync is complete. Called only from
   * the caught_up case: the server sends that exactly once per stream, after
   * the snapshot or the replay — including a replay of nothing, which sends
   * no chat frame at all. No other frame can stand in for it: a snapshot may
   * still be followed by nothing else, but a replay batch's latest_sequence
   * is that batch's own max, so "reaching" it says nothing about whether more
   * batches follow; and a heartbeat proves only that the socket is alive.
   */
  private settleChatSync(latestSequence: bigint): void {
    if (this.chatSyncPendingFor === null) return;
    this.log.info("Chat sync complete", {
      chatId: this.chatSyncPendingFor.slice(0, 8),
      serverSeq: Number(latestSequence),
      chatSeq: Number(this.lastChatSequence),
    });
    this.setChatSyncPendingFor(null);
  }

  /**
   * A persisted chat update was missed upstream (sequence gap). Reconnect
   * preserving the cursor; the server replays (since_seq, latest] from the
   * DB, restoring the missed updates in order.
   */
  private resyncAfterGap(gapSeq: bigint): void {
    const now = Date.now();
    if (now - this.lastChatGapResyncAt < CHAT_GAP_RESYNC_MIN_INTERVAL_MS) {
      // A resync is already in flight; its replay covers this gap too.
      return;
    }
    this.lastChatGapResyncAt = now;

    this.log.warn("Chat update sequence gap detected — resyncing from last contiguous sequence",
      {
        chatId: this.chatId?.slice(0, 8),
        lastContiguous: Number(this.lastChatSequence),
        gapSeq: Number(gapSeq),
      },
    );
    this.reconnectNow("resubscribe");
  }

  protected openStream(signal: AbortSignal): AsyncIterable<ChatStreamEvent> {
    const client = this.client();
    const request = create(StreamChatUpdatesRequestSchema, {
      chatId: this.chatId ?? "",
      sinceSeq: this.lastChatSequence,
      projectId: this.projectId,
    });
    this.log.info("Connecting to chat stream", {
      chatId: this.chatId?.slice(0, 8),
      chatSinceSeq: Number(this.lastChatSequence),
    });
    this.connectionOpenedAtMs = performance.now();
    this.connectionTraceId = "";
    this.resetConnectionSync();
    return client.streamChatUpdates(request, {
      signal,
      onHeader: (headers) => {
        this.connectionTraceId = traceIdFromTraceresponse(headers.get("traceresponse"));
      },
    });
  }

  protected describe(): Record<string, unknown> {
    return {
      chatId: this.chatId?.slice(0, 8),
      chatFromSeq: Number(this.lastChatSequence),
    };
  }

  /**
   * The server refuses a chat that is gone (deleted, possibly from another
   * device) or is not this user's. Every retry would carry the same chat and
   * be refused the same way, so drop it and go idle instead of backing off
   * against it forever.
   */
  protected handleConnectFailure(error: unknown, delivered: boolean): boolean {
    if (!delivered && this.chatId && isChatRefused(error)) {
      this.dropRefusedChat(this.chatId);
      return true;
    }
    return false;
  }

  protected handleEvent(event: ChatStreamEvent): void {
    const chatId = this.chatId;
    if (!chatId) return;

    switch (event.event.case) {
      case "snapshot": {
        const snapshot = event.event.value;
        const receivedAtMs = performance.now();
        // Read before any callback runs, so whatever they settle cannot
        // change how this snapshot is reported.
        const initialOpen = this.chatSyncPendingFor === chatId;
        if (!this.connectionCaughtUp) this.connectionSync = "snapshot";
        this.hooks.onSnapshotReceived?.({
          chatId,
          messages: snapshot.messages.length,
          otherUpdates: snapshot.otherUpdates.length,
        });
        this.log.info("Received chat snapshot", {
          chatId: chatId.slice(0, 8),
          messageCount: snapshot.messages.length,
          otherUpdates: snapshot.otherUpdates.length,
          latestSequence: Number(snapshot.latestSequence),
        });

        this.lastChatSequence = snapshot.latestSequence;

        // Wrap proto Messages with update_type discriminator
        const messageUpdates: ChatUpdate[] = snapshot.messages.map(
          (msg): ProtoMessageUpdate => ({
            update_type: "message",
            message: msg,
          }),
        );

        // Convert other updates (approvals, threads, etc.)
        const otherUpdates: ChatUpdate[] = snapshot.otherUpdates
          .map((u) => decodeChatUpdate(u, this.log))
          .filter((u): u is ChatUpdate => u !== null);

        // A snapshot replaces the chat's state (it prevents cross-chat
        // message leaking and drops what the server no longer has).
        this.callbacks.onChatSnapshot(chatId, [...messageUpdates, ...otherUpdates]);

        this.callbacks.onChatPaginationInfo?.(chatId, {
          total: snapshot.total,
          hasMore: snapshot.hasMore,
          oldestSeq: Number(snapshot.oldestSeq),
        });

        this.callbacks.onChatContextUsage?.(chatId, {
          threadTokenCount: Number(snapshot.threadTokenCount),
          compactionThreshold: Number(snapshot.compactionThreshold),
        });

        // Sampled after onChatSnapshot returns: it commits the snapshot to
        // the chat store synchronously, so the store holds it by now, and
        // the paint measurement starts from this point.
        this.hooks.onSnapshotApplied?.({
          chatId,
          traceId: this.connectionTraceId,
          openedAtMs: this.connectionOpenedAtMs,
          receivedAtMs,
          appliedAtMs: performance.now(),
          initialOpen,
          messages: snapshot.messages.length,
          otherUpdates: snapshot.otherUpdates.length,
        });
        break;
      }

      case "updates": {
        const batch = event.event.value;

        // Before caught_up, persisted updates are the connection's replay.
        if (!this.connectionCaughtUp) {
          const persisted = batch.updates.filter((u) => u.sequenceNumber !== 0n).length;
          if (persisted > 0 && this.connectionSync === null) {
            this.connectionSync = "replay";
            this.hooks.onReplayStarted?.({
              chatId,
              sinceSequence: Number(this.lastChatSequence),
              updates: persisted,
            });
          }
        }

        // Persisted chat updates carry contiguous per-chat sequence numbers
        // (ephemeral streaming deltas carry 0). A jump past cursor+1 means an
        // upstream drop (e.g. NATS hub slow consumer): apply the contiguous
        // prefix, drop the rest, and resync — the reconnect resumes from
        // lastChatSequence and the server replays the missed range from the
        // DB in order. batch.latestSequence is deliberately NOT trusted for
        // the cursor: it echoes server state that can be ahead of what this
        // client actually received.
        const accepted: ChatUpdateData[] = [];
        let gapAt: bigint | null = null;
        for (const update of batch.updates) {
          const seq = update.sequenceNumber;
          if (seq === 0n) {
            accepted.push(update); // ephemeral delta — no ordering contract
            continue;
          }
          if (this.lastChatSequence > 0n && seq > this.lastChatSequence + 1n) {
            gapAt = seq;
            break;
          }
          if (seq > this.lastChatSequence) {
            this.lastChatSequence = seq;
          }
          accepted.push(update);
        }

        if (!this.connectionCaughtUp) {
          this.connectionReplayedUpdates += accepted.filter((u) => u.sequenceNumber !== 0n).length;
        }

        const updates: ChatUpdate[] = accepted
          .map((u) => decodeChatUpdate(u, this.log))
          .filter((u): u is ChatUpdate => u !== null);

        if (updates.length > 0) {
          this.callbacks.onChatUpdate(chatId, updates);
        }

        // A replay resume gets no snapshot, so the compaction indicator would
        // keep showing the usage from when the chat was last open. Every
        // persisted main-thread message carries the current usage; the
        // latest one accepted here is what a snapshot would report now.
        const contextUsage = latestMainThreadContextUsage(accepted, chatId);
        if (contextUsage) {
          this.callbacks.onChatContextUsage?.(chatId, contextUsage);
        }

        if (gapAt !== null) {
          this.resyncAfterGap(gapAt);
        }
        break;
      }

      case "caughtUp": {
        // The initial chat sync for this stream is over — the only signal
        // that clears the syncing indicator. See settleChatSync.
        const initialSync = this.chatSyncPendingFor === chatId;
        this.settleChatSync(event.event.value.latestSequence);
        if (this.connectionCaughtUp) break;
        this.connectionCaughtUp = true;
        this.hooks.onCaughtUp?.({
          chatId,
          sync: this.connectionSync ?? "replay",
          initialSync,
          replayedUpdates: this.connectionReplayedUpdates,
          latestSequence: Number(event.event.value.latestSequence),
        });
        break;
      }

      case "userUpdates": {
        const updates = event.event.value.updates.map((u) => decodeUserUpdate(u, this.log));
        if (updates.length > 0) {
          this.callbacks.onUserUpdates?.(updates);
        }
        break;
      }

      case "heartbeat": {
        // Liveness only (lastEventAt is bumped for every event).
        break;
      }

      default:
        this.log.warn("Unknown event type", {
          case: event.event.case,
        });
    }
  }
}
