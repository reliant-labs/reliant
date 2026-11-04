// Copyright (c) 2025 Reliant Labs

/**
 * An in-memory IssuesService, GetReleaseHealth and ActivityService for the
 * fixture app (fixtureApi.ts), telling the same story as the fixture ledger
 * (fixtureDelivery.ts): v1.8.3 reached prod 40 minutes ago and opened a
 * timeout on `api` (a new issue) while the checkout page's old TypeError came
 * back (regressed) — so the release's health check FAILS, and the Errors view
 * has an inbox worth reading: browser and server issues, every status.
 *
 * Triage keeps state for the fixture's lifetime and enforces the version check
 * the server does: a stale expectedVersion is Aborted. `issueConflict` makes
 * the first triage lose a race to "someone else", for the conflict path.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type { ActivityEvent, ActivityList } from "@/services/controlPlane/observability/activityApi";
import type { Promotion, ReleaseHealth, ReleaseVerdict } from "@/services/controlPlane/observability/deliveryTypes";
import type {
  Issue,
  IssueActor,
  LinkIssueChatResult,
  IssueComment,
  IssueDetail,
  IssueEvent,
  IssueEventList,
  IssueList,
  IssueStatus,
  IssueTimelineEntry,
  IssueTimelineList,
  IssueTrendPoint,
  IssueUser,
} from "@/services/controlPlane/observability/issuesTypes";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { TimeWindow } from "@/services/controlPlane/observability/types";

const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

export interface IssuesFixtureOptions {
  /** "empty": no issues. "not-configured": a control plane from before issues (Unimplemented). */
  issues?: "ok" | "empty" | "not-configured";
  /** The first triage finds the issue changed by someone else (Aborted). */
  issueConflict?: boolean;
  /** The first assignment finds the issue assigned by someone else meanwhile (Aborted). */
  assignConflict?: boolean;
  /** The reader: "ada" (default; an org admin), "grace" (a plain member) or "none" (a credential that acts for no person). */
  issueReader?: "ada" | "grace" | "none";
  /** The member roster is closed to the reader (it needs token:read): PermissionDenied. */
  rosterDenied?: boolean;
  /** iss-checkout-total was merged into iss-orders-deadline, which carries its entries and refuses writes to it. */
  mergedIssue?: boolean;
  /**
   * The release-health answer. Absent: judged from the story (prod's v1.8.3
   * fails, everything else passes). "unimplemented": an older control plane.
   * "not-evaluated": a pass from a server that cannot read alerts or issues.
   */
  health?: ReleaseVerdict | "unimplemented" | "not-evaluated";
}

type Seed = Omit<Issue, "events" | "affected" | "trend" | "version" | "assignee" | "incidentKey" | "relatedCount"> & {
  rate: number;
  affectedShare: number;
  assignee?: IssueUser | null;
  incidentKey?: string;
};

export const FIXTURE_USERS = {
  ada: { id: "user-ada", name: "ada@example.com", role: "admin" },
  grace: { id: "user-grace", name: "grace@example.com", role: "member" },
  alan: { id: "user-alan", name: "alan@example.com", role: "member" },
} as const;
const userOf = (u: { id: string; name: string }): IssueUser => ({ id: u.id, name: u.name });

const TRACE = "4bf92f3577b34da6a3ce929d0e0e4736";

