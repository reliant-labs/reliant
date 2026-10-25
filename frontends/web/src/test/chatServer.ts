// Copyright (c) 2025 Reliant Labs

/**
 * Reading ChatService requests in tests that answer the chat core's writes at
 * the transport (hooks/chat-actions.ts is a set of connectClient calls, so a
 * test fakes the RPC, not the module).
 */

import type { Value } from "@bufbuild/protobuf/wkt";
import { protoValueToJs } from "@/api/proto-utils";

/**
 * Workflow params as a request carries them (`workflowParams` on SendMessage
 * and StartChat, `params` on UpdateWorkflowParams), back as the JS object the
 * caller passed.
 */
export function paramsOf(params: Record<string, Value>): Record<string, unknown> {
  return Object.fromEntries(Object.entries(params).map(([key, value]) => [key, protoValueToJs(value)]));
}
