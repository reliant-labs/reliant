// Copyright (c) 2025 Reliant Labs

/**
 * How the web opens a chat: with the bodies of its finished tool calls left
 * out ("stub"), fetched when a row shows them (hooks/content-block-bodies.ts),
 * or whole ("full", what every client had before).
 *
 * The one switch for the whole opt-in (dev-docs/design/chat-lazy-tool-bodies.md):
 * every transcript read the web makes (ListMessages, GetMessage, the chat
 * stream's snapshot and replay) asks with this mode, and chat.open records it
 * as `chat.tool_bodies`. Set it to "full" to go back to full payloads; nothing
 * else needs to change, because the client acts on each block's `bodyElided`,
 * never on the mode it asked for (a server that predates the option, or one
 * with RELIANT_TRANSCRIPT_TOOL_BODY_STUBS=off, sends full bodies either way).
 */

import type { ToolBodies } from "@reliantlabs/sdk/client";
import { ToolBodyMode } from "../gen/reliant/v1/chat_pb";

export const TRANSCRIPT_TOOL_BODIES: ToolBodies = "stub";

/** TRANSCRIPT_TOOL_BODIES as the ToolBodyMode a request carries. */
export const TRANSCRIPT_TOOL_BODY_MODE: ToolBodyMode =
  TRANSCRIPT_TOOL_BODIES === "stub" ? ToolBodyMode.STUB : ToolBodyMode.FULL;