/** The issues, relative to `t0`. */
export function fixtureIssueSeeds(t0: number): Seed[] {
  const base = {
    fingerprint: "",
    exceptionType: "",
    firstRelease: "",
    resolvedAtMs: 0,
    resolvedVersion: "",
    regressedAtMs: 0,
    ignoredUntilMs: 0,
    mergedInto: "",
    symbolicated: true,
  };
  return [
    {
      ...base,
      id: "iss-checkout-total",
      platform: "browser",
      envId: "env-prod",
      envName: "prod",
      service: "web",
      fingerprint: "fp-checkout-total",
      title: "TypeError: Cannot read properties of undefined (reading 'total')",
      culprit: "renderSummary in src/pages/checkout/Summary.tsx",
      exceptionType: "TypeError",
      status: "regressed",
      firstSeenMs: t0 - 9 * DAY,
      firstVersion: "v1.8.1",
      firstRelease: "v1.8.1",
      lastSeenMs: t0 - 2 * MIN,
      lastVersion: "v1.8.3",
      resolvedAtMs: t0 - 4 * DAY,
      resolvedVersion: "v1.8.2",
      regressedAtMs: t0 - 35 * MIN,
      rate: 9,
      affectedShare: 0.31,
    },
    {
      ...base,
      id: "iss-orders-deadline",
      platform: "server",
      envId: "env-prod",
      envName: "prod",
      service: "api",
      fingerprint: "fp-orders-deadline",
      title: "CreateOrder: context deadline exceeded",
      culprit: "(*Service).CreateOrder in internal/handlers/orders/rpc_create_order.go",
      exceptionType: "context.DeadlineExceeded",
      status: "new",
      assignee: userOf(FIXTURE_USERS.grace),
      incidentKey: "sqlstate:53300",
      firstSeenMs: t0 - 38 * MIN,
      firstVersion: "v1.8.3",
      firstRelease: "v1.8.3",
      lastSeenMs: t0 - 1 * MIN,
      lastVersion: "v1.8.3",
      rate: 4,
      affectedShare: 0.9,
    },
    {
      ...base,
      id: "iss-worker-conns",
      platform: "server",
      envId: "env-prod",
      envName: "prod",
      service: "worker",
      fingerprint: "fp-worker-conns",
      title: "pq: sorry, too many clients already",
      culprit: "(*Pool).Acquire in internal/db/pool.go",
      exceptionType: "*pq.Error",
      status: "unresolved",
      incidentKey: "sqlstate:53300",
      firstSeenMs: t0 - 40 * DAY,
      firstVersion: "v1.7.2",
      firstRelease: "v1.7.2",
      lastSeenMs: t0 - 26 * MIN,
      lastVersion: "v1.8.3",
      rate: 1.2,
      affectedShare: 1,
    },
    {
      ...base,
      id: "iss-cart-chunk",
      platform: "browser",
      envId: "env-staging",
      envName: "staging",
      service: "web",
      fingerprint: "fp-cart-chunk",
      title: "ChunkLoadError: Loading chunk 412 failed",
      culprit: "__webpack_require__.f.j in assets/runtime.js",
      exceptionType: "ChunkLoadError",
      status: "unresolved",
      firstSeenMs: t0 - 3 * HOUR,
      firstVersion: "v1.8.3",
      firstRelease: "v1.8.3",
      lastSeenMs: t0 - 12 * MIN,
      lastVersion: "v1.8.3",
      symbolicated: false,
      rate: 0.6,
      affectedShare: 0.8,
    },
    {
      ...base,
      id: "iss-ledger-nil",
      platform: "server",
      envId: "env-staging",
      envName: "staging",
      service: "api",
      fingerprint: "fp-ledger-nil",
      title: "runtime error: invalid memory address or nil pointer dereference",
      culprit: "(*Ledger).Post in internal/billing/ledger.go",
      exceptionType: "runtime.Error",
      status: "resolved",
      firstSeenMs: t0 - 6 * DAY,
      firstVersion: "v1.8.1",
      lastSeenMs: t0 - 5 * DAY,
      lastVersion: "v1.8.1",
      resolvedAtMs: t0 - 5 * DAY,
      resolvedVersion: "v1.8.2",
      rate: 0,
      affectedShare: 1,
    },
    {
      ...base,
      id: "iss-dev-deprecated",
      platform: "server",
      envId: "env-dev",
      envName: "dev",
      service: "api",
      fingerprint: "fp-dev-deprecated",
      title: "orders.v1.Order.legacy_total is deprecated",
      culprit: "protovalidate in internal/handlers/orders/validate.go",
      exceptionType: "",
      status: "ignored",
      firstSeenMs: t0 - 20 * DAY,
      firstVersion: "v1.7.3",
      lastSeenMs: t0 - 5 * MIN,
      lastVersion: "v1.8.4-rc.1",
      rate: 2,
      affectedShare: 1,
    },
  ];
}

const OPEN: IssueStatus[] = ["new", "unresolved", "regressed"];

/** Hourly counts over `window`, from the issue's rate, only while it was being seen. */
function trendOf(seed: Seed, window: TimeWindow): IssueTrendPoint[] {
  const start = Math.floor(window.startMs / HOUR) * HOUR;
  const out: IssueTrendPoint[] = [];
  for (let hour = start; hour < window.endMs && out.length < 24 * 30; hour += HOUR) {
    const live = hour + HOUR > seed.firstSeenMs && hour <= seed.lastSeenMs;
    const wave = 0.6 + 0.4 * Math.abs(Math.sin(hour / HOUR / 3 + seed.id.length));
    out.push({ hourMs: hour, events: live ? Math.round(seed.rate * 60 * wave) : 0 });
  }
  // Only the last day's worth is drawn: an issue's trend is its recent shape.
  return out.slice(-24);
}

