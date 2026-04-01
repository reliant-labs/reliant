// Copyright (c) 2025 Reliant Labs
// gRPC streaming client for real-time process output
// Replaces polling-based output fetching with server-streaming

import { createClient } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { getTransport } from "./grpc-client";
import { logger } from "../lib/logger";
import {
  BackgroundService,
  StreamProcessOutputRequestSchema,
  type ProcessOutputEvent,
  type ProcessOutputLine,
  type ProcessOutputComplete,
  type ProcessOutputSnapshot,
} from "../gen/reliant/v1/background_pb";
import { OutputStreamType, BackgroundProcessStatus } from "../gen/reliant/v1/common_pb";

const LOG_PREFIX = "[📡 ProcessOutput]";

// ============================================================================
// Types
// ============================================================================

/**
 * Output line with metadata
 */
export interface OutputLine {
  type: "stdout" | "stderr";
  text: string;
  sequence: number;
}

/**
 * Callbacks for process output streaming
 */
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

// ============================================================================
// ProcessOutputStreamingService
// ============================================================================

/**
 * Service for streaming real-time process output via gRPC.
 * 
 * Usage:
 * ```ts
 * const service = new ProcessOutputStreamingService(processId, {
 *   onSnapshot: (lines) => setOutputLines(lines),
 *   onLine: (line) => appendLine(line),
 *   onComplete: (status) => setProcessComplete(status),
 *   onError: (err) => console.error(err),
 *   onStatusChange: (status) => setConnectionStatus(status),
 * });
 * 
 * // Start streaming (fetches existing output first)
 * service.start();
 * 
 * // Or start with only new output (skip existing)
 * service.start({ newOnly: true });
 * 
 * // Stop streaming when done
 * service.stop();
 * ```
 */
export class ProcessOutputStreamingService {
  private processId: string;
  private callbacks: ProcessOutputCallbacks;
  private abortController: AbortController | null = null;
  private isIntentionallyClosed = false;
  private reconnectAttempts = 0;
  private maxReconnectAttempts = 5;
  private reconnectDelay = 1000;
  private lastSequence = 0;
  private isConnected_ = false;
  private newOnly = false;

  constructor(processId: string, callbacks: ProcessOutputCallbacks) {
    this.processId = processId;
    this.callbacks = callbacks;
  }

  /**
   * Start streaming process output
   * @param options.newOnly - If true, skip sending existing output
   */
  start(options: { newOnly?: boolean } = {}): void {
    if (this.abortController && !this.abortController.signal.aborted) {
      logger.warn(`${LOG_PREFIX} Already connected`, {
        processId: this.processId.slice(0, 8),
      });
      return;
    }

    logger.info(`${LOG_PREFIX} Starting output stream`, {
      processId: this.processId.slice(0, 8),
      newOnly: options.newOnly,
    });

    this.isIntentionallyClosed = false;
    this.newOnly = options.newOnly ?? false;

    // Start connection asynchronously
    void this.establishConnection();
  }

  private async establishConnection(): Promise<void> {
    this.callbacks.onStatusChange("connecting");
    this.abortController = new AbortController();

    try {
      const client = createClient(BackgroundService, getTransport());

      const request = create(StreamProcessOutputRequestSchema, {
        processId: this.processId,
        newOnly: this.newOnly,
      });

      logger.info(`${LOG_PREFIX} Connecting to gRPC stream`, {
        processId: this.processId.slice(0, 8),
      });

      // Process streaming response
      for await (const event of client.streamProcessOutput(request, {
        signal: this.abortController.signal,
      })) {
        // On first event, mark as connected
        if (!this.isConnected_) {
          this.isConnected_ = true;
          this.callbacks.onStatusChange("connected");
          this.reconnectAttempts = 0;
        }

        this.handleEvent(event);
      }

      // Stream ended normally (process completed or server closed)
      logger.info(`${LOG_PREFIX} Stream ended normally`, {
        processId: this.processId.slice(0, 8),
      });
      this.isConnected_ = false;
      this.callbacks.onStatusChange("disconnected");

      // Don't reconnect on normal end - process is complete
    } catch (error) {
      this.isConnected_ = false;

      if (this.abortController?.signal.aborted) {
        // Intentionally closed
        logger.info(`${LOG_PREFIX} Stream aborted (intentional)`, {
          processId: this.processId.slice(0, 8),
        });
        this.callbacks.onStatusChange("disconnected");
        return;
      }

      const errorMessage = error instanceof Error ? error.message : String(error);
      logger.error(`${LOG_PREFIX} ❌ Stream error`, {
        processId: this.processId.slice(0, 8),
        error: errorMessage,
      });
      this.callbacks.onError(errorMessage);
      this.callbacks.onStatusChange("error");

      // Attempt reconnect on error
      if (!this.isIntentionallyClosed) {
        this.attemptReconnect();
      }
    }
  }

