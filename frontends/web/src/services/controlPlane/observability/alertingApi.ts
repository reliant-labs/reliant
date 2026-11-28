// Copyright (c) 2025 Reliant Labs

/**
 * control-plane `controlplane.v1.AlertingService`, mapped into the console's
 * model (./alertingTypes.ts). The only file that knows its wire shape.
 *
 * Every RPC carries the caller's current org, which the server verifies
 * against membership before anything else; mutations need the org admin
 * role. A silence the console creates can only ever mute this org: the server
 * adds the `reliant_org_id` matcher itself and refuses one of ours on any
 * `reliant_*` label.
 *
 * The console's views read and write on AlertingService's generated hooks
 * (hooks/observability-queries.ts, hooks/controlplane/alerting-service-hooks_gen.ts):
 * each call site builds its request here (`*Request`) and maps the answer into
 * the model here (`*Of`). The model-level functions at the end are the same
 * mapping on a transport: the ObservabilityApi contract the shop fixture pack
 * and the test fakes implement.
 */

import type { Transport } from "@connectrpc/connect";
import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { DurationSchema, TimestampSchema, type Duration, type Timestamp } from "@bufbuild/protobuf/wkt";

import {
  AlertingService,
  ReceiverType as WireReceiverType,
  type CreateReceiverRequestSchema,
  type CreateSilenceRequestSchema,
  type ListAlertHistoryRequestSchema,
  type ListAlertHistoryResponse,
  type ListAlertsRequestSchema,
  type ListAlertsResponse,
  type ListNotificationsRequestSchema,
  type ListNotificationsResponse,
  type ListReceiversResponse,
  type ListSilencesRequestSchema,
  type ListSilencesResponse,
  type Receiver as WireReceiver,
  type Silence as WireSilence,
  type TestReceiverResponse,
  type UpdateReceiverRequestSchema,
} from "@/gen/controlplane/services/alerting/v1/alerting_pb";

import { getControlPlaneClient } from "../client";
import { msOf } from "./api";
import type {
  AlertEvent,
  AlertInstance,
  AlertTransition,
  CreateReceiverInput,
  CreateSilenceInput,
  NotificationList,
  Receiver,
  ReceiverType,
  Silence,
  SilenceState,
  TestReceiverResult,
  UpdateReceiverInput,
} from "./alertingTypes";

function client(transport?: Transport) {
  return getControlPlaneClient(AlertingService, transport);
}

function timestamp(ms: number): Timestamp {
  const seconds = Math.floor(ms / 1000);
  return create(TimestampSchema, { seconds: BigInt(seconds), nanos: Math.round((ms - seconds * 1000) * 1e6) });
}

function duration(ms: number): Duration {
  const seconds = Math.floor(ms / 1000);
  return create(DurationSchema, { seconds: BigInt(seconds), nanos: Math.round((ms - seconds * 1000) * 1e6) });
}

const optMs = (ts: Timestamp | undefined) => (ts ? msOf(ts) : null);

const TYPE_FROM_WIRE: Record<number, ReceiverType> = {
  [WireReceiverType.SLACK]: "slack",
  [WireReceiverType.EMAIL]: "email",
  [WireReceiverType.WEBHOOK]: "webhook",
  [WireReceiverType.PAGERDUTY]: "pagerduty",
  [WireReceiverType.RELIANT_INAPP]: "inapp",
};

const TYPE_TO_WIRE: Record<CreateReceiverInput["type"], WireReceiverType> = {
  slack: WireReceiverType.SLACK,
  email: WireReceiverType.EMAIL,
  webhook: WireReceiverType.WEBHOOK,
  pagerduty: WireReceiverType.PAGERDUTY,
};

function mapReceiver(r: WireReceiver): Receiver {
  return {
    id: r.id,
    name: r.name,
    type: TYPE_FROM_WIRE[r.type] ?? "unknown",
    settings: { ...r.settings },
    hasSecret: r.hasSecret,
    createdAtMs: msOf(r.createdAt),
    updatedAtMs: msOf(r.updatedAt),
  };
}

const SILENCE_STATES: readonly SilenceState[] = ["pending", "active", "expired"];