function issueOf(seed: Seed, window: TimeWindow, version: number, relatedCount = 0): Issue {
  const { rate: _rate, affectedShare, assignee, incidentKey, ...issue } = seed;
  const trend = trendOf(seed, window);
  const events = trend.reduce((sum, p) => sum + p.events, 0);
  return {
    ...issue,
    assignee: assignee ?? null,
    incidentKey: incidentKey ?? "",
    relatedCount,
    trend,
    events,
    affected: Math.max(events ? 1 : 0, Math.round(events * affectedShare)),
    version,
  };
}

function framesOf(seed: Seed) {
  if (seed.platform === "browser") {
    return seed.symbolicated
      ? [
          { function: "renderSummary", file: "src/pages/checkout/Summary.tsx", line: 42, column: 17, inApp: true },
          { function: "CheckoutPage", file: "src/pages/checkout/index.tsx", line: 88, column: 5, inApp: true },
          { function: "renderWithHooks", file: "node_modules/react-dom/cjs/react-dom.development.js", line: 14985, column: 18, inApp: false },
        ]
      : [
          { function: "__webpack_require__.f.j", file: "assets/runtime.4f1c.js", line: 1, column: 8812, inApp: true },
          { function: "", file: "assets/vendor.91aa.js", line: 2, column: 1034, inApp: false },
        ];
  }
  const [fn, file] = seed.culprit.split(" in ");
  return [
    { function: fn, file, line: 57, column: 0, inApp: true },
    { function: "orders.v1.(*ordersServiceHandler).CreateOrder", file: "gen/services/orders/v1/ordersv1connect/orders.connect.go", line: 212, column: 0, inApp: true },
    { function: "connectrpc.com/connect.(*Handler).ServeHTTP", file: "connect/handler.go", line: 236, column: 0, inApp: false },
  ];
}

function eventOf(seed: Seed, atMs: number, index: number): IssueEvent {
  return {
    timeMs: atMs,
    traceId: seed.platform === "server" ? TRACE : index % 2 ? TRACE : "",
    spanId: seed.platform === "server" ? "00f067aa0ba902b7" : "",
    serviceVersion: seed.lastVersion,
    message: seed.title,
    attributes: seed.platform === "browser" ? { "browser.name": "Chrome", "url.path": "/checkout" } : { "rpc.method": "CreateOrder", "http.response.status_code": "504" },
    sessionId: seed.platform === "browser" ? `sess-${(index * 7919).toString(16).padStart(8, "0")}` : "",
    urlPath: seed.platform === "browser" ? "/checkout" : "",
  };
}

type IssuesApi = Pick<
  ObservabilityApi,
  | "listIssues"
  | "getIssue"
  | "listIssueEvents"
  | "updateIssueStatus"
  | "getReleaseHealth"
  | "listActivity"
  | "listIssueTimeline"
  | "addIssueComment"
  | "updateIssueComment"
  | "deleteIssueComment"
  | "assignIssue"
  | "linkIssueChat"
  | "listOrgMembers"
  | "getCurrentIssueUser"
>;

