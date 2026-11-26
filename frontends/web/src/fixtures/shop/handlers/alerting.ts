// Copyright (c) 2025 Reliant Labs

/**
 * The shop pack's AlertingService reads: alerts, their history, silences,
 * receivers and the in-app notifications — shop's story (model/alerting.ts)
 * encoded on the wire, so the console's real client
 * (services/controlPlane/observability/alertingApi.ts) decodes it.
 *
 * Silences, receivers and "mark read" are writes: in the demo and the tour the
 * transport's read-only guard refuses them before they reach a handler. They
 * are encoded here too, over the same model, for a test fake that answers
 * them (test/observabilityFixtures.tsx).
 *
 * A history read names a `lookback` (the last day before the server's now)
 * rather than a fixed since: it is resolved as control-plane resolves it,
 * against the clock the pack answers at (`clock`, shopStory.ts demoNow by
 * default), never the wall clock.
 */

import type { MessageInitShape } from "@bufbuild/protobuf";
import { mockService, type MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { AlertingService, ReceiverType as WireReceiverType, type ReceiverSchema, type SilenceSchema } from "@/gen/controlplane/services/alerting/v1/alerting_pb";
import type { CreateReceiverInput, Receiver, ReceiverType, Silence } from "@/services/controlPlane/observability/alertingTypes";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { createShopApi } from "../model/shopApi";
import { demoNow } from "../shopStory";
import { completed } from "./defaults";
import { instantOf, msOf, msOfDuration, ts, tsOpt, type PackClock } from "./wire";

const WIRE_RECEIVER_TYPE: Record<ReceiverType, WireReceiverType> = {
  slack: WireReceiverType.SLACK,
  email: WireReceiverType.EMAIL,
  webhook: WireReceiverType.WEBHOOK,
  pagerduty: WireReceiverType.PAGERDUTY,
  inapp: WireReceiverType.RELIANT_INAPP,
  unknown: WireReceiverType.UNSPECIFIED,
};

const RECEIVER_TYPE_OF_WIRE = Object.fromEntries(Object.entries(WIRE_RECEIVER_TYPE).map(([model, wire]) => [wire, model])) as Record<number, ReceiverType>;

const wireReceiver = (r: Receiver): MessageInitShape<typeof ReceiverSchema> => ({
  id: r.id,
  name: r.name,
  type: WIRE_RECEIVER_TYPE[r.type],
  settings: { ...r.settings },
  hasSecret: r.hasSecret,
  createdAt: ts(r.createdAtMs),
  updatedAt: ts(r.updatedAtMs),
});

const wireSilence = (s: Silence): MessageInitShape<typeof SilenceSchema> => ({
  id: s.id,
  matchers: s.matchers.map((m) => ({ ...m })),
  startsAt: ts(s.startsAtMs),
  endsAt: ts(s.endsAtMs),
  createdBy: s.createdBy,
  comment: s.comment,
  state: s.state,
});

export function alertingHandlers(fixtures: ObservabilityApi = createShopApi(), { clock = demoNow }: { clock?: PackClock } = {}): MockHandlers {
  return completed(AlertingService, mockService(AlertingService, {
    listAlerts: async (req) => {
      const alerts = await fixtures.listAlerts(req.orgId, req.envId);
      return {
        alerts: alerts.map((a) => ({
          envId: a.envId,
          rule: a.rule,
          fingerprint: a.fingerprint,
          labels: { ...a.labels },
          annotations: { ...a.annotations },
          state: a.state,
          activeAt: tsOpt(a.activeAtMs),
          firingAt: tsOpt(a.firingAtMs),
          lastEvalAt: tsOpt(a.lastEvalAtMs),
          lastValue: a.lastValue,
        })),
      };
    },
    listAlertHistory: async (req) => {
      // A since wins over a lookback, as control-plane has it.
      const lookback = msOfDuration(req.lookback);
      const sinceMs = req.since ? msOf(req.since) : lookback !== undefined ? instantOf(clock) - lookback : undefined;
      const events = await fixtures.listAlertHistory(req.orgId, { envId: req.envId, sinceMs, limit: req.limit });
      return { events: events.map((e) => ({ envId: e.envId, rule: e.rule, fingerprint: e.fingerprint, labels: { ...e.labels }, transition: e.transition, value: e.value, at: ts(e.atMs) })) };
    },
    listSilences: async (req) => {
      const silences = await fixtures.listSilences(req.orgId, req.includeExpired ?? false);
      return { silences: silences.map(wireSilence) };
    },
    listReceivers: async (req) => {
      const receivers = await fixtures.listReceivers(req.orgId);
      return { receivers: receivers.map(wireReceiver) };
    },
    listNotifications: async (req) => {
      const res = await fixtures.listNotifications(req.orgId, { unreadOnly: req.unreadOnly, limit: req.limit });
      return {
        unread: res.unread,
        notifications: res.notifications.map((n) => ({
          id: n.id,
          envId: n.envId,
          status: n.status,
          alertname: n.alertname,
          severity: n.severity,
          labels: { ...n.labels },
          annotations: { ...n.annotations },
          startsAt: tsOpt(n.startsAtMs),
          receivedAt: ts(n.receivedAtMs),
          read: n.read,
        })),
      };
    },
    createSilence: async (req) => ({
      silence: wireSilence(
        await fixtures.createSilence(req.orgId, {
          matchers: req.matchers.map((m) => ({ name: m.name, value: m.value, isRegex: m.isRegex, isEqual: m.isEqual })),
          startsAtMs: req.startsAt ? msOf(req.startsAt) : undefined,
          endsAtMs: msOf(req.endsAt),
          comment: req.comment,
        })
      ),
    }),
    expireSilence: async (req) => {
      await fixtures.expireSilence(req.orgId, req.id);
      return {};
    },
    createReceiver: async (req) => ({
      receiver: wireReceiver(
        await fixtures.createReceiver(req.orgId, {
          name: req.name,
          type: RECEIVER_TYPE_OF_WIRE[req.type] as CreateReceiverInput["type"],
          settings: { ...req.settings },
          secret: req.secret,
        })
      ),
    }),
    // Settings are replaced only when the request says so (set_settings); an empty secret keeps the stored one.
    updateReceiver: async (req) => ({
      receiver: wireReceiver(await fixtures.updateReceiver(req.orgId, { id: req.id, settings: req.setSettings ? { ...req.settings } : undefined, secret: req.secret })),
    }),
    deleteReceiver: async (req) => {
      await fixtures.deleteReceiver(req.orgId, req.id);
      return {};
    },
    testReceiver: async (req) => await fixtures.testReceiver(req.orgId, req.id),
    markNotificationsRead: async (req) => ({ marked: await fixtures.markNotificationsRead(req.orgId, [...req.ids]) }),
  }));
}
