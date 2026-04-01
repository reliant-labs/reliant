// Copyright (c) 2025 Reliant Labs
// gRPC client for terminal session management
// Note: The interactive terminal WebSocket (/terminal/ws) remains as WebSocket
// because it requires true bidirectional streaming (user input + PTY output).

import { create } from "@bufbuild/protobuf";
import { getTerminalClient } from "./grpc-client";
import { logger } from "../lib/logger";
import {
  ListTerminalSessionsRequestSchema,
  CloseTerminalSessionRequestSchema,
  type TerminalSession,
} from "../gen/reliant/v1/terminal_pb";

const LOG_PREFIX = "[🖥️ Terminal]";

// ============================================================================
// Types
// ============================================================================

export interface TerminalSessionInfo {
  id: string;
  workingDir: string;
  createdAt: string;
  lastActive: string;
  pid?: number;
}

// ============================================================================
// API Functions
// ============================================================================

/**
 * List all active terminal sessions
 */
export async function listTerminalSessions(): Promise<TerminalSessionInfo[]> {
  try {
    logger.info(`${LOG_PREFIX} Listing sessions`);
    const client = await getTerminalClient();
    const response = await client.listSessions(
      create(ListTerminalSessionsRequestSchema, {})
    );

    logger.info(`${LOG_PREFIX} Found ${response.total} sessions`);
    return response.sessions.map(convertSession);
  } catch (error) {
    logger.error(`${LOG_PREFIX} ❌ Failed to list sessions`, { error });
    throw error;
  }
}

/**
 * Close a terminal session
 */
export async function closeTerminalSession(
  sessionId: string
): Promise<{ success: boolean; message: string }> {
  try {
    logger.info(`${LOG_PREFIX} Closing session`, { sessionId: sessionId.slice(0, 8) });
    const client = await getTerminalClient();
    const response = await client.closeSession(
      create(CloseTerminalSessionRequestSchema, {
        sessionId,
      })
    );

    if (response.success) {
      logger.info(`${LOG_PREFIX} ✅ Session closed`, { sessionId: sessionId.slice(0, 8) });
    } else {
      logger.warn(`${LOG_PREFIX} ⚠️ Close session failed`, { 
        sessionId: sessionId.slice(0, 8),
        message: response.message 
      });
    }

    return {
      success: response.success,
      message: response.message,
    };
  } catch (error) {
    logger.error(`${LOG_PREFIX} ❌ Failed to close session`, { 
      sessionId: sessionId.slice(0, 8),
      error 
    });
    throw error;
  }
}

// ============================================================================
// Helpers
// ============================================================================

function convertSession(session: TerminalSession): TerminalSessionInfo {
  return {
    id: session.id,
    workingDir: session.workingDir,
    createdAt: session.createdAt,
    lastActive: session.lastActive,
    pid: session.pid ?? undefined,
  };
}

// ============================================================================
// Export API
// ============================================================================

export const terminalApi = {
  listSessions: listTerminalSessions,
  closeSession: closeTerminalSession,
};