export function createIssuesFixture(
  options: IssuesFixtureOptions,
  call: <T>(rpc: string, request: unknown, answer: () => T) => Promise<T>,
  ledger: readonly Promotion[],
  now: () => number = Date.now
): IssuesApi {
  const t0 = now();
  const mode = options.issues ?? "ok";
  const seeds = mode === "empty" ? [] : fixtureIssueSeeds(t0);
  if (options.mergedIssue) {
    const merged = seeds.find((s) => s.id === "iss-checkout-total");
    if (merged) merged.mergedInto = "iss-orders-deadline";
  }
  const versions = new Map(seeds.map((s) => [s.id, 1]));
  let conflictPending = !!options.issueConflict;
  let assignConflictPending = !!options.assignConflict;
  const readerKey = options.issueReader ?? "ada";
  const reader = readerKey === "none" ? null : FIXTURE_USERS[readerKey];
  const relatedOf = (seed: Seed) => (seed.incidentKey ? seeds.filter((o) => o.id !== seed.id && o.incidentKey === seed.incidentKey).length : 0);
  const build = (seed: Seed, window: TimeWindow, version: number): Issue => issueOf(seed, window, version, relatedOf(seed));

  // ── The discussion: one timeline per issue, kept for the fixture's lifetime.
  let nextEntry = 1;
  const SYSTEM: IssueActor = { kind: "system", id: "", name: "" };
  const actorOf = (u: { id: string; name: string } | null): IssueActor => (u ? { kind: "user", id: u.id, name: u.name } : SYSTEM);
  const timeline: IssueTimelineEntry[] = [];
  const push = (issueId: string, timeMs: number, actor: IssueActor | null, body: IssueTimelineEntry["body"]): IssueTimelineEntry => {
    const entry: IssueTimelineEntry = { id: `tl-${nextEntry++}`, timeMs, issueId, actor, body };
    timeline.push(entry);
    return entry;
  };
  const commentOf = (issueId: string, author: { id: string; name: string } | null, body: string, atMs: number): IssueComment => ({
    id: `cmt-${nextEntry}`,
    issueId,
    author: author ? userOf(author) : null,
    body,
    createdMs: atMs,
    updatedMs: atMs,
  });
  for (const seed of seeds) push(seed.id, seed.firstSeenMs, SYSTEM, { kind: "status", from: null, to: "new", version: seed.firstVersion, ignoredUntilMs: 0 });
  if (seeds.length) {
    push("iss-checkout-total", t0 - 4 * DAY, actorOf(FIXTURE_USERS.grace), { kind: "status", from: "unresolved", to: "resolved", version: "v1.8.2", ignoredUntilMs: 0 });
    push("iss-checkout-total", t0 - 35 * MIN, SYSTEM, { kind: "status", from: "resolved", to: "regressed", version: "v1.8.3", ignoredUntilMs: 0 });
    const c1 = commentOf("iss-checkout-total", FIXTURE_USERS.ada, "Back in **v1.8.3**. `renderSummary` reads `order.total` before the cart loads.\n\n<script>alert('not run')</script>\nSee [the fix](https://example.com/pr/4821).", t0 - 34 * MIN);
    push("iss-checkout-total", c1.createdMs, actorOf(FIXTURE_USERS.ada), { kind: "comment", comment: c1 });
    push("iss-orders-deadline", t0 - 30 * MIN, actorOf(FIXTURE_USERS.ada), { kind: "assignment", from: null, to: userOf(FIXTURE_USERS.grace) });
    const c2 = commentOf("iss-orders-deadline", FIXTURE_USERS.grace, "Looks like the pool is exhausted: same cause as iss-worker-conns.", t0 - 26 * MIN);
    push("iss-orders-deadline", c2.createdMs, actorOf(FIXTURE_USERS.grace), { kind: "comment", comment: c2 });
    push("iss-orders-deadline", t0 - 20 * MIN, actorOf(FIXTURE_USERS.grace), {
      kind: "chat",
      link: { issueId: "iss-orders-deadline", chatId: "chat-4f2a91c07b", chatTitle: "Investigate CreateOrder timeouts", linkedBy: userOf(FIXTURE_USERS.grace), linkedAtMs: t0 - 20 * MIN },
    });
  }
  const hidden = new Set<string>();
  const rootOf = (id: string) => seedOf(id).mergedInto || id;
  const refuseIfMerged = (id: string) => {
    const seed = seedOf(id);
    if (seed.mergedInto) throw new ConnectError(`this issue was merged into ${seed.mergedInto}; change that one`, Code.FailedPrecondition);
    return seed;
  };
  const requireReader = () => {
    if (!reader) throw new ConnectError("a credential that acts for no person cannot comment", Code.PermissionDenied);
    return reader;
  };
  const activity: ActivityEvent[] = [
    {
      id: "act-query-1",
      atMs: t0 - 18 * MIN,
      actor: { id: "user-ada", name: "Ada Lovelace", kind: "user" },
      verb: "query_database",
      target: { kind: "database", id: "shop", label: "shop" },
      env: { id: "env-prod", name: "prod" },
      project: "shop",
      via: "cli",
      detail: { reason: "checking the orders behind the CreateOrder timeouts", row_count: "42", duration_ms: "31", sql_sha256: "3f9a1c2b7d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8" },
    },
    {
      id: "act-opened-1",
      atMs: t0 - 38 * MIN,
      actor: { id: "reliant", name: "reliant", kind: "system" },
      verb: "issue_opened",
      target: { kind: "issue", id: "iss-orders-deadline", label: "CreateOrder: context deadline exceeded" },
      env: { id: "env-prod", name: "prod" },
      project: "shop",
      via: "reliant",
      detail: { platform: "server", service: "api", status: "new", version: "v1.8.3" },
    },
    {
      id: "act-regressed-1",
      atMs: t0 - 35 * MIN,
      actor: { id: "reliant", name: "reliant", kind: "system" },
      verb: "issue_regressed",
      target: { kind: "issue", id: "iss-checkout-total", label: "TypeError: Cannot read properties of undefined (reading 'total')" },
      env: { id: "env-prod", name: "prod" },
      project: "shop",
      via: "reliant",
      detail: { platform: "browser", service: "web", status: "regressed", from: "resolved", version: "v1.8.3", resolved_at: new Date(t0 - 4 * DAY).toISOString() },
    },
    {
      id: "act-resolve-1",
      atMs: t0 - 4 * DAY,
      actor: { id: "user-grace", name: "Grace Hopper", kind: "user" },
      verb: "resolve",
      target: { kind: "issue", id: "iss-checkout-total", label: "TypeError: Cannot read properties of undefined (reading 'total')" },
      env: { id: "env-prod", name: "prod" },
      project: "shop",
      via: "console",
      detail: { platform: "browser", service: "web", status: "resolved", from: "unresolved", resolve_in: "version", version: "v1.8.2" },
    },
  ];

  const seedOf = (id: string) => {
    const seed = seeds.find((s) => s.id === id);
    if (!seed) throw new ConnectError("issue not found", Code.NotFound);
    return seed;
  };
  const gate = <T>(rpc: string, request: unknown, answer: () => T) =>
    call(rpc, request, () => {
      if (mode === "not-configured") throw new ConnectError(`unimplemented: controlplane.v1.IssuesService/${rpc}`, Code.Unimplemented);
      return answer();
    });

  const judge = (envId: string, promotionId: string, widthMs: number): ReleaseHealth => {
    const promotion = ledger.find((p) => p.promotionId === promotionId && p.envId === envId);
    if (!promotion) throw new ConnectError("promotion not found", Code.NotFound);
    const same = ledger.filter((p) => p.envId === envId);
    const previous = same.filter((p) => p.promotedAtMs < promotion.promotedAtMs).at(-1) ?? null;
    const next = same.find((p) => p.promotedAtMs > promotion.promotedAtMs) ?? null;
    const at = promotion.promotedAtMs;
    const beforeStart = Math.max(at - widthMs, previous?.promotedAtMs ?? -Infinity);
    const afterEnd = Math.min(at + widthMs, next?.promotedAtMs ?? Infinity, now());
    const story = promotion.envId === "env-prod" && promotion.version === "v1.8.3" ? "fail" : "pass";
    const verdict: ReleaseVerdict = options.health === "not-evaluated" ? "pass" : (options.health as ReleaseVerdict | undefined) ?? story;
    const failing = verdict === "fail";
    const window = { startMs: at, endMs: afterEnd };
    const issue = (id: string) => build(seedOf(id), window, versions.get(id) ?? 1);
    return {
      promotion: { promotionId: promotion.promotionId, version: promotion.version, gitCommit: promotion.gitCommit, promotedAtMs: at, previousVersion: previous?.version ?? "" },
      before: { startMs: beforeStart, endMs: at },
      after: window,
      afterComplete: verdict === "inconclusive" ? false : at + widthMs <= Math.min(next?.promotedAtMs ?? Infinity, now()),
      services: [
        {
          service: "api",
          before: { requests: 10_350, errorRate: 0.004, p50Ms: 41, p95Ms: 118, p99Ms: 240 },
          after: failing ? { requests: 10_890, errorRate: 0.016, p50Ms: 46, p95Ms: 171, p99Ms: 402 } : { requests: 10_410, errorRate: 0.004, p50Ms: 40, p95Ms: 116, p99Ms: 236 },
          versionShare: verdict === "inconclusive" ? 0.42 : 0.97,
        },
        {
          service: "worker",
          before: { requests: 2_880, errorRate: 0.002, p50Ms: 120, p95Ms: 240, p99Ms: 380 },
          after: { requests: 2_910, errorRate: 0.002, p50Ms: 118, p95Ms: 236, p99Ms: 371 },
          versionShare: verdict === "inconclusive" ? 0.5 : 1,
        },
      ],
      newIssues: failing && seeds.length ? [issue("iss-orders-deadline")] : [],
      regressedIssues: failing && seeds.length ? [issue("iss-checkout-total")] : [],
      firingAlerts: failing ? [{ rule: "HighErrorRate", fingerprint: "a1b2c3", firingAtMs: at + 22 * MIN, labels: { service: "api", severity: "page" } }] : [],
      workloadFindings: failing ? [{ workload: "api", restarts: 2, lastTerminatedReason: "OOMKilled" }] : [],
      verdict,
      findings: failing
        ? [
            { code: "error_rate", service: "api", value: 0.012, threshold: 0.005, policy: "fail", message: "api's error rate rose 1.2 points (limit 0.5): roll back, or look at the new CreateOrder timeouts." },
            { code: "latency", service: "api", value: 1.45, threshold: 1.25, policy: "warn", message: "api's p95 is 1.45× what it was (limit 1.25×)." },
            { code: "new_issue", service: "api", value: 38, threshold: 10, policy: "fail", message: "A new issue opened in this release: CreateOrder: context deadline exceeded." },
            { code: "regressed_issue", service: "web", value: 1, threshold: 1, policy: "fail", message: "A resolved issue came back: TypeError … (reading 'total')." },
            { code: "alert_firing", service: "", value: 1, threshold: 1, policy: "fail", message: "HighErrorRate opened 22 minutes after the promotion and is still firing." },
            { code: "workload_restarts", service: "", value: 2, threshold: 1, policy: "warn", message: "api restarted twice (OOMKilled)." },
          ]
        : [],
      thresholds: [
        { name: "max_error_rate_increase", value: "0.005", source: "release_shape" },
        { name: "max_p95_ratio", value: "1.25", source: "default" },
        { name: "min_requests", value: "100", source: "default" },
        { name: "new_issue_min_events", value: "10", source: "default" },
      ],
      notEvaluated: options.health === "not-evaluated" ? ["alert_firing", "new_issue", "regressed_issue"] : [],
    };
  };

  return {
    listIssues: (req) =>
      gate("ListIssues", req, (): IssueList => {
        const statuses = req.statuses.length ? req.statuses : OPEN;
        const text = (req.text ?? "").toLowerCase();
        const hits = seeds
          .filter((s) => !req.envIds.length || req.envIds.includes(s.envId))
          .filter((s) => s.status && statuses.includes(s.status))
          .filter((s) => !req.platform || s.platform === req.platform)
          .filter((s) => !req.services?.length || req.services.includes(s.service))
          .filter((s) => !req.assignee || s.assignee?.id === req.assignee)
          .filter((s) => !text || s.title.toLowerCase().includes(text) || s.culprit.toLowerCase().includes(text))
          .map((s) => build(s, req.window, versions.get(s.id) ?? 1));
        const order: Record<typeof req.sort, (a: Issue, b: Issue) => number> = {
          last: (a, b) => b.lastSeenMs - a.lastSeenMs,
          first: (a, b) => b.firstSeenMs - a.firstSeenMs,
          events: (a, b) => b.events - a.events,
          affected: (a, b) => b.affected - a.affected,
        };
        hits.sort(order[req.sort]);
        const size = req.pageSize || 50;
        const offset = Number(req.pageToken || 0);
        const page = hits.slice(offset, offset + size);
        return { issues: page, nextPageToken: offset + size < hits.length ? String(offset + size) : "" };
      }),
    getIssue: (req) =>
      gate("GetIssue", req, (): IssueDetail => {
        const seed = seedOf(req.issueId);
        const window = req.window ?? { startMs: now() - DAY, endMs: now() };
        const issue = build(seed, window, versions.get(seed.id) ?? 1);
        const release = ledger.find((p) => p.envId === seed.envId && p.version === seed.firstRelease);
        return {
          issue,
          latestEvent: seed.lastSeenMs > now() - 7 * DAY ? eventOf(seed, seed.lastSeenMs, 0) : null,
          frames: framesOf(seed),
          byVersion:
            issue.events > 0
              ? [
                  { version: seed.lastVersion, events: Math.round(issue.events * 0.8), affected: Math.round(issue.affected * 0.8), firstSeenMs: Math.max(seed.firstSeenMs, window.startMs), lastSeenMs: seed.lastSeenMs },
                  ...(seed.firstVersion !== seed.lastVersion
                    ? [{ version: seed.firstVersion, events: Math.round(issue.events * 0.2), affected: Math.round(issue.affected * 0.2), firstSeenMs: seed.firstSeenMs, lastSeenMs: seed.lastSeenMs - HOUR }]
                    : []),
                ]
              : [],
          incidentIssueIds: seed.incidentKey ? seeds.filter((o) => o.id !== seed.id && o.incidentKey === seed.incidentKey).map((o) => o.id) : [],
          coOccurringIssueIds: seed.id === "iss-orders-deadline" ? ["iss-worker-conns"] : seed.id === "iss-worker-conns" ? ["iss-orders-deadline"] : [],
          firstRelease: release ? { version: release.version, gitCommit: release.gitCommit, commitRange: `${release.gitCommit.slice(0, 7)}~12..${release.gitCommit.slice(0, 7)}`, promotedAtMs: release.promotedAtMs } : null,
        };
      }),
    listIssueEvents: (req) =>
      gate("ListIssueEvents", req, (): IssueEventList => {
        const seed = seedOf(req.issueId);
        const all = Array.from({ length: seed.rate > 0 ? 30 : 3 }, (_, i) => eventOf(seed, seed.lastSeenMs - i * 97_000, i));
        const size = req.pageSize || 50;
        const offset = Number(req.pageToken || 0);
        return { events: all.slice(offset, offset + size), nextPageToken: offset + size < all.length ? String(offset + size) : "" };
      }),
    updateIssueStatus: (req) =>
      gate("UpdateIssueStatus", req, (): Issue => {
        const seed = seedOf(req.issueId);
        if (seed.envId !== req.envId) throw new ConnectError("env_id is not the environment of that issue", Code.InvalidArgument);
        const current = versions.get(seed.id) ?? 1;
        if (conflictPending) {
          // Someone else resolved it a moment ago.
          conflictPending = false;
          seed.status = "resolved";
          seed.resolvedVersion = "v1.8.4";
          seed.resolvedAtMs = now();
          versions.set(seed.id, current + 1);
          throw new ConnectError("issue changed since it was read (expected_version is stale)", Code.Aborted);
        }
        if (req.expectedVersion && req.expectedVersion !== current) {
          throw new ConnectError("issue changed since it was read (expected_version is stale)", Code.Aborted);
        }
        const from = seed.status;
        const change = req.change;
        const detail: Record<string, string> = { platform: seed.platform ?? "", service: seed.service, from: from ?? "" };
        if (change.status === "resolved") {
          seed.status = "resolved";
          seed.resolvedAtMs = now();
          seed.resolvedVersion = change.resolveIn.kind === "version" ? change.resolveIn.version : "";
          detail.resolve_in = change.resolveIn.kind;
          if (seed.resolvedVersion) detail.version = seed.resolvedVersion;
        } else if (change.status === "ignored") {
          seed.status = "ignored";
          seed.ignoredUntilMs = change.untilMs ?? 0;
          if (change.untilMs) detail.ignored_until = new Date(change.untilMs).toISOString();
        } else {
          seed.status = "unresolved";
        }
        detail.status = seed.status ?? "";
        versions.set(seed.id, current + 1);
        push(seed.id, now(), actorOf(reader ?? FIXTURE_USERS.ada), {
          kind: "status",
          from,
          to: seed.status,
          version: seed.resolvedVersion,
          ignoredUntilMs: seed.status === "ignored" ? seed.ignoredUntilMs : 0,
        });
        activity.unshift({
          id: `act-triage-${activity.length + 1}`,
          atMs: now(),
          actor: { id: "user-ada", name: "Ada Lovelace", kind: "user" },
          verb: change.status === "resolved" ? "resolve" : change.status === "ignored" ? "ignore" : "unresolve",
          target: { kind: "issue", id: seed.id, label: seed.title },
          env: { id: seed.envId, name: seed.envName },
          project: "shop",
          via: "console",
          detail,
        });
        return build(seed, { startMs: now() - DAY, endMs: now() }, current + 1);
      }),
    listIssueTimeline: (req) =>
      gate("ListIssueTimeline", req, (): IssueTimelineList => {
        const root = rootOf(req.issueId);
        const family = new Set([root, ...seeds.filter((s) => s.mergedInto === root).map((s) => s.id)]);
        const all = timeline
          .filter((e) => family.has(e.issueId) && !hidden.has(e.id))
          .map((entry, index) => ({ entry, index }))
          .sort((a, b) => a.entry.timeMs - b.entry.timeMs || a.index - b.index)
          .map(({ entry }) => structuredClone(entry));
        const size = req.pageSize || 50;
        const offset = Number(req.pageToken || 0);
        return { entries: all.slice(offset, offset + size), nextPageToken: offset + size < all.length ? String(offset + size) : "", issueId: root };
      }),
    addIssueComment: (req) =>
      gate("AddIssueComment", req, (): IssueComment => {
        const seed = refuseIfMerged(req.issueId);
        const who = requireReader();
        const comment = commentOf(seed.id, who, req.body, now());
        push(seed.id, comment.createdMs, actorOf(who), { kind: "comment", comment });
        return comment;
      }),
    updateIssueComment: (req) =>
      gate("UpdateIssueComment", req, (): IssueComment => {
        refuseIfMerged(req.issueId);
        const who = requireReader();
        const entry = timeline.find((e) => e.body.kind === "comment" && e.body.comment.id === req.commentId && !hidden.has(e.id));
        if (!entry || entry.body.kind !== "comment") throw new ConnectError("no such comment", Code.NotFound);
        if (entry.body.comment.author?.id !== who.id) throw new ConnectError("only a comment's author can edit it", Code.PermissionDenied);
        entry.body.comment = { ...entry.body.comment, body: req.body, updatedMs: now() };
        return entry.body.comment;
      }),
    deleteIssueComment: (req) =>
      gate("DeleteIssueComment", req, (): void => {
        refuseIfMerged(req.issueId);
        const who = requireReader();
        const entry = timeline.find((e) => e.body.kind === "comment" && e.body.comment.id === req.commentId && !hidden.has(e.id));
        if (!entry || entry.body.kind !== "comment") throw new ConnectError("no such comment", Code.NotFound);
        const isAdmin = FIXTURE_USERS[(Object.keys(FIXTURE_USERS) as (keyof typeof FIXTURE_USERS)[]).find((k) => FIXTURE_USERS[k].id === who.id) ?? "grace"].role === "admin";
        if (entry.body.comment.author?.id !== who.id && !isAdmin) throw new ConnectError("only the author or an org admin can delete a comment", Code.PermissionDenied);
        hidden.add(entry.id);
      }),
    assignIssue: (req) =>
      gate("AssignIssue", req, (): Issue => {
        const seed = refuseIfMerged(req.issueId);
        const current = versions.get(seed.id) ?? 1;
        if (assignConflictPending) {
          // Someone else assigned it a moment ago.
          assignConflictPending = false;
          seed.assignee = userOf(FIXTURE_USERS.alan);
          versions.set(seed.id, current + 1);
          push(seed.id, now(), actorOf(FIXTURE_USERS.alan), { kind: "assignment", from: null, to: userOf(FIXTURE_USERS.alan) });
          throw new ConnectError("issue changed since it was read (expected_version is stale)", Code.Aborted);
        }
        if (req.expectedVersion && req.expectedVersion !== current) {
          throw new ConnectError("issue changed since it was read (expected_version is stale)", Code.Aborted);
        }
        const to = req.assigneeUserId ? Object.values(FIXTURE_USERS).find((u) => u.id === req.assigneeUserId) : undefined;
        if (req.assigneeUserId && !to) throw new ConnectError("that user is not a member of the issue's organization", Code.InvalidArgument);
        const from = seed.assignee ?? null;
        seed.assignee = to ? userOf(to) : null;
        versions.set(seed.id, current + 1);
        push(seed.id, now(), actorOf(reader), { kind: "assignment", from, to: seed.assignee });
        return build(seed, { startMs: now() - DAY, endMs: now() }, current + 1);
      }),
    linkIssueChat: (req) =>
      gate("LinkIssueChat", req, (): LinkIssueChatResult => {
        const seed = refuseIfMerged(req.issueId);
        const existing = timeline.find((e) => e.body.kind === "chat" && e.issueId === seed.id && e.body.link.chatId === req.chatId);
        if (existing && existing.body.kind === "chat") {
          if (req.chatTitle) existing.body.link = { ...existing.body.link, chatTitle: req.chatTitle };
          return { link: existing.body.link, created: false };
        }
        const link = { issueId: seed.id, chatId: req.chatId, chatTitle: req.chatTitle ?? "", linkedBy: reader ? userOf(reader) : null, linkedAtMs: now() };
        push(seed.id, link.linkedAtMs, actorOf(reader), { kind: "chat", link });
        return { link, created: true };
      }),
    listOrgMembers: (orgId) =>
      call("ListAssignableUsers", { orgId }, () => {
        if (!orgId || options.rosterDenied) throw new ConnectError("issue organization membership required", Code.PermissionDenied);
        return { users: Object.values(FIXTURE_USERS).map((u) => ({ id: u.id, name: u.name })), callerRole: reader ? reader.role : null };
      }),
    getCurrentIssueUser: () => call("GetCurrentUser", {}, () => (reader ? userOf(reader) : null)),
    getReleaseHealth: (req) =>
      call("GetReleaseHealth", req, () => {
        if (options.health === "unimplemented") throw new ConnectError("unimplemented: GetReleaseHealth", Code.Unimplemented);
        return judge(req.envIds[0] ?? "", req.promotionId ?? "", req.windowMs ?? 15 * MIN);
      }),
    listActivity: (req) =>
      call("ListActivity", req, (): ActivityList => {
        const hits = activity.filter(
          (e) =>
            (!req.targetKind || e.target.kind === req.targetKind) &&
            (!req.targetId || e.target.id === req.targetId) &&
            (!req.envIds.length || (e.env && req.envIds.includes(e.env.id)))
        );
        return { events: hits, nextPageToken: "" };
      }),
  };
}
