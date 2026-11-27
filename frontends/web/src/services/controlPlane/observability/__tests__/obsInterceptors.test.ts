// @vitest-environment node
/**
 * The control-plane transport's observability read stage (obsInterceptors.ts),
 * end to end through a real Connect client and an in-process server: what
 * reaches the server, and what never leaves the browser.
 */
import { Code, ConnectError, createClient, createRouterTransport, type CallOptions, type HandlerContext } from "@connectrpc/connect";
import type { DescService } from "@bufbuild/protobuf";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { DashboardService } from "@/gen/controlplane/services/dashboard/v1/dashboard_pb";
import { IssuesService } from "@/gen/controlplane/services/issues/v1/issues_pb";
import { ObservabilityService } from "@/gen/controlplane/services/observability/v1/observability_pb";
import { RumService } from "@/gen/controlplane/services/rum/v1/rum_pb";

import { obsCall, obsReadInterceptor } from "../obsInterceptors";
import { quotaOf, quotaPausedError } from "../quota";
import { obsQuotaStore, resetObsQuotaStore, runAsPoll } from "../quotaStore";
import { OBS_READ_CONCURRENCY, ReadScheduler, resetObsReadScheduler, runAsBackground } from "../readScheduler";
import { NO_READ_SLOT_RPCS, READ_SLOT_RPCS } from "../readSlots";

const QUOTA_SERVICES = [ObservabilityService, IssuesService, RumService];

interface Arrival {
  method: string;
  poll: string | null;
  panel: string | null;
}

type AnyCall = (request: object, options?: CallOptions) => Promise<unknown>;

/**
 * A control-plane stand-in behind the app's stage: every unary method of the
 * quota services (and DashboardService, which the stage must leave alone)
 * records what arrived and answers `answer(method)`, `{}` by default.
 */
function controlPlane(answer: (method: string) => Promise<object> | object = () => ({})) {
  const arrivals: Arrival[] = [];
  const impl = () =>
    new Proxy(
      {},
      {
        get: (_target, method: string) => async (_request: unknown, ctx: HandlerContext) => {
          arrivals.push({ method, poll: ctx.requestHeader.get("Forge-Poll"), panel: ctx.requestHeader.get("Forge-Panel") });
          return answer(method);
        },
      }
    );
  const transport = createRouterTransport(
    (router) => {
      for (const service of [...QUOTA_SERVICES, DashboardService]) router.service(service, impl());
    },
    { transport: { interceptors: [obsReadInterceptor()] } }
  );
  const call = (service: DescService, method: string): AnyCall => (createClient(service, transport) as unknown as Record<string, AnyCall>)[method];
  return { arrivals, call, panel: call(ObservabilityService, "queryPanel") };
}

const exhausted = (resetsAtMs = Date.now() + 3_600_000) =>
  obsQuotaStore().report({ limit: "queries", lane: "interactive", resetsAtMs, soft: false });

const methodsOf = (services: DescService[]) =>
  services.flatMap((service) => service.methods.map((method) => ({ service, method: method.localName, kind: method.methodKind })));

beforeEach(() => {
  resetObsReadScheduler();
  resetObsQuotaStore();
});
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("a read's poll mark", () => {
  it("sends Forge-Poll on a background poll and not on a person's read", async () => {
    const cp = controlPlane();
    await cp.panel({}, obsCall());
    let poll: Promise<unknown> | undefined;
    runAsPoll(() => void (poll = cp.panel({}, obsCall())));
    await poll;
    await cp.panel({}, obsCall());
    expect(cp.arrivals.map((a) => a.poll)).toEqual([null, "1", null]);
  });

  // The stage runs a microtask or more after the call is made (the app's
  // routed transport awaits its route first; here the scheduler may queue it):
  // the mark is the caller's, captured when the read was made.
  it("keeps the mark when the read reaches the stage after the poll that made it has ended", async () => {
    const cp = controlPlane();
    let poll: Promise<unknown> | undefined;
    runAsPoll(() => void (poll = cp.panel({}, obsCall())));
    await poll;
    expect(cp.arrivals[0].poll).toBe("1");
  });

  it("keeps the signal and headers the caller passes (Forge-Panel)", async () => {
    const cp = controlPlane();
    let poll: Promise<unknown> | undefined;
    runAsPoll(() => void (poll = cp.panel({}, obsCall({ headers: { "Forge-Panel": "dash/p1" }, signal: new AbortController().signal }))));
    await poll;
    expect(cp.arrivals[0]).toMatchObject({ poll: "1", panel: "dash/p1" });
  });

  it("queues a poll or a prefetch as background, and a person's read (or an unmarked one) as visible", async () => {
    const run = vi.spyOn(ReadScheduler.prototype, "run");
    const cp = controlPlane();
    const reads: Promise<unknown>[] = [cp.panel({}, obsCall())];
    runAsPoll(() => void reads.push(cp.panel({}, obsCall())));
    runAsBackground(() => void reads.push(cp.panel({}, obsCall())));
    reads.push(cp.panel({}));
    await Promise.all(reads);
    expect(run.mock.calls.map((call) => call[2])).toEqual(["visible", "background", "background", "visible"]);
  });
});

