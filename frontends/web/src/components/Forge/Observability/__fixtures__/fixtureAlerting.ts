// Copyright (c) 2025 Reliant Labs

/**
 * An in-memory AlertingService for the fixture app: rules declared by the
 * environments' shapes (one of the project's own, forge's defaults), what is
 * firing now, a day of history, silences, receivers and in-app notifications.
 *
 * It is stateful per instance, like the real service: creating a silence or a
 * receiver, expiring one, sending a test or marking notifications read is
 * visible to the next read, so a test or the preview can walk a whole flow.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type {
  AlertEvent,
  AlertInstance,
  DeclaredAlertRule,
  Notification,
  Receiver,
  Silence,
} from "@/services/controlPlane/observability/alertingTypes";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";

import type { ObsDeclared } from "../obsPlace";

/** The fixture environments' ids — the same as fixtureApi's FIXTURE_ENVS. */
const DEV = "env-dev";
const STAGING = "env-staging";
const PROD = "env-prod";

const userRule: DeclaredAlertRule = {
  alert: "CheckoutP95High",
  expr: "SELECT ServiceName AS service, quantile(0.95)(Duration) / 1e9 AS value FROM otel_traces WHERE SpanName = 'orders.v1.OrdersService/CreateOrder' AND Timestamp > now() - INTERVAL 5 MINUTE GROUP BY service HAVING value > 0.8",
  for: "10m",
  interval: "1m",
  keepFiringFor: "",
  labels: { severity: "critical", team: "payments" },
  annotations: { summary: "checkout p95 is {{ $value }}s", runbook_url: "https://wiki.example.com/runbooks/checkout" },
  source: "user",
  defaultId: "",
};

const defaultRule = (alert: string, defaultId: string, severity: string, summary: string, forDuration = "5m"): DeclaredAlertRule => ({
  alert,
  expr: `SELECT ServiceName AS service, countIf(StatusCode = 'Error') / count() AS value FROM otel_traces WHERE SpanKind = 'Server' GROUP BY service HAVING value > 0.05 -- forge default ${defaultId}`,
  for: forDuration,
  interval: "1m",
  keepFiringFor: "10m",
  labels: { severity },
  annotations: { summary },
  source: "default",
  defaultId,
});

const DEFAULTS: DeclaredAlertRule[] = [
  defaultRule("ServiceErrorRateHigh", "error-rate", "critical", "{{ $labels.service }} fails {{ $value | humanizePercentage }} of requests"),
  defaultRule("ServiceSilent", "service-silent", "warning", "{{ $labels.service }} received no requests for 15 minutes", "15m"),
  defaultRule("WorkloadOOMKilled", "oom-killed", "warning", "{{ $labels.container }} was OOMKilled"),
];

/** What each fixture environment declares: prod adds the project's own rule; dev opted out of observability. */
export const FIXTURE_DECLARED: Record<string, ObsDeclared> = {
  [DEV]: { alerts: [], alertRoutes: [], observability: { enabled: false, disabled: ["traces", "metrics", "logs", "profiles", "metadata", "browser"] } },
  // No routes declared: the shape records forge's built-in pair, in-app then Slack.
  [STAGING]: {
    alerts: DEFAULTS,
    alertRoutes: [
      { match: {}, receiver: "reliant-inapp", continue: true },
      { match: {}, receiver: "slack", continue: false },
    ],
    observability: null,
  },
  [PROD]: {
    alerts: [userRule, ...DEFAULTS],
    alertRoutes: [
      { match: { severity: "critical" }, receiver: "oncall-slack", continue: true },
      { match: {}, receiver: "reliant-inapp", continue: false },
    ],
    observability: null,
  },
};

export interface AlertingFixtureOptions {
  /** "not-configured": no Alertmanager on this control plane. "viewer": a member who is not an admin. */
  alerting?: "ok" | "not-configured" | "viewer" | "quiet";
}

type AlertingApi = Pick<
  ObservabilityApi,
  | "listAlerts"
  | "listAlertHistory"
  | "listSilences"
  | "createSilence"
  | "expireSilence"
  | "listReceivers"
  | "createReceiver"
  | "updateReceiver"
  | "deleteReceiver"
  | "testReceiver"
  | "listNotifications"
  | "markNotificationsRead"
>;

