// Copyright (c) 2025 Reliant Labs

/**
 * shop's alerting: the rules and routes its shape declares
 * (consoleScenario.ts shape(): OrdersErrorRateHigh → oncall, the ticket rules
 * → github-issues), both receivers set up, and exactly ONE alert firing — the
 * incident's (DEMO_INCIDENT.alert, prod, since 9 minutes after v1.8.3's
 * promotion). One unread notification for it; the rest of the history is
 * resolved.
 */

import type { AlertEvent, AlertInstance, Notification, Receiver, Silence } from "@/services/controlPlane/observability/alertingTypes";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import { DAY, DEMO_INCIDENT, DEMO_ORG, HOUR, incidentStartMs, MINUTE } from "../shopStory";
import { demoWriteRefused, type AlertingKeys } from "./shopApi";

const PROD = "env-prod";
const STAGING = "env-staging";

export function shopAlerting(now: number): Pick<ObservabilityApi, AlertingKeys> {
  const firingAt = incidentStartMs(now) + 9 * MINUTE;
  const labels = { alertname: DEMO_INCIDENT.alert, severity: "page", service: "api", rpc_method: "CreateOrder" };
  const summary = `${(DEMO_INCIDENT.failShare * 100).toFixed(1)}% of CreateOrder requests are failing`;
  const firing: AlertInstance[] = [
    {
      envId: PROD,
      rule: DEMO_INCIDENT.alert,
      fingerprint: "c0f3e1a9",
      labels,
      annotations: { summary },
      state: "firing",
      activeAtMs: firingAt - 5 * MINUTE,
      firingAtMs: firingAt,
      lastEvalAtMs: now - 20_000,
      lastValue: DEMO_INCIDENT.failShare,
    },
  ];
  const event = (envId: string, rule: string, fingerprint: string, l: Record<string, string>, transition: AlertEvent["transition"], value: number, atMs: number): AlertEvent => ({ envId, rule, fingerprint, labels: l, transition, value, atMs });
  const backlog = { severity: "ticket", consumer: "fulfilment" };
  const history: AlertEvent[] = [
    event(PROD, DEMO_INCIDENT.alert, "c0f3e1a9", labels, "firing", DEMO_INCIDENT.failShare, firingAt),
    event(PROD, DEMO_INCIDENT.alert, "c0f3e1a9", labels, "pending", 0.093, firingAt - 5 * MINUTE),
    event(STAGING, "WorkerBacklog", "7b21d0e4", backlog, "resolved", 120, now - 5.6 * DAY + 35 * MINUTE),
    event(STAGING, "WorkerBacklog", "7b21d0e4", backlog, "firing", 640, now - 5.6 * DAY + 10 * MINUTE),
  ].sort((a, b) => b.atMs - a.atMs);
  const silences: Silence[] = [
    {
      id: "sil-loadtest",
      matchers: [
        { name: "alertname", value: "WorkerBacklog", isRegex: false, isEqual: true },
        { name: "reliant_org_id", value: DEMO_ORG, isRegex: false, isEqual: true },
      ],
      startsAtMs: now - 12 * DAY,
      endsAtMs: now - 12 * DAY + 2 * HOUR,
      createdBy: "priya@example.com",
      comment: "load test of the fulfilment queue in staging",
      state: "expired",
    },
  ];
  const receivers: Receiver[] = [
    { id: "rcv-oncall", name: "oncall", type: "slack", settings: { channel: "#shop-oncall" }, hasSecret: true, createdAtMs: now - 38 * DAY, updatedAtMs: now - 38 * DAY },
    { id: "rcv-github", name: "github-issues", type: "webhook", settings: { url: "https://api.github.com/repos/acme/shop/dispatches", send_resolved: "false" }, hasSecret: true, createdAtMs: now - 38 * DAY, updatedAtMs: now - 21 * DAY },
  ];
  const notifications: Notification[] = [
    { id: "ntf-incident", envId: PROD, status: "firing", alertname: DEMO_INCIDENT.alert, severity: "page", labels: { service: "api" }, annotations: { summary }, startsAtMs: firingAt, receivedAtMs: firingAt, read: false },
    { id: "ntf-backlog", envId: STAGING, status: "resolved", alertname: "WorkerBacklog", severity: "ticket", labels: { consumer: "fulfilment" }, annotations: { summary: "The worker caught up on order events" }, startsAtMs: now - 5.6 * DAY + 10 * MINUTE, receivedAtMs: now - 5.6 * DAY + 35 * MINUTE, read: true },
  ];
  return {
    listAlerts: async (_orgId, envId) => firing.filter((a) => !envId || a.envId === envId),
    listAlertHistory: async (_orgId, opts = {}) => history.filter((e) => (!opts.envId || e.envId === opts.envId) && (opts.sinceMs === undefined || e.atMs >= opts.sinceMs)),
    listSilences: async (_orgId, includeExpired) => silences.filter((s) => includeExpired || s.state !== "expired"),
    listReceivers: async () => receivers,
    listNotifications: async (_orgId, opts = {}) => ({ notifications: notifications.filter((n) => !opts.unreadOnly || !n.read), unread: notifications.filter((n) => !n.read).length }),
    createSilence: demoWriteRefused,
    expireSilence: demoWriteRefused,
    createReceiver: demoWriteRefused,
    updateReceiver: demoWriteRefused,
    deleteReceiver: demoWriteRefused,
    testReceiver: demoWriteRefused,
    markNotificationsRead: demoWriteRefused,
  };
}
