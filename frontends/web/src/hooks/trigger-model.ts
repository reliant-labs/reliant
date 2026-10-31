// Copyright (c) 2025 Reliant Labs

/**
 * TriggerService's frontend model — automations. Pure: no React, no client.
 * The reads and writes are the generated hooks, composed in
 * `trigger-queries.ts`; this module is what they `select` through and what
 * their requests are built from.
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

import { create, fromJson, type JsonObject, type JsonValue, type MessageInitShape } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";

import { jsToProtoValue, protoValueToJs } from "../api/proto-utils";
import { replayEventToProto, type ReplayEventInput } from "./chat-model";
import {
  ActingIdentity,
  type EnableWorkflowRequestSchema,
  type EnableWorkflowResponse,
  type ListReplayEventsResponse,
  type PreviewReplayRequestSchema,
  type PreviewReplayResponse,
  type ResolveTriggerSendersResponse,
  AutomationDraftSchema,
  ScheduleSourceSchema,
  TriggerDefinitionSchema,
  TriggerEventKind,
  TriggerEventOutcome,
  TriggerHealthStatus,
  TriggerSenderSchema,
  TriggerOverlapPolicy,
  WorkflowTriggerOutcome,
  IntegrationSourceSchema,
  WebhookSourceSchema,
  WorkflowEventSourceSchema,
  type AutomationDraft as ProtoAutomationDraft,
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

/** A past event a workflow could be run against again. */
export interface ReplayCandidate {
  /** The declared trigger the event arrived through. */
  triggerName: string;
  event: TriggerEvent;
  /** The event's kind as ReplayEvent.kind names it ("integration", "webhook", ...). */
  kind: string;
  /** The recorded payload, to show and edit. */
  payload: JsonObject;
}

const REPLAY_KINDS: Partial<Record<TriggerEventKind, string>> = {
  [TriggerEventKind.SCHEDULE]: "schedule",
  [TriggerEventKind.WEBHOOK]: "webhook",
  [TriggerEventKind.INTEGRATION]: "integration",
  [TriggerEventKind.WORKFLOW_EVENT]: "workflow_event",
};

function eventKindToWire(kind: TriggerEventKind): string {
  return REPLAY_KINDS[kind] ?? "";
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
  /** trigger.sender: who the source says sent the event. Unset when none was recorded. */
  sender?: TriggerSender;
}

/** trigger.sender, as a firing recorded it. */
export interface TriggerSender {
  /** "slack", "github", "email", "sms", "webhook", "workflow", "schedule" or "user". */
  kind: string;
  /** The provider's stable id: a Slack user id, a GitHub user id (digits), an email address. What "Only from" stores. */
  id: string;
  /** For people (a GitHub login). Never authorize on it. */
  displayName: string;
  verified: boolean;
}

/** One person ResolveTriggerSenders found. */
export interface ResolvedSender {
  /** The handle or id as it was asked. */
  query: string;
  /** trigger.sender.id for the person: on GitHub the numeric user id. */
  senderId: string;
  /** Their name now (a GitHub login). */
  displayName: string;
}

/** TriggerHealthStatus; UNSPECIFIED (an older server) reads as unknown. */
export type TriggerHealthStatusKey = "healthy" | "degraded" | "failing" | "broken" | "needs_setup" | "unknown";

/** The server's read-only health summary. Rules: TriggerHealthStatus in trigger.proto. */
/** Who an automation's integration writes speak as. */
export type ActingIdentityKey = "owner" | "app";

function actingIdentityFromProto(identity: ActingIdentity): ActingIdentityKey {
  return identity === ActingIdentity.APP ? "app" : "owner";
}

