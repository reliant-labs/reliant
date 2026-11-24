// Copyright (c) 2025 Reliant Labs

/**
 * The shop pack's overlays for Observability, Dashboards and the metrics
 * explorer (/forge-observability-preview, /forge-dashboards-preview,
 * /forge-metrics-preview), by area and the preview's `?case=` id: the states
 * of control-plane's observability answers that shop's story never reaches
 * (an org that never set it up, an outage, nothing sent this week, an older
 * control plane, a replay that lost its snapshot, …).
 *
 * The pack's own answers are the default; a `refining` overlay takes the
 * pack's answer and turns one fact. Two states are not layers but the
 * transport's behaviour: `retainedCatalog` (a catalog that answers once, then
 * refuses) is a factory, a fresh pack per mount, and `readCap` (control-plane's
 * per-user cap on concurrent panel reads) an interceptor.
 */

import { create, type DescMethod, type DescService } from "@bufbuild/protobuf";
import { BinaryWriter } from "@bufbuild/protobuf/wire";
import { Code, ConnectError, type Interceptor } from "@connectrpc/connect";

import { AlertingService } from "@/gen/controlplane/services/alerting/v1/alerting_pb";
import { DashboardService } from "@/gen/controlplane/services/dashboard/v1/dashboard_pb";
import { DeliveryInsightsService } from "@/gen/controlplane/services/delivery_insights/v1/delivery_insights_pb";
import { IssuesService } from "@/gen/controlplane/services/issues/v1/issues_pb";
import { ObservabilityService, ReleaseVerdict } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { ReplayEventSchema, RumService } from "@/gen/controlplane/services/rum/v1/rum_pb";

import { answering, emptying, failing, methodsOf, refining, stalling } from "../../overlay";
import { isDemoRead } from "../../readOnly";
import type { FixturePack } from "../../transport";
import { shopScenario } from "../index";
import { DEMO_INCIDENT } from "../shopStory";

const obs = ObservabilityService.method;
const dashboards = DashboardService.method;
const issues = IssuesService.method;
const rum = RumService.method;

/** The pack's own answers, "pkg.Service/Method" keyed: what a refining overlay turns. */
const pack = shopScenario().handlers as Record<string, unknown>;

/** Every service the observability views read. */
const OBSERVABILITY: DescService[] = [ObservabilityService, DashboardService, AlertingService, DeliveryInsightsService, IssuesService, RumService];

/** The reads among `services` (what the read-only guard lets through), but `except`. */
function readsOf(services: readonly DescService[], except: readonly DescMethod[] = []): DescMethod[] {
  return methodsOf(services).filter((method) => isDemoRead(method) && !except.includes(method));
}

/** The writes among `services`. */
const writesOf = (services: readonly DescService[]) => methodsOf(services).filter((method) => !isDemoRead(method));

/** forge's machine-readable cause, as control-plane sends it beside the code. */
const reason = (value: string) => ({ "x-forge-error-reason": value });

const NOT_SET_UP = "observability is not set up for this organization yet";

/** What an older control plane answers for an rpc it does not have. */
const unimplemented = (name: string, methods: DescService | DescMethod | DescMethod[]) => failing(name, methods, Code.Unimplemented, "unknown procedure");

/** One rrweb event of a replay page, decoded (its `type`: 2 is the full snapshot). */
const rrwebType = (data: Uint8Array): number | null => {
  try {
    return (JSON.parse(new TextDecoder().decode(data)) as { type?: number }).type ?? null;
  } catch {
    return null;
  }
};

/** prod's release health, judged otherwise than the story judges it (prod's v1.8.3 FAILs). */
const healthAs = (name: string, verdict: ReleaseVerdict, turn: (health: { afterComplete: boolean; notEvaluated: string[] }) => void = () => {}) =>
  refining(name, obs.getReleaseHealth, pack, (health) => {
    health.verdict = verdict;
    health.findings = [];
    turn(health);
  });

/** Dashboards as a member who is not an admin reads them: listed and opened, never written. */
const dashboardsReadOnly: FixturePack[] = [
  refining("dashboards.read-only.list", dashboards.listDashboards, pack, (list) => {
    list.canWrite = false;
  }),
  refining("dashboards.read-only.get", dashboards.getDashboard, pack, (dashboard) => {
    dashboard.canWrite = false;
  }),
];

