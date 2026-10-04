// Copyright (c) 2025 Reliant Labs

/**
 * Thin client over `reliant.v1.TriggerService` — automations.
 *
 * Proto messages are converted to plain frontend types at this boundary so
 * components never touch `$typeName`, oneof envelopes or proto enums. The one
 * write shape, `TriggerDefinitionInput`, is shared by create and update for
 * the same reason the proto shares `TriggerDefinition`: the two must not drift.
 *
 * UpdateTrigger is a FULL replacement on the server. Callers editing a trigger
 * must therefore carry every field they do not show (presets, params,
 * worktree, catch-up window) — and the daemon, which the server requires —
 * through from the loaded trigger, or the update
 * silently clears them. `definitionFromTrigger` exists to make that the
 * default rather than something each caller has to remember.
 */

import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import { grpcClient } from "./grpc-client";
import { jsToProtoValue, protoValueToJs } from "./proto-utils";
import {
  CreateTriggerRequestSchema,
  DeleteTriggerRequestSchema,
  FireTriggerRequestSchema,
  GetTriggerRequestSchema,
  ListTriggerEventsRequestSchema,
  ListTriggersRequestSchema,
  ScheduleSourceSchema,
  SetTriggerEnabledRequestSchema,
  TriggerDefinitionSchema,
  TriggerEventOutcome,
  TriggerOverlapPolicy,
  UpdateTriggerRequestSchema,
  type ScheduleSource as ProtoScheduleSource,
  type Trigger as ProtoTrigger,
  type TriggerDefinition as ProtoTriggerDefinition,
  type TriggerEvent as ProtoTriggerEvent,
} from "../gen/reliant/v1/trigger_pb";
import type { RunDisplayState } from "../gen/reliant/v1/run_pb";

// ============================================
// Frontend types
// ============================================

export type TriggerOutcome = "launched" | "skipped" | "failed" | "unknown";

export type OverlapPolicy = "skip" | "allow";

export interface TriggerSchedule {
  /** 5-field cron expressions; a union with each other and the interval. */
  cron: string[];
  /** Go duration ("15m"); unset when the schedule is cron-only. */
  interval?: string;
  /** IANA zone. The server renders its default ("UTC") explicitly. */
  timezone: string;
  overlap: OverlapPolicy;
  /** Go duration; unset means the server default (10m). */
  catchupWindow?: string;
}

export interface TriggerEvent {
  id: string;
  occurredAt: string;
  outcome: TriggerOutcome;
  outcomeDetail: string;
  chatId?: string;
  /** A "Run now" fire rather than a scheduled one. */
  manual: boolean;
  /**
   * The display state of the run a launched firing started, as of the read.
   * Unset for a firing that launched nothing, or one whose chat is gone.
   */
  runDisplayState?: RunDisplayState;
}

export interface Trigger {
  id: string;
  name: string;
  projectId: string;
  worktreeId?: string;
  enabled: boolean;
  workflow: string;
  presets: Record<string, string>;
  params: Record<string, unknown>;
  message: string;
  /** The daemon every launched run executes on. Always set by the server. */
  daemonId: string;
  createdAt: string;
  updatedAt: string;
  nextFireAt?: string;
  lastEvent?: TriggerEvent;
  /** Unset only for a source arm this client does not know yet. */
  schedule?: TriggerSchedule;
}

/** The writable half of a trigger — what create and update send. */
export interface TriggerDefinitionInput {
  name: string;
  projectId: string;
  worktreeId?: string;
  /** Unset: true on create, unchanged on update. */
  enabled?: boolean;
  /** Empty means the owner's default workflow, resolved at fire time. */
  workflow: string;
  presets: Record<string, string>;
  params: Record<string, unknown>;
  message: string;
  /**
   * Required. One of the caller's daemons and, when the project is installed
   * on any daemon, one that has it installed (validateTriggerDaemon).
   */
  daemonId: string;
  schedule: TriggerSchedule;
}

// ============================================
// Proto → frontend
// ============================================

function outcomeFromProto(outcome: TriggerEventOutcome): TriggerOutcome {
  switch (outcome) {
    case TriggerEventOutcome.LAUNCHED:
      return "launched";
    case TriggerEventOutcome.SKIPPED:
      return "skipped";
    case TriggerEventOutcome.FAILED:
      return "failed";
    default:
      return "unknown";
  }
}

function overlapFromProto(overlap: TriggerOverlapPolicy): OverlapPolicy {
  // Unspecified is SKIP on the server.
  return overlap === TriggerOverlapPolicy.ALLOW ? "allow" : "skip";
}

function overlapToProto(overlap: OverlapPolicy): TriggerOverlapPolicy {
  return overlap === "allow" ? TriggerOverlapPolicy.ALLOW : TriggerOverlapPolicy.SKIP;
}

function scheduleFromProto(source: ProtoScheduleSource): TriggerSchedule {
  return {
    cron: [...source.cron],
    interval: source.interval || undefined,
    timezone: source.timezone || "UTC",
    overlap: overlapFromProto(source.overlap),
    catchupWindow: source.catchupWindow || undefined,
  };
}

export function eventFromProto(event: ProtoTriggerEvent): TriggerEvent {
  return {
    id: event.id,
    occurredAt: event.occurredAt,
    outcome: outcomeFromProto(event.outcome),
    outcomeDetail: event.outcomeDetail,
    chatId: event.chatId || undefined,
    manual: event.payload?.manual === true,
    runDisplayState: event.run?.displayState || undefined,
  };
}

