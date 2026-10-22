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
 */

import { create } from "@bufbuild/protobuf";
import { TimestampSchema, type Timestamp } from "@bufbuild/protobuf/wkt";

import {
  AlertingService,
  ReceiverType as WireReceiverType,
  type Receiver as WireReceiver,
  type Silence as WireSilence,
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

function client() {
  return getControlPlaneClient(AlertingService);
}

function timestamp(ms: number): Timestamp {
  const seconds = Math.floor(ms / 1000);
  return create(TimestampSchema, { seconds: BigInt(seconds), nanos: Math.round((ms - seconds * 1000) * 1e6) });
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

export async function listReceivers(orgId: string): Promise<Receiver[]> {
  const res = await client().listReceivers({ orgId });
  return res.receivers.map(mapReceiver);
}

export async function createReceiver(orgId: string, input: CreateReceiverInput): Promise<Receiver> {
  const res = await client().createReceiver({
    orgId,
    name: input.name,
    type: TYPE_TO_WIRE[input.type],
    settings: input.settings,
    secret: input.secret,
  });
  return mapReceiver(res.receiver!);
}

export async function updateReceiver(orgId: string, input: UpdateReceiverInput): Promise<Receiver> {
  const res = await client().updateReceiver({
    orgId,
    id: input.id,
    setSettings: input.settings !== undefined,
    settings: input.settings ?? {},
    secret: input.secret ?? "",
  });
  return mapReceiver(res.receiver!);
}

export async function deleteReceiver(orgId: string, id: string): Promise<void> {
  await client().deleteReceiver({ orgId, id });
}

export async function testReceiver(orgId: string, id: string): Promise<TestReceiverResult> {
  const res = await client().testReceiver({ orgId, id });
  return { accepted: res.accepted, receiverLive: res.receiverLive };
}

// ── Silences ─────────────────────────────────────────────────────────────────

export async function listSilences(orgId: string, includeExpired: boolean): Promise<Silence[]> {
  const res = await client().listSilences({ orgId, includeExpired: includeExpired || undefined });
  return res.silences.map(mapSilence);
}

export async function createSilence(orgId: string, input: CreateSilenceInput): Promise<Silence> {
  const res = await client().createSilence({
    orgId,
    matchers: input.matchers,
    startsAt: input.startsAtMs !== undefined ? timestamp(input.startsAtMs) : undefined,
    endsAt: timestamp(input.endsAtMs),
    comment: input.comment,
  });
  return mapSilence(res.silence!);
}

export async function expireSilence(orgId: string, id: string): Promise<void> {
  await client().expireSilence({ orgId, id });
}

// ── Alerts and history ───────────────────────────────────────────────────────

export async function listAlerts(orgId: string, envId?: string): Promise<AlertInstance[]> {
  const res = await client().listAlerts({ orgId, envId });
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

export async function listAlertHistory(
  orgId: string,
  opts: { envId?: string; sinceMs?: number; limit?: number } = {}
): Promise<AlertEvent[]> {
  const res = await client().listAlertHistory({
    orgId,
    envId: opts.envId,
    since: opts.sinceMs !== undefined ? timestamp(opts.sinceMs) : undefined,
    limit: opts.limit ?? 200,
  });
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

export async function listNotifications(orgId: string, opts: { unreadOnly?: boolean; limit?: number } = {}): Promise<NotificationList> {
  const res = await client().listNotifications({ orgId, unreadOnly: opts.unreadOnly || undefined, limit: opts.limit ?? 50 });
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

/** Marks these read; with no ids, every unread notification of the org. Returns how many changed. */
export async function markNotificationsRead(orgId: string, ids: string[] = []): Promise<number> {
  const res = await client().markNotificationsRead({ orgId, ids });
  return res.marked;
}