export const observabilityOverlays = {
  /** Every observability read, as the org's state makes control-plane answer it. */
  telemetry: {
    "not-set-up": failing("telemetry.not-set-up", OBSERVABILITY, Code.FailedPrecondition, NOT_SET_UP),
    denied: failing("telemetry.denied", OBSERVABILITY, Code.PermissionDenied, "permission denied: observability:read"),
    unavailable: failing("telemetry.unavailable", OBSERVABILITY, Code.Unavailable, "upstream connect error"),
    quota: failing("telemetry.quota", OBSERVABILITY, Code.ResourceExhausted, "observability query quota exhausted for this organization", reason("query_quota")),
    /** Set up, nothing has arrived this week: the getting-started state. */
    "nothing-yet": emptying("telemetry.nothing-yet", readsOf(OBSERVABILITY)),
    /** Nothing in the window, and the server predates GetServiceSignals: nothing is known of this week. */
    empty: [emptying("telemetry.empty", readsOf(OBSERVABILITY, [obs.getServiceSignals])), unimplemented("telemetry.empty.signals", obs.getServiceSignals)],
    /** Nothing in the window, though the services sent data this week. */
    "window-empty": emptying("telemetry.window-empty", readsOf(OBSERVABILITY, [obs.getServiceSignals])),
    loading: stalling("telemetry.loading", readsOf(OBSERVABILITY)),
  },
  graph: {
    /** Neither the graph's coverage nor the declared catalog is known. */
    unknown: [
      refining("graph.unknown", obs.getServiceGraph, pack, (graph) => {
        graph.completeness = undefined;
      }),
      refining("graph.unknown.catalog", obs.getServiceGraphCatalog, pack, (catalog) => {
        Object.assign(catalog, { completeness: undefined, declaredCatalogStatus: "unavailable", declaredCatalogUnavailableReason: "No verified hosted artifact catalog", declaredCatalogs: [] });
      }),
    ],
    unsupported: unimplemented("graph.unsupported", obs.getServiceGraphCatalog),
  },
  alerting: {
    "not-configured": failing("alerting.not-configured", AlertingService, Code.Unavailable, "alerting is not configured on this server", reason("observability_not_configured")),
    viewer: failing("alerting.viewer", writesOf([AlertingService]), Code.PermissionDenied, "this needs the organization admin role"),
    quiet: emptying("alerting.quiet", [AlertingService.method.listAlerts, AlertingService.method.listAlertHistory]),
  },
  delivery: {
    empty: emptying("delivery.empty", readsOf([DeliveryInsightsService])),
    "not-member": failing("delivery.not-member", DeliveryInsightsService, Code.PermissionDenied, "not a member of this organization", reason("not_org_member")),
    /** A request naming environments is refused, as one naming another org's environment is. */
    "env-denied": answering("delivery.env-denied", readsOf([DeliveryInsightsService]), (method, request) => {
      if (((request as { scope?: { envIds?: string[] } }).scope?.envIds ?? []).length > 0) throw new ConnectError("one or more environments are not available to this organization", Code.PermissionDenied);
      return (pack[`${method.parent.typeName}/${method.name}`] as (req: unknown) => unknown)(request);
    }),
    truncated: refining("delivery.truncated", DeliveryInsightsService.method.listReleases, pack, (list) => {
      list.releases = list.releases.slice(0, 5);
      list.truncated = true;
    }),
  },
  issues: {
    empty: emptying("issues.empty", issues.listIssues),
    "not-configured": unimplemented("issues.not-configured", IssuesService),
    /** The first (every) triage finds the issue changed by someone else meanwhile. */
    conflict: failing("issues.conflict", issues.updateIssueStatus, Code.Aborted, "the issue changed since it was read", reason("version_conflict")),
    "assign-conflict": failing("issues.assign-conflict", issues.assignIssue, Code.Aborted, "the issue was assigned by someone else meanwhile", reason("version_conflict")),
    "roster-denied": failing("issues.roster-denied", issues.listAssignableUsers, Code.PermissionDenied, "issue organization membership required"),
    /** iss-inventory-lock was merged into the incident's issue, which carries its entries. */
    merged: [
      refining("issues.merged.list", issues.listIssues, pack, (list) => {
        for (const issue of list.issues) if (issue.id === "iss-inventory-lock") issue.mergedInto = DEMO_INCIDENT.issueId;
      }),
      refining("issues.merged.get", issues.getIssue, pack, (answer) => {
        if (answer.issue?.id === "iss-inventory-lock") answer.issue.mergedInto = DEMO_INCIDENT.issueId;
      }),
    ],
  },
  sessions: {
    empty: emptying("sessions.empty", rum.listSessions),
    "not-configured": unimplemented("sessions.not-configured", RumService),
  },
  replay: {
    /** The recording stops a third of the way into the session. */
    partial: refining("replay.partial", rum.getSessionReplay, pack, (page) => {
      page.events = page.events.slice(0, Math.max(1, Math.ceil(page.events.length / 3)));
      page.nextPageToken = "";
    }),
    /** The full snapshot was lost: the events cannot be played. */
    "no-snapshot": refining("replay.no-snapshot", rum.getSessionReplay, pack, (page) => {
      page.events = page.events.filter((event) => rrwebType(event.data) !== 2);
    }),
    /** Two events on the first page cannot be read. */
    unreadable: refining("replay.unreadable", rum.getSessionReplay, pack, (page, request) => {
      if (request.pageToken) return;
      const time = page.events[0]?.time;
      for (let i = 0; i < 2; i++) page.events.push(create(ReplayEventSchema, { time, index: page.events.length, chunks: 1, data: new Uint8Array([0]) }));
    }),
    /** The session was recorded, and nothing of the recording is left. */
    none: refining("replay.none", rum.getSessionReplay, pack, (page) => {
      page.events = [];
      page.nextPageToken = "";
    }),
  },
  health: {
    pass: healthAs("health.pass", ReleaseVerdict.PASS),
    inconclusive: healthAs("health.inconclusive", ReleaseVerdict.INCONCLUSIVE, (health) => {
      health.afterComplete = false;
    }),
    /** A pass from a server that cannot read alerts or issues: it says what it did not check. */
    "not-evaluated": healthAs("health.not-evaluated", ReleaseVerdict.PASS, (health) => {
      health.notEvaluated = ["alert_firing", "new_issue", "regressed_issue"];
    }),
    unimplemented: unimplemented("health.unimplemented", obs.getReleaseHealth),
  },
  dashboards: {
    /** A young app: two dashboards. */
    few: refining("dashboards.few", dashboards.listDashboards, pack, (list) => {
      list.dashboards = list.dashboards.slice(0, 2);
    }),
    empty: refining("dashboards.empty", dashboards.listDashboards, pack, (list) => {
      list.dashboards = [];
    }),
    "read-only": dashboardsReadOnly,
    error: failing("dashboards.error", DashboardService, Code.Unavailable, "dashboards unavailable"),
    loading: stalling("dashboards.loading", [...readsOf([DashboardService]), obs.queryPanel]),
  },
  metrics: {
    /** The series cap cut the result, and the panel came back with warnings. */
    cap: refining("metrics.cap", obs.queryPanel, pack, (panel) => {
      panel.seriesTotal = 1204;
      panel.warnings = ["mixed_bounds", "step_raised"];
    }),
    "read-only": dashboardsReadOnly,
    /** Someone else saved the dashboard meanwhile. */
    conflict: failing("metrics.conflict", dashboards.saveDashboard, Code.Aborted, "the dashboard was saved by someone else meanwhile", reason("version_conflict")),
  },
} satisfies Record<string, Record<string, FixturePack | readonly FixturePack[]>>;

