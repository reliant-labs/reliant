// The user-wide update stream (StreamUserUpdates): chat list changes, activity,
// processes, daemon signals and every other user-level event. Per-chat detail
// has its own stream, one per open chat (chat-stream.ts), so switching chats
// never reconnects this one.

import { create } from "@bufbuild/protobuf";
import type { Client } from "@connectrpc/connect";
import {
  StreamUserUpdatesRequestSchema,
  type StreamingService,
  type UserStreamEvent,
} from "#gen/reliant/v1/streaming_pb.js";
import { decodeUserUpdate } from "./decode.js";
import type { ConnectionStatus, UserUpdate } from "./events.js";
import { ReconnectingStream, type StreamEnvironment } from "./reconnecting-stream.js";

/** The StreamingService client a stream opens its connection with. */
export type StreamingClient = Client<typeof StreamingService>;

// Log prefix for filtering/debugging
const LOG_PREFIX_STREAM = "[📡 UserStream]";

/** Callbacks of the user-wide stream. */
export interface UserStreamCallbacks {
  onUpdate: (updates: UserUpdate[]) => void;
  onError: (error: string) => void;
  onStatusChange: (status: ConnectionStatus) => void;
  onSync: (lastSequence: number) => void;
}

export interface UserStreamOptions extends StreamEnvironment {
  callbacks: UserStreamCallbacks;
  /** Resolved on every connection attempt, so a client may follow a changing base URL. */
  client: () => StreamingClient;
}

export class UserStream extends ReconnectingStream<UserStreamEvent> {
  private callbacks: UserStreamCallbacks;
  private readonly client: () => StreamingClient;
  private projectId: string | undefined = undefined;
  private lastSequence: bigint = 0n;

  constructor(options: UserStreamOptions) {
    super(
      LOG_PREFIX_STREAM,
      {
        onStatusChange: (status) => options.callbacks.onStatusChange(status),
        onError: (error) => options.callbacks.onError(error),
      },
      options,
    );
    this.callbacks = options.callbacks;
    this.client = options.client;
  }

  /**
   * Start the user stream.
   * @param fromSeq User-level sequence to resume from
   * @param projectId Scope updates to this project (omit for all projects)
   */
  start(fromSeq: number = 0, projectId?: string): void {
    // Guard on work actually in flight, NOT on the AbortController. A failed
    // attempt leaves behind a non-aborted controller, so the old check made a
    // dead service permanently un-restartable — start() would no-op forever.
    if (
      this.isConnected_ ||
      this.connectAttemptInFlight ||
      this.reconnectTimer !== null
    ) {
      this.log.warn("Already connected or connecting");
      return;
    }

    this.log.info("Starting user stream", { fromSeq });

    this.lastSequence = BigInt(fromSeq);
    this.projectId = projectId;
    this.reconnectAttempts = 0;
    this.open();
  }

  protected openStream(signal: AbortSignal): AsyncIterable<UserStreamEvent> {
    const client = this.client();
    const request = create(StreamUserUpdatesRequestSchema, {
      sinceSeq: this.lastSequence,
      projectId: this.projectId,
    });
    this.log.info("Connecting to user stream", {
      sinceSeq: Number(this.lastSequence),
    });
    return client.streamUserUpdates(request, { signal });
  }

  protected describe(): Record<string, unknown> {
    return { fromSeq: Number(this.lastSequence) };
  }

  protected handleEvent(event: UserStreamEvent): void {
    switch (event.event.case) {
      case "sync": {
        const syncInfo = event.event.value;
        this.log.info("Received sync info", {
          lastSequence: Number(syncInfo.lastSequence),
        });
        // Deliberately does NOT advance this.lastSequence: the server sends
        // sync BEFORE replaying (since_seq, latest]. The resume cursor must
        // only reflect updates we actually processed, otherwise a disconnect
        // mid-replay would skip the remaining updates forever on reconnect.
        this.callbacks.onSync(Number(syncInfo.lastSequence));
        break;
      }

      case "updates": {
        const batch = event.event.value;
        // Process events BEFORE advancing the sequence number.
        // If the stream is aborted mid-batch (e.g. a resume reconnect),
        // the un-acked sequence ensures the server re-sends on reconnect.
        if (batch.updates.length > 0) {
          this.callbacks.onUpdate(batch.updates.map((u) => decodeUserUpdate(u, this.log)));
        }
        // latest_sequence is the cursor: the server has delivered everything
        // this subscription should see up to it. Sequences in `updates` skip
        // by design (the stream is project-filtered), so a skip says nothing
        // about loss — the server sees every sequence, backfills anything its
        // hub dropped before sending later updates, and reports the result
        // here. A batch with no updates only advances the cursor.
        if (batch.latestSequence > this.lastSequence) {
          this.lastSequence = batch.latestSequence;
        }
        break;
      }

      case "heartbeat": {
        // Liveness only (lastEventAt is bumped for every event above).
        break;
      }

      default:
        this.log.warn("Unknown event type", {
          case: event.event.case,
        });
    }
  }

  stop(): void {
    this.log.info("Stopping");
    this.close(true);
  }

  getLastSequence(): number {
    return Number(this.lastSequence);
  }
}