function mapSilence(s: WireSilence): Silence {
  return {
    id: s.id,
    matchers: s.matchers.map((m) => ({ name: m.name, value: m.value, isRegex: m.isRegex, isEqual: m.isEqual })),
    startsAtMs: msOf(s.startsAt),
    endsAtMs: msOf(s.endsAt),
    createdBy: s.createdBy,
    comment: s.comment,
    state: (SILENCE_STATES as readonly string[]).includes(s.state) ? (s.state as SilenceState) : "expired",
  };
}

const TRANSITIONS: readonly AlertTransition[] = ["pending", "firing", "resolved", "inactive", "removed"];

// ── Receivers ────────────────────────────────────────────────────────────────

export function receiversOf(res: ListReceiversResponse): Receiver[] {
  return res.receivers.map(mapReceiver);
}

/** A write's answer: the receiver as it is stored now. */
export function receiverOf(res: { receiver?: WireReceiver }): Receiver {
  return mapReceiver(res.receiver!);
}

export function createReceiverRequest(orgId: string, input: CreateReceiverInput): MessageInitShape<typeof CreateReceiverRequestSchema> {
  return { orgId, name: input.name, type: TYPE_TO_WIRE[input.type], settings: input.settings, secret: input.secret };
}

export function updateReceiverRequest(orgId: string, input: UpdateReceiverInput): MessageInitShape<typeof UpdateReceiverRequestSchema> {
  return { orgId, id: input.id, setSettings: input.settings !== undefined, settings: input.settings ?? {}, secret: input.secret ?? "" };
}

export function testReceiverResultOf(res: TestReceiverResponse): TestReceiverResult {
  return { accepted: res.accepted, receiverLive: res.receiverLive };
}

// ── Silences ─────────────────────────────────────────────────────────────────

export function listSilencesRequest(orgId: string, includeExpired: boolean): MessageInitShape<typeof ListSilencesRequestSchema> {
  return { orgId, includeExpired: includeExpired || undefined };
}

export function silencesOf(res: ListSilencesResponse): Silence[] {
  return res.silences.map(mapSilence);
}

export function createSilenceRequest(orgId: string, input: CreateSilenceInput): MessageInitShape<typeof CreateSilenceRequestSchema> {
  return {
    orgId,
    matchers: input.matchers,
    startsAt: input.startsAtMs !== undefined ? timestamp(input.startsAtMs) : undefined,
    endsAt: timestamp(input.endsAtMs),
    comment: input.comment,
  };
}

export function silenceOf(res: { silence?: WireSilence }): Silence {
  return mapSilence(res.silence!);
}

// ── Alerts and history ───────────────────────────────────────────────────────

export function listAlertsRequest(orgId: string, envId?: string): MessageInitShape<typeof ListAlertsRequestSchema> {
  return { orgId, envId };
}

export function alertsOf(res: ListAlertsResponse): AlertInstance[] {
  return res.alerts.map((a) => ({
    envId: a.envId,
    rule: a.rule,
    fingerprint: a.fingerprint,
    labels: { ...a.labels },
    annotations: { ...a.annotations },
    state: a.state === "firing" ? "firing" : "pending",
    activeAtMs: optMs(a.activeAt),
    firingAtMs: optMs(a.firingAt),
    lastEvalAtMs: optMs(a.lastEvalAt),
    lastValue: a.lastValue,
  }));
}

/**
 * The history since a fixed time (`sinceMs`), or of the last `lookbackMs`
 * before the server's now: a read keyed by its request sends the lookback, so
 * its request stays the same while "the last day" moves (control-plane ignores
 * the lookback when a since is set).
 */
export function listAlertHistoryRequest(
  orgId: string,
  opts: { envId?: string; sinceMs?: number; lookbackMs?: number; limit?: number } = {},
): MessageInitShape<typeof ListAlertHistoryRequestSchema> {
  return {
    orgId,
    envId: opts.envId,
    since: opts.sinceMs !== undefined ? timestamp(opts.sinceMs) : undefined,
    lookback: opts.sinceMs === undefined && opts.lookbackMs !== undefined ? duration(opts.lookbackMs) : undefined,
    limit: opts.limit ?? 200,
  };
}