describe("the quota guard", () => {
  it("has only unary methods to guard", () => {
    expect(methodsOf(QUOTA_SERVICES).filter((m) => m.kind !== "unary")).toEqual([]);
  });

  it("refuses every ClickHouse read of the three services while the quota is gone, with no call options, and sends none", async () => {
    const cp = controlPlane();
    exhausted();
    const reads = methodsOf(QUOTA_SERVICES).filter((m) => READ_SLOT_RPCS.has(m.method));
    expect(reads.length).toBeGreaterThan(20);
    for (const { service, method } of reads) {
      const error = await cp.call(service, method)({}).then(() => null, (e: unknown) => e);
      expect(quotaOf(error), `${service.typeName}/${method}`).toMatchObject({ limit: "queries", soft: false });
    }
    expect(cp.arrivals).toEqual([]);
  });

  it("lets the same services' Postgres reads and mutations through while the quota is gone", async () => {
    const cp = controlPlane();
    exhausted();
    const others = methodsOf(QUOTA_SERVICES).filter((m) => NO_READ_SLOT_RPCS.has(m.method));
    for (const { service, method } of others) await cp.call(service, method)({});
    expect(cp.arrivals.map((a) => a.method)).toEqual(others.map((m) => m.method));
  });

  it("leaves other control-plane services alone", async () => {
    const run = vi.spyOn(ReadScheduler.prototype, "run");
    const cp = controlPlane();
    exhausted();
    let read: Promise<unknown> | undefined;
    runAsPoll(() => void (read = cp.call(DashboardService, "listDashboards")({}, obsCall())));
    await read;
    expect(cp.arrivals).toEqual([{ method: "listDashboards", poll: null, panel: null }]);
    expect(run).not.toHaveBeenCalled();
  });

  it("records the server's quota answer, so the next read never leaves the browser", async () => {
    const cp = controlPlane((method) => {
      if (method === "searchLogs") throw quotaPausedError({ limit: "read_bytes", lane: "interactive", resetsAtMs: Date.now() + 600_000, soft: false });
      return {};
    });
    await expect(cp.call(ObservabilityService, "searchLogs")({}, obsCall())).rejects.toBeInstanceOf(ConnectError);
    expect(obsQuotaStore().getState()).toMatchObject({ kind: "exhausted", limit: "read_bytes" });

    await expect(cp.panel({}, obsCall())).rejects.toMatchObject({ code: Code.ResourceExhausted });
    expect(cp.arrivals.map((a) => a.method)).toEqual(["searchLogs"]);
  });

  it("lets a read out once the reset has passed, and its success ends the pause", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(Date.UTC(2026, 10, 25, 12, 15));
    const cp = controlPlane();
    exhausted(Date.now() + 1_000);
    await expect(cp.panel({}, obsCall())).rejects.toMatchObject({ code: Code.ResourceExhausted });

    vi.setSystemTime(Date.now() + 1_001);
    await cp.panel({}, obsCall());
    expect(cp.arrivals.map((a) => a.method)).toEqual(["queryPanel"]);
    expect(obsQuotaStore().getState().kind).toBe("ok");
  });
});

describe("the read queue", () => {
  it("queues only the reads that take a server read slot", async () => {
    const run = vi.spyOn(ReadScheduler.prototype, "run");
    const cp = controlPlane();
    await cp.panel({}, obsCall());
    await cp.call(IssuesService, "listIssueTimeline")({}, obsCall());
    await cp.call(IssuesService, "addIssueComment")({});
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("starts no more than the queue's width at once, and a queued read's own signal cancels it before it is sent", async () => {
    const releases: (() => void)[] = [];
    const cp = controlPlane((method) => (method === "queryPanel" ? new Promise<object>((resolve) => releases.push(() => resolve({}))) : {}));
    const running = Array.from({ length: OBS_READ_CONCURRENCY }, () => cp.panel({}, obsCall()));
    const queued = new AbortController();
    const abandoned = cp.call(ObservabilityService, "searchLogs")({}, obsCall({ signal: queued.signal }));
    const next = cp.call(ObservabilityService, "getTrace")({}, obsCall());
    await vi.waitFor(() => expect(releases).toHaveLength(OBS_READ_CONCURRENCY));
    expect(cp.arrivals.map((a) => a.method)).not.toContain("searchLogs");

    queued.abort();
    await expect(abandoned).rejects.toThrow(/abort/i);
    releases.forEach((release) => release());
    await Promise.all([...running, next]);
    expect(cp.arrivals.map((a) => a.method).filter((m) => m !== "queryPanel")).toEqual(["getTrace"]);
  });
});

describe("which RPCs wait in the read queue", () => {
  const methods = QUOTA_SERVICES.flatMap((service) => service.methods.map((method) => method.localName));

  it("classifies every method of the three services: a new RPC must say whether it takes a server read slot", () => {
    const unclassified = methods.filter((name) => !READ_SLOT_RPCS.has(name) && !NO_READ_SLOT_RPCS.has(name));
    expect(unclassified).toEqual([]);
  });

  it("names only methods that exist", () => {
    const known = new Set(methods);
    expect([...READ_SLOT_RPCS, ...NO_READ_SLOT_RPCS].filter((name) => !known.has(name))).toEqual([]);
  });

  it("never queues a mutation behind the reads", () => {
    for (const name of ["updateIssueStatus", "addIssueComment", "updateIssueComment", "deleteIssueComment", "assignIssue", "linkIssueChat"]) {
      expect(READ_SLOT_RPCS.has(name)).toBe(false);
    }
  });
});
