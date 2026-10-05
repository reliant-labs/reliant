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
 *
 * The same holds for the source: a kind this client cannot edit is carried
 * through as the opaque proto arm (`TriggerSource`), never re-encoded, so
 * renaming a webhook trigger cannot turn it into an empty schedule.
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
  RotateWebhookTokenRequestSchema,
  ScheduleSourceSchema,
  SetTriggerEnabledRequestSchema,
  TriggerDefinitionSchema,
  TriggerEventOutcome,
  TriggerHealthStatus,
  TriggerOverlapPolicy,
  UpdateTriggerRequestSchema,
  type ScheduleSource as ProtoScheduleSource,
  type Trigger as ProtoTrigger,
  type TriggerDefinition as ProtoTriggerDefinition,
  type TriggerEvent as ProtoTriggerEvent,
  type TriggerHealth as ProtoTriggerHealth,
  type WebhookCredential as ProtoWebhookCredential,
} from "../gen/reliant/v1/trigger_pb";
import type { RunDisplayState } from "../gen/reliant/v1/run_pb";

// ============================================
// Frontend types
// ============================================

export type TriggerOutcome = "launched" | "skipped" | "failed" | "unknown";

export type OverlapPolicy = "skip" | "allow";

/**
 * A proto `source` arm other than schedule, exactly as the server sent it.
 * Derived from the generated oneof, so a new arm in trigger.proto widens this
 * type on regeneration with no change here.
 */
export type PassthroughSourceArm = Exclude<ProtoTrigger["source"], { case: "schedule" } | { case: undefined }>;

/**
 * Which kind of source a passthrough arm (or a declared trigger) is. Read
 * structurally from the arm, so it widens with trigger.proto.
 */
export type SourceArmCase = PassthroughSourceArm["case"];

/**
 * What makes a trigger fire: the proto `source` oneof.
 *
 *   - `schedule`: the one kind this client reads and edits.
 *   - `passthrough`: any other arm this client's generated code knows but no
 *     editor handles yet. Carried opaquely, so the full-replacement update
 *     sends it back exactly as stored.
 *   - `unknown`: the server sent an arm this client's generated code predates.
 *     JSON decoding drops an unknown field, so nothing is left to send back,
 *     and the trigger's definition cannot be saved from this client.
 *   - `activation`: the trigger activates the workflow's declared trigger
 *     `workflowTrigger` (research/INTEGRATIONS_V1_BRIEF.md §3a). Its source,
 *     filter and inputs are the declaration's, re-read on every fire, so the
 *     write sends only the name — never an inline copy. `declared` is the
 *     declaration's source as of the last save, for display only.
 */
export type TriggerSource =
  | { kind: "schedule"; schedule: TriggerSchedule }
  | { kind: "passthrough"; arm: PassthroughSourceArm }
  | { kind: "activation"; workflowTrigger: string; declared?: TriggerSource }
  | { kind: "unknown" };

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

/** TriggerHealthStatus; UNSPECIFIED (an older server) reads as unknown. */
export type TriggerHealthStatusKey = "healthy" | "degraded" | "failing" | "broken" | "unknown";

/** The server's read-only health summary. Rules: TriggerHealthStatus in trigger.proto. */
export interface TriggerHealth {
  status: TriggerHealthStatusKey;
  consecutiveFailures: number;
  consecutiveSkips: number;
  /** Empty when the window holds no failure. */
  lastFailureDetail: string;
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
  /** Tell the owner when a run completes (unread, OS notification, Inbox item). */
  notifyOnComplete: boolean;
  /** The project's name, joined by the server. Unset when the project was deleted. */
  projectName?: string;
  /** The daemon's hostname, joined by the server. Unset when it never reported one. */
  daemonName?: string;
  health: TriggerHealth;
  createdAt: string;
  updatedAt: string;
  nextFireAt?: string;
  lastEvent?: TriggerEvent;
  source: TriggerSource;
  /** The workflow-declared trigger this row activates, when it activates one. */
  workflowTrigger?: string;
  /** Launched runs have no machine (only once the server supports it). */
  noMachine?: boolean;
  /** The connection an integration trigger listens through. */
  connectionId?: string;
  /**
   * Webhook triggers: the URL to POST to. A bare `/hooks/<id>` path when the
   * server has no PUBLIC_URL; see webhookUrlForDisplay.
   */
  webhookUrl?: string;
}