function actingIdentityToProto(identity: ActingIdentityKey | undefined): ActingIdentity {
  switch (identity) {
    case "app":
      return ActingIdentity.APP;
    case "owner":
      return ActingIdentity.OWNER;
    default:
      return ActingIdentity.UNSPECIFIED;
  }
}

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
  changes?: Array<{ id: string; userId: string; chatId: string; runId: string; toolCallId: string; operation: string; beforeState: string; afterState: string; createdAt: string }>;
  projectId: string;
  worktreeId?: string;
  enabled: boolean;
  workflow: string;
  presets: Record<string, string>;
  params: Record<string, unknown>;
  message: string;
  /** The daemon every launched run executes on. Empty exactly when noMachine. */
  daemonId: string;
  /** Runs have no machine: server-side tools only (research/DAEMONLESS_RUNS.md). */
  noMachine: boolean;
  /** Tell the owner when a run succeeds (unread + OS notification; never an Inbox item). */
  notifyOnComplete: boolean;
  /** Who the runs' integration writes speak as: the owner, or the Reliant bot. */
  actingIdentity: ActingIdentityKey;
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
  /** The connection an integration trigger listens through. */
  connectionId?: string;
  /**
   * The CEL filter an event must pass. An activation's is its declaration's,
   * shown here as the server last projected it and never sent back.
   */
  filter?: string;
  /**
   * Webhook triggers: the URL to POST to. A bare `/hooks/<id>` path when the
   * server has no PUBLIC_URL; see webhookUrlForDisplay.
   */
  webhookUrl?: string;
  /**
   * Webhook triggers: Reliant delivers to it itself (a `source: alerting`
   * webhook on a hosted deployment), so there is no URL or token to configure
   * and rotating re-connects rather than showing a token.
   */
  webhookManaged: boolean;
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
   * Required unless noMachine. One of the caller's daemons and, when the
   * project is installed on any daemon, one that has it installed
   * (validateTriggerDaemon).
   */
  daemonId: string;
  /**
   * Runs have no machine. The explicit choice an empty daemonId means; the
   * server refuses a workflow that needs a machine (FailedPrecondition).
   */
  noMachine?: boolean;
  /** Replaced on update like every field here, so an edit must send it back. */
  notifyOnComplete: boolean;
  /**
   * Who the runs' integration writes speak as. Unset: the owner on create,
   * unchanged on update.
   */
  actingIdentity?: ActingIdentityKey;
  /** Replaced on update too: an edit that does not change it sends the stored one. */
  source: TriggerSource;
  /**
   * Integration triggers: the connection to listen through. Unset on create
   * means the owner's default for the integration.
   */
  connectionId?: string;
  /**
   * An inline event source's CEL filter. Replaced on update, so an edit sends
   * the stored one back; never set for an activation, whose filter is its
   * declaration's (the server refuses one).
   */
  filter?: string;
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
    case TriggerHealthStatus.NEEDS_SETUP:
      return "needs_setup";
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
    sender: event.sender
      ? { kind: event.sender.kind, id: event.sender.id, displayName: event.sender.displayName, verified: event.sender.verified }
      : undefined,
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
    changes: proto.changes ?? [],
    projectId: proto.projectId,
    worktreeId: proto.worktreeId || undefined,
    enabled: proto.enabled,
    workflow: proto.workflow,
    presets: { ...proto.presets },
    params,
    message: proto.message,
    daemonId: proto.daemonId,
    noMachine: proto.noMachine,
    notifyOnComplete: proto.notifyOnComplete,
    actingIdentity: actingIdentityFromProto(proto.actingIdentity),
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
    filter: proto.filter || undefined,
    webhookUrl: proto.webhookUrl || undefined,
    webhookManaged: proto.webhookManaged,
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
    daemonId: input.noMachine ? "" : input.daemonId,
    noMachine: input.noMachine ?? false,
    notifyOnComplete: input.notifyOnComplete,
    actingIdentity: actingIdentityToProto(input.actingIdentity),
    connectionId: input.connectionId || undefined,
    filter: input.source.kind === "activation" ? "" : (input.filter ?? "").trim(),
  });
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
 * The inline source for a trigger row that carries its own WHEN — a personal
 * trigger on a workflow whose definition can't gain a declaration (a
 * built-in). The palette builds the same declaration shape for both models,
 * so one editor serves both; this is where the personal one becomes a row's
 * source. Webhook, integration and workflow-event arms are built as real
 * messages, because the update carries them back verbatim.
 */
