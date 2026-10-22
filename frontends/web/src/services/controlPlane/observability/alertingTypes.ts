// Copyright (c) 2025 Reliant Labs

/**
 * The console's model of an org's alerting (control-plane AlertingService).
 *
 * Like ./types.ts these are not the wire types: ./alertingApi.ts is the one
 * file that maps the wire into these shapes. Times are epoch milliseconds.
 *
 * WHAT IS — AND IS NOT — MANAGED HERE. Rules are declared in the project's
 * code (`forge.Observability.alerts` in KCL) and arrive through each
 * environment's release shape; nothing here edits a rule. What an org manages
 * in the console is where notifications go (receivers), when they pause
 * (silences), and what already happened (alerts, history, notifications).
 */

export type ReceiverType = "slack" | "email" | "webhook" | "pagerduty" | "inapp" | "unknown";

export interface Receiver {
  id: string;
  /** Referenced by the routes in the code (`forge.AlertRoute.receiver`). */
  name: string;
  type: ReceiverType;
  /** Non-secret settings: slack {channel}, email {to}, webhook {url, send_resolved}, pagerduty {severity}. */
  settings: Record<string, string>;
  /** A secret is stored; its value is never returned. */
  hasSecret: boolean;
  createdAtMs: number;
  updatedAtMs: number;
}

export interface CreateReceiverInput {
  name: string;
  type: Exclude<ReceiverType, "inapp" | "unknown">;
  settings: Record<string, string>;
  /** Write-only: the Slack webhook URL, the PagerDuty routing key, a webhook bearer. */
  secret: string;
}

export interface UpdateReceiverInput {
  id: string;
  /** Replaces the whole settings map. */
  settings?: Record<string, string>;
  /** Replaces the stored secret when non-empty. */
  secret?: string;
}

export interface TestReceiverResult {
  /** Alertmanager accepted the test alert for routing; delivery is asynchronous. */
  accepted: boolean;
  /** The receiver is already in the configuration Alertmanager runs. A new one may take a minute. */
  receiverLive: boolean;
}

export interface Matcher {
  name: string;
  value: string;
  isRegex: boolean;
  /** False means "not equal". */
  isEqual: boolean;
}

export type SilenceState = "pending" | "active" | "expired";

export interface Silence {
  id: string;
  /** Includes the server-added `reliant_org_id` matcher. */
  matchers: Matcher[];
  startsAtMs: number;
  endsAtMs: number;
  createdBy: string;
  comment: string;
  state: SilenceState;
}

export interface CreateSilenceInput {
  matchers: Matcher[];
  /** Absent: now. */
  startsAtMs?: number;
  endsAtMs: number;
  comment: string;
}

export interface AlertInstance {
  envId: string;
  /** The rule's name (`alertname`). */
  rule: string;
  fingerprint: string;
  labels: Record<string, string>;
  annotations: Record<string, string>;
  state: "pending" | "firing";
  activeAtMs: number | null;
  firingAtMs: number | null;
  lastEvalAtMs: number | null;
  lastValue: number;
}

export type AlertTransition = "pending" | "firing" | "resolved" | "inactive" | "removed";

export interface AlertEvent {
  envId: string;
  rule: string;
  fingerprint: string;
  labels: Record<string, string>;
  transition: AlertTransition;
  value: number;
  atMs: number;
}

export interface Notification {
  id: string;
  envId: string;
  status: "firing" | "resolved";
  alertname: string;
  severity: string;
  labels: Record<string, string>;
  annotations: Record<string, string>;
  startsAtMs: number | null;
  receivedAtMs: number;
  read: boolean;
}

export interface NotificationList {
  notifications: Notification[];
  unread: number;
}

/**
 * One alert rule as an environment DECLARES it (forge release.ShapeAlert, read
 * from the env's recorded shape). `source` says who wrote it: the project
 * (`user`, a forge.AlertRule) or forge's generated defaults (`default`, tuned
 * through forge.Alerts.defaults by `defaultId`).
 */
export interface DeclaredAlertRule {
  alert: string;
  expr: string;
  for: string;
  interval: string;
  keepFiringFor: string;
  labels: Record<string, string>;
  annotations: Record<string, string>;
  source: "user" | "default";
  defaultId: string;
}

/** One declared route (forge.AlertRoute): labels → a receiver NAME. */
export interface DeclaredAlertRoute {
  match: Record<string, string>;
  receiver: string;
  continue: boolean;
}
