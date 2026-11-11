// Copyright (c) 2025 Reliant Labs

/**
 * shop's error inbox, release health and activity (IssuesService,
 * ObservabilityService.GetReleaseHealth, ActivityService), from the story
 * (shopStory.ts):
 *
 *   - ONE open issue: the incident (DEMO_INCIDENT) — new in v1.8.3, prod,
 *     api, assigned to maya. Its counts are the failed requests the traffic
 *     model says prod had (demoStats), and its events are those requests'
 *     traces (busyApp.ts failedIncidentRequests), the toured one first.
 *   - Closed history beside it: issues resolved in earlier releases, and an
 *     ignored deprecation warning in dev.
 *   - Release health judged per promotion of the ledger (demoLedger, the same
 *     ids DeployService answers with): prod's v1.8.3 FAILS on the incident,
 *     every other promotion passes, with before/after RED from demoStats.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type { ReleaseHealth } from "@/services/controlPlane/observability/deliveryTypes";
import type { Issue, IssueDetail, IssueEvent, IssueStatus, IssueTimelineEntry, IssueTrendPoint, IssueUser } from "@/services/controlPlane/observability/issuesTypes";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { TimeWindow } from "@/services/controlPlane/observability/types";
import { NO_CLIENT } from "@/services/controlPlane/observability/client";

import { failedIncidentRequests } from "../busyApp";
import {
  DAY,
  DEMO_BACKENDS,
  DEMO_CAST,
  DEMO_INCIDENT,
  DEMO_READER,
  DEMO_TOURED_TRACE,
  demoLedger,
  demoPerson,
  demoStats,
  HOUR,
  incidentStartMs,
  MINUTE,
} from "../shopStory";
import { shopActivity } from "./activity";
import { demoWriteRefused, type IssueKeys } from "./shopApi";

const userOf = (handle: string): IssueUser => ({ id: demoPerson(handle)!.id, name: handle });

interface Seed {
  id: string;
  platform: "server" | "browser";
  envId: string;
  envName: string;
  service: string;
  title: string;
  culprit: string;
  exceptionType: string;
  status: IssueStatus;
  firstSeenMs: number;
  firstVersion: string;
  lastSeenMs: number;
  lastVersion: string;
  resolvedAtMs: number;
  resolvedVersion: string;
  assignee: IssueUser | null;
  /** Events per hour while it was seen (the incident's come from the traffic model instead). */
  rate: number;
  frames: IssueDetail["frames"];
}