export function alertHistoryOf(res: ListAlertHistoryResponse): AlertEvent[] {
  return res.events.map((e) => ({
    envId: e.envId,
    rule: e.rule,
    fingerprint: e.fingerprint,
    labels: { ...e.labels },
    transition: (TRANSITIONS as readonly string[]).includes(e.transition) ? (e.transition as AlertTransition) : "inactive",
    value: e.value,
    atMs: msOf(e.at),
  }));
}

// ── In-app notifications ─────────────────────────────────────────────────────

export function listNotificationsRequest(orgId: string, opts: { unreadOnly?: boolean; limit?: number } = {}): MessageInitShape<typeof ListNotificationsRequestSchema> {
  return { orgId, unreadOnly: opts.unreadOnly || undefined, limit: opts.limit ?? 50 };
}

export function notificationsOf(res: ListNotificationsResponse): NotificationList {
  return {
    unread: res.unread,
    notifications: res.notifications.map((n) => ({
      id: n.id,
      envId: n.envId,
      status: n.status === "resolved" ? "resolved" : "firing",
      alertname: n.alertname,
      severity: n.severity,
      labels: { ...n.labels },
      annotations: { ...n.annotations },
      startsAtMs: optMs(n.startsAt),
      receivedAtMs: msOf(n.receivedAt),
      read: n.read,
    })),
  };
}

// ── The model-level contract, on a transport ─────────────────────────────────

export async function listReceivers(orgId: string, transport?: Transport): Promise<Receiver[]> {
  return receiversOf(await client(transport).listReceivers({ orgId }));
}

export async function createReceiver(orgId: string, input: CreateReceiverInput, transport?: Transport): Promise<Receiver> {
  return receiverOf(await client(transport).createReceiver(createReceiverRequest(orgId, input)));
}

export async function updateReceiver(orgId: string, input: UpdateReceiverInput, transport?: Transport): Promise<Receiver> {
  return receiverOf(await client(transport).updateReceiver(updateReceiverRequest(orgId, input)));
}

export async function deleteReceiver(orgId: string, id: string, transport?: Transport): Promise<void> {
  await client(transport).deleteReceiver({ orgId, id });
}

export async function testReceiver(orgId: string, id: string, transport?: Transport): Promise<TestReceiverResult> {
  return testReceiverResultOf(await client(transport).testReceiver({ orgId, id }));
}

export async function listSilences(orgId: string, includeExpired: boolean, transport?: Transport): Promise<Silence[]> {
  return silencesOf(await client(transport).listSilences(listSilencesRequest(orgId, includeExpired)));
}

export async function createSilence(orgId: string, input: CreateSilenceInput, transport?: Transport): Promise<Silence> {
  return silenceOf(await client(transport).createSilence(createSilenceRequest(orgId, input)));
}

export async function expireSilence(orgId: string, id: string, transport?: Transport): Promise<void> {
  await client(transport).expireSilence({ orgId, id });
}

export async function listAlerts(orgId: string, envId?: string, transport?: Transport): Promise<AlertInstance[]> {
  return alertsOf(await client(transport).listAlerts(listAlertsRequest(orgId, envId)));
}

export async function listAlertHistory(
  orgId: string,
  opts: { envId?: string; sinceMs?: number; limit?: number } = {},
  transport?: Transport,
): Promise<AlertEvent[]> {
  return alertHistoryOf(await client(transport).listAlertHistory(listAlertHistoryRequest(orgId, opts)));
}

export async function listNotifications(orgId: string, opts: { unreadOnly?: boolean; limit?: number } = {}, transport?: Transport): Promise<NotificationList> {
  return notificationsOf(await client(transport).listNotifications(listNotificationsRequest(orgId, opts)));
}

/** Marks these read; with no ids, every unread notification of the org. Returns how many changed. */
export async function markNotificationsRead(orgId: string, ids: string[] = [], transport?: Transport): Promise<number> {
  const res = await client(transport).markNotificationsRead({ orgId, ids });
  return res.marked;
}