export function triggerFromProto(proto: ProtoTrigger): Trigger {
  const params: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(proto.params)) {
    params[key] = protoValueToJs(value);
  }
  return {
    id: proto.id,
    name: proto.name,
    projectId: proto.projectId,
    worktreeId: proto.worktreeId || undefined,
    enabled: proto.enabled,
    workflow: proto.workflow,
    presets: { ...proto.presets },
    params,
    message: proto.message,
    daemonId: proto.daemonId,
    createdAt: proto.createdAt,
    updatedAt: proto.updatedAt,
    nextFireAt: proto.nextFireAt || undefined,
    lastEvent: proto.lastEvent ? eventFromProto(proto.lastEvent) : undefined,
    schedule: proto.source.case === "schedule" ? scheduleFromProto(proto.source.value) : undefined,
  };
}

// ============================================
// Frontend → proto
// ============================================

export function definitionToProto(input: TriggerDefinitionInput): ProtoTriggerDefinition {
  const params: Record<string, ReturnType<typeof jsToProtoValue>> = {};
  for (const [key, value] of Object.entries(input.params)) {
    params[key] = jsToProtoValue(value);
  }
  const schedule = create(ScheduleSourceSchema, {
    cron: input.schedule.cron,
    interval: input.schedule.interval || undefined,
    timezone: input.schedule.timezone,
    overlap: overlapToProto(input.schedule.overlap),
    catchupWindow: input.schedule.catchupWindow || undefined,
  });
  return create(TriggerDefinitionSchema, {
    name: input.name,
    projectId: input.projectId,
    worktreeId: input.worktreeId || undefined,
    enabled: input.enabled,
    workflow: input.workflow,
    presets: input.presets,
    params,
    message: input.message,
    daemonId: input.daemonId,
    source: { case: "schedule", value: schedule },
  });
}

/**
 * The definition a stored trigger currently has. Start every update from this,
 * then override what the user changed — UpdateTrigger replaces the whole row.
 */
export function definitionFromTrigger(trigger: Trigger): TriggerDefinitionInput {
  return {
    name: trigger.name,
    projectId: trigger.projectId,
    worktreeId: trigger.worktreeId,
    workflow: trigger.workflow,
    presets: { ...trigger.presets },
    params: { ...trigger.params },
    message: trigger.message,
    daemonId: trigger.daemonId,
    schedule: trigger.schedule ?? { cron: [], timezone: "UTC", overlap: "skip" },
  };
}

// ============================================
// Errors
// ============================================

/**
 * The message to show for a failed trigger RPC. Server validation errors
 * (a bad cron, an unknown zone, a too-short interval) arrive as
 * InvalidArgument with a precise message; `rawMessage` is that message without
 * connect's "[invalid_argument]" prefix.
 */
export function triggerErrorMessage(error: unknown): string {
  if (error instanceof ConnectError) return error.rawMessage || error.message;
  if (error instanceof Error) return error.message;
  return String(error);
}

export function isTriggerNotFound(error: unknown): boolean {
  return error instanceof ConnectError && error.code === Code.NotFound;
}

// ============================================
// Client
// ============================================

export const triggerGrpc = {
  async list(projectId?: string): Promise<Trigger[]> {
    const response = await grpcClient
      .trigger()
      .listTriggers(create(ListTriggersRequestSchema, { projectId }));
    return response.triggers.map(triggerFromProto);
  },

  async get(id: string): Promise<Trigger> {
    const response = await grpcClient.trigger().getTrigger(create(GetTriggerRequestSchema, { id }));
    if (!response.trigger) throw new Error("No trigger in response");
    return triggerFromProto(response.trigger);
  },

  async create(input: TriggerDefinitionInput): Promise<Trigger> {
    const response = await grpcClient
      .trigger()
      .createTrigger(create(CreateTriggerRequestSchema, { trigger: definitionToProto(input) }));
    if (!response.trigger) throw new Error("No trigger in response");
    return triggerFromProto(response.trigger);
  },

  async update(id: string, input: TriggerDefinitionInput): Promise<Trigger> {
    const response = await grpcClient
      .trigger()
      .updateTrigger(create(UpdateTriggerRequestSchema, { id, trigger: definitionToProto(input) }));
    if (!response.trigger) throw new Error("No trigger in response");
    return triggerFromProto(response.trigger);
  },

  async delete(id: string): Promise<void> {
    await grpcClient.trigger().deleteTrigger(create(DeleteTriggerRequestSchema, { id }));
  },

  async setEnabled(id: string, enabled: boolean): Promise<Trigger> {
    const response = await grpcClient
      .trigger()
      .setTriggerEnabled(create(SetTriggerEnabledRequestSchema, { id, enabled }));
    if (!response.trigger) throw new Error("No trigger in response");
    return triggerFromProto(response.trigger);
  },

  /** Starts a fire and returns its workflow id. The outcome lands in the event list. */
  async fire(id: string): Promise<string> {
    const response = await grpcClient.trigger().fireTrigger(create(FireTriggerRequestSchema, { id }));
    return response.fireWorkflowId;
  },

  async listEvents(triggerId: string, limit = 50): Promise<TriggerEvent[]> {
    const response = await grpcClient
      .trigger()
      .listTriggerEvents(create(ListTriggerEventsRequestSchema, { triggerId, limit }));
    return response.events.map(eventFromProto);
  },
};