function seeds(now: number): Seed[] {
  const base = { resolvedAtMs: 0, resolvedVersion: "", assignee: null };
  const go = (fn: string, file: string, line: number) => ({ function: fn, file, line, column: 0, inApp: true });
  const connect = { function: "connectrpc.com/connect.(*Handler).ServeHTTP", file: "connect/handler.go", line: 236, column: 0, inApp: false };
  return [
    {
      ...base,
      id: DEMO_INCIDENT.issueId,
      platform: "server",
      envId: "env-prod",
      envName: "prod",
      service: "api",
      title: DEMO_INCIDENT.error,
      culprit: "(*Client).Charge in internal/payments/client.go",
      exceptionType: DEMO_INCIDENT.errorType,
      status: "new",
      firstSeenMs: incidentStartMs(now) + 2 * MINUTE,
      firstVersion: DEMO_INCIDENT.release,
      lastSeenMs: now - MINUTE,
      lastVersion: DEMO_INCIDENT.release,
      assignee: userOf("maya"),
      rate: 0,
      frames: [
        go("(*Client).Charge", "internal/payments/client.go", 88),
        go("(*Service).chargeWithRetry", "internal/handlers/orders/charge.go", 41),
        go("(*Service).CreateOrder", "internal/handlers/orders/rpc_create_order.go", 57),
        connect,
      ],
    },
    {
      ...base,
      id: "iss-inventory-lock",
      platform: "server",
      envId: "env-prod",
      envName: "prod",
      service: "inventory",
      title: 'Reserve: could not obtain lock on row in relation "stock"',
      culprit: "(*Store).Reserve in internal/inventory/store.go",
      exceptionType: "*pgconn.PgError",
      status: "resolved",
      firstSeenMs: now - 9 * DAY,
      firstVersion: "v1.8.1",
      lastSeenMs: now - 5 * DAY - 3 * HOUR,
      lastVersion: "v1.8.1",
      resolvedAtMs: now - 5 * DAY + -2 * HOUR,
      resolvedVersion: "v1.8.2",
      rate: 0.8,
      frames: [go("(*Store).Reserve", "internal/inventory/store.go", 112), go("(*Service).Reserve", "internal/handlers/inventory/rpc_reserve.go", 33), connect],
    },
    {
      ...base,
      id: "iss-confirmation-status",
      platform: "browser",
      envId: "env-prod",
      envName: "prod",
      service: "shop-web",
      title: "TypeError: order.status is undefined",
      culprit: "OrderStatus in src/pages/confirmation/OrderStatus.tsx",
      exceptionType: "TypeError",
      status: "resolved",
      firstSeenMs: now - 5 * DAY + HOUR,
      firstVersion: "v1.8.2",
      lastSeenMs: now - 5 * DAY + 5 * HOUR,
      lastVersion: "v1.8.2",
      resolvedAtMs: now - 4 * DAY,
      resolvedVersion: "v1.8.2",
      assignee: userOf("priya"),
      rate: 1.5,
      frames: [
        { function: "OrderStatus", file: "src/pages/confirmation/OrderStatus.tsx", line: 24, column: 31, inApp: true },
        { function: "ConfirmationPage", file: "src/pages/confirmation/index.tsx", line: 52, column: 9, inApp: true },
      ],
    },
    {
      ...base,
      id: "iss-dev-deprecated",
      platform: "server",
      envId: "env-dev",
      envName: "dev",
      service: "api",
      title: "orders.v1.Order.legacy_total is deprecated",
      culprit: "protovalidate in internal/handlers/orders/validate.go",
      exceptionType: "",
      status: "ignored",
      firstSeenMs: now - 20 * DAY,
      firstVersion: "v1.8.1",
      lastSeenMs: now - 12 * MINUTE,
      lastVersion: "v1.8.4",
      rate: 0.5,
      frames: [go("protovalidate", "internal/handlers/orders/validate.go", 19)],
    },
  ];
}

const isIncident = (s: Pick<Seed, "id">) => s.id === DEMO_INCIDENT.issueId;

/** Hourly counts over the last day of `window`. The incident's are the traffic model's failed requests. */
function trendOf(seed: Seed, window: TimeWindow, now: number): IssueTrendPoint[] {
  const end = Math.min(window.endMs, now);
  const first = Math.floor((end - 23 * HOUR) / HOUR) * HOUR;
  const out: IssueTrendPoint[] = [];
  for (let hour = first; hour < end; hour += HOUR) {
    const from = Math.max(hour, seed.firstSeenMs);
    const to = Math.min(hour + HOUR, seed.lastSeenMs + MINUTE, end);
    if (to <= from) {
      out.push({ hourMs: hour, events: 0 });
      continue;
    }
    const events = isIncident(seed)
      ? demoStats({ startMs: from, endMs: to, envIds: [seed.envId], service: seed.service, now }).errors
      : Math.max(1, Math.round((seed.rate * (to - from)) / HOUR));
    out.push({ hourMs: hour, events });
  }
  return out;
}

function issueOf(seed: Seed, window: TimeWindow, now: number): Issue {
  const trend = trendOf(seed, window, now);
  const events = trend.reduce((a, p) => a + p.events, 0);
  return {
    id: seed.id,
    platform: seed.platform,
    envId: seed.envId,
    envName: seed.envName,
    service: seed.service,
    fingerprint: `fp-${seed.id.slice(4)}`,
    title: seed.title,
    culprit: seed.culprit,
    exceptionType: seed.exceptionType,
    status: seed.status,
    firstSeenMs: seed.firstSeenMs,
    firstVersion: seed.firstVersion,
    firstRelease: seed.firstVersion,
    lastSeenMs: seed.lastSeenMs,
    lastVersion: seed.lastVersion,
    events,
    // A server issue counts the traces it failed; each failed request is one.
    affected: seed.platform === "server" ? events : Math.max(events ? 1 : 0, Math.round(events * 0.7)),
    trend,
    resolvedAtMs: seed.resolvedAtMs,
    resolvedVersion: seed.resolvedVersion,
    regressedAtMs: 0,
    ignoredUntilMs: 0,
    mergedInto: "",
    symbolicated: true,
    version: 1,
    assignee: seed.assignee,
    incidentKey: "",
    relatedCount: 0,
  };
}