/**
 * The service graph's catalog answers once, then refuses (`code`): a catalog
 * the page already holds, revoked or gone on refetch. A factory: its count
 * is per mount.
 */
export function retainedCatalog(code: Code.PermissionDenied | Code.Unavailable): FixturePack {
  let calls = 0;
  return answering(`graph.retained-${code === Code.PermissionDenied ? "denied" : "unavailable"}`, obs.getServiceGraphCatalog, (method, request) => {
    if (++calls > 1) throw new ConnectError("catalog no longer readable", code);
    return (pack[`${method.parent.typeName}/${method.name}`] as (req: unknown) => unknown)(request);
  });
}

declare global {
  interface Window {
    /** The read cap's tally, for looking at the scheduler from the console: window.__panelReads. */
    __panelReads?: { live: number; peak: number; refused: number; served: number };
  }
}

/**
 * control-plane's per-user cap on concurrent console reads, on QueryPanel:
 * over `limit` in flight is refused as `in_flight_limit` with a 1s RetryInfo,
 * and each admitted read takes `ms`. Pass it as the transport's
 * `interceptors`; window.__panelReads tallies it.
 */
export function readCap(limit: number, ms: number): Interceptor {
  return (next) => async (req) => {
    if (req.method !== obs.queryPanel) return next(req);
    const stats = (window.__panelReads ??= { live: 0, peak: 0, refused: 0, served: 0 });
    stats.live++;
    stats.peak = Math.max(stats.peak, stats.live);
    if (stats.live > limit) {
      stats.live--;
      stats.refused++;
      const error = new ConnectError("too many observability queries in flight; retry shortly", Code.ResourceExhausted, reason("in_flight_limit"));
      const duration = new BinaryWriter().tag(1, 0).int64(1).finish();
      error.details.push({ type: "google.rpc.RetryInfo", value: new BinaryWriter().tag(1, 2).bytes(duration).finish() });
      throw error;
    }
    try {
      await new Promise<void>((resolve, reject) => {
        const timer = setTimeout(resolve, ms);
        req.signal.addEventListener("abort", () => {
          clearTimeout(timer);
          reject(new ConnectError("the read was aborted", Code.Canceled));
        }, { once: true });
      });
      const res = await next(req);
      stats.served++;
      return res;
    } finally {
      stats.live--;
    }
  };
}
