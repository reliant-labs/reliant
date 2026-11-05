// Copyright (c) 2025 Reliant Labs
// gRPC streaming client for real-time process output
// Replaces polling-based output fetching with server-streaming
//
// The stream is @reliantlabs/sdk's ProcessOutputStream; this binds it to the
// app's routed Connect client and logger.

import { ProcessOutputStream, type ProcessOutputCallbacks } from "@reliantlabs/sdk/client";
import { connectClient } from "../lib/connect";
import { logger } from "../lib/logger";
import {
  BackgroundService,
  type ProcessOutputEvent,
  type ProcessOutputLine,
  type ProcessOutputComplete,
  type ProcessOutputSnapshot,
} from "../gen/reliant/v1/background_pb";

export type { OutputLine, ProcessOutputCallbacks } from "@reliantlabs/sdk/client";

/**
 * Service for streaming real-time process output via gRPC.
 *
 * ```ts
 * const service = new ProcessOutputStreamingService(processId, {
 *   onSnapshot: (lines) => setOutputLines(lines),
 *   onLine: (line) => appendLine(line),
 *   onComplete: (status) => setProcessComplete(status),
 *   onError: (err) => console.error(err),
 *   onStatusChange: (status) => setConnectionStatus(status),
 * });
 *
 * service.start();                   // existing output first
 * service.start({ newOnly: true });  // or only new output
 * service.stop();
 * ```
 */
export class ProcessOutputStreamingService extends ProcessOutputStream {
  /**
   * @param daemonId The machine the process runs on (a package process's
   *   daemon_id); unset streams from the default machine.
   */
  constructor(processId: string, callbacks: ProcessOutputCallbacks, daemonId?: string) {
    super({
      processId,
      callbacks,
      daemonId,
      client: () => connectClient(BackgroundService),
      logger,
    });
  }
}

// Re-export types from generated code for convenience
export type {
  ProcessOutputEvent,
  ProcessOutputLine,
  ProcessOutputComplete,
  ProcessOutputSnapshot,
};
