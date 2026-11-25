// Copyright (c) 2025 Reliant Labs

/**
 * The shop pack's AlertingService reads: alerts, their history, silences,
 * receivers and the in-app notifications — shop's story (model/alerting.ts)
 * encoded on the wire, so the console's real client
 * (services/controlPlane/observability/alertingApi.ts) decodes it.
 *
 * Silences, receivers and "mark read" are writes: the transport's read-only
 * guard refuses them before they reach a handler.
 */

import { mockService, type MockHandlers } from "@reliantlabs/forge-web-runtime/mock-transport";

import { AlertingService, ReceiverType as WireReceiverType } from "@/gen/controlplane/services/alerting/v1/alerting_pb";
import type { ReceiverType } from "@/services/controlPlane/observability/alertingTypes";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { createShopApi } from "../model/shopApi";
import { completed } from "./defaults";
import { msOf, ts, tsOpt } from "./wire";

const WIRE_RECEIVER_TYPE: Record<ReceiverType, WireReceiverType> = {
  slack: WireReceiverType.SLACK,
  email: WireReceiverType.EMAIL,
  webhook: WireReceiverType.WEBHOOK,
  pagerduty: WireReceiverType.PAGERDUTY,
  inapp: WireReceiverType.RELIANT_INAPP,
  unknown: WireReceiverType.UNSPECIFIED,
};

export function alertingHandlers(fixtures: ObservabilityApi = createShopApi()): MockHandlers {
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
      const events = await fixtures.listAlertHistory(req.orgId, { envId: req.envId, sinceMs: req.since ? msOf(req.since) : undefined, limit: req.limit });
      return { events: events.map((e) => ({ envId: e.envId, rule: e.rule, fingerprint: e.fingerprint, labels: { ...e.labels }, transition: e.transition, value: e.value, at: ts(e.atMs) })) };
    },
    listSilences: async (req) => {
      const silences = await fixtures.listSilences(req.orgId, req.includeExpired ?? false);
      return {
        silences: silences.map((s) => ({
          id: s.id,
          matchers: s.matchers.map((m) => ({ ...m })),
          startsAt: ts(s.startsAtMs),
          endsAt: ts(s.endsAtMs),
          createdBy: s.createdBy,
          comment: s.comment,
          state: s.state,
        })),
      };
    },
    listReceivers: async (req) => {
      const receivers = await fixtures.listReceivers(req.orgId);
      return {
        receivers: receivers.map((r) => ({
          id: r.id,
          name: r.name,
          type: WIRE_RECEIVER_TYPE[r.type],
          settings: { ...r.settings },
          hasSecret: r.hasSecret,
          createdAt: ts(r.createdAtMs),
          updatedAt: ts(r.updatedAtMs),
        })),
      };
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
  }));
}