export function inlineTriggerSource(source: { case?: string; value?: unknown } | undefined): TriggerSource {
  const value = (source?.value ?? {}) as Record<string, unknown>;
  switch (source?.case) {
    case "schedule": {
      const schedule = value as { cron?: string[]; interval?: string; timezone?: string };
      return {
        kind: "schedule",
        schedule: {
          cron: [...(schedule.cron ?? [])].filter((c) => c.trim() !== ""),
          interval: schedule.interval || undefined,
          timezone: schedule.timezone || "UTC",
          overlap: "skip",
        },
      };
    }
    case "webhook":
      return { kind: "passthrough", arm: { case: "webhook", value: create(WebhookSourceSchema, value as MessageInitShape<typeof WebhookSourceSchema>) } };
    case "integration":
      return { kind: "passthrough", arm: { case: "integration", value: create(IntegrationSourceSchema, value as MessageInitShape<typeof IntegrationSourceSchema>) } };
    case "workflowEvent":
      return {
        kind: "passthrough",
        arm: { case: "workflowEvent", value: create(WorkflowEventSourceSchema, value as MessageInitShape<typeof WorkflowEventSourceSchema>) },
      };
    default:
      return { kind: "unknown" };
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
    noMachine: trigger.noMachine,
    notifyOnComplete: trigger.notifyOnComplete,
    actingIdentity: trigger.actingIdentity,
    source: trigger.source,
    connectionId: trigger.connectionId,
    // An activation's filter is its declaration's: not the row's to send.
    filter: trigger.source.kind === "activation" ? undefined : trigger.filter,
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
// Workflow-level enable
// ============================================

/** What EnableWorkflow did for one declared trigger. */
export type WorkflowTriggerOutcomeKind =
  | "enabled"
  | "resumed"
  | "updated"
  | "alreadyEnabled"
  | "failed"
  | "notEnabled"
  | "leftEnabled";

export interface WorkflowTriggerResult {
  trigger: string;
  outcome: WorkflowTriggerOutcomeKind;
  message: string;
  activation?: Trigger;
  /** A webhook's token: shown once, for a newly enabled webhook trigger. */
  webhook?: WebhookCredential;
}

export interface EnableWorkflowResult {
  /** True only when every declared trigger is now running. */
  enabled: boolean;
  results: WorkflowTriggerResult[];
}

export interface WorkflowTriggerOverrideInput {
  trigger: string;
  name?: string;
  /** A daemon id, or `noMachine`. Setting either replaces the shared machine. */
  daemonId?: string;
  noMachine?: boolean;
  connectionId?: string;
  message?: string;
  params?: Record<string, unknown>;
}

export interface EnableWorkflowInput {
  workflow: string;
  projectId: string;
  worktreeId?: string;
  daemonId?: string;
  noMachine?: boolean;
  /** integration id -> connection id */
  connectionIds?: Record<string, string>;
  params?: Record<string, unknown>;
  presets?: Record<string, string>;
  notifyOnComplete?: boolean;
  message?: string;
  namePrefix?: string;
  overrides?: WorkflowTriggerOverrideInput[];
  /** Unset: the owner for a new activation, unchanged for an existing one. */
  actingIdentity?: ActingIdentityKey;
}

const OUTCOME_KIND: Partial<Record<WorkflowTriggerOutcome, WorkflowTriggerOutcomeKind>> = {
  [WorkflowTriggerOutcome.ENABLED]: "enabled",
  [WorkflowTriggerOutcome.RESUMED]: "resumed",
  [WorkflowTriggerOutcome.UPDATED]: "updated",
  [WorkflowTriggerOutcome.ALREADY_ENABLED]: "alreadyEnabled",
  [WorkflowTriggerOutcome.FAILED]: "failed",
  [WorkflowTriggerOutcome.NOT_ENABLED]: "notEnabled",
  [WorkflowTriggerOutcome.LEFT_ENABLED]: "leftEnabled",
};

function paramsToProto(params: Record<string, unknown> | undefined) {
  return Object.fromEntries(Object.entries(params ?? {}).map(([key, value]) => [key, jsToProtoValue(value)]));
}

/**
 * The EnableWorkflow request for a setup: every declared trigger, all or
 * none, with the user's choice of project, machine and accounts.
 */
export function enableWorkflowRequest(input: EnableWorkflowInput): MessageInitShape<typeof EnableWorkflowRequestSchema> {
  return {
    workflow: input.workflow,
    projectId: input.projectId,
    worktreeId: input.worktreeId,
    daemonId: input.daemonId ?? "",
    noMachine: input.noMachine ?? false,
    connectionIds: input.connectionIds ?? {},
    params: paramsToProto(input.params),
    presets: input.presets ?? {},
    notifyOnComplete: input.notifyOnComplete ?? false,
    message: input.message ?? "",
    namePrefix: input.namePrefix ?? "",
    actingIdentity: actingIdentityToProto(input.actingIdentity),
    overrides: (input.overrides ?? []).map((o) => ({
      trigger: o.trigger,
      name: o.name,
      daemonId: o.daemonId,
      noMachine: o.noMachine,
      connectionId: o.connectionId,
      message: o.message,
      params: paramsToProto(o.params),
    })),
  };
}

// ============================================
// Drafts ("Describe it", and the chat's draft_automation tool)
// ============================================

/**
 * A proposed automation, never stored: only the person's Create in the
 * automation dialog makes a trigger from it. The server sets only the WHEN
 * and the WHAT — where it runs and as whom are the person's choices.
 */
export interface AutomationDraft {
  summary: string;
  assumptions: string[];
  name: string;
  workflow: string;
  message: string;
  params: Record<string, unknown>;
  presets: Record<string, string>;
  filter: string;
  /** Unknown when the draft has no source this client can read. */
  source: TriggerSource;
}

export function automationDraftFromProto(proto: ProtoAutomationDraft): AutomationDraft {
  const def = proto.definition;
  const params: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(def?.params ?? {})) params[key] = protoValueToJs(value);
  // A draft never activates a declaration (workflow_trigger): that arm reads as unknown.
  let source: TriggerSource = def && def.source.case !== "workflowTrigger" ? inlineSourceFromProto(def.source) : { kind: "unknown" };
  // A draft with no zone means "the person's own": keep it empty for the
  // dialog to fill with the browser's, rather than the stored default UTC.
  if (source.kind === "schedule" && def?.source.case === "schedule" && !def.source.value.timezone) {
    source = { kind: "schedule", schedule: { ...source.schedule, timezone: "" } };
  }
  return {
    summary: proto.summary,
    assumptions: [...proto.assumptions],
    name: def?.name ?? "",
    workflow: def?.workflow ?? "",
    message: def?.message ?? "",
    params,
    presets: { ...(def?.presets ?? {}) },
    filter: def?.filter ?? "",
    source,
  };
}

/** A draft from its protojson (the draft_automation tool's result metadata), or undefined. */
export function automationDraftFromJson(json: unknown): AutomationDraft | undefined {
  if (!json || typeof json !== "object") return undefined;
  try {
    return automationDraftFromProto(fromJson(AutomationDraftSchema, json as JsonValue, { ignoreUnknownFields: true }));
  } catch {
    return undefined;
  }
}



/**
 * What EnableWorkflow did. A refusal is a normal response (`enabled: false`)
 * whose `results` say which trigger failed and why; only a malformed request
 * or an unreachable server throws.
 */
export function enableWorkflowResultFromProto(response: EnableWorkflowResponse): EnableWorkflowResult {
  return {
    enabled: response.enabled,
    results: response.results.map((r) => ({
      trigger: r.trigger,
      outcome: OUTCOME_KIND[r.outcome] ?? "failed",
      message: r.message,
      activation: r.activation ? triggerFromProto(r.activation) : undefined,
      webhook: credentialFromProto(r.webhook),
    })),
  };
}

/** A created or rotated trigger, with its webhook token when it has one (shown once). */
export function triggerWithCredentialFromProto(response: {
  trigger?: ProtoTrigger;
  webhook?: ProtoWebhookCredential;
}): { trigger: Trigger; webhook?: WebhookCredential } {
  if (!response.trigger) throw new Error("No trigger in response");
  return { trigger: triggerFromProto(response.trigger), webhook: credentialFromProto(response.webhook) };
}

/** The recent events a workflow's declared triggers received, to run it against again. */
export function replayCandidatesFromProto(response: ListReplayEventsResponse): ReplayCandidate[] {
  return response.events.map((candidate) => ({
    triggerName: candidate.triggerName,
    event: eventFromProto(candidate.event!),
    kind: eventKindToWire(candidate.event?.kind ?? TriggerEventKind.UNSPECIFIED),
    payload: candidate.event?.payload ?? {},
  }));
}

/** The PreviewReplay request: a past (or hand-written) event, mapped as a fire of it would be. */
export function previewReplayRequest(
  projectId: string,
  workflow: string,
  replay: ReplayEventInput,
  sender?: TriggerSender,
): MessageInitShape<typeof PreviewReplayRequestSchema> {
  return {
    projectId,
    workflow,
    replay: replayEventToProto(replay),
    sender: sender
      ? create(TriggerSenderSchema, { kind: sender.kind, id: sender.id, displayName: sender.displayName, verified: sender.verified })
      : undefined,
  };
}

export function previewFromProto(response: PreviewReplayResponse): { inputs: Record<string, unknown>; prompt: string } {
  const inputs: Record<string, unknown> = {};
  for (const [name, value] of Object.entries(response.inputs)) inputs[name] = protoValueToJs(value);
  return { inputs, prompt: response.prompt };
}

export function resolvedSendersFromProto(response: ResolveTriggerSendersResponse): ResolvedSender[] {
  return response.senders.map((s) => ({ query: s.query, senderId: s.senderId, displayName: s.displayName }));
}
