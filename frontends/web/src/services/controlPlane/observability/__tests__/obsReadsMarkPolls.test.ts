// @vitest-environment node
/**
 * Every read the console makes of the quota-spending services marks itself
 * (obsCall): made inside a poll, it reaches control-plane with Forge-Poll.
 * The read stage cannot tell a poll from the request (obsInterceptors.ts), so
 * a read that forgets obsCall would spend the server's reserve for a person's
 * own query on a poll. A new read function belongs in READS below.
 */
import { createRouterTransport, type HandlerContext, type Transport } from "@connectrpc/connect";
import { beforeEach, describe, expect, it } from "vitest";

import { IssuesService } from "@/gen/controlplane/services/issues/v1/issues_pb";
import { ObservabilityService } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { RumService } from "@/gen/controlplane/services/rum/v1/rum_pb";

import * as api from "../api";
import * as delivery from "../deliveryApi";
import * as frontendReplay from "../frontendReplayApi";
import * as graph from "../graphApi";
import * as issues from "../issuesApi";
import * as metrics from "../metricsApi";
import { obsReadInterceptor } from "../obsInterceptors";
import { resetObsQuotaStore, runAsPoll } from "../quotaStore";
import { READ_SLOT_RPCS } from "../readSlots";
import { resetObsReadScheduler } from "../readScheduler";
import * as rum from "../rumApi";

const window = { startMs: 0, endMs: 60_000 };
/** Every field any of the read functions below builds its request from. */
const req = {
  orgId: "org",
  projectId: "project",
  envIds: ["env"],
  filters: [],
  services: [],
  window,
  query: "",
  severities: [],
  statuses: [],
  traceId: "0af7651916cd43dd8448eb211c80319c",
  issueId: "iss",
  sessionId: "sess",
  service: "api",
  metric: "http.server.duration",
  name: "http.server.duration",
  key: "service.name",
  workload: "api",
  panel: { id: "p", title: "p", kind: "number", signal: "traces", series: [{ label: "p", agg: "count" }] },
  release: { version: "v1", promotedAtMs: 0 },
  environmentId: "env",
  version: "v1",
} as never;

type Read = (transport: Transport) => Promise<unknown>;

/** The console's reads of ObservabilityService, IssuesService and RumService (mutations excluded). */
const READS: Record<string, Read> = {
  searchLogs: (t) => api.searchLogs(req, {}, t),
  listTraces: (t) => api.listTraces(req, {}, t),
  getTrace: (t) => api.getTrace(req, {}, t),
  queryPanel: (t) => api.queryPanel(req, {}, t),
  getServiceOverview: (t) => api.getServiceOverview(req, {}, t),
  getFlamegraph: (t) => api.getFlamegraph(req, {}, t),
  getServiceSignals: (t) => api.getServiceSignals(req, {}, t),
  listAttributeValues: (t) => api.listAttributeValues(req, {}, t),
  listWorkloads: (t) => api.listWorkloads(req, {}, t),
  getWorkload: (t) => api.getWorkload(req, {}, t),
  listMetrics: (t) => metrics.listMetricCatalog(req, {}, t),
  getMetric: (t) => metrics.getMetric(req, {}, t),
  listMetricExemplars: (t) => metrics.listMetricExemplars(req, {}, t),
  getServiceGraph: (t) => graph.getServiceGraph(req, {}, t),
  getServiceGraphCatalog: (t) => graph.getServiceGraphCatalog(req, {}, t),
  getReleaseHealth: (t) => delivery.getReleaseHealth(req, {}, t),
  listFrontendReplay: (t) => frontendReplay.listFrontendReplay(req, {}, t),
  listIssues: (t) => issues.listIssues(req, {}, t),
  getIssue: (t) => issues.getIssue(req, {}, t),
  listIssueEvents: (t) => issues.listIssueEvents(req, {}, t),
  listIssueTimeline: (t) => issues.listIssueTimeline(req, t),
  listSessions: (t) => rum.listSessions(req, {}, t),
  getSession: (t) => rum.getSession(req, {}, t),
  getSessionTimeline: (t) => rum.getSessionTimeline(req, {}, t),
  getSessionReplay: (t) => rum.getSessionReplay(req, {}, t),
};

/** Server-side reads the console does not make yet (readSlots.ts says so of each). */
const NOT_CALLED = new Set(["getDashboards", "getUserActivity", "exportPGOProfile", "getManagedDatabaseMetrics"]);

function controlPlane() {
  const arrivals = new Map<string, string | null>();
  const impl = new Proxy(
    {},
    {
      get: (_target, method: string) => async (_request: unknown, ctx: HandlerContext) => {
        arrivals.set(method, ctx.requestHeader.get("Forge-Poll"));
        return {};
      },
    }
  );
  const transport = createRouterTransport(
    (router) => {
      for (const service of [ObservabilityService, IssuesService, RumService]) router.service(service, impl);
    },
    { transport: { interceptors: [obsReadInterceptor()] } }
  );
  return { arrivals, transport };
}

beforeEach(() => {
  resetObsReadScheduler();
  resetObsQuotaStore();
});

describe("the console's observability reads", () => {
  it("cover every read-slot rpc the console calls", () => {
    expect([...READ_SLOT_RPCS].filter((rpc) => !NOT_CALLED.has(rpc) && !(rpc in READS))).toEqual([]);
  });

  it.each(Object.keys(READS))("%s marks a poll with Forge-Poll, and a person's read without it", async (rpc) => {
    const cp = controlPlane();
    // The answer is empty, so mapping it may fail; only what reached the server matters here.
    let polled: Promise<unknown> | undefined;
    runAsPoll(() => void (polled = READS[rpc](cp.transport)));
    await polled?.catch(() => undefined);
    expect(cp.arrivals.get(rpc)).toBe("1");

    cp.arrivals.clear();
    await READS[rpc](cp.transport).catch(() => undefined);
    expect(cp.arrivals.has(rpc)).toBe(true);
    expect(cp.arrivals.get(rpc)).toBeNull();
  });
});