/** The incident's events: its failed requests, newest first, the toured trace leading. */
function incidentEvents(now: number, limit: number): IssueEvent[] {
  const failed = failedIncidentRequests(now, incidentStartMs(now), now, limit);
  return failed.map((r) => ({
    timeMs: r.startMs,
    traceId: r.traceId,
    spanId: r.chargeSpanId,
    serviceVersion: DEMO_INCIDENT.release,
    message: DEMO_INCIDENT.error,
    attributes: { "rpc.method": r.method, "http.response.status_code": "504", "payments.attempts": "2" },
    sessionId: r.sessionId,
    urlPath: "",
    userId: r.userId,
    country: "",
    region: "",
    city: "",
    client: NO_CLIENT,
  }));
}

function otherEvents(seed: Seed): IssueEvent[] {
  return Array.from({ length: 4 }, (_, i): IssueEvent => ({
    timeMs: seed.lastSeenMs - i * 41 * MINUTE,
    traceId: "",
    spanId: "",
    serviceVersion: seed.lastVersion,
    message: seed.title,
    attributes: seed.platform === "browser" ? { "url.path": "/orders/confirmation" } : ({} as Record<string, string>),
    sessionId: "",
    urlPath: seed.platform === "browser" ? "/orders/confirmation" : "",
    userId: "",
    country: "",
    region: "",
    city: "",
    client: NO_CLIENT,
  }));
}

/** The incident issue's discussion: opened by the platform, assigned by jon, diagnosed by maya. */
function timelineOf(seed: Seed, now: number): IssueTimelineEntry[] {
  const SYSTEM = { kind: "system" as const, id: "", name: "" };
  const actor = (handle: string) => ({ kind: "user" as const, id: demoPerson(handle)!.id, name: handle });
  const opened: IssueTimelineEntry = { id: `tl-${seed.id}-open`, timeMs: seed.firstSeenMs, issueId: seed.id, actor: SYSTEM, body: { kind: "status", from: null, to: "new", version: seed.firstVersion, ignoredUntilMs: 0 } };
  if (!isIncident(seed)) {
    const closed: IssueTimelineEntry[] = seed.status === "resolved" && seed.assignee
      ? [{ id: `tl-${seed.id}-resolve`, timeMs: seed.resolvedAtMs, issueId: seed.id, actor: actor(seed.assignee.name), body: { kind: "status", from: "new", to: "resolved", version: seed.resolvedVersion, ignoredUntilMs: 0 } }]
      : [];
    return [opened, ...closed];
  }
  const comment = (id: string, handle: string, body: string, ago: number): IssueTimelineEntry => ({
    id,
    timeMs: now - ago,
    issueId: seed.id,
    actor: actor(handle),
    body: { kind: "comment", comment: { id: `cmt-${id}`, issueId: seed.id, author: userOf(handle), body, createdMs: now - ago, updatedMs: now - ago } },
  });
  return [
    opened,
    { id: "tl-incident-assign", timeMs: now - 35 * MINUTE, issueId: seed.id, actor: actor("jon"), body: { kind: "assignment", from: null, to: userOf("maya") } },
    comment(
      "tl-incident-diagnosis",
      "maya",
      "v1.8.3 retries the charge once after **1.5s**, inside CreateOrder's **3s** deadline. When the provider is slow, the retry runs out the clock and the order fails. v1.8.4 gives the charge its own deadline and retry budget; it's in dev.",
      31 * MINUTE,
    ),
    comment("tl-incident-staging", "jon", "Staging never saw it: it talks to the provider's sandbox, which answers in ~200ms.", 28 * MINUTE),
  ];
}

/** Release health for one promotion of the ledger: prod's v1.8.3 fails on the incident; every other promotion passes. */
/** Each issue's environment and first sighting: what "New issues by release" counts. */
export function shopIssueFirstSeen(now: number): { envId: string; firstSeenMs: number; firstVersion: string }[] {
  return seeds(now).map(({ envId, firstSeenMs, firstVersion }) => ({ envId, firstSeenMs, firstVersion }));
}