/** The trigger's schedule, or undefined for any other kind of source. */
export function triggerSchedule(trigger: Pick<Trigger, "source">): TriggerSchedule | undefined {
  const source = trigger.source.kind === "activation" ? trigger.source.declared : trigger.source;
  return source?.kind === "schedule" ? source.schedule : undefined;
}

/**
 * A source's kind in words, for a kind this client has no editor for yet:
 * the proto arm name ("workflowEvent") read as "Workflow event".
 */
export function sourceKindLabel(source: TriggerSource): string {
  switch (source.kind) {
    case "schedule":
      return "Schedule";
    case "passthrough": {
      // Read structurally: while schedule is the only generated arm the
      // passthrough type is `never`, and it widens as arms are added.
      const armCase = (source.arm as { case: string }).case;
      const words = armCase.replace(/([a-z0-9])([A-Z])/g, "$1 $2").toLowerCase();
      return words.charAt(0).toUpperCase() + words.slice(1);
    }
    case "activation":
      return source.declared ? sourceKindLabel(source.declared) : "Declared trigger";
    case "unknown":
      return "Unknown kind";
  }
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
  /** Replaced on update like every field here, so an edit must send it back. */
  notifyOnComplete: boolean;
  /** Replaced on update too: an edit that does not change it sends the stored one. */
  source: TriggerSource;
  /**
   * Integration triggers: the connection to listen through. Unset on create
   * means the owner's default for the integration.
   */
  connectionId?: string;
  /**
   * Launched runs have no machine (TriggerDefinition.no_machine, daemon-less
   * runs). Mutually exclusive with daemonId. Sent only when this client's
   * generated code has the field; see NO_MACHINE_SUPPORTED.
   */
  noMachine?: boolean;
}

/**
 * Whether this build's generated TriggerDefinition carries `no_machine`
 * (research/INTEGRATIONS_V1_BRIEF.md §3c, stream H). Read from the schema,
 * so the "No machine" choice turns on when the proto lands, with no edit.
 */
export const NO_MACHINE_SUPPORTED: boolean = TriggerDefinitionSchema.fields.some((field) => field.localName === "noMachine");

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

function sourceFromProto(source: ProtoTrigger["source"], workflowTrigger?: string): TriggerSource {
  const inline = inlineSourceFromProto(source);
  // An activation's stored arm is its declaration as of the last save: shown,
  // never sent back (the server re-reads the declaration).
  if (workflowTrigger) return { kind: "activation", workflowTrigger, declared: inline.kind === "unknown" ? undefined : inline };
  return inline;
}

function inlineSourceFromProto(source: ProtoTrigger["source"]): TriggerSource {
  switch (source.case) {
    case "schedule":
      return { kind: "schedule", schedule: scheduleFromProto(source.value) };
    case undefined:
      return { kind: "unknown" };
    default:
      return { kind: "passthrough", arm: source };
  }
}

function healthStatusFromProto(status: TriggerHealthStatus): TriggerHealthStatusKey {
  switch (status) {
    case TriggerHealthStatus.HEALTHY:
      return "healthy";
    case TriggerHealthStatus.DEGRADED:
      return "degraded";
    case TriggerHealthStatus.FAILING:
      return "failing";
    case TriggerHealthStatus.BROKEN:
      return "broken";
    default:
      return "unknown";
  }
}

