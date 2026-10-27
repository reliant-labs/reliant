// Copyright (c) 2025 Reliant Labs

/**
 * ChatService's frontend model. Pure: no React, no client. The RPCs are
 * `chat-actions.ts` (and, for the pull reads, the generated hooks); this
 * module is the types they speak and the proto → frontend conversions and
 * request payloads they are built from.
 *
 * Proto messages are converted to plain frontend types at this boundary so
 * components never touch `$typeName`.
 */

import { create, type JsonObject } from "@bufbuild/protobuf";
import type { AgentMessageKind } from "../gen/reliant/v1/chat_pb";
import { jsToProtoValue, type ProtoValue } from "../api/proto-utils";
import { repairLegacyWorkflowParams } from "../lib/paramUtils";
import {
  ReplayEventSchema,
  type DisplayStyle,
  type MessageRole,
  type ReplayEvent,
} from "../gen/reliant/v1/chat_pb";

export type {
  Chat,
  ArchivedChat,
  ContentBlock,
  Attachment,
  Message,
  WorkflowExecutionData,
  StepExecutionData,
} from "../types/chat";

// Re-export proto enums for consumers
export { ChatState, WorkflowState, WorkflowStopReason, MessageRole, StreamingState, DisplayStyle, ContentBlockType } from "../types/chat";

import type {
  Chat,
  Message,
  ContentBlock,
} from "../types/chat";

import type {
  Chat as ProtoChat,
  Message as ProtoMessage,
  ContentBlock as ProtoContentBlock,
} from "../gen/reliant/v1/chat_pb";


// ============================================
// Proto → Frontend Converters
// ============================================
// Proto types have $typeName as a required discriminant. Frontend types
// make $typeName optional, so proto objects are structurally assignable.
// These converters destructure away $typeName and recursively convert
// nested proto types.

function convertProtoContentBlock(b: ProtoContentBlock): ContentBlock {
  const { $typeName: _, ...rest } = b;
  return rest;
}

export function convertProtoMessage(m: ProtoMessage): Message {
  const { $typeName: _, contentBlocks, ...rest } = m;
  return {
    ...rest,
    contentBlocks: contentBlocks.map(convertProtoContentBlock),
  };
}

export function convertProtoChat(proto: ProtoChat): Chat {
  const { $typeName: _, ...rest } = proto;
  return rest;
}

/**
 * One undelivered entry in a running thread's mailbox.
 *
 * `sender_kind` is the AgentMessageKind: HUMAN_MESSAGE is a message the
 * human queued from the composer, MESSAGE is one a peer agent sent via spawn_send.
 * The strip above the composer only offers cancel/send-now on the human ones —
 * revoking another agent's message from a human UI would be a lie about who
 * owns it.
 */
export interface QueuedAgentMessageView {
  id: string;
  body: string;
  created_at: string;
  sender_kind: AgentMessageKind;
  attachments: string[];
}

// InputMessage for StartChat/SendMessage requests
// Supports user and system messages.
export interface InputMessage {
  role: MessageRole;
  content: string;
  display_style?: DisplayStyle;
}

/** A past trigger event a "Run now" replays. The payload is whatever the person edited it to. */
export interface ReplayEventInput {
  /** The declared trigger whose inputs and prompt map the event. */
  triggerName: string;
  /** The recorded event being replayed; empty for a payload the person wrote. */
  eventId?: string;
  /** "schedule" | "webhook" | "integration" | "workflow_event"; omitted means the trigger's own kind. */
  kind?: string;
  /** RFC3339 UTC; omitted means now. */
  occurredAt?: string;
  /** protobuf-es carries a google.protobuf.Struct field as its JSON object, not a message. */
  payload: JsonObject;
}

export interface RunNowOptions {
  projectId: string;
  workflow: string;
  /** What the run starts from. Omitted starts from "Run now." (or, for a replay, the trigger's prompt). */
  message?: string;
  inputs?: Record<string, unknown>;
  presets?: Record<string, string>;
  worktreeId?: string;
  daemonId?: string;
  noMachine?: boolean;
  replay?: ReplayEventInput;
  /** Makes the start idempotent: a repeat returns the run the first call started. */
  clientMessageId?: string;
}

export function replayEventToProto(replay: ReplayEventInput): ReplayEvent {
  return create(ReplayEventSchema, {
    triggerName: replay.triggerName,
    eventId: replay.eventId ?? "",
    kind: replay.kind ?? "",
    occurredAt: replay.occurredAt ?? "",
    payload: replay.payload,
  });
}

// Chat updates only affect title and worktree_id
// Model/temperature/max_tokens are per-message workflow params
export interface UpdateChatOptions {
  title?: string;
  worktree_id?: string;
  workflow_name?: string; // Only changeable when root workflow is pending (chat hasn't started)
}

function hasDottedWorkflowParamKey(workflowParams: Record<string, unknown>): boolean {
  return Object.keys(workflowParams).some((key) => key.includes("."));
}

export function buildWorkflowParamsPayload(
  rawWorkflowParams?: Record<string, unknown>,
): Record<string, ProtoValue> {
  if (!rawWorkflowParams) {
    return {};
  }

  // Params persisted by older builds (a top-level thinking_level, "default."
  // preset keys) would be refused; repair them here so a stored value never
  // blocks a send.
  const workflowParams = repairLegacyWorkflowParams(rawWorkflowParams);

  if (hasDottedWorkflowParamKey(workflowParams)) {
    throw new Error(
      "workflow_params must use nested object keys. Dotted keys are no longer supported.",
    );
  }

  const protoParams: Record<string, ProtoValue> = {};
  for (const [key, value] of Object.entries(workflowParams)) {
    protoParams[key] = jsToProtoValue(value);
  }

  return protoParams;
}