/** GetReleaseHealth over shop's ledger. No promotion id judges the environment's running release, as the server does. */
export function shopReleaseHealth(now: number, envId: string, promotionId: string, widthMs: number): ReleaseHealth {
  const ledger = demoLedger(now);
  const promotion = promotionId
    ? ledger.find((p) => p.promotionId === promotionId && p.envId === envId)
    : ledger.filter((p) => p.envId === envId && !p.targets.length).at(-1);
  if (!promotion) throw new ConnectError("promotion not found", Code.NotFound);
  const same = ledger.filter((p) => p.envId === envId && !p.targets.length);
  const previous = same.filter((p) => p.atMs < promotion.atMs).at(-1) ?? null;
  const next = same.find((p) => p.atMs > promotion.atMs) ?? null;
  const at = promotion.atMs;
  const before = { startMs: Math.max(at - widthMs, previous?.atMs ?? -Infinity), endMs: at };
  const after = { startMs: at, endMs: Math.min(at + widthMs, next?.atMs ?? Infinity, now) };
  const failing = promotion.gate !== "none" && promotion.gate.status === "failed";
  const stats = (w: TimeWindow, service: string) => {
    const s = demoStats({ ...w, envIds: [envId], service, now });
    return { requests: s.requests, errorRate: s.errorRate, p50Ms: s.p50Ms, p95Ms: s.p95Ms, p99Ms: s.p99Ms };
  };
  const services = DEMO_BACKENDS.map((service) => ({ service, before: stats(before, service), after: stats(after, service), versionShare: 1 }));
  const api = services.find((s) => s.service === "api")!;
  const seed = seeds(now).find(isIncident)!;
  return {
    promotion: { promotionId: promotion.promotionId, version: promotion.version, gitCommit: promotion.commit, promotedAtMs: at, previousVersion: previous?.version ?? "" },
    before,
    after,
    afterComplete: at + widthMs <= Math.min(next?.atMs ?? Infinity, now),
    services,
    newIssues: failing ? [issueOf(seed, after, now)] : [],
    regressedIssues: [],
    firingAlerts: failing ? [{ rule: DEMO_INCIDENT.alert, fingerprint: "c0f3e1a9", firingAtMs: at + 9 * MINUTE, labels: { service: "api", severity: "page", env: "prod" } }] : [],
    workloadFindings: [],
    verdict: failing ? "fail" : "pass",
    findings: failing
      ? [
          {
            code: "error_rate",
            service: "api",
            value: api.after!.errorRate - api.before!.errorRate,
            threshold: 0.005,
            policy: "fail",
            message: `api's error rate rose ${((api.after!.errorRate - api.before!.errorRate) * 100).toFixed(1)} points (limit 0.5): CreateOrder and Checkout are timing out on the payment charge.`,
          },
          {
            code: "latency",
            service: "api",
            value: api.after!.p95Ms / Math.max(1, api.before!.p95Ms),
            threshold: 1.25,
            policy: "warn",
            message: `api's p95 is ${(api.after!.p95Ms / Math.max(1, api.before!.p95Ms)).toFixed(1)}× what it was (limit 1.25×).`,
          },
          { code: "new_issue", service: "api", value: issueOf(seed, after, now).events, threshold: 3, policy: "fail", message: `A new issue opened in this release: ${DEMO_INCIDENT.error}.` },
          { code: "alert_firing", service: "api", value: 1, threshold: 1, policy: "fail", message: `${DEMO_INCIDENT.alert} opened 9 minutes after the promotion and is still firing.` },
        ]
      : [],
    thresholds: [
      { name: "max_error_rate_increase", value: "0.005", source: "release_shape" },
      { name: "max_p95_ratio", value: "1.25", source: "default" },
      { name: "min_requests", value: "5", source: "release_shape" },
      { name: "new_issue_min_events", value: "3", source: "release_shape" },
    ],
    notEvaluated: [],
  };
}