export function createAlertingFixture(
  options: AlertingFixtureOptions,
  call: <T>(rpc: string, request: unknown, answer: () => T) => Promise<T>,
  now: () => number = Date.now
): AlertingApi {
  const mode = options.alerting ?? "ok";
  const t0 = now();
  const min = 60_000;

  const firing: AlertInstance[] =
    mode === "quiet"
      ? []
      : [
          {
            envId: PROD,
            rule: "CheckoutP95High",
            fingerprint: "a1f0c3",
            labels: { alertname: "CheckoutP95High", severity: "critical", team: "payments", service: "api" },
            annotations: { summary: "checkout p95 is 1.42s", runbook_url: "https://wiki.example.com/runbooks/checkout" },
            state: "firing",
            activeAtMs: t0 - 23 * min,
            firingAtMs: t0 - 13 * min,
            lastEvalAtMs: t0 - 20_000,
            lastValue: 1.42,
          },
          {
            envId: PROD,
            rule: "WorkloadOOMKilled",
            fingerprint: "b7d912",
            labels: { alertname: "WorkloadOOMKilled", severity: "warning", container: "api", workload: "api" },
            annotations: { summary: "api was OOMKilled" },
            state: "firing",
            activeAtMs: t0 - 16 * min,
            firingAtMs: t0 - 11 * min,
            lastEvalAtMs: t0 - 20_000,
            lastValue: 3,
          },
          {
            envId: STAGING,
            rule: "ServiceErrorRateHigh",
            fingerprint: "c4e7aa",
            labels: { alertname: "ServiceErrorRateHigh", severity: "critical", service: "worker" },
            annotations: { summary: "worker fails 7.3% of requests" },
            state: "pending",
            activeAtMs: t0 - 2 * min,
            firingAtMs: null,
            lastEvalAtMs: t0 - 20_000,
            lastValue: 0.073,
          },
        ];

  const event = (envId: string, rule: string, fingerprint: string, labels: Record<string, string>, transition: AlertEvent["transition"], value: number, minutesAgo: number): AlertEvent => ({
    envId,
    rule,
    fingerprint,
    labels,
    transition,
    value,
    atMs: t0 - minutesAgo * min,
  });
  const history: AlertEvent[] = [
    event(PROD, "CheckoutP95High", "a1f0c3", { severity: "critical", service: "api" }, "firing", 1.42, 13),
    event(PROD, "WorkloadOOMKilled", "b7d912", { severity: "warning", container: "api" }, "firing", 3, 11),
    event(PROD, "WorkloadOOMKilled", "b7d912", { severity: "warning", container: "api" }, "pending", 1, 16),
    event(PROD, "CheckoutP95High", "a1f0c3", { severity: "critical", service: "api" }, "pending", 0.91, 23),
    event(STAGING, "ServiceErrorRateHigh", "c4e7aa", { severity: "critical", service: "worker" }, "pending", 0.073, 2),
    event(PROD, "ServiceSilent", "d00e11", { severity: "warning", service: "worker" }, "resolved", 0, 95),
    event(PROD, "ServiceSilent", "d00e11", { severity: "warning", service: "worker" }, "firing", 0, 140),
  ].sort((a, b) => b.atMs - a.atMs);

  let silences: Silence[] = [
    {
      id: "sil-1",
      matchers: [
        { name: "alertname", value: "ServiceSilent", isRegex: false, isEqual: true },
        { name: "service", value: "worker", isRegex: false, isEqual: true },
        { name: "reliant_org_id", value: "org-fixture", isRegex: false, isEqual: true },
      ],
      startsAtMs: t0 - 30 * min,
      endsAtMs: t0 + 3 * 60 * min,
      createdBy: "ada@example.com",
      comment: "worker is paused for the queue migration",
      state: "active",
    },
    {
      id: "sil-0",
      matchers: [
        { name: "severity", value: "info", isRegex: false, isEqual: true },
        { name: "reliant_org_id", value: "org-fixture", isRegex: false, isEqual: true },
      ],
      startsAtMs: t0 - 3 * 24 * 60 * min,
      endsAtMs: t0 - 2 * 24 * 60 * min,
      createdBy: "ada@example.com",
      comment: "noisy during the load test",
      state: "expired",
    },
  ];

  let receivers: Receiver[] = [
    { id: "rcv-inapp", name: "reliant-inapp", type: "inapp", settings: {}, hasSecret: false, createdAtMs: t0 - 30 * 24 * 60 * min, updatedAtMs: t0 - 30 * 24 * 60 * min },
    { id: "rcv-slack", name: "oncall-slack", type: "slack", settings: { channel: "#oncall" }, hasSecret: true, createdAtMs: t0 - 9 * 24 * 60 * min, updatedAtMs: t0 - 9 * 24 * 60 * min },
    { id: "rcv-email", name: "team-email", type: "email", settings: { to: "team@example.com" }, hasSecret: false, createdAtMs: t0 - 2 * 24 * 60 * min, updatedAtMs: t0 - 2 * 24 * 60 * min },
  ];

  let notifications: Notification[] =
    mode === "quiet"
      ? []
      : [
          { id: "n3", envId: PROD, status: "firing", alertname: "WorkloadOOMKilled", severity: "warning", labels: { container: "api" }, annotations: { summary: "api was OOMKilled" }, startsAtMs: t0 - 11 * min, receivedAtMs: t0 - 11 * min, read: false },
          { id: "n2", envId: PROD, status: "firing", alertname: "CheckoutP95High", severity: "critical", labels: { service: "api" }, annotations: { summary: "checkout p95 is 1.42s" }, startsAtMs: t0 - 13 * min, receivedAtMs: t0 - 13 * min, read: false },
          { id: "n1", envId: PROD, status: "resolved", alertname: "ServiceSilent", severity: "warning", labels: { service: "worker" }, annotations: { summary: "worker received requests again" }, startsAtMs: t0 - 140 * min, receivedAtMs: t0 - 95 * min, read: true },
        ];

  const gate = <T>(rpc: string, request: unknown, mutate: boolean, answer: () => T) =>
    call(rpc, request, () => {
      if (mode === "not-configured") throw new ConnectError("alerting is not configured on this server", Code.Unavailable);
      if (mutate && mode === "viewer") throw new ConnectError("this needs the organization admin role", Code.PermissionDenied);
      return answer();
    });

  return {
    listAlerts: (orgId, envId) => gate("ListAlerts", { orgId, envId }, false, () => firing.filter((a) => !envId || a.envId === envId)),
    listAlertHistory: (orgId, opts = {}) =>
      gate("ListAlertHistory", { orgId, ...opts }, false, () =>
        mode === "quiet" ? [] : history.filter((e) => (!opts.envId || e.envId === opts.envId) && (opts.sinceMs === undefined || e.atMs >= opts.sinceMs))
      ),
    listSilences: (orgId, includeExpired) =>
      gate("ListSilences", { orgId, includeExpired }, false, () => silences.filter((s) => includeExpired || s.state !== "expired")),
    createSilence: (orgId, input) =>
      gate("CreateSilence", { orgId, ...input }, true, () => {
        if (input.matchers.some((m) => m.name.startsWith("reliant_"))) {
          throw new ConnectError("a silence may not name a reliant_* label: the org scope is added for you", Code.InvalidArgument);
        }
        const silence: Silence = {
          id: `sil-${silences.length + 1}`,
          matchers: [...input.matchers, { name: "reliant_org_id", value: orgId, isRegex: false, isEqual: true }],
          startsAtMs: input.startsAtMs ?? now(),
          endsAtMs: input.endsAtMs,
          createdBy: "you@example.com",
          comment: input.comment,
          state: (input.startsAtMs ?? now()) > now() ? "pending" : "active",
        };
        silences = [silence, ...silences];
        return silence;
      }),
    expireSilence: (orgId, id) =>
      gate("ExpireSilence", { orgId, id }, true, () => {
        silences = silences.map((s) => (s.id === id ? { ...s, state: "expired", endsAtMs: now() } : s));
      }),
    listReceivers: (orgId) => gate("ListReceivers", { orgId }, false, () => receivers),
    createReceiver: (orgId, input) =>
      gate("CreateReceiver", { orgId, ...input, secret: input.secret ? "(set)" : "" }, true, () => {
        if (receivers.some((r) => r.name === input.name)) throw new ConnectError(`a receiver named "${input.name}" already exists`, Code.AlreadyExists);
        const receiver: Receiver = {
          id: `rcv-${receivers.length + 1}`,
          name: input.name,
          type: input.type,
          settings: input.settings,
          hasSecret: !!input.secret,
          createdAtMs: now(),
          updatedAtMs: now(),
        };
        receivers = [...receivers, receiver];
        return receiver;
      }),
    updateReceiver: (orgId, input) =>
      gate("UpdateReceiver", { orgId, ...input, secret: input.secret ? "(set)" : "" }, true, () => {
        const current = receivers.find((r) => r.id === input.id);
        if (!current) throw new ConnectError("receiver not found", Code.NotFound);
        const next: Receiver = { ...current, settings: input.settings ?? current.settings, hasSecret: current.hasSecret || !!input.secret, updatedAtMs: now() };
        receivers = receivers.map((r) => (r.id === input.id ? next : r));
        return next;
      }),
    deleteReceiver: (orgId, id) =>
      gate("DeleteReceiver", { orgId, id }, true, () => {
        const current = receivers.find((r) => r.id === id);
        if (current?.name === "oncall-slack") {
          throw new ConnectError("prod still routes to oncall-slack: delete the route from the code first", Code.FailedPrecondition);
        }
        receivers = receivers.filter((r) => r.id !== id);
      }),
    testReceiver: (orgId, id) =>
      gate("TestReceiver", { orgId, id }, true, () => {
        const current = receivers.find((r) => r.id === id);
        // A receiver created in this session is not in Alertmanager's running configuration yet.
        return { accepted: true, receiverLive: !!current && current.createdAtMs < t0 };
      }),
    listNotifications: (orgId, opts = {}) =>
      gate("ListNotifications", { orgId, ...opts }, false, () => ({
        notifications: notifications.filter((n) => !opts.unreadOnly || !n.read),
        unread: notifications.filter((n) => !n.read).length,
      })),
    markNotificationsRead: (orgId, ids = []) =>
      gate("MarkNotificationsRead", { orgId, ids }, false, () => {
        let marked = 0;
        notifications = notifications.map((n) => {
          if (n.read || (ids.length && !ids.includes(n.id))) return n;
          marked += 1;
          return { ...n, read: true };
        });
        return marked;
      }),
  };
}
