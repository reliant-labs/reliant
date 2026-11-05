// The live output of one background process (StreamProcessOutput): a snapshot
// of what it has printed, then each new line, then the completion. The same
// reconnecting stream as the chat and user streams, with this protocol's own
// pacing: it gives up after a few attempts, because a viewer of finished or
// vanished output has no push path to protect, and the server does not
// heartbeat it, so silence is not a dead connection.

import { create } from "@bufbuild/protobuf";
import type { Client } from "@connectrpc/connect";
import {
  type BackgroundService,
  StreamProcessOutputRequestSchema,
  type ProcessOutputEvent,
} from "#gen/reliant/v1/background_pb.js";
import { OutputStreamType, type BackgroundProcessStatus } from "#gen/reliant/v1/common_pb.js";
import { ReconnectingStream, type StreamEnvironment } from "./reconnecting-stream.js";

/** The BackgroundService client a stream opens its connection with. */
export type BackgroundClient = Client<typeof BackgroundService>;

const LOG_PREFIX = "[📡 ProcessOutput]";

/** Output line with metadata */
export interface OutputLine {
  type: "stdout" | "stderr";
  text: string;
  sequence: number;
}

/** Callbacks for process output streaming */
export interface ProcessOutputCallbacks {
  /** Called when initial snapshot of existing output is received */
  onSnapshot: (lines: OutputLine[], latestSequence: number) => void;
  /** Called for each new line of output */
  onLine: (line: OutputLine) => void;
  /** Called when process completes (no more output) */
  onComplete: (
    status: BackgroundProcessStatus,
    exitCode: number | undefined,
    endTime: string,
  ) => void;
  /** Called on stream error */
  onError: (error: string) => void;
  /** Called when connection status changes */
  onStatusChange: (status: "connecting" | "connected" | "disconnected" | "error") => void;
}

export interface ProcessOutputStreamOptions extends StreamEnvironment {
  processId: string;
  callbacks: ProcessOutputCallbacks;
  client: () => BackgroundClient;
  /** The machine the process runs on (a package process's daemon_id); unset streams from the default machine. */
  daemonId?: string;
}

/**
 * Streams a process's output.
 *
 * ```ts
 * const stream = new ProcessOutputStream({ processId, client, callbacks });
 * stream.start();                    // existing output first
 * stream.start({ newOnly: true });   // or only what is printed from now on
 * stream.stop();
 * ```
 */
export class ProcessOutputStream extends ReconnectingStream<ProcessOutputEvent> {
  private readonly processId: string;
  private readonly daemonId: string | undefined;
  private readonly callbacks: ProcessOutputCallbacks;
  private readonly client: () => BackgroundClient;
  private lastSequence = 0;
  private newOnly = false;
  // Whether the server has said the process finished. Only then is a stream
  // that ends cleanly the end of the output.
  private completed = false;

  constructor(options: ProcessOutputStreamOptions) {
    super(
      LOG_PREFIX,
      {
        onStatusChange: (status) => options.callbacks.onStatusChange(status),
        onError: (error) => options.callbacks.onError(error),
      },
      {
        ...options,
        tuning: {
          baseDelayMs: 1_000,
          maxDelayMs: 10_000,
          jitterMs: 500,
          maxReconnectAttempts: 5,
          liveness: false,
          // Resetting on the first event instead would let a server that
          // answers and then immediately ends the stream drive a reconnect
          // every second, forever, now that a stream ending without "complete"
          // reconnects.
          creditLongConnectionOnEnd: true,
          ...options.tuning,
        },
      },
    );
    this.processId = options.processId;
    this.callbacks = options.callbacks;
    this.client = options.client;
    this.daemonId = options.daemonId;
    this.isIntentionallyClosed = true;
  }

  /** Start streaming process output. `newOnly` skips what was already printed. */
  start(options: { newOnly?: boolean } = {}): void {
    if (this.abortController && !this.abortController.signal.aborted) {
      this.log.warn("Already connected", { processId: this.processId.slice(0, 8) });
      return;
    }
    this.log.info("Starting output stream", {
      processId: this.processId.slice(0, 8),
      newOnly: options.newOnly,
    });
    this.newOnly = options.newOnly ?? false;
    this.open();
  }

  /** Stop the streaming connection. */
  stop(): void {
    this.log.info("Stopping", { processId: this.processId.slice(0, 8) });
    this.close(true);
  }

  /** The sequence of the last line received. */
  getLastSequence(): number {
    return this.lastSequence;
  }

  protected openStream(signal: AbortSignal): AsyncIterable<ProcessOutputEvent> {
    const request = create(StreamProcessOutputRequestSchema, {
      processId: this.processId,
      newOnly: this.newOnly,
      daemonId: this.daemonId,
    });
    this.log.info("Connecting to gRPC stream", { processId: this.processId.slice(0, 8) });
    // A reconnect skips the output we already have.
    this.newOnly = true;
    return this.client().streamProcessOutput(request, { signal });
  }

  // The process finished: there is no more output to wait for. A stream that
  // ends WITHOUT the complete event was ended by the server while the process
  // is still running — an API replica shutting down ends every stream it holds
  // so the rollout is not held up by them — so pick the output up again from a
  // live replica.
  protected reconnectAfterCleanEnd(): boolean {
    this.log.info("Stream ended normally", {
      processId: this.processId.slice(0, 8),
      completed: this.completed,
    });
    return !this.completed;
  }

  protected handleEvent(event: ProcessOutputEvent): void {
    switch (event.event.case) {
      case "snapshot": {
        const snapshot = event.event.value;
        this.log.info("📸 Received snapshot", {
          processId: this.processId.slice(0, 8),
          lineCount: snapshot.lines.length,
          latestSequence: Number(snapshot.latestSequence),
          isComplete: snapshot.isComplete,
        });

        // Update last sequence for reconnection
        this.lastSequence = Number(snapshot.latestSequence);

        const lines: OutputLine[] = snapshot.lines.map((line) => ({
          type: line.type === OutputStreamType.STDERR ? "stderr" : "stdout",
          text: line.text,
          sequence: Number(line.sequence),
        }));

        // If the process is already complete, the complete event follows.
        this.callbacks.onSnapshot(lines, Number(snapshot.latestSequence));
        break;
      }

      case "line": {
        const line = event.event.value;
        this.log.debug("Received line", {
          processId: this.processId.slice(0, 8),
          type: line.type,
          sequence: Number(line.sequence),
        });

        this.lastSequence = Number(line.sequence);

        this.callbacks.onLine({
          type: line.type === OutputStreamType.STDERR ? "stderr" : "stdout",
          text: line.text,
          sequence: Number(line.sequence),
        });
        break;
      }

      case "complete": {
        const complete = event.event.value;
        this.log.info("✅ Process complete", {
          processId: this.processId.slice(0, 8),
          status: complete.status,
          exitCode: complete.exitCode,
        });

        this.completed = true;
        this.callbacks.onComplete(complete.status, complete.exitCode, complete.endTime);
        break;
      }

      default:
        this.log.warn("Unknown event type", {
          processId: this.processId.slice(0, 8),
          case: event.event.case,
        });
    }
  }
}