export function shopIssues(now: number): Pick<ObservabilityApi, IssueKeys> {
  const all = seeds(now);
  const seedOf = (id: string) => {
    const seed = all.find((s) => s.id === id);
    if (!seed) throw new ConnectError("issue not found", Code.NotFound);
    return seed;
  };
  const reader = demoPerson(DEMO_READER)!;
  return {
    listIssues: async (req) => {
      const statuses = req.statuses.length ? req.statuses : (["new", "unresolved", "regressed"] as IssueStatus[]);
      const text = (req.text ?? "").toLowerCase();
      const hits = all
        .filter((s) => !req.envIds.length || req.envIds.includes(s.envId))
        .filter((s) => statuses.includes(s.status))
        .filter((s) => !req.platform || s.platform === req.platform)
        .filter((s) => !req.services?.length || req.services.includes(s.service))
        .filter((s) => !req.assignee || s.assignee?.id === req.assignee)
        .filter((s) => !req.serviceVersion || s.lastVersion === req.serviceVersion)
        .filter((s) => !req.userId || (isIncident(s) && incidentEvents(now, 200).some((e) => e.userId === req.userId)))
        .filter((s) => !(req.browser || req.os) || s.platform === "browser")
        .filter((s) => !text || s.title.toLowerCase().includes(text) || s.culprit.toLowerCase().includes(text))
        .map((s) => issueOf(s, req.window, now));
      const order = {
        last: (a: Issue, b: Issue) => b.lastSeenMs - a.lastSeenMs,
        first: (a: Issue, b: Issue) => b.firstSeenMs - a.firstSeenMs,
        events: (a: Issue, b: Issue) => b.events - a.events,
        affected: (a: Issue, b: Issue) => b.affected - a.affected,
      };
      hits.sort(order[req.sort] ?? order.last);
      const size = req.pageSize || 50;
      const offset = Number(req.pageToken || 0);
      return { issues: hits.slice(offset, offset + size), nextPageToken: offset + size < hits.length ? String(offset + size) : "" };
    },
    getIssue: async (req) => {
      const seed = seedOf(req.issueId);
      const window = req.window ?? { startMs: now - DAY, endMs: now };
      const issue = issueOf(seed, window, now);
      const release = demoLedger(now).find((p) => p.envId === seed.envId && p.version === seed.firstVersion && !p.targets.length);
      const latest = isIncident(seed) ? incidentEvents(now, 1)[0] ?? null : otherEvents(seed)[0];
      return {
        issue,
        latestEvent: latest,
        frames: seed.frames,
        byVersion: issue.events ? [{ version: seed.lastVersion, events: issue.events, affected: issue.affected, firstSeenMs: seed.firstSeenMs, lastSeenMs: seed.lastSeenMs }] : [],
        incidentIssueIds: [],
        coOccurringIssueIds: [],
        firstRelease: release ? { version: release.version, gitCommit: release.commit, commitRange: `${release.commit.slice(0, 7)}~3..${release.commit.slice(0, 7)}`, promotedAtMs: release.atMs } : null,
      };
    },
    listIssueEvents: async (req) => {
      const seed = seedOf(req.issueId);
      const events = isIncident(seed) ? incidentEvents(now, 200) : otherEvents(seed);
      const size = req.pageSize || 50;
      const offset = Number(req.pageToken || 0);
      return { events: events.slice(offset, offset + size), nextPageToken: offset + size < events.length ? String(offset + size) : "" };
    },
    listIssueTimeline: async (req) => {
      const seed = seedOf(req.issueId);
      const entries = timelineOf(seed, now).sort((a, b) => a.timeMs - b.timeMs);
      return { entries, nextPageToken: "", issueId: seed.id };
    },
    listOrgMembers: async () => ({ users: DEMO_CAST.map((p) => ({ id: p.id, name: p.handle })), callerRole: reader.role }),
    getCurrentIssueUser: async () => ({ id: reader.id, name: reader.handle }),
    getReleaseHealth: async (req) => shopReleaseHealth(now, req.envIds[0] ?? "", req.promotionId ?? "", req.windowMs ?? 15 * MINUTE),
    listActivity: async (req) => {
      const events = shopActivity(now).filter(
        (e) => (!req.targetKind || e.target.kind === req.targetKind) && (!req.targetId || e.target.id === req.targetId) && (!req.verb || e.verb === req.verb) && (!req.envIds.length || (e.env && req.envIds.includes(e.env.id))),
      );
      return { events, nextPageToken: "" };
    },
    updateIssueStatus: demoWriteRefused,
    addIssueComment: demoWriteRefused,
    updateIssueComment: demoWriteRefused,
    deleteIssueComment: demoWriteRefused,
    assignIssue: demoWriteRefused,
    linkIssueChat: demoWriteRefused,
  };
}

/** The toured trace leads the incident's events (journeys/forge.tsx opens it). */
export const TOURED_TRACE_ID = DEMO_TOURED_TRACE.traceId;