function healthFromProto(health: ProtoTriggerHealth | undefined): TriggerHealth {
  return {
    status: healthStatusFromProto(health?.status ?? TriggerHealthStatus.UNSPECIFIED),
    consecutiveFailures: health?.consecutiveFailures ?? 0,
    consecutiveSkips: health?.consecutiveSkips ?? 0,
    lastFailureDetail: health?.lastFailureDetail ?? "",
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
    notifyOnComplete: proto.notifyOnComplete,
    projectName: proto.projectName || undefined,
    daemonName: proto.daemonName || undefined,
    health: healthFromProto(proto.health),
    createdAt: proto.createdAt,
    updatedAt: proto.updatedAt,
    nextFireAt: proto.nextFireAt || undefined,
    lastEvent: proto.lastEvent ? eventFromProto(proto.lastEvent) : undefined,
    source: sourceFromProto(proto.source, proto.workflowTrigger || undefined),
    workflowTrigger: proto.workflowTrigger || undefined,
    connectionId: proto.connectionId || undefined,
    webhookUrl: proto.webhookUrl || undefined,
    noMachine: (proto as unknown as { noMachine?: boolean }).noMachine || undefined,
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
  const definition = create(TriggerDefinitionSchema, {
    name: input.name,
    projectId: input.projectId,
    worktreeId: input.worktreeId || undefined,
    enabled: input.enabled,
    workflow: input.workflow,
    presets: input.presets,
    params,
    message: input.message,
    daemonId: input.daemonId,
    notifyOnComplete: input.notifyOnComplete,
    connectionId: input.connectionId || undefined,
  });
  if (input.noMachine && NO_MACHINE_SUPPORTED) {
    (definition as unknown as { noMachine: boolean }).noMachine = true;
    definition.daemonId = "";
  }
  // Assigned rather than passed to create(): a passthrough arm is the decoded
  // message the server sent, and goes back as that same object.
  definition.source = sourceToProto(input.source);
  return definition;
}

function sourceToProto(source: TriggerSource): ProtoTriggerDefinition["source"] {
  switch (source.kind) {
    case "schedule":
      return {
        case: "schedule",
        value: create(ScheduleSourceSchema, {
          cron: source.schedule.cron,
          interval: source.schedule.interval || undefined,
          timezone: source.schedule.timezone,
          overlap: overlapToProto(source.schedule.overlap),
          catchupWindow: source.schedule.catchupWindow || undefined,
        }),
      };
    case "passthrough":
      // Trigger and TriggerDefinition declare the same arms, so the stored arm
      // is already a valid definition arm. Never rebuilt: what was stored goes back.
      return source.arm;
    case "activation":
      // The name only: the declaration supplies source, filter and inputs.
      return { case: "workflowTrigger", value: source.workflowTrigger };
    case "unknown":
      // Sending no source (or an invented one) would replace the stored
      // trigger's source on a full-replacement update.
      throw new Error(
        "This automation's trigger was set up in a newer version of Reliant. Reload the app to edit it.",
      );
  }
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
    notifyOnComplete: trigger.notifyOnComplete,
    source: trigger.source,
    connectionId: trigger.connectionId,
    noMachine: trigger.noMachine,
  };
}

/**
 * A webhook trigger's token, returned exactly once (by create and by
 * rotation). `url` already embeds the token, for senders that cannot set an
 * Authorization header.
 */
export interface WebhookCredential {
  token: string;
  url: string;
}

function credentialFromProto(proto: ProtoWebhookCredential | undefined): WebhookCredential | undefined {
  return proto?.token ? { token: proto.token, url: proto.url } : undefined;
}

/**
 * A webhook URL to show and copy. The server returns a bare `/hooks/<id>`
 * path when its worker has no PUBLIC_URL; that path is relative to the API
 * origin the app talks to, so it is resolved against `apiOrigin`.
 */
export function webhookUrlForDisplay(url: string | undefined, apiOrigin: string): string {
  if (!url) return "";
  if (url.startsWith("/")) {
    try {
      return new URL(url, apiOrigin).toString();
    } catch {
      return url;
    }
  }
  return url;
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

  /** Create, and return the webhook token when the trigger is a webhook (shown once). */
  async createWithCredential(input: TriggerDefinitionInput): Promise<{ trigger: Trigger; webhook?: WebhookCredential }> {
    const response = await grpcClient
      .trigger()
      .createTrigger(create(CreateTriggerRequestSchema, { trigger: definitionToProto(input) }));
    if (!response.trigger) throw new Error("No trigger in response");
    return { trigger: triggerFromProto(response.trigger), webhook: credentialFromProto(response.webhook) };
  },

  /** Replace a webhook trigger's token; the old one stops working at once. */
  async rotateWebhookToken(id: string): Promise<{ trigger: Trigger; webhook?: WebhookCredential }> {
    const response = await grpcClient.trigger().rotateWebhookToken(create(RotateWebhookTokenRequestSchema, { id }));
    if (!response.trigger) throw new Error("No trigger in response");
    return { trigger: triggerFromProto(response.trigger), webhook: credentialFromProto(response.webhook) };
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