  private handleEvent(event: ProcessOutputEvent): void {
    switch (event.event.case) {
      case "snapshot": {
        const snapshot = event.event.value;
        logger.info(`${LOG_PREFIX} 📸 Received snapshot`, {
          processId: this.processId.slice(0, 8),
          lineCount: snapshot.lines.length,
          latestSequence: Number(snapshot.latestSequence),
          isComplete: snapshot.isComplete,
        });

        // Update last sequence for reconnection
        this.lastSequence = Number(snapshot.latestSequence);

        // Convert lines to our format
        const lines: OutputLine[] = snapshot.lines.map((line) => ({
          type: line.type === OutputStreamType.STDERR ? "stderr" : "stdout",
          text: line.text,
          sequence: Number(line.sequence),
        }));

        this.callbacks.onSnapshot(lines, Number(snapshot.latestSequence));

        // If process is already complete, fire complete callback
        if (snapshot.isComplete && snapshot.status) {
          // The complete event will follow, so we don't call onComplete here
        }
        break;
      }

      case "line": {
        const line = event.event.value;
        logger.debug(`${LOG_PREFIX} Received line`, {
          processId: this.processId.slice(0, 8),
          type: line.type,
          sequence: Number(line.sequence),
        });

        // Update last sequence
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
        logger.info(`${LOG_PREFIX} ✅ Process complete`, {
          processId: this.processId.slice(0, 8),
          status: complete.status,
          exitCode: complete.exitCode,
        });

        this.callbacks.onComplete(
          complete.status,
          complete.exitCode,
          complete.endTime,
        );
        break;
      }

      default:
        logger.warn(`${LOG_PREFIX} Unknown event type`, {
          processId: this.processId.slice(0, 8),
          case: event.event.case,
        });
    }
  }

  private attemptReconnect(): void {
    if (this.isIntentionallyClosed) {
      return;
    }

    if (this.reconnectAttempts >= this.maxReconnectAttempts) {
      logger.error(`${LOG_PREFIX} ❌ Max reconnect attempts reached`, {
        processId: this.processId.slice(0, 8),
        attempts: this.reconnectAttempts,
      });
      this.callbacks.onError("Max reconnection attempts reached");
      return;
    }

    this.reconnectAttempts++;
    const delay = Math.min(
      this.reconnectDelay * Math.pow(2, this.reconnectAttempts - 1) + Math.random() * 500,
      10000 // Max 10 seconds
    );

    logger.info(`${LOG_PREFIX} 🔄 Reconnecting`, {
      processId: this.processId.slice(0, 8),
      attempt: this.reconnectAttempts,
      delayMs: delay,
    });

    setTimeout(() => {
      if (!this.isIntentionallyClosed) {
        // On reconnect, skip existing output since we already have it
        this.newOnly = true;
        void this.establishConnection();
      }
    }, delay);
  }

  /**
   * Stop the streaming connection
   */
  stop(): void {
    logger.info(`${LOG_PREFIX} Stopping`, {
      processId: this.processId.slice(0, 8),
    });
    this.isIntentionallyClosed = true;
    this.isConnected_ = false;
    if (this.abortController) {
      this.abortController.abort();
      this.abortController = null;
    }
    this.reconnectAttempts = 0;
  }

  /**
   * Check if the stream is currently connected
   */
  isConnected(): boolean {
    return this.isConnected_;
  }

  /**
   * Get the last received sequence number
   */
  getLastSequence(): number {
    return this.lastSequence;
  }
}

// ============================================================================
// Factory Function
// ============================================================================

/**
 * Create a new process output streaming service
 */
export function createProcessOutputStreamingService(
  processId: string,
  callbacks: ProcessOutputCallbacks
): ProcessOutputStreamingService {
  return new ProcessOutputStreamingService(processId, callbacks);
}

// Re-export types from generated code for convenience
export type {
  ProcessOutputEvent,
  ProcessOutputLine,
  ProcessOutputComplete,
  ProcessOutputSnapshot,
};
